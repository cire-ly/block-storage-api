package volume

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/cire-ly/block-storage-api/storage"
)

// -- fake application --------------------------------------------------------

type fakeApp struct {
	volumes      map[string]*storage.Volume
	createErr    error
	deleteErr    error
	attachErr    error
	detachErr    error
	reconcileErr error
	healthErr    error
}

func newFakeApp() *fakeApp {
	return &fakeApp{volumes: make(map[string]*storage.Volume)}
}

func (a *fakeApp) CreateVolume(_ context.Context, name string, sizeMB int) (*storage.Volume, error) {
	if a.createErr != nil {
		return nil, a.createErr
	}
	v := &storage.Volume{ID: "id-1", Name: name, SizeMB: sizeMB, State: StateAvailable, Backend: "mock"}
	a.volumes[name] = v
	return v, nil
}

func (a *fakeApp) DeleteVolume(_ context.Context, name string) error {
	if a.deleteErr != nil {
		return a.deleteErr
	}
	delete(a.volumes, name)
	return nil
}

func (a *fakeApp) ListVolumes(_ context.Context) ([]*storage.Volume, error) {
	out := make([]*storage.Volume, 0, len(a.volumes))
	for _, v := range a.volumes {
		out = append(out, v)
	}
	return out, nil
}

func (a *fakeApp) GetVolume(_ context.Context, name string) (*storage.Volume, error) {
	v, ok := a.volumes[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}
	return v, nil
}

func (a *fakeApp) AttachVolume(_ context.Context, name string, nodeID string) error {
	if a.attachErr != nil {
		return a.attachErr
	}
	v, ok := a.volumes[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}
	v.NodeID = nodeID
	v.State = StateAttached
	return nil
}

func (a *fakeApp) DetachVolume(_ context.Context, name string) error {
	if a.detachErr != nil {
		return a.detachErr
	}
	v, ok := a.volumes[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}
	v.NodeID = ""
	v.State = StateAvailable
	return nil
}

func (a *fakeApp) ReconcileVolume(_ context.Context, name string) (*storage.Volume, error) {
	if a.reconcileErr != nil {
		return nil, a.reconcileErr
	}
	v, ok := a.volumes[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}
	v.State = StateAvailable
	return v, nil
}

func (a *fakeApp) HealthCheck(_ context.Context) error { return a.healthErr }

func (a *fakeApp) Subscribe(_ context.Context, name string) (<-chan VolumeStateEvent, error) {
	if _, ok := a.volumes[name]; !ok {
		return nil, fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}
	ch := make(chan VolumeStateEvent, 1)
	return ch, nil
}

func (a *fakeApp) Unsubscribe(_ string, ch <-chan VolumeStateEvent) {}

// -- helpers -----------------------------------------------------------------

func newTestRouter(app ApplicationContract) *chi.Mux {
	ctrl := newHTTPController(app, fakeLogger{}, noop.NewTracerProvider().Tracer("test"), nil)
	r := chi.NewMux()
	ctrl.registerRoutes(r)
	return r
}

func httpDo(t *testing.T, r http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func decodeResp(t *testing.T, rr *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.NewDecoder(rr.Body).Decode(dst); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

// -- tests -------------------------------------------------------------------

func TestHTTPCreateVolume(t *testing.T) {
	app := newFakeApp()
	r := newTestRouter(app)

	rr := httpDo(t, r, http.MethodPost, "/api/v1/volumes",
		map[string]any{"name": "vol-01", "size_mb": 100})
	if rr.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rr.Code)
	}
	var resp volumeResponse
	decodeResp(t, rr, &resp)
	if resp.Name != "vol-01" {
		t.Errorf("Name = %q, want vol-01", resp.Name)
	}
}

func TestHTTPCreateVolumeMissingName(t *testing.T) {
	rr := httpDo(t, newTestRouter(newFakeApp()), http.MethodPost, "/api/v1/volumes",
		map[string]any{"size_mb": 100})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHTTPCreateVolumeInvalidSize(t *testing.T) {
	rr := httpDo(t, newTestRouter(newFakeApp()), http.MethodPost, "/api/v1/volumes",
		map[string]any{"name": "v", "size_mb": 0})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHTTPCreateVolumeInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/volumes", bytes.NewBufferString("bad"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	newTestRouter(newFakeApp()).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHTTPCreateVolumeConflict(t *testing.T) {
	app := newFakeApp()
	app.createErr = ErrVolumeExists
	rr := httpDo(t, newTestRouter(app), http.MethodPost, "/api/v1/volumes",
		map[string]any{"name": "v", "size_mb": 10})
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

func TestHTTPListVolumes(t *testing.T) {
	app := newFakeApp()
	app.volumes["v1"] = &storage.Volume{ID: "1", Name: "v1"}
	app.volumes["v2"] = &storage.Volume{ID: "2", Name: "v2"}

	rr := httpDo(t, newTestRouter(app), http.MethodGet, "/api/v1/volumes", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	var resp listResponse
	decodeResp(t, rr, &resp)
	if resp.Count != 2 {
		t.Errorf("Count = %d, want 2", resp.Count)
	}
}

func TestHTTPGetVolume(t *testing.T) {
	app := newFakeApp()
	app.volumes["vol-01"] = &storage.Volume{ID: "id-1", Name: "vol-01", SizeMB: 100}

	rr := httpDo(t, newTestRouter(app), http.MethodGet, "/api/v1/volumes/vol-01", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	var resp volumeResponse
	decodeResp(t, rr, &resp)
	if resp.Name != "vol-01" {
		t.Errorf("Name = %q, want vol-01", resp.Name)
	}
}

func TestHTTPGetVolumeNotFound(t *testing.T) {
	rr := httpDo(t, newTestRouter(newFakeApp()), http.MethodGet, "/api/v1/volumes/ghost", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestHTTPAttachVolume(t *testing.T) {
	app := newFakeApp()
	app.volumes["vol-01"] = &storage.Volume{ID: "id-1", Name: "vol-01", State: StateAvailable}

	rr := httpDo(t, newTestRouter(app), http.MethodPut, "/api/v1/volumes/vol-01/attach",
		map[string]any{"node_id": "node-1"})
	if rr.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rr.Code)
	}
	var resp volumeResponse
	decodeResp(t, rr, &resp)
	if resp.NodeID != "node-1" {
		t.Errorf("NodeID = %q, want node-1", resp.NodeID)
	}
}

func TestHTTPAttachVolumeMissingNodeID(t *testing.T) {
	rr := httpDo(t, newTestRouter(newFakeApp()), http.MethodPut, "/api/v1/volumes/v/attach",
		map[string]any{})
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHTTPAttachVolumeInvalidJSON(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "/api/v1/volumes/v/attach", bytes.NewBufferString("bad"))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	newTestRouter(newFakeApp()).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestHTTPAttachVolumeConflict(t *testing.T) {
	app := newFakeApp()
	app.attachErr = ErrInvalidTransition
	rr := httpDo(t, newTestRouter(app), http.MethodPut, "/api/v1/volumes/v/attach",
		map[string]any{"node_id": "n"})
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

func TestHTTPDetachVolume(t *testing.T) {
	app := newFakeApp()
	app.volumes["vol-01"] = &storage.Volume{ID: "id-1", Name: "vol-01", State: StateAttached, NodeID: "n"}

	rr := httpDo(t, newTestRouter(app), http.MethodPut, "/api/v1/volumes/vol-01/detach", nil)
	if rr.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rr.Code)
	}
}

func TestHTTPDetachVolumeNotFound(t *testing.T) {
	app := newFakeApp()
	app.detachErr = ErrVolumeNotFound
	rr := httpDo(t, newTestRouter(app), http.MethodPut, "/api/v1/volumes/ghost/detach", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestHTTPDeleteVolume(t *testing.T) {
	app := newFakeApp()
	app.volumes["vol-01"] = &storage.Volume{ID: "id-1", Name: "vol-01"}

	rr := httpDo(t, newTestRouter(app), http.MethodDelete, "/api/v1/volumes/vol-01", nil)
	if rr.Code != http.StatusAccepted {
		t.Errorf("status = %d, want 202", rr.Code)
	}
}

func TestHTTPDeleteVolumeNotFound(t *testing.T) {
	app := newFakeApp()
	app.deleteErr = ErrVolumeNotFound
	rr := httpDo(t, newTestRouter(app), http.MethodDelete, "/api/v1/volumes/ghost", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestHTTPDeleteVolumeConflict(t *testing.T) {
	app := newFakeApp()
	app.deleteErr = ErrInvalidTransition
	rr := httpDo(t, newTestRouter(app), http.MethodDelete, "/api/v1/volumes/v", nil)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

func TestHTTPReconcileVolume(t *testing.T) {
	app := newFakeApp()
	app.volumes["vol-01"] = &storage.Volume{ID: "id-1", Name: "vol-01", State: StateError}

	rr := httpDo(t, newTestRouter(app), http.MethodPost, "/api/v1/volumes/vol-01/reconcile", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	var resp volumeResponse
	decodeResp(t, rr, &resp)
	if resp.State != StateAvailable {
		t.Errorf("State = %q, want available", resp.State)
	}
}

func TestHTTPReconcileVolumeNotFound(t *testing.T) {
	app := newFakeApp()
	app.reconcileErr = ErrVolumeNotFound
	rr := httpDo(t, newTestRouter(app), http.MethodPost, "/api/v1/volumes/ghost/reconcile", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestHTTPReconcileVolumeConflict(t *testing.T) {
	app := newFakeApp()
	app.reconcileErr = ErrInvalidTransition
	rr := httpDo(t, newTestRouter(app), http.MethodPost, "/api/v1/volumes/v/reconcile", nil)
	if rr.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rr.Code)
	}
}

func TestHTTPReconcileVolumeBackendUnavailable(t *testing.T) {
	app := newFakeApp()
	app.reconcileErr = ErrBackendUnavailable
	rr := httpDo(t, newTestRouter(app), http.MethodPost, "/api/v1/volumes/v/reconcile", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
}

func TestHTTPHealthzOK(t *testing.T) {
	rr := httpDo(t, newTestRouter(newFakeApp()), http.MethodGet, "/healthz", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rr.Code)
	}
	var resp healthResponse
	decodeResp(t, rr, &resp)
	if resp.Status != "ok" {
		t.Errorf("Status = %q, want ok", resp.Status)
	}
}

func TestHTTPHealthzDegraded(t *testing.T) {
	app := newFakeApp()
	app.healthErr = errors.New("backend down")
	rr := httpDo(t, newTestRouter(app), http.MethodGet, "/healthz", nil)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rr.Code)
	}
	var resp healthResponse
	decodeResp(t, rr, &resp)
	if resp.Status != "degraded" {
		t.Errorf("Status = %q, want degraded", resp.Status)
	}
}
