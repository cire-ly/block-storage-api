package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	StorageBackend      string
	ConsistencyStrategy string

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

func Load() (*Config, error) {
	port, err := strconv.Atoi(getEnv("PORT", "8080"))
	if err != nil {
		return nil, fmt.Errorf("invalid PORT: %w", err)
	}

	cfg := &Config{
		StorageBackend:      getEnv("STORAGE_BACKEND", "mock"),
		ConsistencyStrategy: getEnv("CONSISTENCY_STRATEGY", "cp"),
		DatabaseURL:         getEnv("DATABASE_URL", ""),
		Port:                port,
		Env:                 getEnv("ENV", "development"),
		CephMonitors:        splitComma(getEnv("CEPH_MONITORS", "")),
		CephPool:            getEnv("CEPH_POOL", "rbd-demo"),
		CephKeyring:         getEnv("CEPH_KEYRING", "/etc/ceph/ceph.client.admin.keyring"),
		OtelExporter:        getEnv("OTEL_EXPORTER", "stdout"),
		OtelJaegerEndpoint:  getEnv("OTEL_JAEGER_ENDPOINT", "http://localhost:4318/v1/traces"),
		OtelServiceName:     getEnv("OTEL_SERVICE_NAME", "block-storage-api"),
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	valid := map[string]bool{"mock": true, "ceph": true, "lustre": true, "nvmeof": true}
	if !valid[c.StorageBackend] {
		return fmt.Errorf("invalid STORAGE_BACKEND: %s", c.StorageBackend)
	}
	if c.ConsistencyStrategy != "cp" && c.ConsistencyStrategy != "ap" {
		return fmt.Errorf("invalid CONSISTENCY_STRATEGY: %s (must be cp or ap)", c.ConsistencyStrategy)
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
