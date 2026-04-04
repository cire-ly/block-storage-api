// Package mock implements VolumeBackend entirely in memory.
// It reproduces all business constraints (duplicate names, invalid size,
// forbidden FSM transitions) and simulates both CP and AP consistency modes.
package mock

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	volumefsm "github.com/cire-ly/block-storage-api/fsm"
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
	mu          sync.RWMutex
	volumes     map[string]*storage.Volume // key: volume name
	consistency string                     // "cp" or "ap"
}

func New(consistency string) *MockBackend {
	return &MockBackend{
		volumes:     make(map[string]*storage.Volume),
		consistency: consistency,
	}
}

// CreateVolume validates inputs, runs pending→creating→available, and persists in memory.
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
		State:     volumefsm.StatePending,
		Backend:   "mock",
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := m.applyEvent(v, volumefsm.EventCreate); err != nil {
		return nil, err
	}
	if err := m.applyEvent(v, volumefsm.EventReady); err != nil {
		return nil, err
	}

	m.volumes[name] = v
	return copyVolume(v), nil
}

// DeleteVolume runs available→deleting→deleted and removes the entry.
func (m *MockBackend) DeleteVolume(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.volumes[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}

	if err := m.applyEvent(v, volumefsm.EventDelete); err != nil {
		return err
	}
	if err := m.applyEvent(v, volumefsm.EventDeleted); err != nil {
		return err
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

// AttachVolume runs available→attaching→attached and records the nodeID.
func (m *MockBackend) AttachVolume(_ context.Context, name string, nodeID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.volumes[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}

	if err := m.applyEvent(v, volumefsm.EventAttach); err != nil {
		return err
	}
	v.NodeID = nodeID
	if err := m.applyEvent(v, volumefsm.EventAttached); err != nil {
		return err
	}
	return nil
}

// DetachVolume runs attached→detaching→available and clears the nodeID.
func (m *MockBackend) DetachVolume(_ context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	v, ok := m.volumes[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}

	if err := m.applyEvent(v, volumefsm.EventDetach); err != nil {
		return err
	}
	v.NodeID = ""
	if err := m.applyEvent(v, volumefsm.EventDetached); err != nil {
		return err
	}
	return nil
}

func (m *MockBackend) HealthCheck(_ context.Context) error { return nil }
func (m *MockBackend) BackendName() string                 { return "mock" }
func (m *MockBackend) ConsistencyMode() string             { return m.consistency }
func (m *MockBackend) Close(_ context.Context) error       { return nil }

// applyEvent validates and applies a single FSM event to the volume (must hold lock).
func (m *MockBackend) applyEvent(v *storage.Volume, event string) error {
	if !volumefsm.CanTransition(v.State, event) {
		return fmt.Errorf("%w: cannot %q from state %q", ErrInvalidTransition, event, v.State)
	}
	newState, err := volumefsm.Transition(context.Background(), v.State, event)
	if err != nil {
		return err
	}
	v.State = newState
	v.UpdatedAt = time.Now().UTC()
	return nil
}

func copyVolume(v *storage.Volume) *storage.Volume {
	cp := *v
	return &cp
}
