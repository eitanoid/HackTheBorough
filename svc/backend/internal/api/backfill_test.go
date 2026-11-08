package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	v1 "hacktheburgh/backend/gen/hacktheburgh/v1"
	"hacktheburgh/backend/internal/ingest"
	"hacktheburgh/backend/internal/testdb"
)

func TestConnectBackfillRowsIntegration(t *testing.T) {
	store := ingest.NewStore(testdb.Open(t))
	httpServer := httptest.NewServer((&Server{Store: store, BackfillDefault: 250}).Routes())
	t.Cleanup(httpServer.Close)
	client := connectTestClient(t, httpServer)
	ctx := context.Background()

	defaulted, err := client.CreateSource(ctx, &v1.CreateSourceRequest{
		Name: "Default", Kind: v1.SourceKind_SOURCE_KIND_RSS,
		Config: &v1.SourceConfig{Url: "https://example.com/feed.xml"},
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	if got := defaulted.GetSource(); got.GetBackfillRows() != 250 || !got.GetBackfillPending() {
		t.Fatalf("defaulted source = %#v", got)
	}

	created, err := client.CreateSource(ctx, &v1.CreateSourceRequest{
		Name: "Custom", Kind: v1.SourceKind_SOURCE_KIND_RSS,
		Config: &v1.SourceConfig{Url: "https://example.com/feed.xml"}, BackfillRows: 2,
	})
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	id := created.GetSource().GetId()
	if created.GetSource().GetBackfillRows() != 2 {
		t.Fatalf("created source = %#v", created.GetSource())
	}
	if _, err := store.InsertRecords(ctx, id, []ingest.RawRecord{
		{ExternalID: "a", ObservedAt: time.Now().UTC(), Payload: map[string]any{}},
		{ExternalID: "b", ObservedAt: time.Now().UTC(), Payload: map[string]any{}},
	}); err != nil {
		t.Fatalf("InsertRecords: %v", err)
	}
	source, err := store.Source(ctx, id)
	if err != nil {
		t.Fatalf("Source: %v", err)
	}
	report, _ := json.Marshal(ingest.PagingReport{Pages: 4, Methods: map[string]int{"rel-next": 3}, Rows: 2, Stopped: "limit", FinishedAt: time.Now().UTC()})
	if err := store.FinishBackfill(ctx, id, *source.BackfillRequestedAt, report, nil); err != nil {
		t.Fatalf("FinishBackfill: %v", err)
	}

	lowered, err := client.UpdateSource(ctx, &v1.UpdateSourceRequest{Id: id, BackfillRows: int32Ptr(1)})
	if err != nil {
		t.Fatalf("UpdateSource lower: %v", err)
	}
	if lowered.GetBackfillScheduled() || lowered.GetSource().GetBackfillRows() != 1 || lowered.GetSource().GetRecordCount() != 2 {
		t.Fatalf("lowered = %#v", lowered)
	}
	if got := lowered.GetSource().GetLastBackfill(); got.GetPages() != 4 || got.GetMethods()["rel-next"] != 3 || got.GetStopped() != "limit" {
		t.Fatalf("last backfill = %#v", got)
	}

	raised, err := client.UpdateSource(ctx, &v1.UpdateSourceRequest{Id: id, BackfillRows: int32Ptr(300)})
	if err != nil {
		t.Fatalf("UpdateSource raise: %v", err)
	}
	if !raised.GetBackfillScheduled() || !raised.GetSource().GetBackfillPending() {
		t.Fatalf("raised = %#v", raised)
	}

	invalid := []struct {
		name string
		req  *v1.UpdateSourceRequest
		code connect.Code
	}{
		{"missing id", &v1.UpdateSourceRequest{BackfillRows: int32Ptr(5)}, connect.CodeInvalidArgument},
		{"nothing to update", &v1.UpdateSourceRequest{Id: id}, connect.CodeInvalidArgument},
		{"zero", &v1.UpdateSourceRequest{Id: id, BackfillRows: int32Ptr(0)}, connect.CodeInvalidArgument},
		{"negative", &v1.UpdateSourceRequest{Id: id, BackfillRows: int32Ptr(-1)}, connect.CodeInvalidArgument},
		{"bad uuid", &v1.UpdateSourceRequest{Id: "nope", BackfillRows: int32Ptr(5)}, connect.CodeInvalidArgument},
		{"unknown", &v1.UpdateSourceRequest{Id: "00000000-0000-0000-0000-000000000000", BackfillRows: int32Ptr(5)}, connect.CodeNotFound},
	}
	for _, tc := range invalid {
		_, err := client.UpdateSource(ctx, tc.req)
		var connectErr *connect.Error
		if !errors.As(err, &connectErr) || connectErr.Code() != tc.code {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.code)
		}
	}

	if _, err := client.CreateSource(ctx, &v1.CreateSourceRequest{
		Name: "Negative", Kind: v1.SourceKind_SOURCE_KIND_RSS,
		Config: &v1.SourceConfig{Url: "https://example.com/feed.xml"}, BackfillRows: -3,
	}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("negative backfill_rows err = %v", err)
	}

	uploaded, err := client.UploadCSVSource(ctx, &v1.UploadCSVSourceRequest{
		Name: "Capped", FileName: "rows.csv", BackfillRows: 2,
		CsvData: []byte("date,value\n2026-01-01,1\n2026-01-02,2\n2026-01-03,3\n"),
	})
	if err != nil {
		t.Fatalf("UploadCSVSource: %v", err)
	}
	if uploaded.GetSource().GetRecordCount() != 2 || uploaded.GetSource().GetBackfillPending() {
		t.Fatalf("uploaded = %#v", uploaded.GetSource())
	}
	again, err := client.UpdateSource(ctx, &v1.UpdateSourceRequest{Id: uploaded.GetSource().GetId(), BackfillRows: int32Ptr(10)})
	if err != nil || again.GetBackfillScheduled() {
		t.Fatalf("uploaded CSV update: scheduled = %v, err = %v", again.GetBackfillScheduled(), err)
	}
}

func TestRESTBackfillRowsIntegration(t *testing.T) {
	server, _ := newIntegrationServer(t)
	server.BackfillDefault = 300
	handler := server.Routes()

	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"name":"Default","kind":"rss","config":{"url":"https://example.com/feed.xml"}}`, 300},
		{`{"name":"Custom","kind":"rss","config":{"url":"https://example.com/feed.xml"},"backfill_rows":75}`, 75},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusCreated {
			t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
		}
		var source struct {
			BackfillRows    int  `json:"backfill_rows"`
			BackfillPending bool `json:"backfill_pending"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &source); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if source.BackfillRows != tc.want || !source.BackfillPending {
			t.Fatalf("source = %+v, want rows %d and pending", source, tc.want)
		}
	}
}

func TestPollSourceInProgressConflict(t *testing.T) {
	server, store := newIntegrationServer(t)
	ctx := context.Background()
	source, err := store.CreateSource(ctx, "Busy", "rss", json.RawMessage(`{}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	finishBackfill(t, store, source)
	adapter := &apiFakeAdapter{kind: "rss", block: make(chan struct{}), started: make(chan struct{}), record: ingest.RawRecord{
		ExternalID: "one", ObservedAt: time.Now().UTC(), Payload: map[string]any{},
	}}
	server.Ingest = ingest.NewLoop(store, server.Bus, map[string]ingest.SourceAdapter{"rss": adapter}, time.Second, 1)
	handler := server.Routes()

	first := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/poll", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		first <- rec.Code
	}()
	select {
	case <-adapter.started:
	case <-time.After(5 * time.Second):
		t.Fatal("first poll never started")
	}
	req := httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/poll", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second poll status = %d, body %s; want 409", rec.Code, rec.Body.String())
	}
	close(adapter.block)
	if code := <-first; code != http.StatusOK {
		t.Fatalf("first poll status = %d", code)
	}
}

func int32Ptr(value int32) *int32 { return &value }

func TestPollSourceHandsPendingBackfillToLoop(t *testing.T) {
	server, store := newIntegrationServer(t)
	ctx := context.Background()
	source, err := store.CreateSource(ctx, "Pending", "rss", json.RawMessage(`{}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	// An earlier backfill attempt failed, so the request is no longer due.
	time.Sleep(5 * time.Millisecond)
	if err := store.MarkPolled(ctx, source.ID, time.Now().UTC(), errors.New("boom")); err != nil {
		t.Fatalf("MarkPolled: %v", err)
	}
	adapter := &apiFakeAdapter{kind: "rss", block: make(chan struct{})} // must not be called
	server.Ingest = ingest.NewLoop(store, server.Bus, map[string]ingest.SourceAdapter{"rss": adapter}, time.Second, 1)

	if due, _ := store.DueSources(ctx); len(due) != 0 {
		t.Fatalf("precondition: failed backfill should wait for its interval, due = %v", due)
	}
	time.Sleep(5 * time.Millisecond)
	req := httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/poll", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"backfill":true`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	due, err := store.DueSources(ctx)
	if err != nil || len(due) != 1 || due[0].ID != source.ID {
		t.Fatalf("the requeued backfill must be due on the next tick: due = %v, err = %v", due, err)
	}
}

func TestUploadCSVWithoutBackfillRowsKeepsEveryRow(t *testing.T) {
	store := ingest.NewStore(testdb.Open(t))
	httpServer := httptest.NewServer((&Server{Store: store, BackfillDefault: 1}).Routes())
	t.Cleanup(httpServer.Close)
	client := connectTestClient(t, httpServer)
	uploaded, err := client.UploadCSVSource(context.Background(), &v1.UploadCSVSourceRequest{
		Name: "All rows", FileName: "rows.csv",
		CsvData: []byte("value\n1\n2\n3\n"),
	})
	if err != nil {
		t.Fatalf("UploadCSVSource: %v", err)
	}
	if uploaded.GetSource().GetRecordCount() != 3 {
		t.Fatalf("record count = %d, want every row", uploaded.GetSource().GetRecordCount())
	}
}

func TestPollSourceDoesNotRequeueBusyBackfill(t *testing.T) {
	server, store := newIntegrationServer(t)
	ctx := context.Background()
	source, err := store.CreateSource(ctx, "Running", "rss", json.RawMessage(`{}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	adapter := &apiFakeAdapter{kind: "rss", block: make(chan struct{}), started: make(chan struct{}), record: ingest.RawRecord{
		ExternalID: "one", ObservedAt: time.Now().UTC(), Payload: map[string]any{},
	}}
	loop := ingest.NewLoop(store, server.Bus, map[string]ingest.SourceAdapter{"rss": adapter}, time.Second, 1)
	server.Ingest = loop
	done := make(chan error, 1)
	go func() { _, err := loop.PollOnce(ctx, source.ID); done <- err }()
	<-adapter.started

	req := httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/poll", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	close(adapter.block)
	if err := <-done; err != nil {
		t.Fatalf("backfill: %v", err)
	}
	got, err := store.Source(ctx, source.ID)
	if err != nil || got.BackfillPending {
		t.Fatalf("the running backfill must finish its own request (no duplicate): pending = %v, err = %v", got.BackfillPending, err)
	}
}

func TestRESTZeroBackfillRowsUsesDefault(t *testing.T) {
	server, _ := newIntegrationServer(t)
	server.BackfillDefault = 123
	req := httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader(
		`{"name":"Zero","kind":"rss","config":{"url":"https://example.com/feed.xml"},"backfill_rows":0}`))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"backfill_rows":123`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestBackfillRowsAboveIntegerRangeIsBadRequest(t *testing.T) {
	server, _ := newIntegrationServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader(
		`{"name":"Huge","kind":"rss","config":{"url":"https://example.com/feed.xml"},"backfill_rows":3000000000}`))
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at most") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}
