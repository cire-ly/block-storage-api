package volume

import (
	"context"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/metric/noop"
	nooptrace "go.opentelemetry.io/otel/trace/noop"

	"github.com/cire-ly/block-storage-api/config"
	"github.com/cire-ly/block-storage-api/storage"
)

func defaultTestReconcilePolicy() config.ReconcilePolicy {
	return config.ReconcilePolicy{DBOnly: "error", CephOnly: "ignore"}
}

func TestNewVolumeFeatureSuccess(t *testing.T) {
	feat, err := NewVolumeFeature(NewVolumeFeatureParams{
		Logger:  fakeLogger{},
		Backend: newFakeBackend(),
		DB:      newFakeDB(),
		Tracer:  nooptrace.NewTracerProvider().Tracer("test"),
		Meter:   noop.NewMeterProvider().Meter("test"),
		Router:  chi.NewMux(),
	})
	if err != nil {
		t.Fatalf("NewVolumeFeature: %v", err)
	}
	if feat == nil {
		t.Fatal("expected non-nil VolumeFeature")
	}
	if feat.Application() == nil {
		t.Error("Application() returned nil")
	}
}

func TestNewVolumeFeatureMissingLogger(t *testing.T) {
	_, err := NewVolumeFeature(NewVolumeFeatureParams{
		Backend: newFakeBackend(),
		DB:      newFakeDB(),
		Tracer:  nooptrace.NewTracerProvider().Tracer("test"),
		Router:  chi.NewMux(),
	})
	if err == nil {
		t.Error("expected error when logger is nil")
	}
}

func TestNewVolumeFeatureMissingBackend(t *testing.T) {
	_, err := NewVolumeFeature(NewVolumeFeatureParams{
		Logger: fakeLogger{},
		DB:     newFakeDB(),
		Tracer: nooptrace.NewTracerProvider().Tracer("test"),
		Router: chi.NewMux(),
	})
	if err == nil {
		t.Error("expected error when backend is nil")
	}
}

func TestNewVolumeFeatureMissingDB(t *testing.T) {
	_, err := NewVolumeFeature(NewVolumeFeatureParams{
		Logger:  fakeLogger{},
		Backend: newFakeBackend(),
		Tracer:  nooptrace.NewTracerProvider().Tracer("test"),
		Router:  chi.NewMux(),
	})
	if err == nil {
		t.Error("expected error when db is nil")
	}
}

func TestNewVolumeFeatureMissingTracer(t *testing.T) {
	_, err := NewVolumeFeature(NewVolumeFeatureParams{
		Logger:  fakeLogger{},
		Backend: newFakeBackend(),
		DB:      newFakeDB(),
		Router:  chi.NewMux(),
	})
	if err == nil {
		t.Error("expected error when tracer is nil")
	}
}

func TestNewVolumeFeatureMissingRouter(t *testing.T) {
	_, err := NewVolumeFeature(NewVolumeFeatureParams{
		Logger:  fakeLogger{},
		Backend: newFakeBackend(),
		DB:      newFakeDB(),
		Tracer:  nooptrace.NewTracerProvider().Tracer("test"),
	})
	if err == nil {
		t.Error("expected error when router is nil")
	}
}

func TestVolumeFeatureClose(t *testing.T) {
	feat, err := NewVolumeFeature(NewVolumeFeatureParams{
		Logger:  fakeLogger{},
		Backend: newFakeBackend(),
		DB:      newFakeDB(),
		Tracer:  nooptrace.NewTracerProvider().Tracer("test"),
		Router:  chi.NewMux(),
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := feat.Close(context.Background()); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestReconcileOnStartupNoVolumes(t *testing.T) {
	feat, err := NewVolumeFeature(NewVolumeFeatureParams{
		Logger:  fakeLogger{},
		Backend: newFakeBackend(),
		DB:      newFakeDB(),
		Tracer:  nooptrace.NewTracerProvider().Tracer("test"),
		Router:  chi.NewMux(),
	})
	if err != nil {
		t.Fatal(err)
	}

	// Reconcile with empty DB should not panic.
	feat.reconcileOnStartup(context.Background(), defaultTestReconcilePolicy())
}

func TestReconcileOnStartupBackendMissing(t *testing.T) {
	db := newFakeDB()
	backend := newFakeBackend()

	// Pre-seed a volume in "creating" state with no matching backend entry.
	db.volumes["stuck"] = &storage.Volume{
		ID: "id-1", Name: "stuck", State: StateCreating, Backend: "fake",
	}

	feat, err := NewVolumeFeature(NewVolumeFeatureParams{
		Logger:  fakeLogger{},
		Backend: backend,
		DB:      db,
		Tracer:  nooptrace.NewTracerProvider().Tracer("test"),
		Router:  chi.NewMux(),
	})
	if err != nil {
		t.Fatal(err)
	}

	feat.reconcileOnStartup(context.Background(), defaultTestReconcilePolicy())

	// Volume should be in error state after reconcile detects it is absent from backend.
	v, _ := db.LoadVolume(context.Background(), "stuck")
	if v == nil {
		t.Fatal("volume disappeared from DB")
	}
	if v.State != StateError {
		t.Errorf("State = %q after reconcile, want error", v.State)
	}
}

func TestReconcileOnStartupBackendPresent(t *testing.T) {
	db := newFakeDB()
	backend := newFakeBackend()

	// Pre-seed in DB AND backend.
	db.volumes["present"] = &storage.Volume{
		ID: "id-2", Name: "present", State: StateCreating, Backend: "fake",
	}
	backend.volumes["present"] = &storage.Volume{
		Name: "present", State: StateAvailable,
	}

	feat, err := NewVolumeFeature(NewVolumeFeatureParams{
		Logger:  fakeLogger{},
		Backend: backend,
		DB:      db,
		Tracer:  nooptrace.NewTracerProvider().Tracer("test"),
		Router:  chi.NewMux(),
	})
	if err != nil {
		t.Fatal(err)
	}

	feat.reconcileOnStartup(context.Background(), defaultTestReconcilePolicy())

	// Volume should be available after reconcile confirms it exists in backend.
	v, _ := db.LoadVolume(context.Background(), "present")
	if v == nil {
		t.Fatal("volume disappeared from DB")
	}
	if v.State != StateAvailable {
		t.Errorf("State = %q after reconcile, want available", v.State)
	}
}
