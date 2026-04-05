package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

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
}

// Load reads configuration from environment variables and validates it.
func Load() (*Config, error) {
	port, err := strconv.Atoi(getEnv("PORT", "8080"))
	if err != nil {
		return nil, fmt.Errorf("invalid PORT: %w", err)
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
