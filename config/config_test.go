package config_test

import (
	"os"
	"testing"
	"time"

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
	if cfg.Port != 8080 {
		t.Errorf("Port = %d, want 8080", cfg.Port)
	}
	// Retry policy defaults.
	if cfg.RetryPolicy.MaxAttempts != 3 {
		t.Errorf("RetryPolicy.MaxAttempts = %d, want 3", cfg.RetryPolicy.MaxAttempts)
	}
	if cfg.RetryPolicy.InitialWait != 500*time.Millisecond {
		t.Errorf("RetryPolicy.InitialWait = %s, want 500ms", cfg.RetryPolicy.InitialWait)
	}
	if cfg.RetryPolicy.Multiplier != 2.0 {
		t.Errorf("RetryPolicy.Multiplier = %g, want 2.0", cfg.RetryPolicy.Multiplier)
	}
	if cfg.RetryPolicy.MaxWait != 10*time.Second {
		t.Errorf("RetryPolicy.MaxWait = %s, want 10s", cfg.RetryPolicy.MaxWait)
	}
}

func TestLoadFromEnv(t *testing.T) {
	setEnv(t,
		"STORAGE_BACKEND", "ceph",
		"PORT", "9090",
		"DATABASE_URL", "postgres://u:p@host/db",
	)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.StorageBackend != "ceph" {
		t.Errorf("StorageBackend = %q", cfg.StorageBackend)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port = %d", cfg.Port)
	}
	if cfg.DatabaseURL != "postgres://u:p@host/db" {
		t.Errorf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}

func TestInvalidBackend(t *testing.T) {
	setEnv(t, "STORAGE_BACKEND", "s3")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid backend, got nil")
	}
}

func TestInvalidPort(t *testing.T) {
	setEnv(t, "PORT", "not-a-number")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid port, got nil")
	}
}

func TestCephMonitorsParsed(t *testing.T) {
	setEnv(t, "CEPH_MONITORS", "10.0.0.1:6789,10.0.0.2:6789")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.CephMonitors) != 2 {
		t.Errorf("CephMonitors len = %d, want 2", len(cfg.CephMonitors))
	}
	if cfg.CephMonitors[0] != "10.0.0.1:6789" {
		t.Errorf("CephMonitors[0] = %q, want 10.0.0.1:6789", cfg.CephMonitors[0])
	}
}

func TestRetryPolicyFromEnv(t *testing.T) {
	setEnv(t,
		"VOLUME_RETRY_MAX_ATTEMPTS", "5",
		"VOLUME_RETRY_INITIAL_WAIT", "1s",
		"VOLUME_RETRY_MULTIPLIER", "1.5",
		"VOLUME_RETRY_MAX_WAIT", "30s",
	)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RetryPolicy.MaxAttempts != 5 {
		t.Errorf("MaxAttempts = %d, want 5", cfg.RetryPolicy.MaxAttempts)
	}
	if cfg.RetryPolicy.InitialWait != time.Second {
		t.Errorf("InitialWait = %s, want 1s", cfg.RetryPolicy.InitialWait)
	}
	if cfg.RetryPolicy.Multiplier != 1.5 {
		t.Errorf("Multiplier = %g, want 1.5", cfg.RetryPolicy.Multiplier)
	}
	if cfg.RetryPolicy.MaxWait != 30*time.Second {
		t.Errorf("MaxWait = %s, want 30s", cfg.RetryPolicy.MaxWait)
	}
}

func TestInvalidRetryMaxAttempts(t *testing.T) {
	setEnv(t, "VOLUME_RETRY_MAX_ATTEMPTS", "0")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for MaxAttempts=0, got nil")
	}
}

func TestInvalidRetryMaxAttemptsNotANumber(t *testing.T) {
	setEnv(t, "VOLUME_RETRY_MAX_ATTEMPTS", "bad")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for non-numeric MaxAttempts, got nil")
	}
}

func TestInvalidRetryInitialWait(t *testing.T) {
	setEnv(t, "VOLUME_RETRY_INITIAL_WAIT", "bad")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid InitialWait, got nil")
	}
}

func TestInvalidRetryMultiplier(t *testing.T) {
	setEnv(t, "VOLUME_RETRY_MULTIPLIER", "bad")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid Multiplier, got nil")
	}
}

func TestInvalidRetryMultiplierTooLow(t *testing.T) {
	setEnv(t, "VOLUME_RETRY_MULTIPLIER", "0.5")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for Multiplier < 1.0, got nil")
	}
}

func TestInvalidRetryMaxWait(t *testing.T) {
	setEnv(t, "VOLUME_RETRY_MAX_WAIT", "bad")
	_, err := config.Load()
	if err == nil {
		t.Error("expected error for invalid MaxWait, got nil")
	}
}
