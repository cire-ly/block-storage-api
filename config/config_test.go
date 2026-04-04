package config_test

import (
	"os"
	"testing"

	"github.com/cire-ly/block-storage-api/config"
)

func setEnv(t *testing.T, pairs ...string) {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		old, existed := os.LookupEnv(pairs[i])
		t.Cleanup(func() {
			if existed {
				os.Setenv(pairs[i], old)
			} else {
				os.Unsetenv(pairs[i])
			}
		})
		os.Setenv(pairs[i], pairs[i+1])
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StorageBackend != "mock" {
		t.Errorf("StorageBackend = %q, want mock", cfg.StorageBackend)
	}
	if cfg.ConsistencyStrategy != "cp" {
		t.Errorf("ConsistencyStrategy = %q, want cp", cfg.ConsistencyStrategy)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
}

func TestLoadFromEnv(t *testing.T) {
	setEnv(t,
		"STORAGE_BACKEND", "lustre",
		"CONSISTENCY_STRATEGY", "ap",
		"PORT", "9090",
		"DATABASE_URL", "postgres://u:p@host/db",
	)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StorageBackend != "lustre" {
		t.Errorf("StorageBackend = %q", cfg.StorageBackend)
	}
	if cfg.ConsistencyStrategy != "ap" {
		t.Errorf("ConsistencyStrategy = %q", cfg.ConsistencyStrategy)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d", cfg.Port)
	}
}

func TestInvalidBackend(t *testing.T) {
	setEnv(t, "STORAGE_BACKEND", "s3")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid backend, got nil")
	}
}

func TestInvalidConsistency(t *testing.T) {
	setEnv(t, "CONSISTENCY_STRATEGY", "both")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid consistency, got nil")
	}
}

func TestInvalidPort(t *testing.T) {
	setEnv(t, "PORT", "not-a-number")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid port, got nil")
	}
}
