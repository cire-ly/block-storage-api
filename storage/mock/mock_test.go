package mock_test

import (
	"context"
	"errors"
	"testing"

	"github.com/cire-ly/block-storage-api/storage/mock"
)

func newBackend() *mock.MockBackend {
	return mock.New("cp")
}

func TestCreateVolume(t *testing.T) {
	ctx := context.Background()
	b := newBackend()

	vol, err := b.CreateVolume(ctx, "vol-01", 100)
	if err != nil {
		t.Fatalf("CreateVolume: %v", err)
	}
	if vol.Name != "vol-01" {
		t.Errorf("Name = %q, want %q", vol.Name, "vol-01")
	}
	if vol.SizeMB != 100 {
		t.Errorf("SizeMB = %d, want 100", vol.SizeMB)
	}
	if vol.State != "available" {
		t.Errorf("State = %q, want available", vol.State)
	}
	if vol.ID == "" {
		t.Error("ID should not be empty")
	}
}

func TestCreateVolumeDuplicate(t *testing.T) {
	ctx := context.Background()
	b := newBackend()

	if _, err := b.CreateVolume(ctx, "vol-dup", 10); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := b.CreateVolume(ctx, "vol-dup", 20)
	if !errors.Is(err, mock.ErrVolumeExists) {
		t.Errorf("expected ErrVolumeExists, got %v", err)
	}
}

func TestCreateVolumeInvalidSize(t *testing.T) {
	ctx := context.Background()
	b := newBackend()

	_, err := b.CreateVolume(ctx, "bad", 0)
	if !errors.Is(err, mock.ErrInvalidSize) {
		t.Errorf("expected ErrInvalidSize, got %v", err)
	}

	_, err = b.CreateVolume(ctx, "bad", -1)
	if !errors.Is(err, mock.ErrInvalidSize) {
		t.Errorf("expected ErrInvalidSize for negative, got %v", err)
	}
}

func TestGetVolumeNotFound(t *testing.T) {
	ctx := context.Background()
	b := newBackend()

	_, err := b.GetVolume(ctx, "ghost")
	if !errors.Is(err, mock.ErrVolumeNotFound) {
		t.Errorf("expected ErrVolumeNotFound, got %v", err)
	}
}

func TestListVolumes(t *testing.T) {
	ctx := context.Background()
	b := newBackend()

	for _, name := range []string{"a", "b", "c"} {
		if _, err := b.CreateVolume(ctx, name, 10); err != nil {
			t.Fatalf("create %q: %v", name, err)
		}
	}

	vols, err := b.ListVolumes(ctx)
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	if len(vols) != 3 {
		t.Errorf("expected 3 volumes, got %d", len(vols))
	}
}

func TestAttachDetach(t *testing.T) {
	ctx := context.Background()
	b := newBackend()

	if _, err := b.CreateVolume(ctx, "v", 10); err != nil {
		t.Fatal(err)
	}

	if err := b.AttachVolume(ctx, "v", "node-1"); err != nil {
		t.Fatalf("AttachVolume: %v", err)
	}

	vol, _ := b.GetVolume(ctx, "v")
	if vol.State != "attached" {
		t.Errorf("State after attach = %q, want attached", vol.State)
	}
	if vol.NodeID != "node-1" {
		t.Errorf("NodeID = %q, want node-1", vol.NodeID)
	}

	// Cannot delete while attached.
	if err := b.DeleteVolume(ctx, "v"); !errors.Is(err, mock.ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition on delete while attached, got %v", err)
	}

	if err := b.DetachVolume(ctx, "v"); err != nil {
		t.Fatalf("DetachVolume: %v", err)
	}

	vol, _ = b.GetVolume(ctx, "v")
	if vol.State != "available" {
		t.Errorf("State after detach = %q, want available", vol.State)
	}
	if vol.NodeID != "" {
		t.Errorf("NodeID after detach = %q, want empty", vol.NodeID)
	}
}

func TestDeleteVolume(t *testing.T) {
	ctx := context.Background()
	b := newBackend()

	if _, err := b.CreateVolume(ctx, "del-me", 5); err != nil {
		t.Fatal(err)
	}
	if err := b.DeleteVolume(ctx, "del-me"); err != nil {
		t.Fatalf("DeleteVolume: %v", err)
	}

	_, err := b.GetVolume(ctx, "del-me")
	if !errors.Is(err, mock.ErrVolumeNotFound) {
		t.Errorf("expected ErrVolumeNotFound after delete, got %v", err)
	}
}

func TestDeleteVolumeNotFound(t *testing.T) {
	ctx := context.Background()
	b := newBackend()

	if err := b.DeleteVolume(ctx, "ghost"); !errors.Is(err, mock.ErrVolumeNotFound) {
		t.Errorf("expected ErrVolumeNotFound, got %v", err)
	}
}

func TestHealthCheck(t *testing.T) {
	if err := newBackend().HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
}

func TestConsistencyMode(t *testing.T) {
	cp := mock.New("cp")
	if cp.ConsistencyMode() != "cp" {
		t.Errorf("expected cp, got %q", cp.ConsistencyMode())
	}
	ap := mock.New("ap")
	if ap.ConsistencyMode() != "ap" {
		t.Errorf("expected ap, got %q", ap.ConsistencyMode())
	}
}

func TestConcurrentAccess(t *testing.T) {
	ctx := context.Background()
	b := newBackend()

	// Create volumes concurrently.
	done := make(chan struct{}, 10)
	for i := range 10 {
		go func(i int) {
			defer func() { done <- struct{}{} }()
			name := "concurrent-" + string(rune('a'+i))
			_, _ = b.CreateVolume(ctx, name, 1)
		}(i)
	}
	for range 10 {
		<-done
	}

	vols, err := b.ListVolumes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 10 {
		t.Errorf("expected 10 volumes, got %d", len(vols))
	}
}
