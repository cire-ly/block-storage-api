package storage

import (
	"context"
	"time"
)

// Volume states — used by backends and the volume FSM.
const (
	StatePending         = "pending"
	StateCreating        = "creating"
	StateCreatingFailed  = "creating_failed"
	StateAvailable       = "available"
	StateAttaching       = "attaching"
	StateAttachingFailed = "attaching_failed"
	StateAttached        = "attached"
	StateDetaching       = "detaching"
	StateDetachingFailed = "detaching_failed"
	StateDeleting        = "deleting"
	StateDeletingFailed  = "deleting_failed"
	StateDeleted         = "deleted"
	StateError           = "error"
)

// VolumeBackend is the central interface every storage backend must implement.
// Defined on the consumer side — backends implement it.
type VolumeBackend interface {
	CreateVolume(ctx context.Context, name string, sizeMB int) (*Volume, error)
	DeleteVolume(ctx context.Context, name string) error
	ListVolumes(ctx context.Context) ([]*Volume, error)
	GetVolume(ctx context.Context, name string) (*Volume, error)
	AttachVolume(ctx context.Context, name string, nodeID string) error
	DetachVolume(ctx context.Context, name string) error
	HealthCheck(ctx context.Context) error
	BackendName() string
	Close(context.Context) error
}

// Volume holds the metadata for a single block storage volume.
type Volume struct {
	ID        string
	Name      string
	SizeMB    int
	State     string
	Backend   string
	NodeID    string
	CreatedAt time.Time
	UpdatedAt time.Time
}
