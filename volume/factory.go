package volume

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/cire-ly/block-storage-api/assertor"
	"github.com/cire-ly/block-storage-api/config"
	"github.com/cire-ly/block-storage-api/storage"
)

// NewVolumeFeatureParams holds all required dependencies for the volume feature.
// Every field is required unless noted.
type NewVolumeFeatureParams struct {
	Logger          LoggerDependency         // required
	Backend         StorageBackendDependency // required
	DB              DatabaseDependency       // required
	Tracer          trace.Tracer             // required
	Meter           metric.Meter             // optional
	Router          chi.Router               // required
	RetryPolicy     RetryPolicy              // optional — DefaultRetryPolicy applied when zero
	ReconcilePolicy config.ReconcilePolicy   // optional — defaults: DBOnly=error, CephOnly=ignore
}

// VolumeFeature is the wired volume feature, implementing FeatureContract.
type VolumeFeature struct {
	app       *application
	wg        sync.WaitGroup     // tracks all internal goroutines
	cancelCtx context.CancelFunc // cancels the internal lifecycle context
	bgCtx     context.Context    // independent of HTTP requests — used by retry goroutines
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
	// Canceling it (via feat.cancelCtx) stops reconciliation and retry goroutines.
	// It is intentionally independent of any HTTP request context.
	internalCtx, cancel := context.WithCancel(context.Background())

	feat := &VolumeFeature{cancelCtx: cancel, bgCtx: internalCtx}

	// Application holds a pointer to feat.wg so retry goroutines are tracked,
	// and bgCtx so retry goroutines outlive the HTTP request that triggered them.
	feat.app = newApplication(params.Backend, params.DB, params.Logger, params.Tracer, policy, &feat.wg, internalCtx)

	ctrl := newHTTPController(feat.app, params.Logger, params.Tracer, params.Meter)
	ctrl.registerRoutes(params.Router)

	// Reconcile volumes interrupted mid-transition at previous shutdown.
	// Bounded to 60 seconds so a slow backend does not block startup indefinitely.
	// Runs inside the tracked WaitGroup so Close() waits for it to finish.
	reconcilePolicy := params.ReconcilePolicy
	if reconcilePolicy.DBOnly == "" {
		reconcilePolicy.DBOnly = "error"
	}
	if reconcilePolicy.CephOnly == "" {
		reconcilePolicy.CephOnly = "ignore"
	}

	feat.wg.Add(1)
	go func() {
		defer feat.wg.Done()
		reconcileCtx, reconcileCancel := context.WithTimeout(feat.bgCtx, 60*time.Second)
		defer reconcileCancel()
		feat.reconcileOnStartup(reconcileCtx, reconcilePolicy)
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

// reconcileOnStartup aligns the DB and the backend at startup according to policy.
//
// Step 1 — for every volume stuck in a transitional state in the DB, check if
// it still exists in the backend and apply the appropriate FSM target state.
//
// Step 2 — when policy.CephOnly == "import", list all backend volumes and
// create a DB record for any that are absent.
func (f *VolumeFeature) reconcileOnStartup(ctx context.Context, policy config.ReconcilePolicy) {
	log := f.app.logger

	// --- Step 1: DB transitional volumes vs backend ---
	transitional := []string{
		StateCreating, StateAttaching, StateDetaching, StateDeleting,
		StateCreatingFailed, StateAttachingFailed, StateDetachingFailed, StateDeletingFailed,
	}

	dbVolumes, err := f.app.db.ListVolumesByState(ctx, transitional...)
	if err != nil {
		log.Error("reconcile: cannot list transitional volumes", "err", err)
		return
	}

	var wg sync.WaitGroup
	for _, v := range dbVolumes {
		wg.Add(1)
		go func(vol *storage.Volume) {
			defer wg.Done()
			f.reconcileDBVolume(ctx, vol, policy)
		}(v)
	}
	wg.Wait()

	// --- Step 2: backend-only volumes ---
	if policy.CephOnly != "import" {
		return
	}

	backendVolumes, err := f.app.backend.ListVolumes(ctx)
	if err != nil {
		log.Error("reconcile: cannot list backend volumes", "err", err)
		return
	}

	for _, cv := range backendVolumes {
		wg.Add(1)
		go func(bv *storage.Volume) {
			defer wg.Done()

			dbCtx, cancel := context.WithTimeout(ctx, dbTimeout)
			existing, loadErr := f.app.db.LoadVolume(dbCtx, bv.Name)
			cancel()

			if loadErr != nil || existing != nil {
				return // DB error or volume already known
			}

			now := time.Now().UTC()
			v := &storage.Volume{
				ID:        uuid.New().String(),
				Name:      bv.Name,
				SizeMB:    bv.SizeMB,
				State:     StateAvailable,
				Backend:   f.app.backend.BackendName(),
				CreatedAt: now,
				UpdatedAt: now,
			}
			dbCtx2, cancel2 := context.WithTimeout(ctx, dbTimeout)
			defer cancel2()
			if saveErr := f.app.db.SaveVolume(dbCtx2, v); saveErr != nil {
				log.Error("reconcile: cannot import backend volume", "volume", bv.Name, "err", saveErr)
			} else {
				log.Info("reconcile: imported backend-only volume", "volume", bv.Name)
			}
		}(cv)
	}
	wg.Wait()
}

// reconcileDBVolume reconciles a single transitional DB volume against the backend.
func (f *VolumeFeature) reconcileDBVolume(ctx context.Context, vol *storage.Volume, policy config.ReconcilePolicy) {
	log := f.app.logger
	log.Info("reconcile: found transitional volume", "name", vol.Name, "state", vol.State)

	bCtx, cancel := context.WithTimeout(ctx, backendTimeout)
	real, backendErr := f.app.backend.GetVolume(bCtx, vol.Name)
	cancel()

	if backendErr != nil {
		// Backend unreachable — leave volume as-is.
		log.Warn("reconcile: backend unavailable for volume", "name", vol.Name, "err", backendErr)
		return
	}

	if real == nil {
		// Volume absent from backend — apply DBOnly policy.
		switch policy.DBOnly {
		case "delete":
			dbCtx, dbCancel := context.WithTimeout(ctx, dbTimeout)
			defer dbCancel()
			if delErr := f.app.db.DeleteVolume(dbCtx, vol.Name); delErr != nil {
				log.Error("reconcile: cannot delete DB-only volume", "name", vol.Name, "err", delErr)
			} else {
				log.Info("reconcile: deleted DB-only volume", "name", vol.Name)
			}
		case "ignore":
			log.Info("reconcile: ignoring DB-only volume", "name", vol.Name)
		default: // "error"
			log.Warn("reconcile: volume absent from backend, marking error", "name", vol.Name)
			f.reconcileToError(ctx, vol.Name, vol.State)
		}
		return
	}

	// Volume exists in backend — align FSM to the appropriate target state.
	var targetState string
	switch vol.State {
	case StateCreating, StateCreatingFailed,
		StateAttaching, StateAttachingFailed,
		StateDetaching, StateDetachingFailed:
		targetState = StateAvailable
	case StateDeleting, StateDeletingFailed:
		// Volume still exists but we were trying to delete it — push to error.
		targetState = StateError
	default:
		targetState = StateAvailable
	}

	if targetState == StateError {
		log.Warn("reconcile: volume should be deleted but still exists, marking error",
			"name", vol.Name, "state", vol.State)
		f.reconcileToError(ctx, vol.Name, vol.State)
		return
	}

	log.Info("reconcile: volume confirmed in backend, aligning state",
		"name", vol.Name, "from", vol.State, "to", targetState)

	fresh, _ := f.app.db.LoadVolume(ctx, vol.Name)
	if fresh == nil {
		return
	}
	fresh.State = targetState
	fresh.NodeID = real.NodeID
	fresh.UpdatedAt = time.Now().UTC()
	_ = f.app.db.UpdateVolume(ctx, fresh)
	_ = f.app.db.SaveEvent(ctx, VolumeEvent{
		VolumeID:  fresh.ID,
		Event:     "reconcile",
		FromState: vol.State,
		ToState:   targetState,
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

	_ = currentState
}
