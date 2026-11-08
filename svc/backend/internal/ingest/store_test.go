package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"hacktheburgh/backend/internal/db"
	"hacktheburgh/backend/internal/testdb"
)

func intPtr(value int) *int { return &value }

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(testdb.Open(t))
}

func TestStoreCreateAndGetSource(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	config := json.RawMessage(`{"url":"https://example.com/feed.xml"}`)
	source, err := store.CreateSource(ctx, "Example", "rss", config, intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if source.ID == "" || source.Name != "Example" || source.Kind != "rss" {
		t.Fatalf("source = %#v", source)
	}
	if source.PollIntervalS == nil || *source.PollIntervalS != 300 {
		t.Fatalf("PollIntervalS = %#v", source.PollIntervalS)
	}

	got, err := store.Source(ctx, source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.Name != "Example" {
		t.Fatalf("got = %#v", got)
	}
	var gotConfig map[string]any
	if err := json.Unmarshal(got.Config, &gotConfig); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if gotConfig["url"] != "https://example.com/feed.xml" {
		t.Fatalf("config = %#v", gotConfig)
	}

	byName, err := store.SourceByName(ctx, "Example")
	if err != nil {
		t.Fatalf("SourceByName: %v", err)
	}
	if byName.ID != source.ID {
		t.Fatalf("SourceByName ID = %q, want %q", byName.ID, source.ID)
	}

	sources, err := store.ListSources(ctx)
	if err != nil {
		t.Fatalf("ListSources: %v", err)
	}
	if len(sources) != 1 || sources[0].ID != source.ID {
		t.Fatalf("ListSources = %#v", sources)
	}
}

func TestStoreOneOffSourceHasNilInterval(t *testing.T) {
	store := newTestStore(t)

	source, err := store.CreateSource(context.Background(), "Upload", "csv", json.RawMessage(`{}`), nil)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if source.PollIntervalS != nil {
		t.Fatalf("PollIntervalS = %#v, want nil", source.PollIntervalS)
	}
}

func TestStoreDueSources(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Due", "rss", json.RawMessage(`{"url":"https://example.com/feed"}`), intPtr(60))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	due, err := store.DueSources(ctx)
	if err != nil {
		t.Fatalf("DueSources: %v", err)
	}
	if len(due) != 1 || due[0].ID != source.ID {
		t.Fatalf("due = %#v", due)
	}

	if err := store.MarkPolled(ctx, source.ID, time.Now().UTC(), nil); err != nil {
		t.Fatalf("MarkPolled: %v", err)
	}
	due, err = store.DueSources(ctx)
	if err != nil {
		t.Fatalf("DueSources: %v", err)
	}
	if len(due) != 0 {
		t.Fatalf("expected no due sources, got %#v", due)
	}

	if err := store.MarkPolled(ctx, source.ID, time.Now().UTC().Add(-2*time.Minute), nil); err != nil {
		t.Fatalf("MarkPolled: %v", err)
	}
	due, err = store.DueSources(ctx)
	if err != nil {
		t.Fatalf("DueSources: %v", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected one due source, got %#v", due)
	}
}

func TestStoreInsertRecordsDedup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Records", "rss", json.RawMessage(`{"url":"https://example.com/feed"}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	now := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	records := []RawRecord{
		{ExternalID: "a", ObservedAt: now, Payload: map[string]any{"value": 1}},
		{ExternalID: "b", ObservedAt: now.Add(time.Minute), Payload: map[string]any{"value": 2}},
	}
	inserted, err := store.InsertRecords(ctx, source.ID, records)
	if err != nil {
		t.Fatalf("InsertRecords: %v", err)
	}
	if inserted != 2 {
		t.Fatalf("inserted = %d, want 2", inserted)
	}

	inserted, err = store.InsertRecords(ctx, source.ID, records)
	if err != nil {
		t.Fatalf("InsertRecords duplicate: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("duplicate inserted = %d, want 0", inserted)
	}

	got, err := store.Source(ctx, source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.RecordCount != 2 {
		t.Fatalf("RecordCount = %d, want 2", got.RecordCount)
	}
}

func TestStoreMarkPolled(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Errors", "rss", json.RawMessage(`{"url":"https://example.com/feed"}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	polledAt := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := store.MarkPolled(ctx, source.ID, polledAt, errors.New("boom")); err != nil {
		t.Fatalf("MarkPolled: %v", err)
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.LastError == nil || *got.LastError != "boom" {
		t.Fatalf("LastError = %#v", got.LastError)
	}
	if got.LastPolledAt == nil || !got.LastPolledAt.Equal(polledAt) {
		t.Fatalf("LastPolledAt = %#v", got.LastPolledAt)
	}

	if err := store.MarkPolled(ctx, source.ID, polledAt.Add(time.Minute), nil); err != nil {
		t.Fatalf("MarkPolled clear: %v", err)
	}
	got, err = store.Source(ctx, source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if got.LastError != nil {
		t.Fatalf("LastError = %#v, want nil", got.LastError)
	}
}

func TestStoreEnsureSourceDoesNotUpdate(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	first, err := store.EnsureSource(ctx, "Stable", "rss", json.RawMessage(`{"url":"https://example.com/one"}`), intPtr(300), 0)
	if err != nil {
		t.Fatalf("EnsureSource: %v", err)
	}
	second, err := store.EnsureSource(ctx, "Stable", "rss", json.RawMessage(`{"url":"https://example.com/two"}`), intPtr(60), 0)
	if err != nil {
		t.Fatalf("EnsureSource second: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("ID changed: %q != %q", second.ID, first.ID)
	}
	var config map[string]any
	if err := json.Unmarshal(second.Config, &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if config["url"] != "https://example.com/one" {
		t.Fatalf("config changed: %#v", config)
	}
	if second.PollIntervalS == nil || *second.PollIntervalS != 300 {
		t.Fatalf("interval changed: %#v", second.PollIntervalS)
	}
}

func TestStoreCascadeDelete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Cascade", "rss", json.RawMessage(`{"url":"https://example.com/feed"}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if _, err := store.InsertRecords(ctx, source.ID, []RawRecord{
		{ExternalID: "a", ObservedAt: time.Now().UTC(), Payload: map[string]any{"value": 1}},
	}); err != nil {
		t.Fatalf("InsertRecords: %v", err)
	}

	if _, err := store.Pool.Exec(ctx, `DELETE FROM data_sources WHERE id = $1::uuid`, source.ID); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	var count int
	if err := store.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM data_records`).Scan(&count); err != nil {
		t.Fatalf("count records: %v", err)
	}
	if count != 0 {
		t.Fatalf("records remain after cascade: %d", count)
	}
}

func TestStoreMigrationsAreIdempotent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	if err := db.Migrate(ctx, store.Pool); err != nil {
		t.Fatalf("second migration: %v", err)
	}
	if err := db.Migrate(ctx, store.Pool); err != nil {
		t.Fatalf("third migration: %v", err)
	}
}

func TestStoreInvalidSourceID(t *testing.T) {
	store := newTestStore(t)

	if _, err := store.Source(context.Background(), "not-a-uuid"); err == nil {
		t.Fatal("expected invalid UUID error")
	}
}

func TestStoreInsertRecordsEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Empty", "rss", json.RawMessage(`{"url":"https://example.com/feed"}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	inserted, err := store.InsertRecords(ctx, source.ID, nil)
	if err != nil {
		t.Fatalf("InsertRecords: %v", err)
	}
	if inserted != 0 {
		t.Fatalf("inserted = %d, want 0", inserted)
	}
}
