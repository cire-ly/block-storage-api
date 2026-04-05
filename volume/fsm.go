package volume

import (
	"context"
	"fmt"
	"time"

	"github.com/looplab/fsm"

	"github.com/cire-ly/block-storage-api/storage"
)

// Volume states — aliases to storage constants for use within this package.
const (
	StatePending         = storage.StatePending
	StateCreating        = storage.StateCreating
	StateCreatingFailed  = storage.StateCreatingFailed
	StateAvailable       = storage.StateAvailable
	StateAttaching       = storage.StateAttaching
	StateAttachingFailed = storage.StateAttachingFailed
	StateAttached        = storage.StateAttached
	StateDetaching       = storage.StateDetaching
	StateDetachingFailed = storage.StateDetachingFailed
	StateDeleting        = storage.StateDeleting
	StateDeletingFailed  = storage.StateDeletingFailed
	StateDeleted         = storage.StateDeleted
	StateError           = storage.StateError
)

// Volume events.
const (
	EventCreate   = "create"
	EventReady    = "ready"
	EventAttach   = "attach"
	EventAttached = "attached"
	EventDetach   = "detach"
	EventDetached = "detached"
	EventDelete   = "delete"
	EventDeleted  = "deleted"
	EventError    = "error"
	EventRetry    = "retry"
	EventFail     = "fail"
)

// RetryPolicy controls exponential back-off for failed FSM transitions.
type RetryPolicy struct {
	MaxAttempts int
	InitialWait time.Duration
	Multiplier  float64
	MaxWait     time.Duration
}

// DefaultRetryPolicy returns the recommended retry configuration.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxAttempts: 3,
		InitialWait: 500 * time.Millisecond,
		Multiplier:  2.0,
		MaxWait:     10 * time.Second,
	}
}

// NewVolumeFSM builds a new FSM seeded at initialState.
//
// Transitions:
//
//	pending → [create] → creating → [ready] → available
//	available → [attach] → attaching → [attached] → attached
//	attached → [detach] → detaching → [detached] → available
//	available → [delete] → deleting → [deleted] → deleted
//	creating|attaching|detaching|deleting → [error] → *_failed
//	*_failed → [retry] → original in-progress state
//	*_failed → [fail] → error (after MaxAttempts)
//	error → reconcile via POST /api/v1/volumes/{name}/reconcile
func NewVolumeFSM(initialState string) *fsm.FSM {
	return fsm.NewFSM(
		initialState,
		fsm.Events{
			// Normal progression
			{Name: EventCreate, Src: []string{StatePending}, Dst: StateCreating},
			{Name: EventReady, Src: []string{StateCreating}, Dst: StateAvailable},
			{Name: EventAttach, Src: []string{StateAvailable}, Dst: StateAttaching},
			{Name: EventAttached, Src: []string{StateAttaching}, Dst: StateAttached},
			{Name: EventDetach, Src: []string{StateAttached}, Dst: StateDetaching},
			{Name: EventDetached, Src: []string{StateDetaching}, Dst: StateAvailable},
			{Name: EventDelete, Src: []string{StateAvailable}, Dst: StateDeleting},
			{Name: EventDeleted, Src: []string{StateDeleting}, Dst: StateDeleted},
			// In-progress → failed
			{Name: EventError, Src: []string{StateCreating}, Dst: StateCreatingFailed},
			{Name: EventError, Src: []string{StateAttaching}, Dst: StateAttachingFailed},
			{Name: EventError, Src: []string{StateDetaching}, Dst: StateDetachingFailed},
			{Name: EventError, Src: []string{StateDeleting}, Dst: StateDeletingFailed},
			// Failed → retry (back to in-progress)
			{Name: EventRetry, Src: []string{StateCreatingFailed}, Dst: StateCreating},
			{Name: EventRetry, Src: []string{StateAttachingFailed}, Dst: StateAttaching},
			{Name: EventRetry, Src: []string{StateDetachingFailed}, Dst: StateDetaching},
			{Name: EventRetry, Src: []string{StateDeletingFailed}, Dst: StateDeleting},
			// Failed → terminal error (MaxAttempts exhausted)
			{
				Name: EventFail,
				Src: []string{
					StateCreatingFailed, StateAttachingFailed,
					StateDetachingFailed, StateDeletingFailed,
				},
				Dst: StateError,
			},
		},
		fsm.Callbacks{},
	)
}

// CanTransition reports whether event is valid from currentState.
func CanTransition(currentState, event string) bool {
	return NewVolumeFSM(currentState).Can(event)
}

// Transition applies event to currentState and returns the new state.
func Transition(ctx context.Context, currentState, event string) (string, error) {
	f := NewVolumeFSM(currentState)
	if err := f.Event(ctx, event); err != nil {
		return currentState, fmt.Errorf("fsm %s -[%s]->: %w", currentState, event, err)
	}
	return f.Current(), nil
}
