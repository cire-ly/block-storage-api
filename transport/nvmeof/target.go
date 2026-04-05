// Package nvmeof provides an NVMe-oF target implementation via Linux configfs.
// NVMe-oF is a transport layer — it exposes existing storage backend volumes
// over the network, not a storage backend in its own right.
//
// Real configfs paths: /sys/kernel/config/nvmet/
// No mature Go library exists; direct os.MkdirAll + os.WriteFile is used.
package nvmeof

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/cire-ly/block-storage-api/storage"
)

const configfsBase = "/sys/kernel/config/nvmet"

// ExposedVolume describes a volume currently exported over NVMe-oF.
type ExposedVolume struct {
	VolumeName string
	NQN        string // NVMe Qualified Name
	DevPath    string // e.g. /dev/rbd0
}

// Target manages NVMe-oF subsystem exposure of block volumes.
type Target interface {
	ExposeVolume(ctx context.Context, volumeName string, backend storage.VolumeBackend) error
	UnexposeVolume(ctx context.Context, volumeName string) error
	ListExposed(ctx context.Context) ([]ExposedVolume, error)
	Close(context.Context) error
}

// ConfigfsTarget is a Target backed by the Linux nvmet configfs layer.
type ConfigfsTarget struct {
	mu      sync.RWMutex
	exposed map[string]ExposedVolume // key: volumeName
}

// NewConfigfsTarget creates a new configfs-backed NVMe-oF target.
func NewConfigfsTarget() *ConfigfsTarget {
	return &ConfigfsTarget{
		exposed: make(map[string]ExposedVolume),
	}
}

// ExposeVolume creates an NVMe-oF subsystem for the given volume via configfs.
func (t *ConfigfsTarget) ExposeVolume(_ context.Context, volumeName string, backend storage.VolumeBackend) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, exists := t.exposed[volumeName]; exists {
		return fmt.Errorf("nvmeof: volume %q is already exposed", volumeName)
	}

	nqn := nqnFor(volumeName)
	subsysPath := filepath.Join(configfsBase, "subsystems", nqn)

	if err := os.MkdirAll(subsysPath, 0o755); err != nil {
		return fmt.Errorf("nvmeof: create subsystem dir: %w", err)
	}

	// Allow any host to connect (demo only — production should restrict).
	allowAllHosts := filepath.Join(subsysPath, "attr_allow_any_host")
	if err := os.WriteFile(allowAllHosts, []byte("1"), 0o644); err != nil {
		return fmt.Errorf("nvmeof: set allow_any_host: %w", err)
	}

	// Create namespace 1 and set device path.
	nsPath := filepath.Join(subsysPath, "namespaces", "1")
	if err := os.MkdirAll(nsPath, 0o755); err != nil {
		return fmt.Errorf("nvmeof: create namespace dir: %w", err)
	}

	devPath := devPathFor(backend.BackendName(), volumeName)
	if err := os.WriteFile(filepath.Join(nsPath, "device_path"), []byte(devPath), 0o644); err != nil {
		return fmt.Errorf("nvmeof: set device_path: %w", err)
	}

	t.exposed[volumeName] = ExposedVolume{
		VolumeName: volumeName,
		NQN:        nqn,
		DevPath:    devPath,
	}
	return nil
}

// UnexposeVolume removes the NVMe-oF subsystem for the given volume.
func (t *ConfigfsTarget) UnexposeVolume(_ context.Context, volumeName string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	ev, exists := t.exposed[volumeName]
	if !exists {
		return fmt.Errorf("nvmeof: volume %q is not exposed", volumeName)
	}

	subsysPath := filepath.Join(configfsBase, "subsystems", ev.NQN)
	if err := os.RemoveAll(subsysPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("nvmeof: remove subsystem dir: %w", err)
	}

	delete(t.exposed, volumeName)
	return nil
}

// ListExposed returns all currently exported volumes.
func (t *ConfigfsTarget) ListExposed(_ context.Context) ([]ExposedVolume, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	out := make([]ExposedVolume, 0, len(t.exposed))
	for _, ev := range t.exposed {
		out = append(out, ev)
	}
	return out, nil
}

// Close unexposes all volumes and releases resources.
func (t *ConfigfsTarget) Close(_ context.Context) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	var last error
	for volumeName, ev := range t.exposed {
		subsysPath := filepath.Join(configfsBase, "subsystems", ev.NQN)
		if err := os.RemoveAll(subsysPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			last = fmt.Errorf("nvmeof: cleanup %q: %w", volumeName, err)
		}
		delete(t.exposed, volumeName)
	}
	return last
}

// nqnFor builds a deterministic NVMe Qualified Name for the given volume.
func nqnFor(volumeName string) string {
	return "nqn.2024-01.io.block-storage:" + volumeName
}

// devPathFor resolves the block device path for a given backend and volume.
func devPathFor(backendName, volumeName string) string {
	switch backendName {
	case "ceph":
		return "/dev/rbd/rbd-demo/" + volumeName
	default:
		return "/dev/block/" + volumeName
	}
}
