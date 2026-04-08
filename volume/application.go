package volume

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	ErrVolumeNotFound     = errors.New("volume not found")
	ErrVolumeExists       = errors.New("volume already exists")
	ErrInvalidTransition  = errors.New("invalid state transition")
	ErrInvalidSize        = errors.New("size must be > 0 MB")
	ErrBackendUnavailable = errors.New("backend unavailable")
)

// application implements ApplicationContract.
// It owns zero HTTP or transport imports — pure business logic only.
type application struct {
	backend StorageBackendDependency
	db      DatabaseDependency
	logger  LoggerDependency
	tracer  trace.Tracer
	policy  RetryPolicy
	wg      *sync.WaitGroup // shared with VolumeFeature — tracks all retry goroutines
	bgCtx   context.Context // independent of HTTP requests — outlives any single handler
}

func newApplication(
	backend StorageBackendDependency,
	db DatabaseDependency,
	logger LoggerDependency,
	tracer trace.Tracer,
	policy RetryPolicy,
	wg *sync.WaitGroup,
	bgCtx context.Context,
) *application {
	return &application{
		backend: backend,
		db:      db,
		logger:  logger,
		tracer:  tracer,
		policy:  policy,
		wg:      wg,
		bgCtx:   bgCtx,
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
	// Uses a.bgCtx (not the HTTP request ctx) so the goroutine outlives the 202 response.
	// The goroutine is tracked by wg so VolumeFeature.Close() can drain it.
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.runWithRetry(
			a.bgCtx, v.Name,
			func(bCtx context.Context) error {
				_, bErr := a.backend.CreateVolume(bCtx, v.Name, v.SizeMB)
				return bErr
			},
			EventReady,
		)
	}()

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

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.runWithRetry(
			a.bgCtx, v.Name,
			func(bCtx context.Context) error {
				return a.backend.DeleteVolume(bCtx, v.Name)
			},
			EventDeleted,
		)
	}()

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

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.runWithRetry(
			a.bgCtx, v.Name,
			func(bCtx context.Context) error {
				return a.backend.AttachVolume(bCtx, v.Name, nodeID)
			},
			EventAttached,
		)
	}()

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

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.runWithRetry(
			a.bgCtx, v.Name,
			func(bCtx context.Context) error {
				return a.backend.DetachVolume(bCtx, v.Name)
			},
			EventDetached,
			func(v *storage.Volume) { v.NodeID = "" }, // clear node assignment on success
		)
	}()

	return nil
}

// ReconcileVolume aligns the FSM state with the real backend state.
// Only valid when the volume is in the error state — returns ErrInvalidTransition otherwise.
// Decision logic:
//   - backend unavailable          → keeps error state, returns ErrBackendUnavailable
//   - volume absent from backend   → FSM → pending
//   - volume present, NodeID != "" → FSM → attached (preserves NodeID)
//   - volume present, NodeID == "" → FSM → available
//
// Resets the retry counter and records a volume_event for audit.
func (a *application) ReconcileVolume(ctx context.Context, name string) (*storage.Volume, error) {
	ctx, span := a.tracer.Start(ctx, "application.ReconcileVolume")
	defer span.End()

	v, err := a.loadOrNotFound(ctx, name)
	if err != nil {
		return nil, err
	}

	if v.State != StateError {
		return nil, fmt.Errorf("%w: reconcile requires error state, got %q", ErrInvalidTransition, v.State)
	}

	bCtx, bCancel := context.WithTimeout(ctx, backendTimeout)
	real, backendErr := a.backend.GetVolume(bCtx, v.Name)
	bCancel()

	// A non-nil error means the backend is unreachable (network failure, etc.).
	// A nil result with no error means the volume does not exist in the backend.
	if backendErr != nil {
		return nil, fmt.Errorf("%w: %s", ErrBackendUnavailable, backendErr)
	}

	fromState := v.State

	if real == nil {
		// Volume does not exist in backend — reset to pending.
		v.State = StatePending
		v.NodeID = ""
	} else if real.NodeID != "" {
		// Volume is attached on a node.
		v.State = StateAttached
		v.NodeID = real.NodeID
	} else {
		// Volume exists but is not attached.
		v.State = StateAvailable
		v.NodeID = ""
	}

	v.UpdatedAt = time.Now().UTC()

	dbCtx, dbCancel := context.WithTimeout(ctx, dbTimeout)
	defer dbCancel()

	if err := a.db.UpdateVolume(dbCtx, v); err != nil {
		return nil, fmt.Errorf("persist reconciled state: %w", err)
	}

	if saveErr := a.db.SaveEvent(dbCtx, VolumeEvent{
		VolumeID:  v.ID,
		Event:     "reconcile",
		FromState: fromState,
		ToState:   v.State,
	}); saveErr != nil {
		loggerFromCtx(ctx, a.logger).Warn("failed to save reconcile event",
			"err", saveErr, "volume", v.Name)
	}

	return copyVolume(v), nil
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

// runWithRetry executes op with exponential back-off, up to policy.MaxAttempts times.
// On success it fires successEvent. On exhausted retries it fires EventFail.
// postSuccess, if provided, mutates the volume before the success event is persisted.
// The caller MUST wrap this function in a goroutine tracked by a.wg.
// The loop checks ctx.Done() before each attempt and during back-off so the
// goroutine stops cleanly when the server shuts down.
func (a *application) runWithRetry(
	ctx context.Context,
	name string,
	op func(context.Context) error,
	successEvent string,
	postSuccess ...func(*storage.Volume),
) {
	log := loggerFromCtx(ctx, a.logger)
	wait := a.policy.InitialWait

	for attempt := 1; attempt <= a.policy.MaxAttempts; attempt++ {
		// Stop before each attempt if the application is shutting down.
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Each backend call gets its own deadline.
		bCtx, bCancel := context.WithTimeout(ctx, backendTimeout)
		err := op(bCtx)
		bCancel()

		if err == nil {
			// Success: load volume, apply optional post-success mutation, fire event.
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

		log.Warn("operation failed",
			"name", name, "attempt", attempt, "max", a.policy.MaxAttempts, "err", err)

		// Transition to *_failed state.
		dbCtx, dbCancel := context.WithTimeout(ctx, dbTimeout)
		v, loadErr := a.db.LoadVolume(dbCtx, name)
		dbCancel()
		if loadErr != nil || v == nil {
			log.Error("retry: cannot load volume", "name", name)
			return
		}
		if applyErr := a.applyEvent(ctx, v, EventError); applyErr != nil {
			log.Error("retry: cannot apply error event", "name", name, "err", applyErr)
			return
		}

		// Exhausted all attempts — transition to terminal error state.
		if attempt == a.policy.MaxAttempts {
			dbCtx2, dbCancel2 := context.WithTimeout(ctx, dbTimeout)
			v, _ = a.db.LoadVolume(dbCtx2, name)
			dbCancel2()
			if v != nil {
				if applyErr := a.applyEvent(ctx, v, EventFail); applyErr != nil {
					log.Error("retry: cannot apply fail event", "name", name, "err", applyErr)
				}
			}
			log.Error("operation failed after max retries",
				"name", name, "attempts", attempt)
			return
		}

		// Back-off — exit early if the application is shutting down.
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		// *_failed → back to the in-progress state for the next attempt.
		dbCtx3, dbCancel3 := context.WithTimeout(ctx, dbTimeout)
		v, _ = a.db.LoadVolume(dbCtx3, name)
		dbCancel3()
		if v != nil {
			if applyErr := a.applyEvent(ctx, v, EventRetry); applyErr != nil {
				log.Error("retry: cannot apply retry event", "name", name, "err", applyErr)
				return
			}
		}

		// Exponential back-off with ceiling.
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
