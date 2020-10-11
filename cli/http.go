package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/pprof"
	"strings"

	"github.com/ziyan/gatewaysshd/gateway"
)

var (
	ErrNotFound = errors.New("404: not found")
)

func wrapHttpHandler(handler func(*http.Request) (interface{}, error)) func(http.ResponseWriter, *http.Request) {
	return func(response http.ResponseWriter, request *http.Request) {
		result, err := handler(request)
		if err != nil {
			if err == ErrNotFound {
				http.NotFound(response, request)
				return
			}
			log.Errorf("failed to handle request: %s", err)
			http.Error(response, "500 internal server error", http.StatusInternalServerError)
			return
		}

		raw, err := json.Marshal(result)
		if err != nil {
			log.Errorf("failed to encode json: %s", err)
			http.Error(response, "500 internal server error", http.StatusInternalServerError)
			return
		}

		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		response.Write(raw)
	}
}

func newHttpHandler(gateway gateway.Gateway, enablePprof bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/user", wrapHttpHandler(func(request *http.Request) (interface{}, error) {
		return gateway.ListUsers()
	}))
	mux.HandleFunc("/api/user/", wrapHttpHandler(func(request *http.Request) (interface{}, error) {
		parts := strings.Split(request.URL.Path, "/")
		if len(parts) != 4 {
			return nil, ErrNotFound
		}
		user, err := gateway.GetUser(parts[3])
		if err != nil {
			return nil, err
		}
		if user == nil {
			return nil, ErrNotFound
		}
		return user, nil
	}))

	if enablePprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return mux
}
