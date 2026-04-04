// Package lustre is a stub backend for Lustre parallel filesystem volumes.
// A real implementation would call lfs(1) and manage MDT/OST allocations.
package lustre

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cire-ly/block-storage-api/fsm"
	"github.com/cire-ly/block-storage-api/storage"
)

var errNotImplemented = errors.New("lustre: not implemented — stub only")

type LustreBackend struct {
	mu          sync.RWMutex
	volumes     map[string]*storage.Volume
	consistency string
}

func New(consistency string) *LustreBackend {
	return &LustreBackend{
		volumes:     make(map[string]*storage.Volume),
		consistency: consistency,
	}
}

func (l *LustreBackend) CreateVolume(_ context.Context, name string, sizeMB int) (*storage.Volume, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.volumes[name]; ok {
		return nil, fmt.Errorf("lustre: volume %q already exists", name)
	}
	now := time.Now().UTC()
	v := &storage.Volume{
		ID: uuid.New().String(), Name: name, SizeMB: sizeMB,
		State: fsm.StateAvailable, Backend: "lustre",
		CreatedAt: now, UpdatedAt: now,
	}
	l.volumes[name] = v
	return v, nil
}

func (l *LustreBackend) DeleteVolume(_ context.Context, name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.volumes[name]; !ok {
		return fmt.Errorf("lustre: volume %q not found", name)
	}
	delete(l.volumes, name)
	return nil
}

func (l *LustreBackend) ListVolumes(_ context.Context) ([]*storage.Volume, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]*storage.Volume, 0, len(l.volumes))
	for _, v := range l.volumes {
		cp := *v
		out = append(out, &cp)
	}
	return out, nil
}

func (l *LustreBackend) GetVolume(_ context.Context, name string) (*storage.Volume, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	v, ok := l.volumes[name]
	if !ok {
		return nil, fmt.Errorf("lustre: volume %q not found", name)
	}
	cp := *v
	return &cp, nil
}

func (l *LustreBackend) AttachVolume(_ context.Context, _ string, _ string) error {
	return errNotImplemented
}

func (l *LustreBackend) DetachVolume(_ context.Context, _ string) error {
	return errNotImplemented
}

func (l *LustreBackend) HealthCheck(_ context.Context) error { return nil }
func (l *LustreBackend) BackendName() string                 { return "lustre" }
func (l *LustreBackend) ConsistencyMode() string             { return l.consistency }
func (l *LustreBackend) Close(_ context.Context) error       { return nil }
