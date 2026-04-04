package main

import (
	"context"
	"errors"
	"net/http"
	"os/signal"
	"syscall"
)

func main() {
	rr := &ResourcesRegistry{}
	if err := rr.Setup(); err != nil {
		rr.Shutdown(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer stop()

	go func() {
		if err := rr.http.server.ListenAndServe(); err != nil &&
			!errors.Is(err, http.ErrServerClosed) {
			rr.errorChan <- err
		}
	}()

	rr.logger.Info("block-storage-api is running",
		"backend", rr.storage.backend.BackendName(),
		"consistency", rr.config.ConsistencyStrategy,
		"port", rr.config.Port,
	)

	select {
	case <-ctx.Done():
		rr.logger.Info("shutdown signal received")
	case err := <-rr.errorChan:
		rr.logger.Error("server error", "err", err)
	}

	rr.Shutdown(nil)
}
