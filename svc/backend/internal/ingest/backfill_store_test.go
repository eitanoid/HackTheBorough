package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestStoreBackfillDefaults(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	polled, err := store.CreateSource(ctx, "Polled", "rss", json.RawMessage(`{"url":"https://example.com/feed"}`), intPtr(300), 0)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if polled.BackfillRows != DefaultBackfillRows || !polled.BackfillPending {
		t.Fatalf("polled source: rows = %d, pending = %v", polled.BackfillRows, polled.BackfillPending)
	}
	custom, err := store.CreateSource(ctx, "Custom", "rss", json.RawMessage(`{"url":"https://example.com/feed"}`), intPtr(300), 42)
	if err != nil || custom.BackfillRows != 42 {
		t.Fatalf("custom rows = %d, err = %v", custom.BackfillRows, err)
	}
	oneOff, err := store.CreateSource(ctx, "One-off", "csv", json.RawMessage(`{}`), nil, 0)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if oneOff.BackfillPending {
		t.Fatal("a source that is never polled must not have a pending backfill")
	}
}

func TestStoreSetBackfillRows(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Target", "rss", json.RawMessage(`{"url":"https://example.com/feed"}`), intPtr(300), 5)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if _, err := store.InsertRecords(ctx, source.ID, recs("a", "b", "c", "d", "e")); err != nil {
		t.Fatalf("InsertRecords: %v", err)
	}
	if err := store.FinishBackfill(ctx, source.ID, *source.BackfillRequestedAt, json.RawMessage(`{"pages":1}`), nil); err != nil {
		t.Fatalf("FinishBackfill: %v", err)
	}

	got, scheduled, err := store.SetBackfillRows(ctx, source.ID, 5)
	if err != nil || scheduled || got.BackfillPending {
		t.Fatalf("equal target: scheduled = %v, pending = %v, err = %v", scheduled, got.BackfillPending, err)
	}
	got, scheduled, err = store.SetBackfillRows(ctx, source.ID, 2)
	if err != nil || scheduled || got.BackfillRows != 2 || got.RecordCount != 5 {
		t.Fatalf("lower target: scheduled = %v, rows = %d, count = %d, err = %v", scheduled, got.BackfillRows, got.RecordCount, err)
	}
	got, scheduled, err = store.SetBackfillRows(ctx, source.ID, 50)
	if err != nil || !scheduled || !got.BackfillPending || got.BackfillRows != 50 {
		t.Fatalf("raise target: scheduled = %v, pending = %v, err = %v", scheduled, got.BackfillPending, err)
	}
	// Lowering again must not cancel the pending request or delete rows.
	got, scheduled, err = store.SetBackfillRows(ctx, source.ID, 1)
	if err != nil || scheduled || !got.BackfillPending || got.RecordCount != 5 {
		t.Fatalf("lower while pending: scheduled = %v, pending = %v, count = %d, err = %v", scheduled, got.BackfillPending, got.RecordCount, err)
	}
	if string(got.LastBackfill) != `{"pages": 1}` {
		t.Fatalf("LastBackfill = %s", got.LastBackfill)
	}

	if _, _, err := store.SetBackfillRows(ctx, "00000000-0000-0000-0000-000000000000", 10); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown id err = %v, want ErrNoRows", err)
	}

	oneOff, err := store.CreateUploadedCSV(ctx, "Upload", "data.csv", recs("x"), 0)
	if err != nil {
		t.Fatalf("CreateUploadedCSV: %v", err)
	}
	if _, scheduled, err := store.SetBackfillRows(ctx, oneOff.ID, 100); err != nil || scheduled {
		t.Fatalf("uploaded CSV: scheduled = %v, err = %v (it has no file to re-read)", scheduled, err)
	}
}

func TestStoreDueSourcesIncludesPendingBackfillOnce(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Due", "rss", json.RawMessage(`{"url":"https://example.com/feed"}`), intPtr(3600), 0)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	// Polled just now and backfill done: not due for an hour.
	if err := store.MarkPolled(ctx, source.ID, time.Now().UTC(), nil); err != nil {
		t.Fatalf("MarkPolled: %v", err)
	}
	if err := store.FinishBackfill(ctx, source.ID, *source.BackfillRequestedAt, nil, nil); err != nil {
		t.Fatalf("FinishBackfill: %v", err)
	}
	assertDue(t, store, 0)

	// Raising the target makes it due on the next tick...
	time.Sleep(5 * time.Millisecond)
	if _, scheduled, err := store.SetBackfillRows(ctx, source.ID, 1000); err != nil || !scheduled {
		t.Fatalf("SetBackfillRows: scheduled = %v, err = %v", scheduled, err)
	}
	assertDue(t, store, 1)

	// ...and a failed backfill poll falls back to the normal interval while
	// staying pending.
	time.Sleep(5 * time.Millisecond)
	if err := store.MarkPolled(ctx, source.ID, time.Now().UTC(), errors.New("boom")); err != nil {
		t.Fatalf("MarkPolled: %v", err)
	}
	assertDue(t, store, 0)
	got, err := store.Source(ctx, source.ID)
	if err != nil || !got.BackfillPending {
		t.Fatalf("pending = %v, err = %v", got.BackfillPending, err)
	}
}

func assertDue(t *testing.T, store *Store, want int) {
	t.Helper()
	due, err := store.DueSources(context.Background())
	if err != nil {
		t.Fatalf("DueSources: %v", err)
	}
	if len(due) != want {
		t.Fatalf("due = %d sources, want %d", len(due), want)
	}
}

func TestStoreFinishBackfillKeepsNewerRequest(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Race", "rss", json.RawMessage(`{"url":"https://example.com/feed"}`), intPtr(300), 0)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, _, err := store.SetBackfillRows(ctx, source.ID, 900); err != nil {
		t.Fatalf("SetBackfillRows: %v", err)
	}
	// The poll that served the original request finishes afterwards.
	if err := store.FinishBackfill(ctx, source.ID, *source.BackfillRequestedAt, json.RawMessage(`{"pages":2}`), nil); err != nil {
		t.Fatalf("FinishBackfill: %v", err)
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	if !got.BackfillPending {
		t.Fatal("a newer backfill request must stay pending")
	}
	if len(got.LastBackfill) != 0 {
		t.Fatalf("a stale walk must not overwrite the pending request's report: %s", got.LastBackfill)
	}
}

func TestStoreCreateUploadedCSVCapsRows(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	var timeless []RawRecord
	for i := 0; i < 5; i++ {
		timeless = append(timeless, RawRecord{
			ExternalID: fmt.Sprintf("row-%d", i), ObservedAt: time.Now().UTC(),
			Payload: map[string]any{"i": i, "_time_estimated": true},
		})
	}
	source, err := store.CreateUploadedCSV(ctx, "Upload", "data.csv", timeless, 2)
	if err != nil {
		t.Fatalf("CreateUploadedCSV: %v", err)
	}
	if source.RecordCount != 2 || source.BackfillRows != 2 || source.BackfillPending {
		t.Fatalf("source = %+v", source)
	}
	records, err := store.RecentRecords(ctx, source.ID, 10)
	if err != nil {
		t.Fatalf("RecentRecords: %v", err)
	}
	stored := map[string]bool{}
	for _, record := range records {
		stored[record.ExternalID] = true
	}
	if !stored["row-3"] || !stored["row-4"] {
		t.Fatalf("timeless upload must keep the last rows of the file (like CSV URL sources), got %v", stored)
	}

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	timed := []RawRecord{
		{ExternalID: "jan", ObservedAt: base, Payload: map[string]any{}},
		{ExternalID: "mar", ObservedAt: base.AddDate(0, 2, 0), Payload: map[string]any{}},
		{ExternalID: "feb", ObservedAt: base.AddDate(0, 1, 0), Payload: map[string]any{}},
	}
	source, err = store.CreateUploadedCSV(ctx, "Timed", "timed.csv", timed, 2)
	if err != nil {
		t.Fatalf("CreateUploadedCSV: %v", err)
	}
	records, err = store.RecentRecords(ctx, source.ID, 10)
	if err != nil || len(records) != 2 || records[0].ExternalID != "mar" || records[1].ExternalID != "feb" {
		t.Fatalf("timed upload must keep the newest rows: %+v, err = %v", records, err)
	}
}

func TestStoreSetBackfillRowsSameTargetBelowCountReschedules(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	// Like a pre-existing source: target 500 but only one page (3 rows) stored.
	source, err := store.CreateSource(ctx, "Shallow", "web", json.RawMessage(`{}`), intPtr(300), 500)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if _, err := store.InsertRecords(ctx, source.ID, recs("a", "b", "c")); err != nil {
		t.Fatalf("InsertRecords: %v", err)
	}
	if err := store.FinishBackfill(ctx, source.ID, *source.BackfillRequestedAt, nil, nil); err != nil {
		t.Fatalf("FinishBackfill: %v", err)
	}
	got, scheduled, err := store.SetBackfillRows(ctx, source.ID, 500)
	if err != nil || !scheduled || !got.BackfillPending {
		t.Fatalf("scheduled = %v, pending = %v, err = %v", scheduled, got.BackfillPending, err)
	}
}

func TestStoreCreateUploadedCSVWithoutTargetKeepsAll(t *testing.T) {
	store := newTestStore(t)
	source, err := store.CreateUploadedCSV(context.Background(), "All", "all.csv", recs("a", "b", "c"), 0)
	if err != nil || source.RecordCount != 3 || source.BackfillRows != 3 {
		t.Fatalf("count = %d, rows = %d, err = %v", source.RecordCount, source.BackfillRows, err)
	}
}

func TestStoreSetBackfillRowsRaiseSchedulesEvenWithManyRows(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	source, err := store.CreateSource(ctx, "Grown", "rss", json.RawMessage(`{}`), intPtr(300), 2)
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	// Normal polls have added more rows than the new target.
	if _, err := store.InsertRecords(ctx, source.ID, recs("a", "b", "c", "d", "e")); err != nil {
		t.Fatalf("InsertRecords: %v", err)
	}
	if err := store.FinishBackfill(ctx, source.ID, *source.BackfillRequestedAt, nil, nil); err != nil {
		t.Fatalf("FinishBackfill: %v", err)
	}
	if _, scheduled, err := store.SetBackfillRows(ctx, source.ID, 4); err != nil || !scheduled {
		t.Fatalf("raising the target must reach older history: scheduled = %v, err = %v", scheduled, err)
	}
}
