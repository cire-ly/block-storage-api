package repository_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/cire-ly/block-storage-api/storage"
	"github.com/cire-ly/block-storage-api/volume"
	"github.com/cire-ly/block-storage-api/volume/repository"
)

func newVolume(name, state string) *storage.Volume {
	now := time.Now().UTC()
	return &storage.Volume{
		ID:        uuid.New().String(),
		Name:      name,
		SizeMB:    100,
		State:     state,
		Backend:   "mock",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

func TestInMemorySaveAndLoad(t *testing.T) {
	ctx := context.Background()
	r := repository.NewInMemoryRepository()

	v := newVolume("vol-01", storage.StatePending)
	if err := r.SaveVolume(ctx, v); err != nil {
		t.Fatalf("SaveVolume: %v", err)
	}

	got, err := r.LoadVolume(ctx, "vol-01")
	if err != nil {
		t.Fatalf("LoadVolume: %v", err)
	}
	if got == nil {
		t.Fatal("LoadVolume returned nil, want volume")
	}
	if got.Name != "vol-01" {
		t.Errorf("Name = %q, want vol-01", got.Name)
	}
}

func TestInMemoryLoadNotFound(t *testing.T) {
	r := repository.NewInMemoryRepository()
	got, err := r.LoadVolume(context.Background(), "missing")
	if err != nil {
		t.Fatalf("LoadVolume: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestInMemorySaveDuplicate(t *testing.T) {
	ctx := context.Background()
	r := repository.NewInMemoryRepository()

	v := newVolume("dup", storage.StatePending)
	if err := r.SaveVolume(ctx, v); err != nil {
		t.Fatalf("first save: %v", err)
	}
	if err := r.SaveVolume(ctx, v); err == nil {
		t.Error("expected error for duplicate, got nil")
	}
}

func TestInMemoryUpdateVolume(t *testing.T) {
	ctx := context.Background()
	r := repository.NewInMemoryRepository()

	v := newVolume("vol-02", storage.StatePending)
	if err := r.SaveVolume(ctx, v); err != nil {
		t.Fatalf("SaveVolume: %v", err)
	}

	v.State = storage.StateAvailable
	if err := r.UpdateVolume(ctx, v); err != nil {
		t.Fatalf("UpdateVolume: %v", err)
	}

	got, _ := r.LoadVolume(ctx, "vol-02")
	if got.State != storage.StateAvailable {
		t.Errorf("State = %q, want %q", got.State, storage.StateAvailable)
	}
}

func TestInMemoryListVolumes(t *testing.T) {
	ctx := context.Background()
	r := repository.NewInMemoryRepository()

	for _, name := range []string{"a", "b", "c"} {
		_ = r.SaveVolume(ctx, newVolume(name, storage.StatePending))
	}

	vols, err := r.ListVolumes(ctx)
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(vols) != 3 {
		t.Errorf("len = %d, want 3", len(vols))
	}
}

func TestInMemoryListVolumesByState(t *testing.T) {
	ctx := context.Background()
	r := repository.NewInMemoryRepository()

	_ = r.SaveVolume(ctx, newVolume("p1", storage.StatePending))
	_ = r.SaveVolume(ctx, newVolume("p2", storage.StatePending))
	_ = r.SaveVolume(ctx, newVolume("av1", storage.StateAvailable))

	vols, err := r.ListVolumesByState(ctx, storage.StatePending)
	if err != nil {
		t.Fatalf("ListVolumesByState: %v", err)
	}
	if len(vols) != 2 {
		t.Errorf("len = %d, want 2", len(vols))
	}
}

func TestInMemoryListVolumesByStateMultiple(t *testing.T) {
	ctx := context.Background()
	r := repository.NewInMemoryRepository()

	_ = r.SaveVolume(ctx, newVolume("c1", storage.StateCreating))
	_ = r.SaveVolume(ctx, newVolume("a1", storage.StateAttaching))
	_ = r.SaveVolume(ctx, newVolume("ok", storage.StateAvailable))

	vols, err := r.ListVolumesByState(ctx, storage.StateCreating, storage.StateAttaching)
	if err != nil {
		t.Fatalf("ListVolumesByState: %v", err)
	}
	if len(vols) != 2 {
		t.Errorf("len = %d, want 2", len(vols))
	}
}

func TestInMemorySaveEvent(t *testing.T) {
	ctx := context.Background()
	r := repository.NewInMemoryRepository()

	e := volume.VolumeEvent{
		VolumeID:  "id-1",
		Event:     "create",
		FromState: storage.StatePending,
		ToState:   storage.StateCreating,
	}
	if err := r.SaveEvent(ctx, e); err != nil {
		t.Fatalf("SaveEvent: %v", err)
	}
}

func TestInMemoryUpdateNotFound(t *testing.T) {
	r := repository.NewInMemoryRepository()
	v := newVolume("ghost", storage.StatePending)
	if err := r.UpdateVolume(context.Background(), v); err == nil {
		t.Error("expected error for update of non-existent volume")
	}
}

func TestInMemoryIsolation(t *testing.T) {
	// Ensure returned copies are independent from internal state.
	ctx := context.Background()
	r := repository.NewInMemoryRepository()

	v := newVolume("iso", storage.StatePending)
	_ = r.SaveVolume(ctx, v)

	got, _ := r.LoadVolume(ctx, "iso")
	got.State = "mutated" // must not affect stored value

	fresh, _ := r.LoadVolume(ctx, "iso")
	if fresh.State == "mutated" {
		t.Error("LoadVolume returned a reference, not a copy")
	}
}
