package api_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"go.opentelemetry.io/otel"

	"github.com/cire-ly/block-storage-api/api"
	"github.com/cire-ly/block-storage-api/storage/mock"
)

func newTestRouter() http.Handler {
	backend := mock.New("cp")
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	tracer := otel.Tracer("test")
	meter := otel.Meter("test")
	return api.NewRouter(backend, logger, tracer, meter)
}

func TestCreateVolume(t *testing.T) {
	r := newTestRouter()

	body := `{"name":"vol-test","size_mb":100}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/volumes", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body: %s", w.Code, w.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp["name"] != "vol-test" {
		t.Errorf("name = %v, want vol-test", resp["name"])
	}
	if resp["state"] != "available" {
		t.Errorf("state = %v, want available", resp["state"])
	}
}

func TestCreateVolumeDuplicate(t *testing.T) {
	r := newTestRouter()

	body := `{"name":"dup","size_mb":10}`
	do := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/volumes", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	if w := do(); w.Code != http.StatusCreated {
		t.Fatalf("first create: %d", w.Code)
	}
	if w := do(); w.Code != http.StatusConflict {
		t.Errorf("duplicate: status = %d, want 409", w.Code)
	}
}

func TestCreateVolumeValidation(t *testing.T) {
	r := newTestRouter()

	cases := []struct {
		body   string
		status int
	}{
		{`{"name":"","size_mb":10}`, http.StatusBadRequest},
		{`{"name":"x","size_mb":0}`, http.StatusBadRequest},
		{`{"name":"x","size_mb":-1}`, http.StatusBadRequest},
		{`not-json`, http.StatusBadRequest},
	}

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/volumes", bytes.NewBufferString(c.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != c.status {
			t.Errorf("body=%q: status = %d, want %d", c.body, w.Code, c.status)
		}
	}
}

func TestListVolumes(t *testing.T) {
	r := newTestRouter()

	// Create two volumes.
	for _, name := range []string{"la", "lb"} {
		body := `{"name":"` + name + `","size_mb":5}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/volumes", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(httptest.NewRecorder(), req)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/volumes", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if count, ok := resp["count"].(float64); !ok || int(count) != 2 {
		t.Errorf("count = %v, want 2", resp["count"])
	}
}

func TestGetVolumeNotFound(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/volumes/ghost", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestAttachDetachDelete(t *testing.T) {
	r := newTestRouter()

	// Create
	req := httptest.NewRequest(http.MethodPost, "/api/v1/volumes",
		bytes.NewBufferString(`{"name":"v1","size_mb":10}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatal("create failed:", w.Body.String())
	}

	// Attach
	req = httptest.NewRequest(http.MethodPut, "/api/v1/volumes/v1/attach",
		bytes.NewBufferString(`{"node_id":"node-42"}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatal("attach failed:", w.Body.String())
	}

	// Delete while attached should be 409.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/volumes/v1", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Errorf("delete while attached: status = %d, want 409", w.Code)
	}

	// Detach
	req = httptest.NewRequest(http.MethodPut, "/api/v1/volumes/v1/detach", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatal("detach failed:", w.Body.String())
	}

	// Delete
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/volumes/v1", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("delete: status = %d, want 204", w.Code)
	}
}

func TestHealthz(t *testing.T) {
	r := newTestRouter()

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "ok" {
		t.Errorf("status = %v, want ok", resp["status"])
	}
	if resp["backend"] != "mock" {
		t.Errorf("backend = %v, want mock", resp["backend"])
	}
}
