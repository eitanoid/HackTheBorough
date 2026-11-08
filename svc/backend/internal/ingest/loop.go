package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"hacktheburgh/backend/internal/events"
)

// ErrPollInProgress is returned when a poll for the same source is already
// running (for example a long backfill).
var ErrPollInProgress = errors.New("source poll already in progress")

// ErrPollQueued is returned when a poll for the source is already queued
// behind other polls and will run as soon as a slot frees up.
var ErrPollQueued = errors.New("source poll already queued")

type Loop struct {
	Store       *Store
	Bus         *events.Bus
	Adapters    map[string]SourceAdapter
	Tick        time.Duration
	MaxParallel int

	semOnce     sync.Once
	sem         chan struct{} // normal polls: MaxParallel at once
	backfillSem chan struct{} // backfills: one at a time, separate from normal polls
	mu          sync.Mutex
	inflight    map[string]bool // source ID → running (false while queued for a slot)
	running     sync.WaitGroup  // polls started by pollDue
}

func NewLoop(store *Store, bus *events.Bus, adapters map[string]SourceAdapter, tick time.Duration, maxParallel int) *Loop {
	if tick <= 0 {
		tick = 10 * time.Second
	}
	if maxParallel <= 0 {
		maxParallel = 4
	}
	return &Loop{Store: store, Bus: bus, Adapters: adapters, Tick: tick, MaxParallel: maxParallel}
}

// Run dispatches due sources every tick. Dispatching never waits for polls to
// finish; each source has at most one poll running or queued (the in-flight
// set), so the number of waiting goroutines is bounded by the number of
// sources. Normal polls share MaxParallel slots; backfills, which can take
// minutes, run one at a time in their own lane so they never hold up normal
// polls.
func (l *Loop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.Tick)
	defer ticker.Stop()
	l.pollDue(ctx)
	for {
		select {
		case <-ctx.Done():
			l.running.Wait()
			return
		case <-ticker.C:
			l.pollDue(ctx)
		}
	}
}

func (l *Loop) lanes() (normal, backfill chan struct{}) {
	l.semOnce.Do(func() {
		size := l.MaxParallel
		if size <= 0 {
			size = 4
		}
		l.sem = make(chan struct{}, size)
		l.backfillSem = make(chan struct{}, 1)
	})
	return l.sem, l.backfillSem
}

// claim marks a source as queued for polling. If it already is queued or
// running it returns ErrPollQueued or ErrPollInProgress.
func (l *Loop) claim(sourceID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight == nil {
		l.inflight = map[string]bool{}
	}
	if running, busy := l.inflight[sourceID]; busy {
		if running {
			return ErrPollInProgress
		}
		return ErrPollQueued
	}
	l.inflight[sourceID] = false
	return nil
}

func (l *Loop) markRunning(sourceID string) {
	l.mu.Lock()
	if _, ok := l.inflight[sourceID]; ok {
		l.inflight[sourceID] = true
	}
	l.mu.Unlock()
}

func (l *Loop) release(sourceID string) {
	l.mu.Lock()
	delete(l.inflight, sourceID)
	l.mu.Unlock()
}

// pollDue starts a poll for every due source that is not already being
// polled and returns without waiting for them (see waitIdle).
func (l *Loop) pollDue(ctx context.Context) {
	sources, err := l.Store.DueSources(ctx)
	if err != nil {
		slog.Error("list due sources", "err", err)
		return
	}
	for _, source := range sources {
		source := source
		if l.claim(source.ID) != nil {
			continue
		}
		l.running.Add(1)
		go func() {
			defer l.running.Done()
			defer l.release(source.ID)
			if _, err := l.poll(ctx, source.ID, true); err != nil && !errors.Is(err, errLaneChanged) {
				slog.Warn("source poll failed", "source_id", source.ID, "err", err)
			}
		}()
	}
}

// PollOnce fetches one source now, regardless of its schedule. It waits for a
// free slot in the source's lane and returns ErrPollInProgress or
// ErrPollQueued if the source is already being polled.
func (l *Loop) PollOnce(ctx context.Context, sourceID string) (int, error) {
	if err := l.claim(sourceID); err != nil {
		return 0, err
	}
	defer l.release(sourceID)
	return l.poll(ctx, sourceID, true)
}

// PollResult is the outcome of a manual poll (PollNow).
type PollResult struct {
	Inserted int
	// Queued is true when the poll was handed to the ingest loop instead of
	// running now; Backfill is true when that is because a backfill is
	// pending (backfills can page for minutes, longer than a request).
	Queued   bool
	Backfill bool
}

// PollNow is the manual "poll now": a normal poll runs immediately, but a
// pending backfill is never run inside the caller's request. It is re-queued
// for the backfill lane instead (resetting its attempts), while holding the
// source's claim so no scheduled poll can read the old request in between.
// It returns ErrPollQueued or ErrPollInProgress if a poll already holds the
// source.
func (l *Loop) PollNow(ctx context.Context, sourceID string) (PollResult, error) {
	if err := l.claim(sourceID); err != nil {
		return PollResult{}, err
	}
	defer l.release(sourceID)
	source, err := l.Store.SourceForPoll(ctx, sourceID)
	if err != nil {
		return PollResult{}, err
	}
	if source.BackfillPending {
		if _, err := l.Store.RequeueBackfill(ctx, sourceID); err != nil {
			return PollResult{}, err
		}
		return PollResult{Queued: true, Backfill: true}, nil
	}
	inserted, err := l.poll(ctx, sourceID, false)
	if errors.Is(err, errBackfillRequested) || errors.Is(err, errLaneChanged) {
		// A backfill was requested while this poll waited; the loop runs it.
		return PollResult{Queued: true, Backfill: errors.Is(err, errBackfillRequested)}, nil
	}
	return PollResult{Inserted: inserted}, err
}

// errLaneChanged means the source's backfill state kept changing while the
// poll waited for a slot; nothing was fetched or marked, so the next tick
// polls it again in the right lane.
var errLaneChanged = errors.New("source backfill state changed while waiting for a slot")

// errBackfillRequested means a manual poll found a backfill pending once it
// got its slot; nothing was fetched or marked and the loop will run it.
var errBackfillRequested = errors.New("backfill requested while waiting")

// acquireLane waits for a slot in the lane the source needs and returns the
// source as read after getting it, so the backfill state, target and token are
// current when the fetch starts (they can change while a poll waits). If the
// state changed while waiting, the slot is swapped for the right lane.
func (l *Loop) acquireLane(ctx context.Context, sourceID string) (Source, func(), error) {
	normalLane, backfillLane := l.lanes()
	source, err := l.Store.SourceForPoll(ctx, sourceID)
	if err != nil {
		return Source{}, nil, err
	}
	for attempt := 0; ; attempt++ {
		backfill := source.BackfillRequestedAt != nil
		lane := normalLane
		if backfill {
			lane = backfillLane
		}
		select {
		case lane <- struct{}{}:
		case <-ctx.Done():
			return Source{}, nil, ctx.Err()
		}
		release := func() { <-lane }
		fresh, err := l.Store.SourceForPoll(ctx, sourceID)
		if err != nil {
			release()
			return Source{}, nil, err
		}
		if (fresh.BackfillRequestedAt != nil) == backfill {
			return fresh, release, nil
		}
		release()
		if attempt >= 2 {
			return Source{}, nil, errLaneChanged
		}
		source = fresh
	}
}

// poll does the work of one poll; the caller must hold the source's claim.
//
// Backfills page through the source until BackfillRows records are found and
// keep the newest BackfillRows of them. A failed or incomplete backfill stays
// pending and is retried on later scheduled polls, up to MaxBackfillAttempts.
// Normal polls fetch a single page and skip the ids a capped backfill
// deliberately left out (sources keep re-serving them); everything else,
// dated or not, is inserted and deduplicated by the database.
//
// With allowBackfill false (manual polls) a pending backfill is not run; the
// poll returns errBackfillRequested without fetching or marking anything.
func (l *Loop) poll(ctx context.Context, sourceID string, allowBackfill bool) (int, error) {
	source, release, err := l.acquireLane(ctx, sourceID)
	if err != nil {
		return 0, err
	}
	defer release()
	if source.BackfillPending && !allowBackfill {
		return 0, errBackfillRequested
	}
	l.markRunning(source.ID)

	markCtx := context.WithoutCancel(ctx)
	adapter, ok := l.Adapters[source.Kind]
	if !ok {
		err := fmt.Errorf("unsupported source kind %q", source.Kind)
		_ = l.Store.MarkPolled(markCtx, source.ID, time.Now().UTC(), err)
		return 0, err
	}
	backfill := source.BackfillRequestedAt != nil
	opts := FetchOptions{Since: source.LastPolledAt}
	if backfill {
		opts.Backfill = true
		opts.Limit = source.BackfillRows
	}
	polledAt := time.Now().UTC()
	result, fetchErr := adapter.Fetch(ctx, source.Config, opts)
	if fetchErr != nil {
		if backfill && ctx.Err() != nil {
			// Cancelled (shutdown or timeout): record the error but do not
			// count it as a poll or an attempt, so the backfill stays due.
			_ = l.Store.MarkError(markCtx, source.ID, fetchErr)
			return 0, fetchErr
		}
		_ = l.Store.MarkPolled(markCtx, source.ID, polledAt, fetchErr)
		if backfill {
			l.failBackfill(markCtx, source, nil, nil, fetchErr)
		}
		return 0, fetchErr
	}
	records := result.Records
	fetched := len(records)
	var skipped []string
	if backfill {
		SortNewestFirst(records)
		all := records
		records = CapRecords(records, opts.Limit)
		skipped = Dropped(all, records)
	} else if len(records) > 0 && source.HasSkipped {
		ids := make([]string, len(records))
		for i, record := range records {
			ids[i] = record.ExternalID
		}
		skip, err := l.Store.SkippedIDs(ctx, source.ID, ids)
		if err != nil {
			_ = l.Store.MarkPolled(markCtx, source.ID, polledAt, err)
			return 0, err
		}
		records = WithoutIDs(records, skip)
	}
	inserted, insertErr := l.Store.InsertRecords(ctx, source.ID, records)
	if insertErr != nil {
		if backfill && ctx.Err() != nil {
			_ = l.Store.MarkError(markCtx, source.ID, insertErr)
			return 0, insertErr
		}
		_ = l.Store.MarkPolled(markCtx, source.ID, polledAt, insertErr)
		if backfill {
			l.failBackfill(markCtx, source, nil, nil, insertErr)
		}
		return 0, insertErr
	}

	var partialErr error
	if backfill && result.Report != nil && result.Report.Error != "" {
		partialErr = fmt.Errorf("backfill stopped early: %s", result.Report.Error)
	}
	if err := l.Store.MarkPolled(markCtx, source.ID, polledAt, partialErr); err != nil {
		return inserted, err
	}
	if backfill {
		report := result.Report
		if report == nil {
			report = &PagingReport{Pages: 1, Methods: map[string]int{}, Stopped: StopNoNext, FinishedAt: time.Now().UTC()}
		}
		report.Rows = len(records)
		encoded, _ := json.Marshal(report)
		if partialErr != nil {
			// Keep the rows, but retry the rest of the walk later.
			l.failBackfill(markCtx, source, encoded, skipped, partialErr)
		} else if err := l.Store.FinishBackfill(markCtx, source.ID, *source.BackfillRequestedAt, encoded, skipped); err != nil {
			return inserted, err
		}
	}
	if inserted > 0 && l.Bus != nil {
		l.Bus.Publish(events.Event{Type: events.SourceUpdated, ID: source.ID})
	}
	attrs := []any{"source_id", source.ID, "kind", source.Kind, "fetched", fetched, "inserted", inserted, "backfill", backfill}
	if backfill {
		attrs = append(attrs, "limit", opts.Limit, "paging", describeReport(result.Report))
	} else {
		attrs = append(attrs, "skipped_old", fetched-len(records))
	}
	if partialErr != nil {
		attrs = append(attrs, "err", partialErr)
	}
	slog.Info("source polled", attrs...)
	// A partial backfill is not a failed poll: its rows are stored and the
	// error is recorded on the source for the UI.
	return inserted, nil
}

// failBackfill counts a failed or incomplete backfill try. After
// MaxBackfillAttempts it gives up so the source returns to normal polling;
// the error stays on the source so the user can see why.
func (l *Loop) failBackfill(ctx context.Context, source Source, report json.RawMessage, skipped []string, cause error) {
	gaveUp, err := l.Store.FailBackfill(ctx, source.ID, *source.BackfillRequestedAt, report, skipped)
	if err != nil {
		slog.Warn("record backfill failure", "source_id", source.ID, "err", err)
		return
	}
	if gaveUp {
		_ = l.Store.MarkError(ctx, source.ID, fmt.Errorf("backfill gave up after %d attempts: %v", MaxBackfillAttempts, cause))
		slog.Warn("backfill given up", "source_id", source.ID, "attempts", MaxBackfillAttempts, "err", cause)
	}
}
