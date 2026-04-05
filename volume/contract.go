package volume

import (
	"context"

	"github.com/cire-ly/block-storage-api/storage"
)

// FeatureContract is the external face of the volume feature, consumed by setup.go.
type FeatureContract interface {
	Application() ApplicationContract
	Close(context.Context) error
}

// ApplicationContract defines all volume business operations.
// Implemented by application, consumed by controller_http.
type ApplicationContract interface {
	CreateVolume(ctx context.Context, name string, sizeMB int) (*storage.Volume, error)
	DeleteVolume(ctx context.Context, name string) error
	ListVolumes(ctx context.Context) ([]*storage.Volume, error)
	GetVolume(ctx context.Context, name string) (*storage.Volume, error)
	AttachVolume(ctx context.Context, name string, nodeID string) error
	DetachVolume(ctx context.Context, name string) error
	ResetVolume(ctx context.Context, name string) error
	HealthCheck(ctx context.Context) error
}
