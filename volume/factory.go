package volume

import (
	"context"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/cire-ly/block-storage-api/assertor"
)

// NewVolumeFeatureParams holds all required dependencies for the volume feature.
// Every field is required unless noted.
type NewVolumeFeatureParams struct {
	Logger  LoggerDependency         // required
	Backend StorageBackendDependency // required
	DB      DatabaseDependency       // required
	Tracer  trace.Tracer             // required
	Meter   metric.Meter             // optional
	Router  chi.Router               // required
}

// VolumeFeature is the wired volume feature, implementing FeatureContract.
type VolumeFeature struct {
	app     *application
	closers []interface{ Close(context.Context) error }
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

	app := newApplication(params.Backend, params.DB, params.Logger, params.Tracer)

	ctrl := newHTTPController(app, params.Logger, params.Tracer, params.Meter)
	ctrl.registerRoutes(params.Router)

	feat := &VolumeFeature{app: app}

	// Reconcile volumes interrupted mid-transition at previous shutdown.
	go feat.reconcileOnStartup(context.Background())

	return feat, nil
}

// Application returns the ApplicationContract for this feature.
func (f *VolumeFeature) Application() ApplicationContract {
	return f.app
}

// Close shuts down all feature-owned resources in LIFO order.
func (f *VolumeFeature) Close(ctx context.Context) error {
	for i := len(f.closers) - 1; i >= 0; i-- {
		if err := f.closers[i].Close(ctx); err != nil {
			return err
		}
	}
	return nil
}

// reconcileOnStartup re-drives volumes that were stuck in a transitional state
// at the time of the last shutdown. It compares the DB state against the real
// backend and either marks them available or transitions them to error.
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

	for _, v := range volumes {
		f.app.logger.Info("reconcile: found transitional volume",
			"name", v.Name, "state", v.State)

		real, backendErr := f.app.backend.GetVolume(ctx, v.Name)
		if backendErr != nil || real == nil {
			// Volume not found in backend — push it to error state.
			f.app.logger.Warn("reconcile: volume absent from backend, marking error",
				"name", v.Name, "state", v.State)
			f.reconcileToError(ctx, v.Name, v.State)
			continue
		}

		// Volume exists in backend — force it to available.
		f.app.logger.Info("reconcile: volume confirmed in backend, marking available",
			"name", v.Name, "state", v.State)
		fresh, _ := f.app.db.LoadVolume(ctx, v.Name)
		if fresh == nil {
			continue
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
}
