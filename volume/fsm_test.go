package volume

import (
	"context"
	"testing"
	"time"
)

// -- DefaultRetryPolicy --

func TestDefaultRetryPolicy(t *testing.T) {
	p := DefaultRetryPolicy()
	if p.MaxAttempts != 3 {
		t.Errorf("MaxAttempts = %d, want 3", p.MaxAttempts)
	}
	if p.InitialWait != 500*time.Millisecond {
		t.Errorf("InitialWait = %v, want 500ms", p.InitialWait)
	}
	if p.Multiplier != 2.0 {
		t.Errorf("Multiplier = %v, want 2.0", p.Multiplier)
	}
	if p.MaxWait != 10*time.Second {
		t.Errorf("MaxWait = %v, want 10s", p.MaxWait)
	}
}

// -- CanTransition --

func TestCanTransitionValid(t *testing.T) {
	cases := []struct {
		state string
		event string
	}{
		{StatePending, EventCreate},
		{StateCreating, EventReady},
		{StateCreating, EventError},
		{StateAvailable, EventAttach},
		{StateAvailable, EventDelete},
		{StateAttaching, EventAttached},
		{StateAttaching, EventError},
		{StateAttached, EventDetach},
		{StateDetaching, EventDetached},
		{StateDetaching, EventError},
		{StateDeleting, EventDeleted},
		{StateDeleting, EventError},
		{StateCreatingFailed, EventRetry},
		{StateCreatingFailed, EventFail},
		{StateAttachingFailed, EventRetry},
		{StateAttachingFailed, EventFail},
		{StateDetachingFailed, EventRetry},
		{StateDetachingFailed, EventFail},
		{StateDeletingFailed, EventRetry},
		{StateDeletingFailed, EventFail},
	}
	for _, tc := range cases {
		if !CanTransition(tc.state, tc.event) {
			t.Errorf("CanTransition(%q, %q) = false, want true", tc.state, tc.event)
		}
	}
}

func TestCanTransitionInvalid(t *testing.T) {
	cases := []struct {
		state string
		event string
	}{
		{StatePending, EventReady},
		{StatePending, EventAttach},
		{StateAvailable, EventCreate},
		{StateAvailable, EventAttached},
		{StateAttached, EventDelete},
		{StateAttached, EventReady},
		{StateDeleted, EventCreate},
		{StateError, EventCreate},
		{StateError, EventRetry},
		{StateError, EventFail},
	}
	for _, tc := range cases {
		if CanTransition(tc.state, tc.event) {
			t.Errorf("CanTransition(%q, %q) = true, want false", tc.state, tc.event)
		}
	}
}

// -- Transition --

func TestTransitionNormalPath(t *testing.T) {
	ctx := context.Background()

	steps := []struct {
		from  string
		event string
		to    string
	}{
		{StatePending, EventCreate, StateCreating},
		{StateCreating, EventReady, StateAvailable},
		{StateAvailable, EventAttach, StateAttaching},
		{StateAttaching, EventAttached, StateAttached},
		{StateAttached, EventDetach, StateDetaching},
		{StateDetaching, EventDetached, StateAvailable},
		{StateAvailable, EventDelete, StateDeleting},
		{StateDeleting, EventDeleted, StateDeleted},
	}

	for _, tc := range steps {
		got, err := Transition(ctx, tc.from, tc.event)
		if err != nil {
			t.Errorf("Transition(%q, %q): unexpected error: %v", tc.from, tc.event, err)
		}
		if got != tc.to {
			t.Errorf("Transition(%q, %q) = %q, want %q", tc.from, tc.event, got, tc.to)
		}
	}
}

func TestTransitionErrorToFailedStates(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		from string
		to   string
	}{
		{StateCreating, StateCreatingFailed},
		{StateAttaching, StateAttachingFailed},
		{StateDetaching, StateDetachingFailed},
		{StateDeleting, StateDeletingFailed},
	}

	for _, tc := range cases {
		got, err := Transition(ctx, tc.from, EventError)
		if err != nil {
			t.Errorf("Transition(%q, error): %v", tc.from, err)
		}
		if got != tc.to {
			t.Errorf("Transition(%q, error) = %q, want %q", tc.from, got, tc.to)
		}
	}
}

func TestTransitionRetryFromFailedStates(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		from string
		to   string
	}{
		{StateCreatingFailed, StateCreating},
		{StateAttachingFailed, StateAttaching},
		{StateDetachingFailed, StateDetaching},
		{StateDeletingFailed, StateDeleting},
	}

	for _, tc := range cases {
		got, err := Transition(ctx, tc.from, EventRetry)
		if err != nil {
			t.Errorf("Transition(%q, retry): %v", tc.from, err)
		}
		if got != tc.to {
			t.Errorf("Transition(%q, retry) = %q, want %q", tc.from, got, tc.to)
		}
	}
}

func TestTransitionFailFromFailedStates(t *testing.T) {
	ctx := context.Background()

	for _, from := range []string{
		StateCreatingFailed, StateAttachingFailed,
		StateDetachingFailed, StateDeletingFailed,
	} {
		got, err := Transition(ctx, from, EventFail)
		if err != nil {
			t.Errorf("Transition(%q, fail): %v", from, err)
		}
		if got != StateError {
			t.Errorf("Transition(%q, fail) = %q, want %q", from, got, StateError)
		}
	}
}

func TestTransitionInvalidReturnsCurrentState(t *testing.T) {
	ctx := context.Background()

	got, err := Transition(ctx, StatePending, EventReady)
	if err == nil {
		t.Error("expected error for invalid transition, got nil")
	}
	if got != StatePending {
		t.Errorf("expected current state %q on error, got %q", StatePending, got)
	}
}

// -- State constant aliases --

func TestStateConstantsMatchStorage(t *testing.T) {
	// Verify that the aliases in this package resolve to the expected strings.
	cases := map[string]string{
		StatePending:         "pending",
		StateCreating:        "creating",
		StateCreatingFailed:  "creating_failed",
		StateAvailable:       "available",
		StateAttaching:       "attaching",
		StateAttachingFailed: "attaching_failed",
		StateAttached:        "attached",
		StateDetaching:       "detaching",
		StateDetachingFailed: "detaching_failed",
		StateDeleting:        "deleting",
		StateDeletingFailed:  "deleting_failed",
		StateDeleted:         "deleted",
		StateError:           "error",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("constant = %q, want %q", got, want)
		}
	}
}
