package debugutil

import (
	"context"
	"net"
	"net/http"
	"net/http/pprof"
	"time"
)

func RunDebugServer(endpoint string) (func(), error) {
	log.Debugf("listening on debug endpoint: %s", endpoint)
	debugListener, err := net.Listen("tcp", endpoint)
	if err != nil {
		log.Errorf("failed to listen on %q: %s", endpoint, err)
		return nil, err
	}
	debugHandler := http.NewServeMux()
	debugHandler.HandleFunc("/debug/pprof/", pprof.Index)
	debugHandler.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	debugHandler.HandleFunc("/debug/pprof/profile", pprof.Profile)
	debugHandler.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	debugHandler.HandleFunc("/debug/pprof/trace", pprof.Trace)
	debugServer := &http.Server{
		Handler:           debugHandler,
		ReadHeaderTimeout: 30 * time.Second,
	}
	go func() {
		log.Debugf("running and serving debug endpoint")
		if err := debugServer.Serve(debugListener); err != nil && err != http.ErrServerClosed {
			log.Errorf("debug server exited with error: %s", err)
		}
		log.Debugf("stop serving debug endpoint")
	}()
	return func() {
		if err := debugServer.Shutdown(context.Background()); err != nil {
			log.Errorf("failed to shutdown debug server: %s", err)
		}
	}, nil
}
