package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/cire-ly/block-storage-api/storage"
)

// NewRouter builds and returns the chi router with all middleware and routes.
func NewRouter(backend storage.VolumeBackend, logger *slog.Logger, tracer trace.Tracer, meter metric.Meter) *chi.Mux {
	h := &Handler{
		backend: backend,
		logger:  logger,
		tracer:  tracer,
	}

	// volume.operations.total counter — best-effort; skip if meter not ready.
	if meter != nil {
		var err error
		h.opTotal, err = meter.Int64Counter("volume.operations.total",
			metric.WithDescription("Total number of volume operations"),
		)
		if err != nil {
			logger.Warn("failed to create volume.operations.total counter", "err", err)
		}
	}

	r := chi.NewRouter()

	// Middleware chain (outermost → innermost):
	//   1. OTel HTTP — creates span, propagates W3C trace context
	//   2. Recoverer  — catches panics
	//   3. Request logger — logs method/path/status/duration + trace ID
	r.Use(func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "chi.router")
	})
	r.Use(recoverer)
	r.Use(requestLogger(logger))

	r.Get("/healthz", h.Healthz)

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/volumes", h.CreateVolume)
		r.Get("/volumes", h.ListVolumes)
		r.Get("/volumes/{name}", h.GetVolume)
		r.Put("/volumes/{name}/attach", h.AttachVolume)
		r.Put("/volumes/{name}/detach", h.DetachVolume)
		r.Delete("/volumes/{name}", h.DeleteVolume)
	})

	return r
}
