// Package assertor provides a simple multi-error validator for constructor params.
package assertor

import (
	"errors"
	"strings"
)

// Assertor collects validation failures.
type Assertor struct {
	errs []string
}

// New returns a fresh Assertor.
func New() *Assertor {
	return &Assertor{}
}

// Assert adds msg to the failure list when cond is false.
func (a *Assertor) Assert(cond bool, msg string) {
	if !cond {
		a.errs = append(a.errs, msg)
	}
}

// Validate returns an error joining all failures, or nil if none.
func (a *Assertor) Validate() error {
	if len(a.errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(a.errs, "; "))
}
