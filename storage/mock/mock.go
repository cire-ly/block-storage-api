// Package mock implements VolumeBackend entirely in memory.
// It reproduces all business constraints (duplicate names, invalid size,
// forbidden transitions) without any external dependencies.
package mock

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cire-ly/block-storage-api/storage"
)

// Sentinel errors — callers may use errors.Is() to distinguish them.
var (
	ErrVolumeNotFound    = errors.New("volume not found")
	ErrVolumeExists      = errors.New("volume already exists")
	ErrInvalidSize       = errors.New("size must be > 0 MB")
	ErrInvalidTransition = errors.New("invalid state transition")
)

// MockBackend is an in-memory VolumeBackend.
// sync.RWMutex: RLock for reads, Lock for writes.
type MockBackend struct {
	mu      sync.RWMutex
	volumes map[string]*storage.Volume // key: volume name
}

// New creates a new in-memory backend.
func New() *MockBackend {
	return &MockBackend{
		volumes: make(map[string]*storage.Volume),
	}
}

// CreateVolume validates input and stores the volume in the available state.
func (m *MockBackend) CreateVolume(_ context.Context, name string, sizeMB int) (*storage.Volume, error) {
	if sizeMB <= 0 {
		return nil, ErrInvalidSize
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.volumes[name]; exists {
		return nil, fmt.Errorf("%w: %q", ErrVolumeExists, name)
	}

	now := time.Now().UTC()
	v := &storage.Volume{
		ID:        uuid.New().String(),
		Name:      name,
		SizeMB:    sizeMB,
		State:     storage.StateAvailable,
		Backend:   "mock",
		CreatedAt: now,
		UpdatedAt: now,
	}

	m.volumes[name] = v
	return copyVolume(v), nil
}

// DeleteVolume removes the volume. Returns ErrInvalidTransition when attached.
func (m *MockBackend) DeleteVolume(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.volumes[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}
	if v.State == storage.StateAttached {
		return fmt.Errorf("%w: cannot delete attached volume", ErrInvalidTransition)
	}

	delete(m.volumes, name)
	return nil
}

func (m *MockBackend) ListVolumes(_ context.Context) ([]*storage.Volume, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*storage.Volume, 0, len(m.volumes))
	for _, v := range m.volumes {
		out = append(out, copyVolume(v))
	}
	return out, nil
}

func (m *MockBackend) GetVolume(_ context.Context, name string) (*storage.Volume, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	v, ok := m.volumes[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}
	return copyVolume(v), nil
}

// AttachVolume sets NodeID and marks the volume as attached.
// Returns ErrInvalidTransition when the volume is not in the available state.
func (m *MockBackend) AttachVolume(_ context.Context, name string, nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.volumes[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}
	if v.State != storage.StateAvailable {
		return fmt.Errorf("%w: cannot attach volume in state %q", ErrInvalidTransition, v.State)
	}

	v.NodeID = nodeID
	v.State = storage.StateAttached
	v.UpdatedAt = time.Now().UTC()
	return nil
}

// DetachVolume clears NodeID and marks the volume as available.
// Returns ErrInvalidTransition when the volume is not attached.
func (m *MockBackend) DetachVolume(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.volumes[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}
	if v.State != storage.StateAttached {
		return fmt.Errorf("%w: cannot detach volume in state %q", ErrInvalidTransition, v.State)
	}

	v.NodeID = ""
	v.State = storage.StateAvailable
	v.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *MockBackend) HealthCheck(_ context.Context) error { return nil }
func (m *MockBackend) BackendName() string                  { return "mock" }
func (m *MockBackend) Close(_ context.Context) error        { return nil }

func copyVolume(v *storage.Volume) *storage.Volume {
	cp := *v
	return &cp
}
