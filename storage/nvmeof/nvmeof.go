// Package nvmeof is a stub backend for NVMe-oF volumes via kernel configfs.
// A real implementation would manage nvmet subsystems, namespaces, and ports.
package nvmeof

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

var errNotImplemented = errors.New("nvmeof: not implemented — stub only")

type NVMeoFBackend struct {
	mu          sync.RWMutex
	volumes     map[string]*storage.Volume
	consistency string
}

func New(consistency string) *NVMeoFBackend {
	return &NVMeoFBackend{
		volumes:     make(map[string]*storage.Volume),
		consistency: consistency,
	}
}

func (n *NVMeoFBackend) CreateVolume(_ context.Context, name string, sizeMB int) (*storage.Volume, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.volumes[name]; ok {
		return nil, fmt.Errorf("nvmeof: volume %q already exists", name)
	}
	now := time.Now().UTC()
	v := &storage.Volume{
		ID: uuid.New().String(), Name: name, SizeMB: sizeMB,
		State: fsm.StateAvailable, Backend: "nvmeof",
		CreatedAt: now, UpdatedAt: now,
	}
	n.volumes[name] = v
	return v, nil
}

func (n *NVMeoFBackend) DeleteVolume(_ context.Context, name string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.volumes[name]; !ok {
		return fmt.Errorf("nvmeof: volume %q not found", name)
	}
	delete(n.volumes, name)
	return nil
}

func (n *NVMeoFBackend) ListVolumes(_ context.Context) ([]*storage.Volume, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	out := make([]*storage.Volume, 0, len(n.volumes))
	for _, v := range n.volumes {
		cp := *v
		out = append(out, &cp)
	}
	return out, nil
}

func (n *NVMeoFBackend) GetVolume(_ context.Context, name string) (*storage.Volume, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	v, ok := n.volumes[name]
	if !ok {
		return nil, fmt.Errorf("nvmeof: volume %q not found", name)
	}
	cp := *v
	return &cp, nil
}

func (n *NVMeoFBackend) AttachVolume(_ context.Context, _ string, _ string) error {
	return errNotImplemented
}

func (n *NVMeoFBackend) DetachVolume(_ context.Context, _ string) error {
	return errNotImplemented
}

func (n *NVMeoFBackend) HealthCheck(_ context.Context) error { return nil }
func (n *NVMeoFBackend) BackendName() string                 { return "nvmeof" }
func (n *NVMeoFBackend) ConsistencyMode() string             { return n.consistency }
func (n *NVMeoFBackend) Close(_ context.Context) error       { return nil }
