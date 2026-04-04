package storage

import (
	"context"
	"time"
)

// VolumeBackend is the central interface every storage backend must implement.
// Defined on the consumer side (cmd/api) — backends implement it, not the other way around.
type VolumeBackend interface {
	CreateVolume(ctx context.Context, name string, sizeMB int) (*Volume, error)
	DeleteVolume(ctx context.Context, name string) error
	ListVolumes(ctx context.Context) ([]*Volume, error)
	GetVolume(ctx context.Context, name string) (*Volume, error)
	AttachVolume(ctx context.Context, name string, nodeID string) error
	DetachVolume(ctx context.Context, name string) error
	HealthCheck(ctx context.Context) error
	BackendName() string
	ConsistencyMode() string     // "cp" or "ap"
	Close(context.Context) error // implements io.Closer-like contract
}

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
