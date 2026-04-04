package fsm_test

import (
	"context"
	"testing"

	volumefsm "github.com/cire-ly/block-storage-api/fsm"
)

func TestHappyPath(t *testing.T) {
	ctx := context.Background()

	steps := []struct {
		from  string
		event string
		to    string
	}{
		{volumefsm.StatePending, volumefsm.EventCreate, volumefsm.StateCreating},
		{volumefsm.StateCreating, volumefsm.EventReady, volumefsm.StateAvailable},
		{volumefsm.StateAvailable, volumefsm.EventAttach, volumefsm.StateAttaching},
		{volumefsm.StateAttaching, volumefsm.EventAttached, volumefsm.StateAttached},
		{volumefsm.StateAttached, volumefsm.EventDetach, volumefsm.StateDetaching},
		{volumefsm.StateDetaching, volumefsm.EventDetached, volumefsm.StateAvailable},
		{volumefsm.StateAvailable, volumefsm.EventDelete, volumefsm.StateDeleting},
		{volumefsm.StateDeleting, volumefsm.EventDeleted, volumefsm.StateDeleted},
	}

	for _, s := range steps {
		got, err := volumefsm.Transition(ctx, s.from, s.event)
		if err != nil {
			t.Fatalf("Transition(%q, %q): unexpected error: %v", s.from, s.event, err)
		}
		if got != s.to {
			t.Errorf("Transition(%q, %q) = %q, want %q", s.from, s.event, got, s.to)
		}
	}
}

func TestErrorTransitions(t *testing.T) {
	ctx := context.Background()
	errStates := []string{
		volumefsm.StateCreating,
		volumefsm.StateAttaching,
		volumefsm.StateDetaching,
		volumefsm.StateDeleting,
	}
	for _, state := range errStates {
		got, err := volumefsm.Transition(ctx, state, volumefsm.EventError)
		if err != nil {
			t.Fatalf("Transition(%q, error): %v", state, err)
		}
		if got != volumefsm.StateError {
			t.Errorf("Transition(%q, error) = %q, want %q", state, got, volumefsm.StateError)
		}
	}
}

func TestInvalidTransition(t *testing.T) {
	ctx := context.Background()

	_, err := volumefsm.Transition(ctx, volumefsm.StateAttached, volumefsm.EventDelete)
	if err == nil {
		t.Error("expected error for attached → delete, got nil")
	}
}

func TestCanTransition(t *testing.T) {
	if !volumefsm.CanTransition(volumefsm.StatePending, volumefsm.EventCreate) {
		t.Error("pending should accept create event")
	}
	if volumefsm.CanTransition(volumefsm.StateAttached, volumefsm.EventCreate) {
		t.Error("attached should not accept create event")
	}
}
