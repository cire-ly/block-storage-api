package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// RetryPolicy controls exponential back-off for failed FSM transitions.
// Values are loaded from environment variables; defaults are applied when the
// variables are absent.
type RetryPolicy struct {
	MaxAttempts int           // VOLUME_RETRY_MAX_ATTEMPTS, default: 3
	InitialWait time.Duration // VOLUME_RETRY_INITIAL_WAIT, default: 500ms
	Multiplier  float64       // VOLUME_RETRY_MULTIPLIER,   default: 2.0
	MaxWait     time.Duration // VOLUME_RETRY_MAX_WAIT,     default: 10s
}

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	StorageBackend string

	DatabaseURL string

	Port int
	Env  string

	CephMonitors []string
	CephPool     string
	CephKeyring  string

	OtelExporter       string
	OtelJaegerEndpoint string
	OtelServiceName    string

	RetryPolicy RetryPolicy
}

// Load reads configuration from environment variables and validates it.
func Load() (*Config, error) {
	port, err := strconv.Atoi(getEnv("PORT", "8080"))
	if err != nil {
		return nil, fmt.Errorf("invalid PORT: %w", err)
	}

	retryMaxAttempts, err := strconv.Atoi(getEnv("VOLUME_RETRY_MAX_ATTEMPTS", "3"))
	if err != nil {
		return nil, fmt.Errorf("invalid VOLUME_RETRY_MAX_ATTEMPTS: %w", err)
	}

	retryInitialWait, err := time.ParseDuration(getEnv("VOLUME_RETRY_INITIAL_WAIT", "500ms"))
	if err != nil {
		return nil, fmt.Errorf("invalid VOLUME_RETRY_INITIAL_WAIT: %w", err)
	}

	retryMultiplier, err := strconv.ParseFloat(getEnv("VOLUME_RETRY_MULTIPLIER", "2.0"), 64)
	if err != nil {
		return nil, fmt.Errorf("invalid VOLUME_RETRY_MULTIPLIER: %w", err)
	}

	retryMaxWait, err := time.ParseDuration(getEnv("VOLUME_RETRY_MAX_WAIT", "10s"))
	if err != nil {
		return nil, fmt.Errorf("invalid VOLUME_RETRY_MAX_WAIT: %w", err)
	}

	cfg := &Config{
		StorageBackend:     getEnv("STORAGE_BACKEND", "mock"),
		DatabaseURL:        getEnv("DATABASE_URL", ""),
		Port:               port,
		Env:                getEnv("ENV", "development"),
		CephMonitors:       splitComma(getEnv("CEPH_MONITORS", "")),
		CephPool:           getEnv("CEPH_POOL", "rbd-demo"),
		CephKeyring:        getEnv("CEPH_KEYRING", "/etc/ceph/ceph.client.admin.keyring"),
		OtelExporter:       getEnv("OTEL_EXPORTER", "stdout"),
		OtelJaegerEndpoint: getEnv("OTEL_JAEGER_ENDPOINT", "http://localhost:4318/v1/traces"),
		OtelServiceName:    getEnv("OTEL_SERVICE_NAME", "block-storage-api"),
		RetryPolicy: RetryPolicy{
			MaxAttempts: retryMaxAttempts,
			InitialWait: retryInitialWait,
			Multiplier:  retryMultiplier,
			MaxWait:     retryMaxWait,
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	valid := map[string]bool{"mock": true, "ceph": true}
	if !valid[c.StorageBackend] {
		return fmt.Errorf("invalid STORAGE_BACKEND: %s (valid: mock, ceph)", c.StorageBackend)
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("invalid PORT: %d", c.Port)
	}
	if c.RetryPolicy.MaxAttempts < 1 {
		return fmt.Errorf("invalid VOLUME_RETRY_MAX_ATTEMPTS: must be >= 1, got %d", c.RetryPolicy.MaxAttempts)
	}
	if c.RetryPolicy.InitialWait <= 0 {
		return fmt.Errorf("invalid VOLUME_RETRY_INITIAL_WAIT: must be > 0, got %s", c.RetryPolicy.InitialWait)
	}
	if c.RetryPolicy.Multiplier < 1.0 {
		return fmt.Errorf("invalid VOLUME_RETRY_MULTIPLIER: must be >= 1.0, got %g", c.RetryPolicy.Multiplier)
	}
	if c.RetryPolicy.MaxWait <= 0 {
		return fmt.Errorf("invalid VOLUME_RETRY_MAX_WAIT: must be > 0, got %s", c.RetryPolicy.MaxWait)
	}
	return nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
