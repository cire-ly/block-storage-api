package fsm

import (
	"context"
	"fmt"

	"github.com/looplab/fsm"
)

// Volume states.
const (
	StatePending   = "pending"
	StateCreating  = "creating"
	StateAvailable = "available"
	StateAttaching = "attaching"
	StateAttached  = "attached"
	StateDetaching = "detaching"
	StateDeleting  = "deleting"
	StateDeleted   = "deleted"
	StateError     = "error"
)

// Volume events.
const (
	EventCreate   = "create"
	EventReady    = "ready"
	EventError    = "error"
	EventAttach   = "attach"
	EventAttached = "attached"
	EventDetach   = "detach"
	EventDetached = "detached"
	EventDelete   = "delete"
	EventDeleted  = "deleted"
)

// NewVolumeFSM builds an FSM seeded at initialState.
//
// Transitions:
//
//	pending → [create] → creating → [ready] → available
//	available → [attach] → attaching → [attached] → attached
//	attached → [detach] → detaching → [detached] → available
//	available → [delete] → deleting → [deleted] → deleted
//	creating|attaching|detaching|deleting → [error] → error
func NewVolumeFSM(initialState string) *fsm.FSM {
	return fsm.NewFSM(
		initialState,
		fsm.Events{
			{Name: EventCreate,   Src: []string{StatePending},                                               Dst: StateCreating},
			{Name: EventReady,    Src: []string{StateCreating},                                              Dst: StateAvailable},
			{Name: EventError,    Src: []string{StateCreating, StateAttaching, StateDetaching, StateDeleting}, Dst: StateError},
			{Name: EventAttach,   Src: []string{StateAvailable},                                             Dst: StateAttaching},
			{Name: EventAttached, Src: []string{StateAttaching},                                             Dst: StateAttached},
			{Name: EventDetach,   Src: []string{StateAttached},                                              Dst: StateDetaching},
			{Name: EventDetached, Src: []string{StateDetaching},                                             Dst: StateAvailable},
			{Name: EventDelete,   Src: []string{StateAvailable},                                             Dst: StateDeleting},
			{Name: EventDeleted,  Src: []string{StateDeleting},                                              Dst: StateDeleted},
		},
		fsm.Callbacks{},
	)
}

// CanTransition reports whether the given event is valid from currentState.
func CanTransition(currentState, event string) bool {
	return NewVolumeFSM(currentState).Can(event)
}

// Transition applies event to currentState and returns the resulting state.
func Transition(ctx context.Context, currentState, event string) (string, error) {
	f := NewVolumeFSM(currentState)
	if err := f.Event(ctx, event); err != nil {
		return currentState, fmt.Errorf("fsm %s -[%s]->: %w", currentState, event, err)
	}
	return f.Current(), nil
}
