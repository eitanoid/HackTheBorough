// Package events is a tiny in-process pub/sub connecting the ingest loop,
// the run loop and the SSE handler.
package events

import "sync"

const (
	SourceUpdated       = "source_updated"
	ProjectUpdated      = "project_updated"
	WorkflowRegenerated = "workflow_regenerated"
	InvestigationStep   = "investigation_step"
	RunFailed           = "run_failed"
)

// Event carries ids only; clients refetch from the API.
type Event struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type Bus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

func NewBus() *Bus { return &Bus{subs: map[chan Event]struct{}{}} }

// Subscribe returns a channel and a cancel func. Slow subscribers drop events.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 32)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

func (b *Bus) Publish(e Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
