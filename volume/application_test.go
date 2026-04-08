package volume

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/cire-ly/block-storage-api/storage"
)

// -- fake dependencies -------------------------------------------------------

// fakeDB is an in-memory DatabaseDependency for tests.
type fakeDB struct {
	mu      sync.RWMutex
	volumes map[string]*storage.Volume
	events  []VolumeEvent
	saveErr error
	loadErr error
	listErr error
}

func newFakeDB() *fakeDB {
	return &fakeDB{volumes: make(map[string]*storage.Volume)}
}

func (d *fakeDB) SaveVolume(_ context.Context, v *storage.Volume) error {
	if d.saveErr != nil {
		return d.saveErr
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.volumes[v.Name]; exists {
		return fmt.Errorf("duplicate name %q", v.Name)
	}
	cp := *v
	d.volumes[v.Name] = &cp
	return nil
}

func (d *fakeDB) UpdateVolume(_ context.Context, v *storage.Volume) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	cp := *v
	d.volumes[v.Name] = &cp
	return nil
}

func (d *fakeDB) LoadVolume(_ context.Context, name string) (*storage.Volume, error) {
	if d.loadErr != nil {
		return nil, d.loadErr
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	v, ok := d.volumes[name]
	if !ok {
		return nil, nil
	}
	cp := *v
	return &cp, nil
}

func (d *fakeDB) ListVolumes(_ context.Context) ([]*storage.Volume, error) {
	if d.listErr != nil {
		return nil, d.listErr
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]*storage.Volume, 0, len(d.volumes))
	for _, v := range d.volumes {
		cp := *v
		out = append(out, &cp)
	}
	return out, nil
}

func (d *fakeDB) ListVolumesByState(_ context.Context, states ...string) ([]*storage.Volume, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	want := make(map[string]bool)
	for _, s := range states {
		want[s] = true
	}
	var out []*storage.Volume
	for _, v := range d.volumes {
		if want[v.State] {
			cp := *v
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (d *fakeDB) SaveEvent(_ context.Context, e VolumeEvent) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, e)
	return nil
}

func (d *fakeDB) eventCount() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.events)
}

// fakeBackend is a StorageBackendDependency for tests.
type fakeBackend struct {
	mu        sync.RWMutex
	volumes   map[string]*storage.Volume
	createErr error
	deleteErr error
	attachErr error
	detachErr error
	healthErr error
	getErr    error // non-nil simulates backend unreachable (distinct from not-found)
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{volumes: make(map[string]*storage.Volume)}
}

func (b *fakeBackend) CreateVolume(_ context.Context, name string, sizeMB int) (*storage.Volume, error) {
	if b.createErr != nil {
		return nil, b.createErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	v := &storage.Volume{Name: name, SizeMB: sizeMB, State: storage.StateAvailable}
	b.volumes[name] = v
	return v, nil
}

func (b *fakeBackend) DeleteVolume(_ context.Context, name string) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.volumes, name)
	return nil
}

func (b *fakeBackend) ListVolumes(_ context.Context) ([]*storage.Volume, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*storage.Volume, 0, len(b.volumes))
	for _, v := range b.volumes {
		cp := *v
		out = append(out, &cp)
	}
	return out, nil
}

// GetVolume returns nil, nil when the volume is not in the map (not-found),
// and nil, getErr when getErr is set (backend unreachable).
func (b *fakeBackend) GetVolume(_ context.Context, name string) (*storage.Volume, error) {
	if b.getErr != nil {
		return nil, b.getErr
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	v, ok := b.volumes[name]
	if !ok {
		return nil, nil
	}
	cp := *v
	return &cp, nil
}

func (b *fakeBackend) AttachVolume(_ context.Context, _ string, _ string) error { return b.attachErr }
func (b *fakeBackend) DetachVolume(_ context.Context, _ string) error           { return b.detachErr }
func (b *fakeBackend) HealthCheck(_ context.Context) error                      { return b.healthErr }
func (b *fakeBackend) BackendName() string                                      { return "fake" }
func (b *fakeBackend) Close(_ context.Context) error                            { return nil }

// fakeLogger is a no-op LoggerDependency.
type fakeLogger struct{}

func (fakeLogger) Debug(_ string, _ ...any) {}
func (fakeLogger) Info(_ string, _ ...any)  {}
func (fakeLogger) Warn(_ string, _ ...any)  {}
func (fakeLogger) Error(_ string, _ ...any) {}

func newApp(db *fakeDB, backend StorageBackendDependency) *application {
	var wg sync.WaitGroup
	return newApplication(backend, db, fakeLogger{}, noop.NewTracerProvider().Tracer("test"), DefaultRetryPolicy(), &wg, context.Background())
}

// waitState polls the DB until volume.State == want or timeout.
func waitState(t *testing.T, db *fakeDB, name, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		v, _ := db.LoadVolume(context.Background(), name)
		if v != nil && v.State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	v, _ := db.LoadVolume(context.Background(), name)
	state := "<nil>"
	if v != nil {
		state = v.State
	}
	t.Errorf("volume %q: state = %q after %v, want %q", name, state, timeout, want)
}

// -- tests -------------------------------------------------------------------

func TestCreateVolumeSuccess(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	app := newApp(db, newFakeBackend())

	vol, err := app.CreateVolume(ctx, "vol-01", 100)
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if vol.Name != "vol-01" {
		t.Errorf("Name = %q, want vol-01", vol.Name)
	}
	if vol.SizeMB != 100 {
		t.Errorf("SizeMB = %d, want 100", vol.SizeMB)
	}
	// Immediately after create the volume is in "creating" state (async transitions to available).
	if vol.State != StateCreating {
		t.Errorf("initial State = %q, want creating", vol.State)
	}

	// Wait for async backend create + ready transition.
	waitState(t, db, "vol-01", StateAvailable, 2*time.Second)
}

func TestCreateVolumeInvalidSize(t *testing.T) {
	ctx := context.Background()
	app := newApp(newFakeDB(), newFakeBackend())

	_, err := app.CreateVolume(ctx, "vol", 0)
	if !errors.Is(err, ErrInvalidSize) {
		t.Errorf("expected ErrInvalidSize, got %v", err)
	}

	_, err = app.CreateVolume(ctx, "vol", -1)
	if !errors.Is(err, ErrInvalidSize) {
		t.Errorf("expected ErrInvalidSize for negative, got %v", err)
	}
}

func TestCreateVolumeDuplicate(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	app := newApp(db, newFakeBackend())

	if _, err := app.CreateVolume(ctx, "dup", 10); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Wait for it to land in DB.
	waitState(t, db, "dup", StateAvailable, 2*time.Second)

	_, err := app.CreateVolume(ctx, "dup", 20)
	if !errors.Is(err, ErrVolumeExists) {
		t.Errorf("expected ErrVolumeExists, got %v", err)
	}
}

func TestCreateVolumeLoadError(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	db.loadErr = errors.New("db down")
	app := newApp(db, newFakeBackend())

	_, err := app.CreateVolume(ctx, "vol", 10)
	if err == nil {
		t.Error("expected error when db.LoadVolume fails")
	}
}

func TestGetVolumeSuccess(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	app := newApp(db, newFakeBackend())

	if _, err := app.CreateVolume(ctx, "vol", 50); err != nil {
		t.Fatal(err)
	}

	v, err := app.GetVolume(ctx, "vol")
	if err != nil {
		t.Fatalf("GetVolume: %v", err)
	}
	if v.Name != "vol" {
		t.Errorf("Name = %q, want vol", v.Name)
	}
}

func TestGetVolumeNotFound(t *testing.T) {
	ctx := context.Background()
	app := newApp(newFakeDB(), newFakeBackend())

	_, err := app.GetVolume(ctx, "ghost")
	if !errors.Is(err, ErrVolumeNotFound) {
		t.Errorf("expected ErrVolumeNotFound, got %v", err)
	}
}

func TestListVolumes(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	app := newApp(db, newFakeBackend())

	for _, name := range []string{"a", "b", "c"} {
		if _, err := app.CreateVolume(ctx, name, 10); err != nil {
			t.Fatal(err)
		}
	}

	vols, err := app.ListVolumes(ctx)
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(vols) != 3 {
		t.Errorf("expected 3 volumes, got %d", len(vols))
	}
}

func TestListVolumesError(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	db.listErr = errors.New("db down")
	app := newApp(db, newFakeBackend())

	_, err := app.ListVolumes(ctx)
	if err == nil {
		t.Error("expected error when db.ListVolumes fails")
	}
}

func TestDeleteVolumeSuccess(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	app := newApp(db, newFakeBackend())

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}
	waitState(t, db, "vol", StateAvailable, 2*time.Second)

	if err := app.DeleteVolume(ctx, "vol"); err != nil {
		t.Fatalf("DeleteVolume: %v", err)
	}
	// Wait for async backend delete + deleted transition.
	waitState(t, db, "vol", StateDeleted, 2*time.Second)
}

func TestDeleteVolumeNotFound(t *testing.T) {
	ctx := context.Background()
	app := newApp(newFakeDB(), newFakeBackend())

	err := app.DeleteVolume(ctx, "ghost")
	if !errors.Is(err, ErrVolumeNotFound) {
		t.Errorf("expected ErrVolumeNotFound, got %v", err)
	}
}

func TestDeleteVolumeInvalidTransition(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	app := newApp(db, newFakeBackend())

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}
	// Volume is in "creating" state — can't delete from there.
	err := app.DeleteVolume(ctx, "vol")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestAttachVolumeSuccess(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	app := newApp(db, newFakeBackend())

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}
	waitState(t, db, "vol", StateAvailable, 2*time.Second)

	if err := app.AttachVolume(ctx, "vol", "node-1"); err != nil {
		t.Fatalf("AttachVolume: %v", err)
	}
	waitState(t, db, "vol", StateAttached, 2*time.Second)

	v, _ := db.LoadVolume(ctx, "vol")
	if v.NodeID != "node-1" {
		t.Errorf("NodeID = %q, want node-1", v.NodeID)
	}
}

func TestAttachVolumeNotFound(t *testing.T) {
	ctx := context.Background()
	app := newApp(newFakeDB(), newFakeBackend())

	err := app.AttachVolume(ctx, "ghost", "node-1")
	if !errors.Is(err, ErrVolumeNotFound) {
		t.Errorf("expected ErrVolumeNotFound, got %v", err)
	}
}

func TestDetachVolumeSuccess(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	app := newApp(db, newFakeBackend())

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}
	waitState(t, db, "vol", StateAvailable, 2*time.Second)

	if err := app.AttachVolume(ctx, "vol", "node-1"); err != nil {
		t.Fatal(err)
	}
	waitState(t, db, "vol", StateAttached, 2*time.Second)

	if err := app.DetachVolume(ctx, "vol"); err != nil {
		t.Fatalf("DetachVolume: %v", err)
	}
	waitState(t, db, "vol", StateAvailable, 2*time.Second)

	v, _ := db.LoadVolume(ctx, "vol")
	if v.NodeID != "" {
		t.Errorf("NodeID = %q after detach, want empty", v.NodeID)
	}
}

func TestDetachVolumeNotFound(t *testing.T) {
	ctx := context.Background()
	app := newApp(newFakeDB(), newFakeBackend())

	err := app.DetachVolume(ctx, "ghost")
	if !errors.Is(err, ErrVolumeNotFound) {
		t.Errorf("expected ErrVolumeNotFound, got %v", err)
	}
}

func TestReconcileVolumeToAvailable(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	backend := newFakeBackend()
	backend.createErr = errors.New("backend error")
	app := newApp(db, backend)
	app.policy = RetryPolicy{MaxAttempts: 1, InitialWait: 1 * time.Millisecond, Multiplier: 1.0, MaxWait: 1 * time.Millisecond}

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}
	waitState(t, db, "vol", StateError, 3*time.Second)

	// Fix backend: volume now exists and is not attached.
	backend.createErr = nil
	backend.mu.Lock()
	backend.volumes["vol"] = &storage.Volume{Name: "vol", SizeMB: 10, State: storage.StateAvailable}
	backend.mu.Unlock()

	vol, err := app.ReconcileVolume(ctx, "vol")
	if err != nil {
		t.Fatalf("ReconcileVolume: %v", err)
	}
	if vol.State != StateAvailable {
		t.Errorf("State = %q, want available", vol.State)
	}
}

func TestReconcileVolumeToPending(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	backend := newFakeBackend()
	backend.createErr = errors.New("backend error")
	app := newApp(db, backend)
	app.policy = RetryPolicy{MaxAttempts: 1, InitialWait: 1 * time.Millisecond, Multiplier: 1.0, MaxWait: 1 * time.Millisecond}

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}
	waitState(t, db, "vol", StateError, 3*time.Second)

	// Backend has no record of the volume (GetVolume returns nil, nil) — reconcile to pending.
	vol, err := app.ReconcileVolume(ctx, "vol")
	if err != nil {
		t.Fatalf("ReconcileVolume: %v", err)
	}
	if vol.State != StatePending {
		t.Errorf("State = %q, want pending", vol.State)
	}
}

func TestReconcileVolumeToAttached(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	backend := newFakeBackend()
	backend.createErr = errors.New("backend error")
	app := newApp(db, backend)
	app.policy = RetryPolicy{MaxAttempts: 1, InitialWait: 1 * time.Millisecond, Multiplier: 1.0, MaxWait: 1 * time.Millisecond}

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}
	waitState(t, db, "vol", StateError, 3*time.Second)

	// Backend reports volume as attached to node-1.
	backend.mu.Lock()
	backend.volumes["vol"] = &storage.Volume{Name: "vol", SizeMB: 10, State: storage.StateAttached, NodeID: "node-1"}
	backend.mu.Unlock()

	vol, err := app.ReconcileVolume(ctx, "vol")
	if err != nil {
		t.Fatalf("ReconcileVolume: %v", err)
	}
	if vol.State != StateAttached {
		t.Errorf("State = %q, want attached", vol.State)
	}
	if vol.NodeID != "node-1" {
		t.Errorf("NodeID = %q, want node-1", vol.NodeID)
	}
}

func TestReconcileVolumeNotFound(t *testing.T) {
	ctx := context.Background()
	app := newApp(newFakeDB(), newFakeBackend())

	_, err := app.ReconcileVolume(ctx, "ghost")
	if !errors.Is(err, ErrVolumeNotFound) {
		t.Errorf("expected ErrVolumeNotFound, got %v", err)
	}
}

func TestReconcileVolumeInvalidTransition(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	app := newApp(db, newFakeBackend())

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}
	// Volume is in "creating" — reconcile only works from "error".
	_, err := app.ReconcileVolume(ctx, "vol")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

func TestReconcileVolumeBackendUnavailable(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	backend := newFakeBackend()
	backend.createErr = errors.New("backend error")
	app := newApp(db, backend)
	app.policy = RetryPolicy{MaxAttempts: 1, InitialWait: 1 * time.Millisecond, Multiplier: 1.0, MaxWait: 1 * time.Millisecond}

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}
	waitState(t, db, "vol", StateError, 3*time.Second)

	// Simulate backend being unreachable.
	backend.getErr = errors.New("connection refused")

	_, err := app.ReconcileVolume(ctx, "vol")
	if !errors.Is(err, ErrBackendUnavailable) {
		t.Errorf("expected ErrBackendUnavailable, got %v", err)
	}
}

func TestHealthCheckOK(t *testing.T) {
	ctx := context.Background()
	app := newApp(newFakeDB(), newFakeBackend())

	if err := app.HealthCheck(ctx); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestHealthCheckError(t *testing.T) {
	ctx := context.Background()
	backend := newFakeBackend()
	backend.healthErr = errors.New("backend down")
	app := newApp(newFakeDB(), backend)

	if err := app.HealthCheck(ctx); err == nil {
		t.Error("expected error from HealthCheck")
	}
}

func TestRetryOnBackendFailure(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	backend := newFakeBackend()

	// Fail twice, succeed on 3rd attempt — within MaxAttempts.
	callCount := 0
	origCreate := backend.createErr
	_ = origCreate

	// Use a counter via a closure by wrapping via custom backend.
	cb := &countingBackend{
		fakeBackend: backend,
		maxFails:    2,
	}

	app := newApp(db, cb)
	app.policy = RetryPolicy{MaxAttempts: 3, InitialWait: 5 * time.Millisecond, Multiplier: 1.0, MaxWait: 5 * time.Millisecond}
	_ = callCount

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}

	waitState(t, db, "vol", StateAvailable, 5*time.Second)
}

// countingBackend fails the first maxFails CreateVolume calls, then succeeds.
type countingBackend struct {
	*fakeBackend
	mu       sync.Mutex
	calls    int
	maxFails int
}

func (b *countingBackend) CreateVolume(ctx context.Context, name string, sizeMB int) (*storage.Volume, error) {
	b.mu.Lock()
	b.calls++
	fail := b.calls <= b.maxFails
	b.mu.Unlock()

	if fail {
		return nil, errors.New("transient error")
	}
	return b.fakeBackend.CreateVolume(ctx, name, sizeMB)
}

func TestEventsRecordedOnTransition(t *testing.T) {
	ctx := context.Background()
	db := newFakeDB()
	app := newApp(db, newFakeBackend())

	if _, err := app.CreateVolume(ctx, "vol", 10); err != nil {
		t.Fatal(err)
	}
	waitState(t, db, "vol", StateAvailable, 2*time.Second)

	// Should have recorded: create (pending→creating) + ready (creating→available)
	if db.eventCount() < 2 {
		t.Errorf("expected ≥ 2 events, got %d", db.eventCount())
	}
}
