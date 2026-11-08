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
			if _, err := l.poll(ctx, source.ID); err != nil {
				slog.Warn("source poll failed", "source_id", source.ID, "err", err)
			}
		}()
	}
}

// waitIdle blocks until every poll started by pollDue has finished.
func (l *Loop) waitIdle() { l.running.Wait() }

// PollOnce fetches one source now, regardless of its schedule. It waits for a
// free slot in the source's lane and returns ErrPollInProgress or
// ErrPollQueued if the source is already being polled.
func (l *Loop) PollOnce(ctx context.Context, sourceID string) (int, error) {
	if err := l.claim(sourceID); err != nil {
		return 0, err
	}
	defer l.release(sourceID)
	return l.poll(ctx, sourceID)
}

// poll does the work of one poll; the caller must hold the source's claim.
//
// The lane is chosen from the freshly read source, so a backfill requested
// after the source was listed as due still runs in the backfill lane.
//
// Backfills page through the source until BackfillRows records are found and
// keep the newest BackfillRows of them. Normal polls fetch a single page and
// only add rows newer than what is stored: sources keep re-serving rows a
// backfill left out (a long RSS page, a whole CSV file), and those must not
// leak back in.
func (l *Loop) poll(ctx context.Context, sourceID string) (int, error) {
	source, err := l.Store.Source(ctx, sourceID)
	if err != nil {
		return 0, err
	}
	markCtx := context.WithoutCancel(ctx)
	adapter, ok := l.Adapters[source.Kind]
	if !ok {
		err := fmt.Errorf("unsupported source kind %q", source.Kind)
		_ = l.Store.MarkPolled(markCtx, source.ID, time.Now().UTC(), err)
		return 0, err
	}
	backfill := source.BackfillRequestedAt != nil

	normalLane, backfillLane := l.lanes()
	lane := normalLane
	if backfill {
		lane = backfillLane
	}
	select {
	case lane <- struct{}{}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	defer func() { <-lane }()
	l.markRunning(source.ID)

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
			// count it as a poll, so the backfill stays due on the next tick.
			_ = l.Store.MarkError(markCtx, source.ID, fetchErr)
			return 0, fetchErr
		}
		// A failed backfill stays pending and is retried on the next
		// scheduled poll.
		_ = l.Store.MarkPolled(markCtx, source.ID, polledAt, fetchErr)
		return 0, fetchErr
	}
	records := result.Records
	SortNewestFirst(records)
	fetched := len(records)
	if backfill {
		records = CapRecords(records, opts.Limit)
	} else if len(records) > 0 {
		ids := make([]string, len(records))
		for i, record := range records {
			ids[i] = record.ExternalID
		}
		known, err := l.Store.KnownExternalIDs(ctx, source.ID, ids)
		if err != nil {
			_ = l.Store.MarkPolled(markCtx, source.ID, polledAt, err)
			return 0, err
		}
		records = TrimKnownTail(records, known)
	}
	inserted, insertErr := l.Store.InsertRecords(ctx, source.ID, records)
	if insertErr != nil {
		_ = l.Store.MarkPolled(markCtx, source.ID, polledAt, insertErr)
		return 0, insertErr
	}

	// A backfill that stopped on a failing page keeps its rows but stays
	// pending (and shows the error) so the next scheduled poll retries it.
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
		var err error
		if partialErr != nil {
			err = l.Store.RecordBackfillReport(markCtx, source.ID, encoded)
		} else {
			err = l.Store.FinishBackfill(markCtx, source.ID, *source.BackfillRequestedAt, encoded)
		}
		if err != nil {
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
