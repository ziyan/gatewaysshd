package cli

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/gorilla/mux"

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
		if handler, ok := result.(http.Handler); ok {
			// allow returned handler to handle the request
			handler.ServeHTTP(response, request)
			return
		}
		if handler, ok := result.(http.HandlerFunc); ok {
			// allow returned handler to handle the request
			handler(response, request)
			return
		}

		raw, err := json.Marshal(result)
		if err != nil {
			log.Errorf("failed to encode json: %s", err)
			http.Error(response, "500 internal server error", http.StatusInternalServerError)
			return
		}

		response.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = response.Write(raw)
	}
}

func newHttpHandler(gateway gateway.Gateway) http.Handler {
	router := mux.NewRouter().StrictSlash(true)
	router.Path("/api/user").HandlerFunc(wrapHttpHandler(func(request *http.Request) (interface{}, error) {
		return gateway.ListUsers()
	}))
	router.Path("/api/user/{userId}").HandlerFunc(wrapHttpHandler(func(request *http.Request) (interface{}, error) {
		user, err := gateway.GetUser(mux.Vars(request)["userId"])
		if err != nil {
			return nil, err
		}
		if user == nil {
			return nil, ErrNotFound
		}
		return user, nil
	}))
	router.Path("/api/user/{userId}/screenshot").HandlerFunc(wrapHttpHandler(func(request *http.Request) (interface{}, error) {
		screenshot, err := gateway.GetUserScreenshot(mux.Vars(request)["userId"])
		if err != nil {
			return nil, err
		}
		if len(screenshot) == 0 {
			return nil, ErrNotFound
		}
		return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set("Content-Type", "image/png")
			_, _ = response.Write(screenshot)
		}), nil
	}))
	return router
}
