package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os/signal"
	"syscall"
)

func main() {
	// appCtx is the root lifecycle context. When SIGTERM/SIGINT fires, appCtx
	// is canceled, which propagates through BaseContext to all request contexts
	// and retry goroutines, triggering a clean shutdown.
	appCtx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	rr := &ResourcesRegistry{}
	if err := rr.Setup(); err != nil {
		rr.Shutdown(err)
	}

	// BaseContext ensures every request context is derived from appCtx.
	// When SIGTERM is received, appCtx is canceled → all in-flight handlers
	// and their retry goroutines receive ctx.Done().
	rr.http.server.BaseContext = func(_ net.Listener) context.Context {
		return appCtx
	}

	go func() {
		if err := rr.http.server.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			rr.errorChan <- err
		}
	}()

	rr.logger.Info("block-storage-api is running",
		"backend", rr.storage.backend.BackendName(),
		"port", rr.config.Port,
	)

	select {
	case <-appCtx.Done():
		rr.logger.Info("shutdown signal received")
	case err := <-rr.errorChan:
		rr.logger.Error("server error", "err", err)
	}

	rr.Shutdown(nil)
}
