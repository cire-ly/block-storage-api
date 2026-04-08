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
	ReconcileVolume(ctx context.Context, name string) (*storage.Volume, error)
	HealthCheck(ctx context.Context) error
	// Subscribe returns a buffered channel that receives every FSM state transition
	// for the named volume. The channel is closed when the volume reaches a terminal
	// state or when Unsubscribe is called.
	Subscribe(ctx context.Context, name string) (<-chan VolumeStateEvent, error)
	Unsubscribe(name string, ch <-chan VolumeStateEvent)
}
