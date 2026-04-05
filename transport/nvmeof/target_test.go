package nvmeof_test

import (
	"context"
	"testing"

	"github.com/cire-ly/block-storage-api/storage/mock"
	"github.com/cire-ly/block-storage-api/transport/nvmeof"
)

// newTarget creates a ConfigfsTarget for testing.
// Note: configfs operations will fail on machines without nvmet loaded,
// so tests are written to exercise logic that does not require the kernel module.
func newTarget() *nvmeof.ConfigfsTarget {
	return nvmeof.NewConfigfsTarget()
}

func TestListExposedEmpty(t *testing.T) {
	ctx := context.Background()
	target := newTarget()

	volumes, err := target.ListExposed(ctx)
	if err != nil {
		t.Fatalf("ListExposed: %v", err)
	}
	if len(volumes) != 0 {
		t.Errorf("expected 0 exposed volumes, got %d", len(volumes))
	}
}

func TestUnexposeNotExposed(t *testing.T) {
	ctx := context.Background()
	target := newTarget()

	err := target.UnexposeVolume(ctx, "ghost")
	if err == nil {
		t.Error("expected error when unexposing non-existent volume")
	}
}

func TestCloseEmpty(t *testing.T) {
	ctx := context.Background()
	target := newTarget()

	// Close on an empty target should not panic or error.
	if err := target.Close(ctx); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// TestExposeRequiresConfigfs verifies that ExposeVolume fails gracefully on
// systems without the nvmet kernel module loaded (no configfs mount).
func TestExposeRequiresConfigfs(t *testing.T) {
	ctx := context.Background()
	target := newTarget()
	backend := mock.New()

	if _, err := backend.CreateVolume(ctx, "test-vol", 100); err != nil {
		t.Fatalf("create volume: %v", err)
	}

	// On systems without configfs, ExposeVolume returns an error — not a panic.
	// On systems with configfs, this would succeed.
	err := target.ExposeVolume(ctx, "test-vol", backend)
	if err != nil {
		t.Logf("ExposeVolume returned error (expected on non-configfs system): %v", err)
		return
	}

	// If it succeeded (configfs available), verify the volume is listed.
	volumes, listErr := target.ListExposed(ctx)
	if listErr != nil {
		t.Fatalf("ListExposed: %v", listErr)
	}
	if len(volumes) != 1 {
		t.Errorf("expected 1 exposed volume, got %d", len(volumes))
	}

	// Clean up.
	_ = target.UnexposeVolume(ctx, "test-vol")
}
