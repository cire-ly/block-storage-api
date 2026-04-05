package repository

import (
	"context"
	"fmt"
	"sync"

	"github.com/cire-ly/block-storage-api/storage"
	"github.com/cire-ly/block-storage-api/volume"
)

// InMemoryRepository implements volume.DatabaseDependency entirely in memory.
// Used when no DATABASE_URL is provided (local dev without PostgreSQL).
type InMemoryRepository struct {
	mu      sync.RWMutex
	volumes map[string]*storage.Volume // key: name
	events  []volume.VolumeEvent
}

// NewInMemoryRepository returns an empty in-memory repository.
func NewInMemoryRepository() *InMemoryRepository {
	return &InMemoryRepository{
		volumes: make(map[string]*storage.Volume),
	}
}

func (r *InMemoryRepository) SaveVolume(_ context.Context, v *storage.Volume) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.volumes[v.Name]; exists {
		return fmt.Errorf("volume %q already exists", v.Name)
	}
	cp := *v
	r.volumes[v.Name] = &cp
	return nil
}

func (r *InMemoryRepository) UpdateVolume(_ context.Context, v *storage.Volume) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.volumes[v.Name]; !exists {
		return fmt.Errorf("volume %q not found", v.Name)
	}
	cp := *v
	r.volumes[v.Name] = &cp
	return nil
}

// LoadVolume returns nil, nil when the volume does not exist.
func (r *InMemoryRepository) LoadVolume(_ context.Context, name string) (*storage.Volume, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.volumes[name]
	if !ok {
		return nil, nil
	}
	cp := *v
	return &cp, nil
}

func (r *InMemoryRepository) ListVolumes(_ context.Context) ([]*storage.Volume, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*storage.Volume, 0, len(r.volumes))
	for _, v := range r.volumes {
		cp := *v
		out = append(out, &cp)
	}
	return out, nil
}

func (r *InMemoryRepository) ListVolumesByState(_ context.Context, states ...string) ([]*storage.Volume, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	want := make(map[string]bool, len(states))
	for _, s := range states {
		want[s] = true
	}
	var out []*storage.Volume
	for _, v := range r.volumes {
		if want[v.State] {
			cp := *v
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (r *InMemoryRepository) SaveEvent(_ context.Context, e volume.VolumeEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}
