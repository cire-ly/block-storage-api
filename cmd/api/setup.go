package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/cire-ly/block-storage-api/config"
	internaldb "github.com/cire-ly/block-storage-api/internal/db"
	"github.com/cire-ly/block-storage-api/internal/observability"
	"github.com/cire-ly/block-storage-api/storage"
	"github.com/cire-ly/block-storage-api/storage/ceph"
	"github.com/cire-ly/block-storage-api/storage/mock"
	"github.com/cire-ly/block-storage-api/volume"
	"github.com/cire-ly/block-storage-api/volume/repository"
)

// ResourcesRegistry owns every long-lived resource and manages their lifecycle.
// Startup order is explicit; shutdown is LIFO via closers.
type ResourcesRegistry struct {
	config *config.Config
	logger *slog.Logger
	db     struct {
		pool *pgxpool.Pool
		repo volume.DatabaseDependency
	}
	storage struct {
		backend storage.VolumeBackend
	}
	http struct {
		router chi.Router
		server *http.Server
	}
	observability struct {
		tracerProvider *observability.TracerProvider
		meterProvider  *observability.MeterProvider
		tracer         trace.Tracer
		meter          metric.Meter
	}
	closers   []interface{ Close(context.Context) error }
	errorChan chan error
}

type setupFn struct {
	fn   func() error
	desc string
}

// Setup initialises every resource in dependency order.
func (rr *ResourcesRegistry) Setup() error {
	rr.errorChan = make(chan error, 1)

	// Default logger before config is loaded, so setup errors are visible.
	rr.logger = slog.Default()

	steps := []setupFn{
		{rr.setupConfig, "configuration"},
		{rr.setupLogger, "logger"},
		{rr.setupObservability, "observability (otel)"},
		{rr.setupDatabase, "database (postgresql)"},
		{rr.setupMigrations, "migrations"},
		{rr.setupBackend, "storage backend"},
		{rr.setupHTTP, "http server"},
		{rr.setupVolumeFeature, "volume feature"},
	}

	for _, s := range steps {
		rr.logger.Info("setup: " + s.desc + " ...")
		if err := s.fn(); err != nil {
			return fmt.Errorf("setup %s: %w", s.desc, err)
		}
		rr.logger.Info(s.desc + ": ok")
	}
	return nil
}

// Shutdown closes resources in reverse order (LIFO) and exits the process.
func (rr *ResourcesRegistry) Shutdown(appErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, closer := range slices.Backward(rr.closers) {
		if err := closer.Close(ctx); err != nil {
			rr.logger.Error("shutdown error", "err", err)
		}
	}

	if appErr != nil {
		rr.logger.Error("fatal error", "err", appErr)
		os.Exit(1)
	}
	os.Exit(0)
}

// -- individual setup steps --------------------------------------------------

func (rr *ResourcesRegistry) setupConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	rr.config = cfg
	return nil
}

func (rr *ResourcesRegistry) setupLogger() error {
	level := slog.LevelInfo
	if rr.config.Env == "development" {
		level = slog.LevelDebug
	}
	rr.logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(rr.logger)
	return nil
}

func (rr *ResourcesRegistry) setupObservability() error {
	ctx := context.Background()

	tp, err := observability.NewTracerProvider(ctx,
		rr.config.OtelServiceName,
		rr.config.OtelExporter,
		rr.config.OtelJaegerEndpoint,
	)
	if err != nil {
		return fmt.Errorf("tracer provider: %w", err)
	}
	rr.observability.tracerProvider = tp
	rr.observability.tracer = observability.Tracer(rr.config.OtelServiceName)
	rr.closers = append(rr.closers, tp)

	mp, err := observability.NewMeterProvider(ctx, rr.config.OtelServiceName)
	if err != nil {
		return fmt.Errorf("meter provider: %w", err)
	}
	rr.observability.meterProvider = mp
	rr.observability.meter = observability.Meter(rr.config.OtelServiceName)
	rr.closers = append(rr.closers, mp)

	return nil
}

func (rr *ResourcesRegistry) setupDatabase() error {
	if rr.config.DatabaseURL == "" {
		rr.logger.Warn("DATABASE_URL not set — using in-memory volume repository")
		rr.db.repo = repository.NewInMemoryRepository()
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := internaldb.Connect(ctx, rr.config.DatabaseURL)
	if err != nil {
		return err
	}
	rr.db.pool = pool
	rr.db.repo = repository.NewPostgresRepository(pool)
	rr.closers = append(rr.closers, &poolCloser{pool})
	return nil
}

func (rr *ResourcesRegistry) setupMigrations() error {
	if rr.db.pool == nil {
		return nil
	}
	return internaldb.RunMigrations(rr.config.DatabaseURL)
}

func (rr *ResourcesRegistry) setupBackend() error {
	switch rr.config.StorageBackend {
	case "ceph":
		b, err := ceph.New(ceph.Config{
			Monitors: rr.config.CephMonitors,
			Pool:     rr.config.CephPool,
			Keyring:  rr.config.CephKeyring,
		})
		if err != nil {
			return fmt.Errorf("ceph backend: %w", err)
		}
		rr.storage.backend = b
	default:
		rr.storage.backend = mock.New()
	}
	rr.closers = append(rr.closers, rr.storage.backend)
	return nil
}

func (rr *ResourcesRegistry) setupHTTP() error {
	rr.http.router = chi.NewMux()

	rr.http.server = &http.Server{
		Addr:         ":" + strconv.Itoa(rr.config.Port),
		Handler:      rr.http.router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	rr.closers = append(rr.closers, &httpCloser{rr.http.server})
	return nil
}

func (rr *ResourcesRegistry) setupVolumeFeature() error {
	feat, err := volume.NewVolumeFeature(volume.NewVolumeFeatureParams{
		Logger:  rr.logger,
		Backend: rr.storage.backend,
		DB:      rr.db.repo,
		Tracer:  rr.observability.tracer,
		Meter:   rr.observability.meter,
		Router:  rr.http.router,
		RetryPolicy: volume.RetryPolicy{
			MaxAttempts: rr.config.RetryPolicy.MaxAttempts,
			InitialWait: rr.config.RetryPolicy.InitialWait,
			Multiplier:  rr.config.RetryPolicy.Multiplier,
			MaxWait:     rr.config.RetryPolicy.MaxWait,
		},
		ReconcilePolicy: rr.config.ReconcilePolicy,
	})
	if err != nil {
		return fmt.Errorf("volume feature: %w", err)
	}
	rr.closers = append(rr.closers, feat)
	return nil
}

// -- closer adapters ---------------------------------------------------------

type poolCloser struct{ p *pgxpool.Pool }

func (c *poolCloser) Close(_ context.Context) error {
	c.p.Close()
	return nil
}

type httpCloser struct{ s *http.Server }

func (c *httpCloser) Close(ctx context.Context) error {
	return c.s.Shutdown(ctx)
}
