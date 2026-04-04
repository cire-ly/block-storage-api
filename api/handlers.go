package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/cire-ly/block-storage-api/storage"
	"github.com/cire-ly/block-storage-api/storage/mock"
)

const version = "1.0.0"

// Handler holds the dependencies for all HTTP handlers.
type Handler struct {
	backend storage.VolumeBackend
	logger  *slog.Logger
	tracer  trace.Tracer
	opTotal metric.Int64Counter
}

// -- request / response types ------------------------------------------------

type createVolumeRequest struct {
	Name   string `json:"name"`
	SizeMB int    `json:"size_mb"`
}

type attachVolumeRequest struct {
	NodeID string `json:"node_id"`
}

type volumeResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	SizeMB    int    `json:"size_mb"`
	State     string `json:"state"`
	Backend   string `json:"backend"`
	NodeID    string `json:"node_id,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type listResponse struct {
	Volumes []*volumeResponse `json:"volumes"`
	Count   int               `json:"count"`
}

type healthResponse struct {
	Status          string `json:"status"`
	Backend         string `json:"backend"`
	ConsistencyMode string `json:"consistency_mode"`
	Version         string `json:"version"`
}

type errResponse struct {
	Error string `json:"error"`
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

func (h *Handler) setAPHeaders(w http.ResponseWriter) {
	if h.backend.ConsistencyMode() == "ap" {
		w.Header().Set("X-Data-Staleness", "true")
		w.Header().Set("X-Data-Timestamp", "now")
	}
}

func (h *Handler) recordOp(ctx context.Context, op string) {
	if h.opTotal != nil {
		h.opTotal.Add(ctx, 1, metric.WithAttributes(attribute.String("operation", op)))
	}
}

// -- handlers ----------------------------------------------------------------

// POST /api/v1/volumes
func (h *Handler) CreateVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := h.tracer.Start(r.Context(), "handler.CreateVolume")
	defer span.End()
	h.recordOp(ctx, "create")

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

	vol, err := h.backend.CreateVolume(ctx, req.Name, req.SizeMB)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, mock.ErrVolumeExists) {
			status = http.StatusConflict
		} else if errors.Is(err, mock.ErrInvalidSize) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, toResponse(vol))
}

// GET /api/v1/volumes
func (h *Handler) ListVolumes(w http.ResponseWriter, r *http.Request) {
	ctx, span := h.tracer.Start(r.Context(), "handler.ListVolumes")
	defer span.End()
	h.recordOp(ctx, "list")
	h.setAPHeaders(w)

	volumes, err := h.backend.ListVolumes(ctx)
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

// GET /api/v1/volumes/{name}
func (h *Handler) GetVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := h.tracer.Start(r.Context(), "handler.GetVolume")
	defer span.End()
	h.recordOp(ctx, "get")
	h.setAPHeaders(w)

	name := r.PathValue("name")
	vol, err := h.backend.GetVolume(ctx, name)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, mock.ErrVolumeNotFound) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toResponse(vol))
}

// PUT /api/v1/volumes/{name}/attach
func (h *Handler) AttachVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := h.tracer.Start(r.Context(), "handler.AttachVolume")
	defer span.End()
	h.recordOp(ctx, "attach")

	name := r.PathValue("name")

	var req attachVolumeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.NodeID == "" {
		writeErr(w, http.StatusBadRequest, "node_id is required")
		return
	}

	if err := h.backend.AttachVolume(ctx, name, req.NodeID); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, mock.ErrVolumeNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, mock.ErrInvalidTransition) {
			status = http.StatusConflict
		}
		writeErr(w, status, err.Error())
		return
	}

	vol, _ := h.backend.GetVolume(ctx, name)
	writeJSON(w, http.StatusOK, toResponse(vol))
}

// PUT /api/v1/volumes/{name}/detach
func (h *Handler) DetachVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := h.tracer.Start(r.Context(), "handler.DetachVolume")
	defer span.End()
	h.recordOp(ctx, "detach")

	name := r.PathValue("name")

	if err := h.backend.DetachVolume(ctx, name); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, mock.ErrVolumeNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, mock.ErrInvalidTransition) {
			status = http.StatusConflict
		}
		writeErr(w, status, err.Error())
		return
	}

	vol, _ := h.backend.GetVolume(ctx, name)
	writeJSON(w, http.StatusOK, toResponse(vol))
}

// DELETE /api/v1/volumes/{name}
func (h *Handler) DeleteVolume(w http.ResponseWriter, r *http.Request) {
	ctx, span := h.tracer.Start(r.Context(), "handler.DeleteVolume")
	defer span.End()
	h.recordOp(ctx, "delete")

	name := r.PathValue("name")

	if err := h.backend.DeleteVolume(ctx, name); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, mock.ErrVolumeNotFound) {
			status = http.StatusNotFound
		} else if errors.Is(err, mock.ErrInvalidTransition) {
			status = http.StatusConflict
		}
		writeErr(w, status, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GET /healthz
func (h *Handler) Healthz(w http.ResponseWriter, r *http.Request) {
	ctx, span := h.tracer.Start(r.Context(), "handler.Healthz")
	defer span.End()

	status := "ok"
	httpStatus := http.StatusOK

	if h.backend.ConsistencyMode() == "cp" {
		if err := h.backend.HealthCheck(ctx); err != nil {
			status = "degraded"
			httpStatus = http.StatusServiceUnavailable
		}
	}

	writeJSON(w, httpStatus, healthResponse{
		Status:          status,
		Backend:         h.backend.BackendName(),
		ConsistencyMode: h.backend.ConsistencyMode(),
		Version:         version,
	})
}
