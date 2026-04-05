package volume

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"

	"github.com/cire-ly/block-storage-api/storage"
)

// Timeout constants applied to every external call.
const (
	dbTimeout      = 5 * time.Second
	backendTimeout = 30 * time.Second
	healthTimeout  = 3 * time.Second
)

// Sentinel errors used for HTTP status mapping.
var (
	ErrVolumeNotFound    = errors.New("volume not found")
	ErrVolumeExists      = errors.New("volume already exists")
	ErrInvalidTransition = errors.New("invalid state transition")
	ErrInvalidSize       = errors.New("size must be > 0 MB")
)

// application implements ApplicationContract.
// It owns zero HTTP or transport imports — pure business logic only.
type application struct {
	backend StorageBackendDependency
	db      DatabaseDependency
	logger  LoggerDependency
	tracer  trace.Tracer
	policy  RetryPolicy
}

func newApplication(
	backend StorageBackendDependency,
	db DatabaseDependency,
	logger LoggerDependency,
	tracer trace.Tracer,
) *application {
	return &application{
		backend: backend,
		db:      db,
		logger:  logger,
		tracer:  tracer,
		policy:  DefaultRetryPolicy(),
	}
}

// loggerFromCtx extracts the enriched logger injected by the HTTP middleware.
// Falls back to the provided default if none is present in ctx.
func loggerFromCtx(ctx context.Context, fallback LoggerDependency) LoggerDependency {
	if l, ok := ctx.Value(loggerCtxKey).(LoggerDependency); ok {
		return l
	}
	return fallback
}

func (a *application) CreateVolume(ctx context.Context, name string, sizeMB int) (*storage.Volume, error) {
	ctx, span := a.tracer.Start(ctx, "application.CreateVolume")
	defer span.End()

	if sizeMB <= 0 {
		return nil, ErrInvalidSize
	}

	// Guard against duplicate names before touching the DB.
	dbCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()

	existing, err := a.db.LoadVolume(dbCtx, name)
	if err != nil {
		return nil, fmt.Errorf("check existing volume: %w", err)
	}
	if existing != nil {
		return nil, fmt.Errorf("%w: %q", ErrVolumeExists, name)
	}

	now := time.Now().UTC()
	v := &storage.Volume{
		ID:        uuid.New().String(),
		Name:      name,
		SizeMB:    sizeMB,
		State:     StatePending,
		Backend:   a.backend.BackendName(),
		CreatedAt: now,
		UpdatedAt: now,
	}

	if err := a.db.SaveVolume(dbCtx, v); err != nil {
		return nil, fmt.Errorf("save volume: %w", err)
	}

	// pending → creating
	if err := a.applyEvent(ctx, v, EventCreate); err != nil {
		return nil, err
	}

	// Async: create on backend, then transition to available or retry.
	// ctx is derived from BaseContext (app lifecycle) — goroutine stops on shutdown.
	go a.runWithRetry(
		ctx, v.Name,
		func(bCtx context.Context) error {
			_, bErr := a.backend.CreateVolume(bCtx, v.Name, v.SizeMB)
			return bErr
		},
		EventReady,
	)

	return copyVolume(v), nil
}

func (a *application) DeleteVolume(ctx context.Context, name string) error {
	ctx, span := a.tracer.Start(ctx, "application.DeleteVolume")
	defer span.End()

	v, err := a.loadOrNotFound(ctx, name)
	if err != nil {
		return err
	}

	// available → deleting
	if err := a.applyEvent(ctx, v, EventDelete); err != nil {
		return err
	}

	go a.runWithRetry(
		ctx, v.Name,
		func(bCtx context.Context) error {
			return a.backend.DeleteVolume(bCtx, v.Name)
		},
		EventDeleted,
	)

	return nil
}

func (a *application) ListVolumes(ctx context.Context) ([]*storage.Volume, error) {
	ctx, span := a.tracer.Start(ctx, "application.ListVolumes")
	defer span.End()

	dbCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()

	return a.db.ListVolumes(dbCtx)
}

func (a *application) GetVolume(ctx context.Context, name string) (*storage.Volume, error) {
	ctx, span := a.tracer.Start(ctx, "application.GetVolume")
	defer span.End()

	return a.loadOrNotFound(ctx, name)
}

func (a *application) AttachVolume(ctx context.Context, name string, nodeID string) error {
	ctx, span := a.tracer.Start(ctx, "application.AttachVolume")
	defer span.End()

	v, err := a.loadOrNotFound(ctx, name)
	if err != nil {
		return err
	}

	// Record node before transitioning so the DB reflects the target node.
	v.NodeID = nodeID
	dbCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()
	if err := a.db.UpdateVolume(dbCtx, v); err != nil {
		return fmt.Errorf("set node_id: %w", err)
	}

	// available → attaching
	if err := a.applyEvent(ctx, v, EventAttach); err != nil {
		return err
	}

	go a.runWithRetry(
		ctx, v.Name,
		func(bCtx context.Context) error {
			return a.backend.AttachVolume(bCtx, v.Name, nodeID)
		},
		EventAttached,
	)

	return nil
}

func (a *application) DetachVolume(ctx context.Context, name string) error {
	ctx, span := a.tracer.Start(ctx, "application.DetachVolume")
	defer span.End()

	v, err := a.loadOrNotFound(ctx, name)
	if err != nil {
		return err
	}

	// attached → detaching
	if err := a.applyEvent(ctx, v, EventDetach); err != nil {
		return err
	}

	go a.runWithRetry(
		ctx, v.Name,
		func(bCtx context.Context) error {
			return a.backend.DetachVolume(bCtx, v.Name)
		},
		EventDetached,
		func(v *storage.Volume) { v.NodeID = "" }, // clear node assignment on success
	)

	return nil
}

// ResetVolume transitions a volume from the terminal error state back to pending.
// Returns ErrInvalidTransition when the volume is not in the error state.
func (a *application) ResetVolume(ctx context.Context, name string) error {
	ctx, span := a.tracer.Start(ctx, "application.ResetVolume")
	defer span.End()

	v, err := a.loadOrNotFound(ctx, name)
	if err != nil {
		return err
	}

	// error → pending
	return a.applyEvent(ctx, v, EventReset)
}

func (a *application) HealthCheck(ctx context.Context) error {
	hCtx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	return a.backend.HealthCheck(hCtx)
}

// applyEvent applies a single FSM event, persists the new state, and records
// it in the audit trail.
func (a *application) applyEvent(ctx context.Context, v *storage.Volume, event string) error {
	if !CanTransition(v.State, event) {
		return fmt.Errorf("%w: cannot %q from %q", ErrInvalidTransition, event, v.State)
	}

	fromState := v.State
	newState, err := Transition(ctx, v.State, event)
	if err != nil {
		return err
	}

	v.State = newState
	v.UpdatedAt = time.Now().UTC()

	dbCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()

	if err := a.db.UpdateVolume(dbCtx, v); err != nil {
		return fmt.Errorf("persist state %s: %w", newState, err)
	}

	if saveErr := a.db.SaveEvent(dbCtx, VolumeEvent{
		VolumeID:  v.ID,
		Event:     event,
		FromState: fromState,
		ToState:   newState,
	}); saveErr != nil {
		loggerFromCtx(ctx, a.logger).Warn("failed to save volume event",
			"err", saveErr, "volume", v.Name, "event", event)
	}

	return nil
}

// runWithRetry executes op with exponential back-off.
// On success it fires successEvent; on exhausted retries it fires EventFail.
// postSuccess, if non-nil, mutates the volume before the success event is persisted.
// The goroutine stops when ctx is canceled (e.g., server shutdown via BaseContext).
func (a *application) runWithRetry(
	ctx context.Context,
	name string,
	op func(context.Context) error,
	successEvent string,
	postSuccess ...func(*storage.Volume),
) {
	log := loggerFromCtx(ctx, a.logger)
	attempts := 0
	wait := a.policy.InitialWait

	for {
		// Stop if the application is shutting down.
		select {
		case <-ctx.Done():
			return
		default:
		}

		attempts++

		// Each backend attempt gets a dedicated timeout.
		bCtx, bCancel := context.WithTimeout(ctx, backendTimeout)
		err := op(bCtx)
		bCancel()

		if err == nil {
			dbCtx, dbCancel := context.WithTimeout(ctx, dbTimeout)
			v, loadErr := a.db.LoadVolume(dbCtx, name)
			dbCancel()
			if loadErr != nil || v == nil {
				log.Error("retry: cannot load volume after success",
					"name", name, "event", successEvent)
				return
			}
			if len(postSuccess) > 0 && postSuccess[0] != nil {
				postSuccess[0](v)
			}
			if applyErr := a.applyEvent(ctx, v, successEvent); applyErr != nil {
				log.Error("retry: cannot apply success event",
					"name", name, "event", successEvent, "err", applyErr)
			}
			return
		}

		log.Warn("operation failed, will retry",
			"name", name, "attempt", attempts, "max", a.policy.MaxAttempts, "err", err)

		// Load fresh state to apply error event.
		dbCtx, dbCancel := context.WithTimeout(ctx, dbTimeout)
		v, loadErr := a.db.LoadVolume(dbCtx, name)
		dbCancel()
		if loadErr != nil || v == nil {
			log.Error("retry: cannot load volume", "name", name)
			return
		}

		// Transition to *_failed state.
		if applyErr := a.applyEvent(ctx, v, EventError); applyErr != nil {
			log.Error("retry: cannot apply error event", "name", name, "err", applyErr)
			return
		}

		if attempts >= a.policy.MaxAttempts {
			// Exhausted retries — move to terminal error.
			dbCtx2, dbCancel2 := context.WithTimeout(ctx, dbTimeout)
			v, _ = a.db.LoadVolume(dbCtx2, name)
			dbCancel2()
			if v != nil {
				if applyErr := a.applyEvent(ctx, v, EventFail); applyErr != nil {
					log.Error("retry: cannot apply fail event", "name", name, "err", applyErr)
				}
			}
			log.Error("operation failed after max retries", "name", name, "attempts", attempts)
			return
		}

		// Back-off — cancel early if shutdown is requested.
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		// *_failed → back to in-progress.
		dbCtx3, dbCancel3 := context.WithTimeout(ctx, dbTimeout)
		v, _ = a.db.LoadVolume(dbCtx3, name)
		dbCancel3()
		if v != nil {
			if applyErr := a.applyEvent(ctx, v, EventRetry); applyErr != nil {
				log.Error("retry: cannot apply retry event", "name", name, "err", applyErr)
				return
			}
		}

		// Exponential back-off.
		wait = time.Duration(float64(wait) * a.policy.Multiplier)
		if wait > a.policy.MaxWait {
			wait = a.policy.MaxWait
		}
	}
}

// loadOrNotFound wraps LoadVolume with a friendly not-found error.
func (a *application) loadOrNotFound(ctx context.Context, name string) (*storage.Volume, error) {
	dbCtx, cancel := context.WithTimeout(ctx, dbTimeout)
	defer cancel()

	v, err := a.db.LoadVolume(dbCtx, name)
	if err != nil {
		return nil, fmt.Errorf("load volume: %w", err)
	}
	if v == nil {
		return nil, fmt.Errorf("%w: %q", ErrVolumeNotFound, name)
	}
	return v, nil
}

func copyVolume(v *storage.Volume) *storage.Volume {
	cp := *v
	return &cp
}

// slogAdapter wraps *slog.Logger to satisfy LoggerDependency.
// Used internally when the context carries a *slog.Logger.
type slogAdapter struct{ l *slog.Logger }

func (s slogAdapter) Debug(msg string, args ...any) { s.l.Debug(msg, args...) }
func (s slogAdapter) Info(msg string, args ...any)  { s.l.Info(msg, args...) }
func (s slogAdapter) Warn(msg string, args ...any)  { s.l.Warn(msg, args...) }
func (s slogAdapter) Error(msg string, args ...any) { s.l.Error(msg, args...) }
