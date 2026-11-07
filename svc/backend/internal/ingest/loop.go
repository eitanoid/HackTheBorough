package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"hacktheburgh/backend/internal/events"
)

type Loop struct {
	Store       *Store
	Bus         *events.Bus
	Adapters    map[string]SourceAdapter
	Tick        time.Duration
	MaxParallel int
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

func (l *Loop) Run(ctx context.Context) {
	ticker := time.NewTicker(l.Tick)
	defer ticker.Stop()
	l.pollDue(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.pollDue(ctx)
		}
	}
}

func (l *Loop) pollDue(ctx context.Context) {
	sources, err := l.Store.DueSources(ctx)
	if err != nil {
		slog.Error("list due sources", "err", err)
		return
	}
	if len(sources) == 0 {
		return
	}
	sem := make(chan struct{}, l.MaxParallel)
	var wg sync.WaitGroup
	for _, source := range sources {
		source := source
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			if _, err := l.PollOnce(ctx, source.ID); err != nil {
				slog.Warn("source poll failed", "source_id", source.ID, "err", err)
			}
		}()
	}
	wg.Wait()
}

// PollOnce fetches one source now, regardless of its schedule.
func (l *Loop) PollOnce(ctx context.Context, sourceID string) (int, error) {
	source, err := l.Store.Source(ctx, sourceID)
	if err != nil {
		return 0, err
	}
	adapter, ok := l.Adapters[source.Kind]
	if !ok {
		err := fmt.Errorf("unsupported source kind %q", source.Kind)
		_ = l.Store.MarkPolled(ctx, source.ID, time.Now().UTC(), err)
		return 0, err
	}
	polledAt := time.Now().UTC()
	records, fetchErr := adapter.Fetch(ctx, source.Config, source.LastPolledAt)
	if fetchErr != nil {
		_ = l.Store.MarkPolled(ctx, source.ID, polledAt, fetchErr)
		return 0, fetchErr
	}
	inserted, insertErr := l.Store.InsertRecords(ctx, source.ID, records)
	if insertErr != nil {
		_ = l.Store.MarkPolled(ctx, source.ID, polledAt, insertErr)
		return 0, insertErr
	}
	if err := l.Store.MarkPolled(ctx, source.ID, polledAt, nil); err != nil {
		return inserted, err
	}
	if inserted > 0 && l.Bus != nil {
		l.Bus.Publish(events.Event{Type: events.SourceUpdated, ID: source.ID})
	}
	slog.Info("source polled", "source_id", source.ID, "kind", source.Kind, "inserted", inserted)
	return inserted, nil
}
