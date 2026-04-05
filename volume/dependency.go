package volume

import (
	"context"

	"github.com/cire-ly/block-storage-api/storage"
)

// StorageBackendDependency is the storage backend required by the volume feature.
// Defined on the consumer side (volume package) — implementations live in storage/.
type StorageBackendDependency interface {
	storage.VolumeBackend
}

// VolumeEvent records a single FSM state transition for the audit trail.
type VolumeEvent struct {
	VolumeID  string
	Event     string
	FromState string
	ToState   string
}

// DatabaseDependency is the persistence layer required by the volume feature.
// Defined on the consumer side — the concrete implementation lives in internal/db.
type DatabaseDependency interface {
	SaveVolume(ctx context.Context, v *storage.Volume) error
	UpdateVolume(ctx context.Context, v *storage.Volume) error
	// LoadVolume returns nil, nil when the volume does not exist.
	LoadVolume(ctx context.Context, name string) (*storage.Volume, error)
	ListVolumes(ctx context.Context) ([]*storage.Volume, error)
	ListVolumesByState(ctx context.Context, states ...string) ([]*storage.Volume, error)
	SaveEvent(ctx context.Context, e VolumeEvent) error
}

// LoggerDependency is the structured logger required by the volume feature.
type LoggerDependency interface {
	Debug(string, ...any)
	Info(string, ...any)
	Warn(string, ...any)
	Error(string, ...any)
}
