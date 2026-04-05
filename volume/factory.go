package volume

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/cire-ly/block-storage-api/assertor"
	"github.com/cire-ly/block-storage-api/storage"
)

// NewVolumeFeatureParams holds all required dependencies for the volume feature.
// Every field is required unless noted.
type NewVolumeFeatureParams struct {
	Logger      LoggerDependency         // required
	Backend     StorageBackendDependency // required
	DB          DatabaseDependency       // required
	Tracer      trace.Tracer             // required
	Meter       metric.Meter             // optional
	Router      chi.Router               // required
	RetryPolicy RetryPolicy              // optional — DefaultRetryPolicy applied when zero
}

// VolumeFeature is the wired volume feature, implementing FeatureContract.
type VolumeFeature struct {
	app       *application
	wg        sync.WaitGroup             // tracks all internal goroutines
	cancelCtx context.CancelFunc         // cancels the internal lifecycle context
	closers   []interface{ Close(context.Context) error }
}

// NewVolumeFeature validates all params, wires the application and HTTP controller,
// registers routes, and schedules startup reconciliation.
func NewVolumeFeature(params NewVolumeFeatureParams) (*VolumeFeature, error) {
	v := assertor.New()
	v.Assert(params.Logger != nil, "logger is required")
	v.Assert(params.Backend != nil, "storage backend is required")
	v.Assert(params.DB != nil, "database is required")
	v.Assert(params.Tracer != nil, "tracer is required")
	v.Assert(params.Router != nil, "router is required")
	if err := v.Validate(); err != nil {
		return nil, err
	}

	policy := params.RetryPolicy
	if policy.MaxAttempts == 0 {
		policy = DefaultRetryPolicy()
	}

	// internalCtx is the lifecycle context for all goroutines owned by this feature.
	// Canceling it (via feat.cancelCtx) stops reconciliation and any future monitors.
	internalCtx, cancel := context.WithCancel(context.Background())

	feat := &VolumeFeature{cancelCtx: cancel}

	// Application holds a pointer to feat.wg so retry goroutines are tracked.
	feat.app = newApplication(params.Backend, params.DB, params.Logger, params.Tracer, policy, &feat.wg)

	ctrl := newHTTPController(feat.app, params.Logger, params.Tracer, params.Meter)
	ctrl.registerRoutes(params.Router)

	// Reconcile volumes interrupted mid-transition at previous shutdown.
	// Runs inside the tracked WaitGroup so Close() waits for it to finish.
	feat.wg.Add(1)
	go func() {
		defer feat.wg.Done()
		feat.reconcileOnStartup(internalCtx)
	}()

	return feat, nil
}

// Application returns the ApplicationContract for this feature.
func (f *VolumeFeature) Application() ApplicationContract {
	return f.app
}

// Close shuts down the feature in three steps:
//  1. Cancel the internal lifecycle context — signals all goroutines to stop.
//  2. Wait for all goroutines to finish, bounded by the caller's deadline.
//  3. Close remaining resources in LIFO order.
func (f *VolumeFeature) Close(ctx context.Context) error {
	// Step 1: signal all internal goroutines to stop.
	f.cancelCtx()

	// Step 2: wait for goroutines with the caller's deadline.
	done := make(chan struct{})
	go func() {
		f.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// all goroutines exited cleanly
	case <-ctx.Done():
		return fmt.Errorf("shutdown timeout: goroutines still running")
	}

	// Step 3: close other resources LIFO, collecting all errors.
	var err error
	for _, closer := range slices.Backward(f.closers) {
		err = errors.Join(err, closer.Close(ctx))
	}
	return err
}

// reconcileOnStartup re-drives volumes that were stuck in a transitional state
// at the time of the last shutdown. Each volume is reconciled in its own goroutine;
// the function blocks until all are done.
func (f *VolumeFeature) reconcileOnStartup(ctx context.Context) {
	transitional := []string{
		StateCreating, StateAttaching, StateDetaching, StateDeleting,
		StateCreatingFailed, StateAttachingFailed, StateDetachingFailed, StateDeletingFailed,
	}

	volumes, err := f.app.db.ListVolumesByState(ctx, transitional...)
	if err != nil {
		f.app.logger.Error("reconcile: failed to list transitional volumes", "err", err)
		return
	}

	if len(volumes) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, v := range volumes {
		wg.Add(1)
		go func(vol *storage.Volume) {
			defer wg.Done()
			f.reconcileVolume(ctx, vol)
		}(v)
	}
	wg.Wait()
}

// reconcileVolume checks a single transitional volume against the backend and
// moves it to either available or error state.
func (f *VolumeFeature) reconcileVolume(ctx context.Context, v *storage.Volume) {
	f.app.logger.Info("reconcile: found transitional volume",
		"name", v.Name, "state", v.State)

	real, backendErr := f.app.backend.GetVolume(ctx, v.Name)
	if backendErr != nil || real == nil {
		// Volume not found in backend — push it to error state.
		f.app.logger.Warn("reconcile: volume absent from backend, marking error",
			"name", v.Name, "state", v.State)
		f.reconcileToError(ctx, v.Name, v.State)
		return
	}

	// Volume exists in backend — force it to available.
	f.app.logger.Info("reconcile: volume confirmed in backend, marking available",
		"name", v.Name, "state", v.State)
	fresh, _ := f.app.db.LoadVolume(ctx, v.Name)
	if fresh == nil {
		return
	}
	fresh.State = StateAvailable
	fresh.NodeID = real.NodeID
	_ = f.app.db.UpdateVolume(ctx, fresh)
	_ = f.app.db.SaveEvent(ctx, VolumeEvent{
		VolumeID:  fresh.ID,
		Event:     "reconcile",
		FromState: v.State,
		ToState:   StateAvailable,
	})
}

// reconcileToError moves a volume to the terminal error state, going through
// the *_failed intermediate if necessary.
func (f *VolumeFeature) reconcileToError(ctx context.Context, name, currentState string) {
	v, _ := f.app.db.LoadVolume(ctx, name)
	if v == nil {
		return
	}

	// If still in an in-progress state, apply error first to reach *_failed.
	if CanTransition(v.State, EventError) {
		_ = f.app.applyEvent(ctx, v, EventError)
		v, _ = f.app.db.LoadVolume(ctx, name)
		if v == nil {
			return
		}
	}

	// From *_failed apply fail to reach terminal error.
	if CanTransition(v.State, EventFail) {
		_ = f.app.applyEvent(ctx, v, EventFail)
	}

	_ = currentState // suppress unused-variable lint when the compiler inlines
}
