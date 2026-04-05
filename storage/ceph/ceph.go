//go:build ceph

// Package ceph implements VolumeBackend backed by Ceph RBD via go-ceph/librbd.
// Build with: go build -tags ceph ./...
// Requires: librados-dev, librbd-dev, and a running Ceph cluster.
package ceph

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ceph/go-ceph/rados"
	"github.com/ceph/go-ceph/rbd"
	"github.com/google/uuid"

	"github.com/cire-ly/block-storage-api/storage"
)

// Config holds Ceph connection parameters.
type Config struct {
	Monitors []string
	Pool     string
	Keyring  string
}

// CephBackend manages RBD images as block volumes.
type CephBackend struct {
	cfg   Config
	conn  *rados.Conn
	ioctx *rados.IOContext

	mu      sync.RWMutex
	volumes map[string]*storage.Volume // name → metadata cache
}

// New connects to the Ceph cluster and opens the configured pool.
func New(cfg Config) (*CephBackend, error) {
	conn, err := rados.NewConn()
	if err != nil {
		return nil, fmt.Errorf("rados.NewConn: %w", err)
	}
	if err := conn.ReadConfigFile("/etc/ceph/ceph.conf"); err != nil {
		// Fallback: set monitors programmatically.
		for _, mon := range cfg.Monitors {
			_ = conn.SetConfigOption("mon_host", mon)
		}
	}
	if cfg.Keyring != "" {
		_ = conn.SetConfigOption("keyring", cfg.Keyring)
	}
	if err := conn.Connect(); err != nil {
		return nil, fmt.Errorf("rados connect: %w", err)
	}
	ioctx, err := conn.OpenIOContext(cfg.Pool)
	if err != nil {
		conn.Shutdown()
		return nil, fmt.Errorf("open IO context pool=%s: %w", cfg.Pool, err)
	}
	return &CephBackend{
		cfg:     cfg,
		conn:    conn,
		ioctx:   ioctx,
		volumes: make(map[string]*storage.Volume),
	}, nil
}

func (c *CephBackend) CreateVolume(_ context.Context, name string, sizeMB int) (*storage.Volume, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.volumes[name]; ok {
		return nil, fmt.Errorf("ceph: volume %q already exists", name)
	}

	sizeBytes := uint64(sizeMB) * 1024 * 1024
	_, err := rbd.Create(c.ioctx, name, sizeBytes, 22) // order 22 = 4 MiB objects
	if err != nil {
		return nil, fmt.Errorf("rbd.Create %q: %w", name, err)
	}

	now := time.Now().UTC()
	v := &storage.Volume{
		ID:        uuid.New().String(),
		Name:      name,
		SizeMB:    sizeMB,
		State:     storage.StateAvailable,
		Backend:   "ceph",
		CreatedAt: now,
		UpdatedAt: now,
	}
	c.volumes[name] = v
	return copyVolume(v), nil
}

func (c *CephBackend) DeleteVolume(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, ok := c.volumes[name]; !ok {
		return fmt.Errorf("ceph: volume %q not found", name)
	}

	img, err := rbd.OpenImage(c.ioctx, name, rbd.NoSnapshot)
	if err != nil {
		return fmt.Errorf("rbd.OpenImage %q: %w", name, err)
	}
	if err := img.Close(); err != nil {
		return fmt.Errorf("rbd close %q: %w", name, err)
	}
	if err := rbd.RemoveImage(c.ioctx, name); err != nil {
		return fmt.Errorf("rbd.RemoveImage %q: %w", name, err)
	}

	delete(c.volumes, name)
	return nil
}

func (c *CephBackend) ListVolumes(_ context.Context) ([]*storage.Volume, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*storage.Volume, 0, len(c.volumes))
	for _, v := range c.volumes {
		out = append(out, copyVolume(v))
	}
	return out, nil
}

func (c *CephBackend) GetVolume(_ context.Context, name string) (*storage.Volume, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	v, ok := c.volumes[name]
	if !ok {
		return nil, fmt.Errorf("ceph: volume %q not found", name)
	}
	return copyVolume(v), nil
}

func (c *CephBackend) AttachVolume(_ context.Context, name string, nodeID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.volumes[name]
	if !ok {
		return fmt.Errorf("ceph: volume %q not found", name)
	}
	v.NodeID = nodeID
	v.State = storage.StateAttached
	v.UpdatedAt = time.Now().UTC()
	return nil
}

func (c *CephBackend) DetachVolume(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.volumes[name]
	if !ok {
		return fmt.Errorf("ceph: volume %q not found", name)
	}
	v.NodeID = ""
	v.State = storage.StateAvailable
	v.UpdatedAt = time.Now().UTC()
	return nil
}

func (c *CephBackend) HealthCheck(_ context.Context) error {
	_, err := c.conn.GetClusterStats()
	if err != nil {
		return fmt.Errorf("ceph health check: %w", err)
	}
	return nil
}

func (c *CephBackend) BackendName() string { return "ceph" }

func (c *CephBackend) Close(_ context.Context) error {
	c.ioctx.Destroy()
	c.conn.Shutdown()
	return nil
}

func copyVolume(v *storage.Volume) *storage.Volume {
	cp := *v
	return &cp
}
