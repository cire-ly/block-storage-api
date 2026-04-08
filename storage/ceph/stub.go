//go:build !ceph

// Package ceph is gated behind -tags ceph (requires librados-dev + librbd-dev).
// This stub makes the package importable in default builds so that setup.go
// can reference ceph.Config and ceph.New without build-tag splits.
package ceph

import (
	"errors"

	"github.com/cire-ly/block-storage-api/storage"
)

// Config holds Ceph connection parameters.
type Config struct {
	Monitors []string
	Pool     string
	Keyring  string
}

// New always returns an error when built without -tags ceph.
func New(_ Config) (storage.VolumeBackend, error) {
	return nil, errors.New("ceph backend requires -tags ceph (librados-dev + librbd-dev)")
}
