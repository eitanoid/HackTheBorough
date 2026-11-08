// Package pyrunnertest holds a fake pyrunner.Runner for other packages' tests.
package pyrunnertest

import (
	"context"
	"encoding/json"
	"sync"

	"hacktheburgh/backend/internal/pyrunner"
)

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

func (f *Fake) Requests() []pyrunner.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pyrunner.Request(nil), f.requests...)
}

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

func Raises(tb string) *Fake {
	return &Fake{Fn: func(pyrunner.Request) (pyrunner.Outcome, error) {
		return pyrunner.Outcome{Error: tb}, nil
	}}
}

func TimesOut() *Fake {
	return &Fake{Fn: func(pyrunner.Request) (pyrunner.Outcome, error) {
		return pyrunner.Outcome{TimedOut: true}, nil
	}}
}
