// Package volume provides the HTTP transport layer and business logic for
// block storage volume lifecycle management.
//
//	@title			Block Storage API
//	@version		1.0.0
//	@description	Pluggable block storage API — Ceph, Lustre backends with FSM volume lifecycle.
//
//	@contact.name	Ciré LY
//	@contact.url	https://github.com/cire-ly/block-storage-api
//	@contact.email	cire.ly@nexonode.tech
//
//	@license.name	MIT
//	@license.url	https://opensource.org/licenses/MIT
//
//	@host		163.172.144.70:8080
//	@BasePath	/
//
//	@tag.name			volumes
//	@tag.description	Block storage volume lifecycle operations
//	@tag.name			system
//	@tag.description	Health and observability endpoints
package volume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/go-chi/chi/v5"
	httpSwagger "github.com/swaggo/http-swagger"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	_ "github.com/cire-ly/block-storage-api/docs" // Swagger generated spec
	"github.com/cire-ly/block-storage-api/storage"
)

const apiVersion = "1.0.0"

// httpController holds the dependencies for all HTTP handlers.
// It has zero business logic — it only translates HTTP ↔ ApplicationContract.
type httpController struct {
	app         ApplicationContract
	backendName string // cached at construction, used in healthz response
	logger      LoggerDependency
	tracer      trace.Tracer
	opTotal     metric.Int64Counter
}

func newHTTPController(
	app ApplicationContract,
	logger LoggerDependency,
	tracer trace.Tracer,
	meter metric.Meter,
) *httpController {
	backendName := "unknown"
	if a, ok := app.(*application); ok {
		backendName = a.backend.BackendName()
	}

	c := &httpController{
		app:         app,
		backendName: backendName,
		logger:      logger,
		tracer:      tracer,
	}

	if meter != nil {
		var err error
		c.opTotal, err = meter.Int64Counter("volume.operations.total",
			metric.WithDescription("Total number of volume operations"),
		)
		if err != nil {
			logger.Warn("failed to create volume.operations.total counter", "err", err)
		}
	}

	return c
}

// registerRoutes attaches middleware and all routes to the given chi.Router.
func (c *httpController) registerRoutes(r chi.Router) {
	r.Use(func(next http.Handler) http.Handler {
		return otelhttp.NewHandler(next, "chi.router")
	})
	r.Use(c.recoverer)
	r.Use(c.requestLogger)

	r.Get("/healthz", c.healthz)

	r.Get("/swagger/*", httpSwagger.Handler(
		httpSwagger.URL("/swagger/doc.json"),
	))

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/volumes", c.createVolume)
		r.Get("/volumes", c.listVolumes)
		r.Get("/volumes/{name}", c.getVolume)
		r.Put("/volumes/{name}/attach", c.attachVolume)
		r.Put("/volumes/{name}/detach", c.detachVolume)
		r.Delete("/volumes/{name}", c.deleteVolume)
		r.Post("/volumes/{name}/reconcile", c.reconcileVolume)
		r.Get("/volumes/{name}/events", c.streamVolumeEvents)
	})
}

// -- request / response types ------------------------------------------------

// createVolumeRequest is the body for POST /api/v1/volumes.
// @Description Create volume request
type createVolumeRequest struct {
	Name   string `json:"name"    example:"vol-01"`
	SizeMB int    `json:"size_mb" example:"1024"`
}

// attachVolumeRequest is the body for PUT /api/v1/volumes/{name}/attach.
// @Description Attach volume request
type attachVolumeRequest struct {
	NodeID string `json:"node_id" example:"node-paris-01"`
}

// volumeResponse is the representation of a volume returned by the API.
// @Description Volume resource
type volumeResponse struct {
	ID        string `json:"id"                example:"550e8400-e29b-41d4-a716-446655440000"`
	Name      string `json:"name"              example:"vol-01"`
	SizeMB    int    `json:"size_mb"           example:"1024"`
	State     string `json:"state"             example:"available"`
	Backend   string `json:"backend"           example:"mock"`
	NodeID    string `json:"node_id,omitempty" example:"node-paris-01"`
	CreatedAt string `json:"created_at"        example:"2024-01-01T00:00:00Z"`
	UpdatedAt string `json:"updated_at"        example:"2024-01-01T00:00:00Z"`
}

// listResponse wraps a slice of volumes with a total count.
// @Description List of volumes
type listResponse struct {
	Volumes []*volumeResponse `json:"volumes"`
	Count   int               `json:"count" example:"1"`
}

// healthResponse is returned by GET /healthz.
// @Description Health check response
type healthResponse struct {
	Status  string `json:"status"  example:"ok"`
	Backend string `json:"backend" example:"mock"`
	Version string `json:"version" example:"1.0.0"`
}

// errResponse wraps an error message for client consumption.
// @Description Error response
type errResponse struct {
	Error string `json:"error" example:"volume not found"`
}

// -- helpers -----------------------------------------------------------------

func toResponse(v *storage.Volume) *volumeResponse {
	return &volumeResponse{
		ID:        v.ID,
		Name:      v.Name,
		SizeMB:    v.SizeMB,
		State:     v.State,
		Backend:   v.Backend,
		NodeID:    v.NodeID,
		CreatedAt: v.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		UpdatedAt: v.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, errResponse{Error: msg})
}

func (c *httpController) recordOp(ctx context.Context, op string) {
	if c.opTotal != nil {
		c.opTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("operation", op)))
	}
}

// mapAppError translates application-layer sentinel errors to HTTP status codes.
func mapAppError(err error) int {
	switch {
	case errors.Is(err, ErrVolumeNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrVolumeExists):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidTransition):
		return http.StatusConflict
	case errors.Is(err, ErrInvalidSize):
		return http.StatusBadRequest
	case errors.Is(err, ErrBackendUnavailable):
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// -- handlers ----------------------------------------------------------------

// createVolume godoc
//
//	@Summary		Create a volume
//	@Description	Creates a new block storage volume. The volume transitions PENDING → CREATING → AVAILABLE asynchronously.
//	@Tags			volumes
//	@Accept			json
//	@Produce		json
//	@Param			body	body		createVolumeRequest	true	"Volume creation parameters"
//	@Success		201		{object}	volumeResponse
//	@Failure		400		{object}	errResponse	"Invalid request"
//	@Failure		409		{object}	errResponse	"Volume already exists"
//	@Failure		500		{object}	errResponse	"Internal error"
//	@Router			/api/v1/volumes [post]
func (c *httpController) createVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := c.tracer.Start(r.Context(), "handler.CreateVolume")
	defer span.End()
	c.recordOp(ctx, "create")

	var req createVolumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.SizeMB <= 0 {
		writeErr(w, http.StatusBadRequest, "size_mb must be > 0")
		return
	}

	vol, err := c.app.CreateVolume(ctx, req.Name, req.SizeMB)
	if err != nil {
		writeErr(w, mapAppError(err), err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, toResponse(vol))
}

// listVolumes godoc
//
//	@Summary		List all volumes
//	@Description	Returns all volumes managed by the storage backend.
//	@Tags			volumes
//	@Produce		json
//	@Success		200	{object}	listResponse
//	@Failure		500	{object}	errResponse
//	@Router			/api/v1/volumes [get]
func (c *httpController) listVolumes(w http.ResponseWriter, r *http.Request) {
	ctx, span := c.tracer.Start(r.Context(), "handler.ListVolumes")
	defer span.End()
	c.recordOp(ctx, "list")

	volumes, err := c.app.ListVolumes(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := &listResponse{Count: len(volumes), Volumes: make([]*volumeResponse, 0, len(volumes))}
	for _, v := range volumes {
		resp.Volumes = append(resp.Volumes, toResponse(v))
	}
	writeJSON(w, http.StatusOK, resp)
}

// getVolume godoc
//
//	@Summary		Get a volume
//	@Description	Returns a single volume by name.
//	@Tags			volumes
//	@Produce		json
//	@Param			name	path		string	true	"Volume name"
//	@Success		200		{object}	volumeResponse
//	@Failure		404		{object}	errResponse	"Volume not found"
//	@Failure		500		{object}	errResponse
//	@Router			/api/v1/volumes/{name} [get]
func (c *httpController) getVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := c.tracer.Start(r.Context(), "handler.GetVolume")
	defer span.End()
	c.recordOp(ctx, "get")

	name := chi.URLParam(r, "name")
	vol, err := c.app.GetVolume(ctx, name)
	if err != nil {
		writeErr(w, mapAppError(err), err.Error())
		return
	}

	writeJSON(w, http.StatusOK, toResponse(vol))
}

// attachVolume godoc
//
//	@Summary		Attach a volume to a node
//	@Description	Attaches an available volume to a compute node. Transition: AVAILABLE → ATTACHING → ATTACHED (async).
//	@Tags			volumes
//	@Accept			json
//	@Produce		json
//	@Param			name	path		string				true	"Volume name"
//	@Param			body	body		attachVolumeRequest	true	"Node to attach to"
//	@Success		202		{object}	volumeResponse
//	@Failure		400		{object}	errResponse	"Invalid request"
//	@Failure		404		{object}	errResponse	"Volume not found"
//	@Failure		409		{object}	errResponse	"Invalid FSM transition"
//	@Failure		500		{object}	errResponse
//	@Router			/api/v1/volumes/{name}/attach [put]
func (c *httpController) attachVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := c.tracer.Start(r.Context(), "handler.AttachVolume")
	defer span.End()
	c.recordOp(ctx, "attach")

	name := chi.URLParam(r, "name")

	var req attachVolumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.NodeID == "" {
		writeErr(w, http.StatusBadRequest, "node_id is required")
		return
	}

	if err := c.app.AttachVolume(ctx, name, req.NodeID); err != nil {
		writeErr(w, mapAppError(err), err.Error())
		return
	}

	vol, err := c.app.GetVolume(ctx, name)
	if err != nil {
		writeErr(w, mapAppError(err), err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, toResponse(vol))
}

// detachVolume godoc
//
//	@Summary		Detach a volume from its node
//	@Description	Detaches an attached volume. Transition: ATTACHED → DETACHING → AVAILABLE (async).
//	@Tags			volumes
//	@Produce		json
//	@Param			name	path		string	true	"Volume name"
//	@Success		202		{object}	volumeResponse
//	@Failure		404		{object}	errResponse	"Volume not found"
//	@Failure		409		{object}	errResponse	"Invalid FSM transition"
//	@Failure		500		{object}	errResponse
//	@Router			/api/v1/volumes/{name}/detach [put]
func (c *httpController) detachVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := c.tracer.Start(r.Context(), "handler.DetachVolume")
	defer span.End()
	c.recordOp(ctx, "detach")

	name := chi.URLParam(r, "name")

	if err := c.app.DetachVolume(ctx, name); err != nil {
		writeErr(w, mapAppError(err), err.Error())
		return
	}

	vol, err := c.app.GetVolume(ctx, name)
	if err != nil {
		writeErr(w, mapAppError(err), err.Error())
		return
	}

	writeJSON(w, http.StatusAccepted, toResponse(vol))
}

// deleteVolume godoc
//
//	@Summary		Delete a volume
//	@Description	Deletes an available volume. Transition: AVAILABLE → DELETING → DELETED (async). Returns 409 if attached.
//	@Tags			volumes
//	@Produce		json
//	@Param			name	path	string	true	"Volume name"
//	@Success		202		"Accepted"
//	@Failure		404		{object}	errResponse	"Volume not found"
//	@Failure		409		{object}	errResponse	"Invalid FSM transition (e.g. volume is attached)"
//	@Failure		500		{object}	errResponse
//	@Router			/api/v1/volumes/{name} [delete]
func (c *httpController) deleteVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := c.tracer.Start(r.Context(), "handler.DeleteVolume")
	defer span.End()
	c.recordOp(ctx, "delete")

	name := chi.URLParam(r, "name")

	if err := c.app.DeleteVolume(ctx, name); err != nil {
		writeErr(w, mapAppError(err), err.Error())
		return
	}

	w.WriteHeader(http.StatusAccepted)
}

// reconcileVolume godoc
//
//	@Summary		Reconcile a volume with backend state
//	@Description	Aligns the FSM state with the real backend state. Only valid when the volume is in the error state.
//	@Description	Decision logic: backend unavailable → 503; volume absent → pending; volume attached → attached; volume present → available.
//	@Description	Returns 409 if the volume is not in the error state.
//	@Tags			volumes
//	@Produce		json
//	@Param			name	path		string	true	"Volume name"
//	@Success		200		{object}	volumeResponse
//	@Failure		404		{object}	errResponse	"Volume not found"
//	@Failure		409		{object}	errResponse	"Volume not in error state"
//	@Failure		503		{object}	errResponse	"Backend unavailable"
//	@Failure		500		{object}	errResponse
//	@Router			/api/v1/volumes/{name}/reconcile [post]
func (c *httpController) reconcileVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := c.tracer.Start(r.Context(), "handler.ReconcileVolume")
	defer span.End()
	c.recordOp(ctx, "reconcile")

	name := chi.URLParam(r, "name")

	vol, err := c.app.ReconcileVolume(ctx, name)
	if err != nil {
		writeErr(w, mapAppError(err), err.Error())
		return
	}

	writeJSON(w, http.StatusOK, toResponse(vol))
}

// streamVolumeEvents godoc
//
//	@Summary		Stream volume state changes
//	@Description	Server-Sent Events stream of FSM state transitions. Closes automatically when the volume reaches a terminal state (available, attached, deleted, error).
//	@Tags			volumes
//	@Produce		text/event-stream
//	@Param			name	path	string	true	"Volume name"
//	@Success		200
//	@Failure		404	{object}	errResponse	"Volume not found"
//	@Failure		500	{object}	errResponse
//	@Router			/api/v1/volumes/{name}/events [get]
func (c *httpController) streamVolumeEvents(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")

	ch, err := c.app.Subscribe(r.Context(), name)
	if err != nil {
		writeErr(w, mapAppError(err), err.Error())
		return
	}
	defer c.app.Unsubscribe(name, ch)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	terminal := map[string]bool{
		StateAvailable: true,
		StateAttached:  true,
		StateDeleted:   true,
		StateError:     true,
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}

			data, _ := json.Marshal(event)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()

			if terminal[event.State] {
				fmt.Fprintf(w, "event: done\ndata: %s\n\n", data)
				flusher.Flush()
				return
			}
		}
	}
}

// healthz godoc
//
//	@Summary		Health check
//	@Description	Returns the health status of the API and storage backend.
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	healthResponse
//	@Failure		503	{object}	healthResponse	"Backend degraded"
//	@Router			/healthz [get]
func (c *httpController) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, span := c.tracer.Start(r.Context(), "handler.Healthz")
	defer span.End()

	status := "ok"
	httpStatus := http.StatusOK

	if err := c.app.HealthCheck(ctx); err != nil {
		status = "degraded"
		httpStatus = http.StatusServiceUnavailable
	}

	writeJSON(w, httpStatus, healthResponse{
		Status:  status,
		Backend: c.backendName,
		Version: apiVersion,
	})
}

// -- middleware --------------------------------------------------------------

type ctxKey string

const loggerCtxKey ctxKey = "logger"

func withLogger(ctx context.Context, l LoggerDependency) context.Context {
	return context.WithValue(ctx, loggerCtxKey, l)
}

// requestLogger logs each request's method, path, status, and duration.
// It enriches the logger with trace_id and injects it into the request context
// so that downstream application code (including retry goroutines) can use it.
// Must run after the OTel middleware so the span (and trace ID) already exist.
func (c *httpController) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		spanCtx := trace.SpanFromContext(r.Context()).SpanContext()
		traceID := ""
		if spanCtx.IsValid() {
			traceID = spanCtx.TraceID().String()
		}

		// Enrich the logger with trace_id so every downstream log carries it.
		// *slog.Logger implements LoggerDependency, so the type assertion is safe
		// when setup.go passes rr.logger (*slog.Logger) as the dependency.
		enriched := c.logger
		if sl, ok := c.logger.(*slog.Logger); ok {
			enriched = sl.With("trace_id", traceID, "method", r.Method, "path", r.URL.Path)
		}

		ctx := withLogger(r.Context(), enriched)
		r = r.WithContext(ctx)

		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)

		c.logger.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"trace_id", traceID,
		)
	})
}

// recoverer catches panics, logs the stack trace, and returns 500.
func (c *httpController) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				loggerFromCtx(r.Context(), c.logger).Error("panic recovered",
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// responseWriter captures the HTTP status code written by downstream handlers.
type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}
