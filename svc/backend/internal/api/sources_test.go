package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"hacktheburgh/backend/internal/events"
	"hacktheburgh/backend/internal/ingest"
	"hacktheburgh/backend/internal/testdb"
)

func TestCreateSourceValidation(t *testing.T) {
	t.Parallel()

	server := &Server{Store: &ingest.Store{}}
	handler := server.Routes()

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantError  string
	}{
		{name: "invalid json", body: `{`, wantStatus: http.StatusBadRequest, wantError: "invalid JSON"},
		{name: "missing name", body: `{"kind":"rss","config":{"url":"https://example.com/feed"}}`, wantStatus: http.StatusBadRequest, wantError: "name is required"},
		{name: "missing config", body: `{"name":"X","kind":"rss"}`, wantStatus: http.StatusBadRequest, wantError: "config must be valid JSON"},
		{name: "unknown kind", body: `{"name":"X","kind":"bogus","config":{}}`, wantStatus: http.StatusBadRequest, wantError: "kind must be"},
		{name: "bad rss config", body: `{"name":"X","kind":"rss","config":{"url":"nope"}}`, wantStatus: http.StatusBadRequest, wantError: "rss config.url"},
		{name: "bad csv config", body: `{"name":"X","kind":"csv","config":{"url":"nope"}}`, wantStatus: http.StatusBadRequest, wantError: "csv config.url"},
		{name: "bad web config", body: `{"name":"X","kind":"web","config":{"url":"https://example.com"}}`, wantStatus: http.StatusBadRequest, wantError: "schema is required"},
		{name: "interval zero", body: `{"name":"X","kind":"rss","config":{"url":"https://example.com/feed"},"poll_interval_s":0}`, wantStatus: http.StatusBadRequest, wantError: "poll_interval_s"},
		{name: "interval too high", body: `{"name":"X","kind":"rss","config":{"url":"https://example.com/feed"},"poll_interval_s":86401}`, wantStatus: http.StatusBadRequest, wantError: "poll_interval_s"},
		{name: "backfill negative", body: `{"name":"X","kind":"rss","config":{"url":"https://example.com/feed"},"backfill_rows":-5}`, wantStatus: http.StatusBadRequest, wantError: "backfill_rows"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantError) {
				t.Fatalf("body = %s, want substring %q", rec.Body.String(), tt.wantError)
			}
		})
	}
}

func TestSourceHandlersRequireStore(t *testing.T) {
	t.Parallel()

	handler := (&Server{}).Routes()
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "create", method: http.MethodPost, path: "/api/sources"},
		{name: "list", method: http.MethodGet, path: "/api/sources"},
		{name: "get", method: http.MethodGet, path: "/api/sources/00000000-0000-0000-0000-000000000000"},
		{name: "poll", method: http.MethodPost, path: "/api/sources/00000000-0000-0000-0000-000000000000/poll"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
		})
	}
}

func newIntegrationServer(t *testing.T) (*Server, *ingest.Store) {
	t.Helper()
	store := ingest.NewStore(testdb.Open(t))
	return &Server{Bus: events.NewBus(), Store: store}, store
}

func TestCreateAndListSourcesIntegration(t *testing.T) {
	server, _ := newIntegrationServer(t)
	handler := server.Routes()

	body := `{"name":"Example feed","kind":"rss","config":{"url":"https://example.com/feed.xml"},"poll_interval_s":300}`
	req := httptest.NewRequest(http.MethodPost, "/api/sources", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", rec.Code, rec.Body.String())
	}

	var created ingest.Source
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID == "" || created.Name != "Example feed" || created.Kind != "rss" {
		t.Fatalf("created = %#v", created)
	}
	if created.PollIntervalS == nil || *created.PollIntervalS != 300 {
		t.Fatalf("PollIntervalS = %#v", created.PollIntervalS)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sources", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	var listed struct {
		Sources []ingest.Source `json:"sources"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Sources) != 1 || listed.Sources[0].ID != created.ID {
		t.Fatalf("listed = %#v", listed.Sources)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/sources/"+created.ID, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

func TestListSourcesEmptyEncodesArray(t *testing.T) {
	server, _ := newIntegrationServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sources", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"sources":[]`) {
		t.Fatalf("body = %s, want an empty array", body)
	}
}

func TestGetSourceNotFound(t *testing.T) {
	server, _ := newIntegrationServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/sources/00000000-0000-0000-0000-000000000000", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

type apiFakeAdapter struct {
	kind   string
	record ingest.RawRecord
	block  chan struct{} // when set, Fetch waits until it is closed
	// started, when set, is closed once Fetch begins.
	started chan struct{}
	once    sync.Once
}

func (a *apiFakeAdapter) Kind() string { return a.kind }

func (a *apiFakeAdapter) Fetch(ctx context.Context, _ json.RawMessage, _ ingest.FetchOptions) (ingest.FetchResult, error) {
	if a.started != nil {
		a.once.Do(func() { close(a.started) })
	}
	if a.block != nil {
		select {
		case <-a.block:
		case <-ctx.Done():
			return ingest.FetchResult{}, ctx.Err()
		}
	}
	return ingest.FetchResult{Records: []ingest.RawRecord{a.record}}, nil
}

func TestPollSourceIntegration(t *testing.T) {
	server, store := newIntegrationServer(t)
	ctx := context.Background()

	source, err := store.CreateSource(ctx, "Poll me", "rss", json.RawMessage(`{}`), intPtr(300))
	if err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	finishBackfill(t, store, source) // a pending backfill is handed to the loop instead
	adapter := &apiFakeAdapter{
		kind: "rss",
		record: ingest.RawRecord{
			ExternalID: "one",
			ObservedAt: time.Now().UTC(),
			Payload:    map[string]any{"value": 1},
		},
	}
	server.Ingest = ingest.NewLoop(store, server.Bus, map[string]ingest.SourceAdapter{"rss": adapter}, time.Second, 1)

	req := httptest.NewRequest(http.MethodPost, "/api/sources/"+source.ID+"/poll", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Inserted int `json:"inserted"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&response); err != nil {
		t.Fatalf("decode poll: %v", err)
	}
	if response.Inserted != 1 {
		t.Fatalf("inserted = %d, want 1", response.Inserted)
	}
}

func intPtr(value int) *int { return &value }

// finishBackfill clears a new source's initial backfill so a manual poll runs
// a normal poll inline.
func finishBackfill(t *testing.T, store *ingest.Store, source ingest.Source) {
	t.Helper()
	if source.BackfillRequestedAt == nil {
		return
	}
	if err := store.FinishBackfill(context.Background(), source.ID, *source.BackfillRequestedAt, nil, nil); err != nil {
		t.Fatalf("FinishBackfill: %v", err)
	}
}
