package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hacktheburgh/backend/internal/events"
	"hacktheburgh/backend/internal/testdb"
)

type fakeAdapter struct {
	kind      string
	fetch     func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error)
	active    int32
	maxActive int32
	delay     time.Duration
	report    *PagingReport

	mu       sync.Mutex
	lastOpts FetchOptions
}

func (f *fakeAdapter) opts() FetchOptions {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastOpts
}

func (f *fakeAdapter) Kind() string { return f.kind }

func (f *fakeAdapter) Fetch(ctx context.Context, cfg json.RawMessage, opts FetchOptions) (FetchResult, error) {
	f.mu.Lock()
	f.lastOpts = opts
	f.mu.Unlock()
	records, err := f.records(ctx, cfg, opts)
	return FetchResult{Records: records, Report: f.report}, err
}

func (f *fakeAdapter) records(ctx context.Context, cfg json.RawMessage, opts FetchOptions) ([]RawRecord, error) {
	current := atomic.AddInt32(&f.active, 1)
	for {
		max := atomic.LoadInt32(&f.maxActive)
		if current <= max || atomic.CompareAndSwapInt32(&f.maxActive, max, current) {
			break
		}
	}
	defer atomic.AddInt32(&f.active, -1)

	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	if f.fetch != nil {
		return f.fetch(ctx, cfg, opts)
	}
	return []RawRecord{{
		ExternalID: "record-1",
		ObservedAt: time.Now().UTC(),
		Payload:    map[string]any{"value": 1},
	}}, nil
}

func newLoopTest(t *testing.T) (*Loop, *Store, *fakeAdapter) {
	t.Helper()
	store := NewStore(testdb.Open(t))
	adapter := &fakeAdapter{kind: "rss"}
	loop := NewLoop(store, events.NewBus(), map[string]SourceAdapter{"rss": adapter}, time.Second, 1)
	return loop, store, adapter
}

func TestPollOnceSuccess(t *testing.T) {
	loop, store, _ := newLoopTest(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Fake", "rss", json.RawMessage(`{}`), intPtr(300), 0)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	ch, cancel := loop.Bus.Subscribe()
	defer cancel()

	inserted, err := loop.PollOnce(ctx, source.ID)
	if err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1", inserted)
	}

	got, err := store.Source(ctx, source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.LastError != nil {
		t.Fatalf("LastError = %#v", got.LastError)
	}
	if got.LastPolledAt == nil {
		t.Fatal("LastPolledAt is nil")
	}
	if got.RecordCount != 1 {
		t.Fatalf("RecordCount = %d", got.RecordCount)
	}

	select {
	case event := <-ch:
		if event.Type != events.SourceUpdated || event.ID != source.ID {
			t.Fatalf("event = %#v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for source_updated")
	}
}

func TestPollOnceFetchError(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	ctx := context.Background()

	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		return nil, errors.New("fetch boom")
	}
	source, err := store.CreateSource(ctx, "Failing", "rss", json.RawMessage(`{}`), intPtr(300), 0)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	if _, err := loop.PollOnce(ctx, source.ID); err == nil {
		t.Fatal("expected fetch error")
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.LastError == nil || *got.LastError != "fetch boom" {
		t.Fatalf("LastError = %#v", got.LastError)
	}
	if got.LastPolledAt == nil {
		t.Fatal("LastPolledAt is nil")
	}
}

func TestPollOnceUnsupportedKind(t *testing.T) {
	loop, store, _ := newLoopTest(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Unsupported", "web", json.RawMessage(`{"url":"https://example.com"}`), intPtr(300), 0)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	if _, err := loop.PollOnce(ctx, source.ID); err == nil {
		t.Fatal("expected unsupported kind error")
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.LastError == nil || *got.LastError == "" {
		t.Fatalf("LastError = %#v", got.LastError)
	}
}

func TestPollOnceCanceledContextRecordsError(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	source, err := store.CreateSource(context.Background(), "Canceled", "rss", json.RawMessage(`{}`), intPtr(300), 0)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	adapter.fetch = func(fetchCtx context.Context, _ json.RawMessage, _ FetchOptions) ([]RawRecord, error) {
		cancel()
		return nil, fetchCtx.Err()
	}

	if _, err := loop.PollOnce(ctx, source.ID); err == nil {
		t.Fatal("expected context error")
	}

	got, err := store.Source(context.Background(), source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.LastError == nil || *got.LastError == "" {
		t.Fatalf("LastError = %#v, want a recorded error", got.LastError)
	}
}

func TestPollDueMaxParallel(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	loop.MaxParallel = 2
	adapter.delay = 30 * time.Millisecond

	ctx := context.Background()
	for index := 0; index < 3; index++ {
		if _, err := store.CreateSource(ctx, "Parallel", "rss", json.RawMessage(`{}`), intPtr(300), 0); err != nil {
			t.Fatalf("CreateSource: %v", err)
		}
	}

	loop.pollDue(ctx)
	loop.waitIdle()

	if got := atomic.LoadInt32(&adapter.maxActive); got > 2 {
		t.Fatalf("max concurrent fetches = %d, want <= 2", got)
	}
	if got := atomic.LoadInt32(&adapter.maxActive); got < 1 {
		t.Fatalf("max concurrent fetches = %d, want >= 1", got)
	}

	sources, err := store.ListSources(ctx)
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	for _, source := range sources {
		if source.LastPolledAt == nil {
			t.Fatalf("source %s was not polled", source.Name)
		}
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	loop, _, _ := newLoopTest(t)
	loop.Tick = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		loop.Run(ctx)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}
