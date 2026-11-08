package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func timedRecords(prefix string, n int) []RawRecord {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := make([]RawRecord, 0, n)
	for i := 0; i < n; i++ {
		// Oldest first, so the loop's sort is what picks the newest.
		out = append(out, RawRecord{
			ExternalID: fmt.Sprintf("%s-%d", prefix, i),
			ObservedAt: base.Add(time.Duration(i) * time.Hour),
			Payload:    map[string]any{"i": i},
		})
	}
	return out
}

func TestPollOnceInitialBackfillKeepsNewestN(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	ctx := context.Background()
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		return timedRecords("r", 5), nil
	}
	adapter.report = &PagingReport{Pages: 3, Methods: map[string]int{MethodRelNext: 2}, Stopped: StopLimit}

	source, err := store.CreateSourceWithBackfill(ctx, "Backfill", "rss", json.RawMessage(`{}`), intPtr(300), 3)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	inserted, err := loop.PollOnce(ctx, source.ID)
	if err != nil || inserted != 3 {
		t.Fatalf("inserted = %d, err = %v", inserted, err)
	}
	if opts := adapter.opts(); !opts.Backfill || opts.Limit != 3 {
		t.Fatalf("opts = %+v, want a backfill with limit 3", opts)
	}
	records, err := store.RecentRecords(ctx, source.ID, 10)
	if err != nil {
		t.Fatalf("RecentRecords: %v", err)
	}
	if len(records) != 3 || records[0].ExternalID != "r-4" || records[2].ExternalID != "r-2" {
		t.Fatalf("records = %+v, want the 3 newest", records)
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.BackfillPending {
		t.Fatal("backfill must be cleared after a successful poll")
	}
	var report PagingReport
	if err := json.Unmarshal(got.LastBackfill, &report); err != nil || report.Pages != 3 || report.Rows != 3 {
		t.Fatalf("LastBackfill = %s, err = %v", got.LastBackfill, err)
	}

	// The next normal poll is a single page and is not capped: four rows
	// newer than everything stored are all added.
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		fresh := timedRecords("s", 4)
		for i := range fresh {
			fresh[i].ObservedAt = fresh[i].ObservedAt.Add(24 * time.Hour)
		}
		return fresh, nil
	}
	inserted, err = loop.PollOnce(ctx, source.ID)
	if err != nil || inserted != 4 {
		t.Fatalf("normal poll inserted = %d, err = %v", inserted, err)
	}
	if opts := adapter.opts(); opts.Backfill || opts.Limit != 0 {
		t.Fatalf("opts = %+v, want a normal poll", opts)
	}
}

func TestPollOnceFailedBackfillStaysPending(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	ctx := context.Background()
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		return nil, errors.New("site down")
	}
	source, err := store.CreateSource(ctx, "Down", "rss", json.RawMessage(`{}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if _, err := loop.PollOnce(ctx, source.ID); err == nil {
		t.Fatal("expected fetch error")
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil || !got.BackfillPending {
		t.Fatalf("pending = %v, err = %v", got.BackfillPending, err)
	}
}

func TestPollOnceRejectsConcurrentPoll(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		once.Do(func() { close(started) })
		<-release
		return timedRecords("r", 1), nil
	}
	source, err := store.CreateSource(ctx, "Slow", "rss", json.RawMessage(`{}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := loop.PollOnce(ctx, source.ID)
		done <- err
	}()
	<-started
	if _, err := loop.PollOnce(ctx, source.ID); !errors.Is(err, ErrPollInProgress) {
		t.Fatalf("second poll err = %v, want ErrPollInProgress", err)
	}
	// pollDue must skip the in-flight source rather than queue it.
	loop.pollDue(ctx)
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if _, err := loop.PollOnce(ctx, source.ID); err != nil {
		t.Fatalf("poll after release: %v", err)
	}
}

func TestPollOnceRaisedTargetRebackfills(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	ctx := context.Background()
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		return timedRecords("r", 10), nil
	}
	source, err := store.CreateSourceWithBackfill(ctx, "Grow", "rss", json.RawMessage(`{}`), intPtr(300), 2)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if _, err := loop.PollOnce(ctx, source.ID); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if _, scheduled, err := store.SetBackfillRows(ctx, source.ID, 6); err != nil || !scheduled {
		t.Fatalf("SetBackfillRows: scheduled = %v, err = %v", scheduled, err)
	}
	if _, err := loop.PollOnce(ctx, source.ID); err != nil {
		t.Fatalf("PollOnce: %v", err)
	}
	if opts := adapter.opts(); !opts.Backfill || opts.Limit != 6 {
		t.Fatalf("opts = %+v, want a re-backfill with limit 6", opts)
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil || got.RecordCount != 6 || got.BackfillPending {
		t.Fatalf("count = %d, pending = %v, err = %v", got.RecordCount, got.BackfillPending, err)
	}
}

func TestPollDueBackfillDoesNotBlockNormalPolls(t *testing.T) {
	loop, store, adapter := newLoopTest(t) // MaxParallel = 1
	ctx := context.Background()

	slow, err := store.CreateSource(ctx, "Slow backfill", "rss", json.RawMessage(`{"slow":true}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	normal, err := store.CreateSource(ctx, "Normal", "rss", json.RawMessage(`{}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	// Make the second source a plain due poll: backfill done, interval passed.
	if err := store.FinishBackfill(ctx, normal.ID, *normal.BackfillRequestedAt, nil, nil); err != nil {
		t.Fatalf("FinishBackfill: %v", err)
	}
	if err := store.MarkPolled(ctx, normal.ID, time.Now().UTC().Add(-time.Hour), nil); err != nil {
		t.Fatalf("MarkPolled: %v", err)
	}

	release := make(chan struct{})
	normalDone := make(chan struct{})
	var once sync.Once
	adapter.fetch = func(_ context.Context, cfg json.RawMessage, _ FetchOptions) ([]RawRecord, error) {
		if string(cfg) == `{"slow": true}` || string(cfg) == `{"slow":true}` {
			<-release
		} else {
			once.Do(func() { close(normalDone) })
		}
		return timedRecords(string(cfg), 1), nil
	}
	done := make(chan struct{})
	go func() {
		loop.pollDue(ctx)
		loop.waitIdle()
		close(done)
	}()
	select {
	case <-normalDone:
	case <-time.After(5 * time.Second):
		t.Fatal("a normal poll waited behind a running backfill")
	}
	close(release)
	<-done
	got, err := store.Source(ctx, slow.ID)
	if err != nil || got.BackfillPending {
		t.Fatalf("slow source pending = %v, err = %v", got.BackfillPending, err)
	}
}

func TestNormalPollOnlyAddsRowsNewerThanStored(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	ctx := context.Background()
	// The source always serves the same 10 rows (r-0 oldest … r-9 newest).
	page := timedRecords("r", 10)
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		return append([]RawRecord(nil), page...), nil
	}
	source, err := store.CreateSourceWithBackfill(ctx, "Capped", "rss", json.RawMessage(`{}`), intPtr(300), 3)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if inserted, err := loop.PollOnce(ctx, source.ID); err != nil || inserted != 3 {
		t.Fatalf("backfill inserted = %d, err = %v", inserted, err)
	}
	// Normal poll of the same page: the 7 rows the backfill left out must not
	// leak back in.
	if inserted, err := loop.PollOnce(ctx, source.ID); err != nil || inserted != 0 {
		t.Fatalf("repeat poll inserted = %d, err = %v", inserted, err)
	}
	// Two genuinely new rows appear at the top of the page.
	base := page[len(page)-1].ObservedAt
	page = append(page,
		RawRecord{ExternalID: "fresh-1", ObservedAt: base.Add(time.Hour), Payload: map[string]any{}},
		RawRecord{ExternalID: "fresh-2", ObservedAt: base.Add(2 * time.Hour), Payload: map[string]any{}},
	)
	if inserted, err := loop.PollOnce(ctx, source.ID); err != nil || inserted != 2 {
		t.Fatalf("new rows inserted = %d, err = %v", inserted, err)
	}
	// Raising the target still backfills older rows.
	if _, scheduled, err := store.SetBackfillRows(ctx, source.ID, 9); err != nil || !scheduled {
		t.Fatalf("SetBackfillRows: scheduled = %v, err = %v", scheduled, err)
	}
	if _, err := loop.PollOnce(ctx, source.ID); err != nil {
		t.Fatalf("re-backfill: %v", err)
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil || got.RecordCount != 9 {
		t.Fatalf("count after re-backfill = %d, err = %v", got.RecordCount, err)
	}
}

func TestPartialBackfillKeepsRowsAndStaysPending(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	ctx := context.Background()
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		return timedRecords("r", 4), nil
	}
	adapter.report = &PagingReport{Pages: 2, Methods: map[string]int{MethodRelNext: 1}, Stopped: "error: HTTP 500", Error: "HTTP 500"}
	source, err := store.CreateSourceWithBackfill(ctx, "Flaky", "rss", json.RawMessage(`{}`), intPtr(300), 100)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if inserted, err := loop.PollOnce(ctx, source.ID); err != nil || inserted != 4 {
		t.Fatalf("inserted = %d, err = %v", inserted, err)
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if !got.BackfillPending {
		t.Fatal("an incomplete backfill must stay pending for a retry")
	}
	if got.LastError == nil || !strings.Contains(*got.LastError, "HTTP 500") {
		t.Fatalf("LastError = %v", got.LastError)
	}
	if !strings.Contains(string(got.LastBackfill), "HTTP 500") {
		t.Fatalf("LastBackfill = %s", got.LastBackfill)
	}
}

func TestPollOnceReportsQueuedSeparatelyFromRunning(t *testing.T) {
	loop, store, adapter := newLoopTest(t) // MaxParallel = 1
	ctx := context.Background()
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		once.Do(func() { close(started) })
		<-release
		return timedRecords("r", 1), nil
	}
	busy, err := store.CreateSource(ctx, "Busy", "rss", json.RawMessage(`{}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	waiting, err := store.CreateSource(ctx, "Waiting", "rss", json.RawMessage(`{}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	// Both have pending backfills, which share the single backfill lane.
	go func() { _, _ = loop.PollOnce(ctx, busy.ID) }()
	<-started
	queued := make(chan error, 1)
	go func() {
		_, err := loop.PollOnce(ctx, waiting.ID)
		queued <- err
	}()
	// Wait (without claiming) until the second poll is queued for the lane.
	deadline := time.Now().Add(5 * time.Second)
	for !loop.isQueued(waiting.ID) {
		if time.Now().After(deadline) {
			t.Fatal("second poll never queued")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if _, err := loop.PollOnce(ctx, waiting.ID); !errors.Is(err, ErrPollQueued) {
		t.Fatalf("queued source err = %v, want ErrPollQueued", err)
	}
	if _, err := loop.PollOnce(ctx, busy.ID); !errors.Is(err, ErrPollInProgress) {
		t.Fatalf("running source err = %v, want ErrPollInProgress", err)
	}
	close(release)
	if err := <-queued; err != nil {
		t.Fatalf("queued poll: %v", err)
	}
}

func TestCancelledBackfillStaysDue(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	source, err := store.CreateSource(context.Background(), "Cancelled", "rss", json.RawMessage(`{}`), intPtr(3600))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	adapter.fetch = func(fetchCtx context.Context, _ json.RawMessage, _ FetchOptions) ([]RawRecord, error) {
		cancel()
		return nil, fetchCtx.Err()
	}
	if _, err := loop.PollOnce(ctx, source.ID); err == nil {
		t.Fatal("expected a cancellation error")
	}
	due, err := store.DueSources(context.Background())
	if err != nil || len(due) != 1 {
		t.Fatalf("a cancelled backfill must stay due: due = %d, err = %v", len(due), err)
	}
}

func (l *Loop) isQueued(sourceID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	running, ok := l.inflight[sourceID]
	return ok && !running
}

func TestNormalPollKeepsRowsWithoutTimestamps(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	ctx := context.Background()
	// A ranked listing without timestamps, where new rows appear after old ones.
	untimed := func(ids ...string) []RawRecord {
		out := make([]RawRecord, 0, len(ids))
		for _, id := range ids {
			out = append(out, RawRecord{ExternalID: id, ObservedAt: time.Now().UTC(), Payload: map[string]any{"_time_estimated": true}})
		}
		return out
	}
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		return untimed("a", "b"), nil
	}
	source, err := store.CreateSourceWithBackfill(ctx, "Ranked", "rss", json.RawMessage(`{}`), intPtr(300), 2)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if _, err := loop.PollOnce(ctx, source.ID); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		return untimed("a", "b", "c"), nil
	}
	if inserted, err := loop.PollOnce(ctx, source.ID); err != nil || inserted != 1 {
		t.Fatalf("a new row listed after known ones must be kept: inserted = %d, err = %v", inserted, err)
	}
}

func TestFailingBackfillGivesUpAfterMaxAttempts(t *testing.T) {
	loop, store, adapter := newLoopTest(t)
	ctx := context.Background()
	adapter.fetch = func(context.Context, json.RawMessage, FetchOptions) ([]RawRecord, error) {
		return timedRecords("r", 2), nil
	}
	adapter.report = &PagingReport{Pages: 1, Methods: map[string]int{}, Stopped: "error: HTTP 404", Error: "HTTP 404"}
	source, err := store.CreateSourceWithBackfill(ctx, "Always broken", "rss", json.RawMessage(`{}`), intPtr(300), 100)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	for attempt := 1; attempt <= MaxBackfillAttempts; attempt++ {
		if _, err := loop.PollOnce(ctx, source.ID); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		got, err := store.Source(ctx, source.ID)
		if err != nil {
			t.Fatalf("Source: %v", err)
		}
		if wantPending := attempt < MaxBackfillAttempts; got.BackfillPending != wantPending {
			t.Fatalf("after attempt %d pending = %v, want %v", attempt, got.BackfillPending, wantPending)
		}
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil || got.LastError == nil || !strings.Contains(*got.LastError, "gave up") {
		t.Fatalf("LastError = %v, err = %v", got.LastError, err)
	}
	// Back to normal polling.
	if _, err := loop.PollOnce(ctx, source.ID); err != nil {
		t.Fatalf("normal poll: %v", err)
	}
	if opts := adapter.opts(); opts.Backfill {
		t.Fatalf("opts = %+v, want a normal poll after giving up", opts)
	}
}

func TestQueuedPollPicksUpBackfillRequestedWhileWaiting(t *testing.T) {
	loop, store, adapter := newLoopTest(t) // MaxParallel = 1
	ctx := context.Background()
	release := make(chan struct{})
	var once sync.Once
	started := make(chan struct{})
	adapter.fetch = func(_ context.Context, cfg json.RawMessage, _ FetchOptions) ([]RawRecord, error) {
		if string(cfg) == `{"slow": true}` || string(cfg) == `{"slow":true}` {
			once.Do(func() { close(started) })
			<-release
		}
		return timedRecords(string(cfg), 1), nil
	}
	newNormal := func(name, cfg string) Source {
		source, err := store.CreateSourceWithBackfill(ctx, name, "rss", json.RawMessage(cfg), intPtr(300), 5)
		if err != nil {
			t.Fatalf("CreateSource: %v", err)
		}
		if err := store.FinishBackfill(ctx, source.ID, *source.BackfillRequestedAt, nil, nil); err != nil {
			t.Fatalf("FinishBackfill: %v", err)
		}
		return source
	}
	slow := newNormal("Slow", `{"slow":true}`)
	waiting := newNormal("Waiting", `{}`)
	go func() { _, _ = loop.PollOnce(ctx, slow.ID) }()
	<-started
	done := make(chan error, 1)
	go func() {
		_, err := loop.PollOnce(ctx, waiting.ID)
		done <- err
	}()
	for !loop.isQueued(waiting.ID) {
		time.Sleep(5 * time.Millisecond)
	}
	// Raise the target while the poll waits for the normal lane.
	if _, scheduled, err := store.SetBackfillRows(ctx, waiting.ID, 50); err != nil || !scheduled {
		t.Fatalf("SetBackfillRows: scheduled = %v, err = %v", scheduled, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("queued poll: %v", err)
	}
	if opts := adapter.opts(); !opts.Backfill || opts.Limit != 50 {
		t.Fatalf("opts = %+v, want the backfill requested while waiting", opts)
	}
}
