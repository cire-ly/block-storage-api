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
	ID        string `json:"id"                  example:"550e8400-e29b-41d4-a716-446655440000"`
	Name      string `json:"name"                example:"vol-01"`
	SizeMB    int    `json:"size_mb"             example:"1024"`
	State     string `json:"state"               example:"available"`
	Backend   string `json:"backend"             example:"mock"`
	NodeID    string `json:"node_id,omitempty"   example:"node-paris-01"`
	CreatedAt string `json:"created_at"          example:"2024-01-01T00:00:00Z"`
	UpdatedAt string `json:"updated_at"          example:"2024-01-01T00:00:00Z"`
}

// listResponse wraps a slice of volumes with a count.
// @Description List of volumes
type listResponse struct {
	Volumes []*volumeResponse `json:"volumes"`
	Count   int               `json:"count" example:"1"`
}

// healthResponse is returned by GET /healthz.
// @Description Health check response
type healthResponse struct {
	Status          string `json:"status"           example:"ok"`
	Backend         string `json:"backend"          example:"mock"`
	ConsistencyMode string `json:"consistency_mode" example:"cp"`
	Version         string `json:"version"          example:"1.0.0"`
}

// errResponse wraps an error message.
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

// CreateVolume godoc
//
//	@Summary		Create a volume
//	@Description	Creates a new block storage volume. The volume goes through PENDING → CREATING → AVAILABLE states.
//	@Tags			volumes
//	@Accept			json
//	@Produce		json
//	@Param			body	body		createVolumeRequest	true	"Volume creation parameters"
//	@Success		201		{object}	volumeResponse
//	@Failure		400		{object}	errResponse	"Invalid request"
//	@Failure		409		{object}	errResponse	"Volume already exists"
//	@Failure		500		{object}	errResponse	"Internal error"
//	@Router			/api/v1/volumes [post]
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

// ListVolumes godoc
//
//	@Summary		List all volumes
//	@Description	Returns all volumes managed by the storage backend.
//	@Tags			volumes
//	@Produce		json
//	@Success		200	{object}	listResponse
//	@Failure		500	{object}	errResponse
//	@Router			/api/v1/volumes [get]
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

// GetVolume godoc
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

// AttachVolume godoc
//
//	@Summary		Attach a volume to a node
//	@Description	Attaches an available volume to a compute node. Transition: AVAILABLE → ATTACHING → ATTACHED.
//	@Tags			volumes
//	@Accept			json
//	@Produce		json
//	@Param			name	path		string				true	"Volume name"
//	@Param			body	body		attachVolumeRequest	true	"Node to attach to"
//	@Success		200		{object}	volumeResponse
//	@Failure		400		{object}	errResponse	"Invalid request"
//	@Failure		404		{object}	errResponse	"Volume not found"
//	@Failure		409		{object}	errResponse	"Invalid FSM transition"
//	@Failure		500		{object}	errResponse
//	@Router			/api/v1/volumes/{name}/attach [put]
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

// DetachVolume godoc
//
//	@Summary		Detach a volume from its node
//	@Description	Detaches an attached volume. Transition: ATTACHED → DETACHING → AVAILABLE.
//	@Tags			volumes
//	@Produce		json
//	@Param			name	path		string	true	"Volume name"
//	@Success		200		{object}	volumeResponse
//	@Failure		404		{object}	errResponse	"Volume not found"
//	@Failure		409		{object}	errResponse	"Invalid FSM transition"
//	@Failure		500		{object}	errResponse
//	@Router			/api/v1/volumes/{name}/detach [put]
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

// DeleteVolume godoc
//
//	@Summary		Delete a volume
//	@Description	Deletes an available volume. Transition: AVAILABLE → DELETING → DELETED. Returns 409 if volume is attached.
//	@Tags			volumes
//	@Produce		json
//	@Param			name	path	string	true	"Volume name"
//	@Success		204		"No content"
//	@Failure		404		{object}	errResponse	"Volume not found"
//	@Failure		409		{object}	errResponse	"Invalid FSM transition (e.g. volume is attached)"
//	@Failure		500		{object}	errResponse
//	@Router			/api/v1/volumes/{name} [delete]
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

// Healthz godoc
//
//	@Summary		Health check
//	@Description	Returns the health status of the API and the storage backend. Returns 503 in CP mode if the backend is degraded.
//	@Tags			system
//	@Produce		json
//	@Success		200	{object}	healthResponse
//	@Failure		503	{object}	healthResponse	"Backend degraded (CP mode)"
//	@Router			/healthz [get]
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
