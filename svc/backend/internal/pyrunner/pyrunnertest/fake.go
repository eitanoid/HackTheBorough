// Package pyrunnertest holds a fake pyrunner.Runner for other packages' tests.
// It is the only Runner this repository implements.
package pyrunnertest

import (
	"context"
	"encoding/json"
	"sync"

	"hacktheburgh/backend/internal/pyrunner"
)

// Fake records every request it receives and answers with Fn.
type Fake struct {
	Fn func(pyrunner.Request) (pyrunner.Outcome, error)

	mu       sync.Mutex
	requests []pyrunner.Request
}

var _ pyrunner.Runner = (*Fake)(nil)

func (f *Fake) Run(_ context.Context, r pyrunner.Request) (pyrunner.Outcome, error) {
	f.mu.Lock()
	f.requests = append(f.requests, r)
	f.mu.Unlock()
	return f.Fn(r)
}

// Requests returns a copy of what the fake has been asked to run.
func (f *Fake) Requests() []pyrunner.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pyrunner.Request(nil), f.requests...)
}

// Returns makes every run succeed with v marshalled as the result.
func Returns(v any) *Fake {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return ReturnsRaw(b)
}

// ReturnsRaw makes every run succeed with the given bytes as the result,
// which lets a test supply JSON that Go would not produce (such as NaN).
func ReturnsRaw(raw []byte) *Fake {
	return &Fake{Fn: func(pyrunner.Request) (pyrunner.Outcome, error) {
		return pyrunner.Outcome{OK: true, Result: raw}, nil
	}}
}

// Raises makes every run fail as if the script raised, with traceback tb.
func Raises(tb string) *Fake {
	return &Fake{Fn: func(pyrunner.Request) (pyrunner.Outcome, error) {
		return pyrunner.Outcome{Error: tb}, nil
	}}
}

// TimesOut makes every run fail as if the script ran past its timeout.
func TimesOut() *Fake {
	return &Fake{Fn: func(pyrunner.Request) (pyrunner.Outcome, error) {
		return pyrunner.Outcome{TimedOut: true}, nil
	}}
}
