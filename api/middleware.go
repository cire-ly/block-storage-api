package api

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/otel/trace"
)

type contextKey string

const loggerCtxKey contextKey = "logger"

// withLogger stores a logger in the request context.
func withLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerCtxKey, l)
}

// loggerFromCtx retrieves the logger from context, falling back to the default.
func loggerFromCtx(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerCtxKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// requestLogger logs each request's method, path, status and duration.
// It must run after the OTel middleware so the span (and trace ID) already exist.
func requestLogger(base *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Inject trace ID into logger if a span is active.
			span := trace.SpanFromContext(r.Context())
			sc := span.SpanContext()
			l := base
			if sc.IsValid() {
				l = base.With("trace_id", sc.TraceID().String())
			}
			ctx := withLogger(r.Context(), l)
			r = r.WithContext(ctx)

			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)

			l.Info("http request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rw.status,
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// recoverer catches panics, logs them, and returns 500.
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				loggerFromCtx(r.Context()).Error("panic recovered",
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// responseWriter captures the HTTP status code written by handlers.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
