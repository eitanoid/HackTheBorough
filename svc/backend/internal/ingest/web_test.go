package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func webSchema() json.RawMessage {
	return json.RawMessage(`{
		"type": "array",
		"items": {
			"type": "object",
			"properties": {
				"url": {"type": "string"},
				"title": {"type": "string"},
				"published_at": {"type": "string"}
			},
			"required": ["url", "title"]
		}
	}`)
}

func webConfig(t *testing.T, raw string) WebConfig {
	t.Helper()
	cfg, err := ParseWebConfig(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("ParseWebConfig: %v", err)
	}
	return cfg
}

func TestParseWebConfig(t *testing.T) {
	t.Parallel()

	valid := `{"url":"https://example.com","schema":{"type":"array","items":{"type":"object"}}}`
	cfg, err := ParseWebConfig(json.RawMessage(valid))
	if err != nil {
		t.Fatalf("ParseWebConfig: %v", err)
	}
	if cfg.Query != "Extract all relevant records from this page." {
		t.Fatalf("default query = %q", cfg.Query)
	}

	tests := []struct {
		name    string
		raw     string
		wantErr bool
		check   func(*testing.T, WebConfig)
	}{
		{
			name: "pagination defaults",
			raw:  `{"url":"https://example.com","schema":{"type":"array"},"pagination":{}}`,
			check: func(t *testing.T, cfg WebConfig) {
				if cfg.Pagination == nil || cfg.Pagination.Strategy != "next" || cfg.Pagination.MaxPages != 5 {
					t.Fatalf("pagination = %#v", cfg.Pagination)
				}
			},
		},
		{
			name: "scroll defaults",
			raw:  `{"url":"https://example.com","schema":{"type":"array"},"pagination":{"strategy":"scroll"}}`,
			check: func(t *testing.T, cfg WebConfig) {
				if cfg.Pagination.MaxScrolls != 10 || cfg.Pagination.ScrollDelay != 0.5 {
					t.Fatalf("pagination = %#v", cfg.Pagination)
				}
			},
		},
		{name: "page_links", raw: `{"url":"https://example.com","schema":{"type":"array"},"pagination":{"strategy":"page_links"}}`},
		{name: "invalid url", raw: `{"url":"example.com","schema":{"type":"array"}}`, wantErr: true},
		{name: "missing schema", raw: `{"url":"https://example.com"}`, wantErr: true},
		{name: "schema not object", raw: `{"url":"https://example.com","schema":[]}`, wantErr: true},
		{name: "invalid strategy", raw: `{"url":"https://example.com","schema":{"type":"array"},"pagination":{"strategy":"bogus"}}`, wantErr: true},
		{name: "max pages too high", raw: `{"url":"https://example.com","schema":{"type":"array"},"pagination":{"max_pages":51}}`, wantErr: true},
		{name: "max pages too low", raw: `{"url":"https://example.com","schema":{"type":"array"},"pagination":{"max_pages":-1}}`, wantErr: true},
		{name: "max scrolls too high", raw: `{"url":"https://example.com","schema":{"type":"array"},"pagination":{"strategy":"scroll","max_scrolls":101}}`, wantErr: true},
		{name: "delay too high", raw: `{"url":"https://example.com","schema":{"type":"array"},"pagination":{"strategy":"scroll","scroll_delay":6}}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseWebConfig(json.RawMessage(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestNormalizeExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantErr bool
		check   func(*testing.T, any)
	}{
		{
			name: "array",
			raw:  `[{"title":"A"}]`,
			check: func(t *testing.T, got any) {
				items := got.([]any)
				if len(items) != 1 || items[0].(map[string]any)["title"] != "A" {
					t.Fatalf("got %#v", got)
				}
			},
		},
		{
			name: "object",
			raw:  `{"title":"A"}`,
			check: func(t *testing.T, got any) {
				if got.(map[string]any)["title"] != "A" {
					t.Fatalf("got %#v", got)
				}
			},
		},
		{
			name: "content envelope",
			raw:  `[{"content":"{\"title\":\"A\"}","error":false,"index":0,"tags":[]}]`,
			check: func(t *testing.T, got any) {
				items := got.([]any)
				if items[0].(map[string]any)["title"] != "A" {
					t.Fatalf("got %#v", got)
				}
			},
		},
		{name: "empty array", raw: `[]`, wantErr: true},
		{name: "failed item", raw: `[{"error":true,"content":"boom"}]`, wantErr: true},
		{name: "bad content", raw: `[{"content":"nope"}]`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeExtraction(json.RawMessage(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
		})
	}
}

func TestRecordsFromData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		data        any
		recordsPath string
		wantLen     int
		wantErr     bool
	}{
		{name: "array", data: []any{map[string]any{"a": 1}, map[string]any{"a": 2}}, wantLen: 2},
		{name: "object", data: map[string]any{"a": 1}, wantLen: 1},
		{name: "records path", data: map[string]any{"data": map[string]any{"items": []any{map[string]any{"a": 1}}}}, recordsPath: "data.items", wantLen: 1},
		{name: "missing path", data: map[string]any{"data": map[string]any{}}, recordsPath: "data.items", wantErr: true},
		{name: "scalar", data: "nope", wantErr: true},
		{name: "array with scalar", data: []any{"nope"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records, err := recordsFromData(tt.data, tt.recordsPath)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(records) != tt.wantLen {
				t.Fatalf("got %d records, want %d", len(records), tt.wantLen)
			}
		})
	}
}

func TestRawRecordFromMap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		record     map[string]any
		cfg        WebConfig
		wantID     string
		wantTime   time.Time
		wantErr    bool
		wantStamps bool
	}{
		{
			name:     "explicit fields",
			record:   map[string]any{"url": "https://example.com/a", "published_at": "2024-01-02T03:04:05Z"},
			cfg:      WebConfig{IDField: "url", TimeField: "published_at"},
			wantID:   "https://example.com/a",
			wantTime: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		{
			name:     "fallback url",
			record:   map[string]any{"url": "https://example.com/a"},
			cfg:      WebConfig{},
			wantID:   "https://example.com/a",
			wantTime: time.Now().UTC(),
		},
		{
			name:     "nested id",
			record:   map[string]any{"author": map[string]any{"id": "a1"}},
			cfg:      WebConfig{IDField: "author.id"},
			wantID:   "a1",
			wantTime: time.Now().UTC(),
		},
		{
			name:    "missing explicit id",
			record:  map[string]any{"title": "A"},
			cfg:     WebConfig{IDField: "url"},
			wantErr: true,
		},
		{
			name:     "invalid time is estimated",
			record:   map[string]any{"url": "https://example.com/a", "published_at": "not a date"},
			cfg:      WebConfig{IDField: "url", TimeField: "published_at"},
			wantID:   "https://example.com/a",
			wantTime: time.Now().UTC(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := rawRecordFromMap(tt.record, tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ExternalID != tt.wantID {
				t.Fatalf("ExternalID = %q, want %q", got.ExternalID, tt.wantID)
			}
			if tt.wantID != "" && tt.wantTime.IsZero() == false {
				if diff := got.ObservedAt.Sub(tt.wantTime); diff < -2*time.Second || diff > 2*time.Second {
					t.Fatalf("ObservedAt = %s, want around %s", got.ObservedAt, tt.wantTime)
				}
			}
		})
	}
}

func TestParseHTMLLinks(t *testing.T) {
	t.Parallel()

	html := `<div>
		<a href="/next" rel="next" class="pagination next" aria-label="Next page">Next</a>
		<a href="https://other.example/x">Other</a>
		<a href="javascript:void(0)">JS</a>
	</div>`
	links := parseHTMLLinks(html, "https://example.com/", "https://example.com")
	if len(links) != 1 {
		t.Fatalf("got %d links: %#v", len(links), links)
	}
	if links[0].Href != "https://example.com/next" {
		t.Fatalf("Href = %q", links[0].Href)
	}
	if links[0].Text != "Next" || links[0].Rel != "next" || !strings.Contains(links[0].Class, "next") {
		t.Fatalf("link = %#v", links[0])
	}
}

func TestResolvePageURL(t *testing.T) {
	t.Parallel()

	root, _ := url.Parse("https://example.com")
	tests := []struct {
		name    string
		pageURL string
		href    string
		want    string
		wantErr bool
	}{
		{name: "relative", pageURL: "https://example.com/a", href: "/b", want: "https://example.com/b"},
		{name: "absolute same host", pageURL: "https://example.com/a", href: "https://example.com/b", want: "https://example.com/b"},
		{name: "fragment removed", pageURL: "https://example.com/a", href: "/b#frag", want: "https://example.com/b"},
		{name: "different host", pageURL: "https://example.com/a", href: "https://other.com/b", wantErr: true},
		{name: "non http", pageURL: "https://example.com/a", href: "mailto:x@example.com", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolvePageURL(tt.pageURL, tt.href, root)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindNextPage(t *testing.T) {
	t.Parallel()

	links := []pageLink{
		{Href: "https://example.com/1", Text: "1"},
		{Href: "https://example.com/2", Text: "Next"},
		{Href: "https://example.com/3", Rel: "next"},
	}
	if got := findNextPage(links, ""); got != "https://example.com/2" {
		t.Fatalf("got %q", got)
	}
	if got := findNextPage(links, "[rel=next]"); got != "https://example.com/3" {
		t.Fatalf("selector got %q", got)
	}
}

func TestFindPageLinks(t *testing.T) {
	t.Parallel()

	links := []pageLink{
		{Href: "https://example.com/page/2", Text: "2"},
		{Href: "https://example.com/news?page=3", Text: "3"},
		{Href: "https://example.com/about", Text: "About"},
	}
	got := findPageLinks(links, "")
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if got[0] != links[0].Href || got[1] != links[1].Href {
		t.Fatalf("got %v", got)
	}
}

func TestMatchesPageSelector(t *testing.T) {
	t.Parallel()

	link := pageLink{Href: "https://example.com/next", Text: "Next", Rel: "next", Class: "pagination next", Aria: "Next page"}
	tests := []struct {
		selector string
		want     bool
	}{
		{selector: "a", want: true},
		{selector: ".next", want: true},
		{selector: "[rel=next]", want: true},
		{selector: "[aria-label=Next page]", want: true},
		{selector: "#id", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.selector, func(t *testing.T) {
			if got := matchesPageSelector(link, tt.selector); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateExtracted(t *testing.T) {
	t.Parallel()

	schema := json.RawMessage(`{"type":"object","required":["title"],"properties":{"title":{"type":"string"}}}`)
	if err := validateExtracted(map[string]any{"title": "A"}, schema); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := validateExtracted(map[string]any{}, schema); err == nil {
		t.Fatal("expected error")
	}
	if err := validateExtracted([]any{map[string]any{"title": "A"}, map[string]any{"title": "B"}}, schema); err != nil {
		t.Fatalf("unexpected error for array of records: %v", err)
	}
	if err := validateExtracted([]any{map[string]any{}}, schema); err == nil {
		t.Fatal("expected error for array item missing required field")
	}
	if err := validateExtracted([]any{map[string]any{"title": 1}}, schema); err == nil {
		t.Fatal("expected error for array item with wrong field type")
	}
}

type fakeCrawl4AI struct {
	t             *testing.T
	results       map[string]any
	links         map[string]string
	defaultResult any
	failStatus    int
	pending       bool
}

func (f *fakeCrawl4AI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.t.Helper()
	if f.failStatus != 0 {
		http.Error(w, "nope", f.failStatus)
		return
	}
	switch {
	case r.URL.Path == "/llm/job" && r.Method == http.MethodPost:
		var req llmJobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			f.t.Errorf("decode llm request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"task_id": url.QueryEscape(req.URL)})
	case strings.HasPrefix(r.URL.Path, "/llm/job/"):
		if f.pending {
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
			return
		}
		taskID := strings.TrimPrefix(r.URL.Path, "/llm/job/")
		pageURL, _ := url.QueryUnescape(taskID)
		result, ok := f.results[pageURL]
		if !ok {
			result = f.defaultResult
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "result": result})
	case r.URL.Path == "/crawl" && r.Method == http.MethodPost:
		var req struct {
			URLs []string `json:"urls"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.URLs) == 0 {
			f.t.Errorf("decode crawl request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		pageURL := req.URLs[0]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"results": []any{map[string]any{
				"success": true,
				"url":     pageURL,
				"html":    f.links[pageURL],
			}},
		})
	default:
		http.NotFound(w, r)
	}
}

func TestWebAdapterFetchDirect(t *testing.T) {
	t.Parallel()

	pageURL := "https://example.com/"
	result := []any{map[string]any{
		"url":          pageURL + "a",
		"title":        "Article",
		"published_at": "2024-01-02T03:04:05Z",
	}}
	fake := &fakeCrawl4AI{
		t:       t,
		results: map[string]any{pageURL: result},
		links:   map[string]string{},
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	adapter := &WebAdapter{Crawl4AIURL: server.URL, Token: "test-token"}
	raw, _ := json.Marshal(map[string]any{
		"url":        pageURL,
		"schema":     webSchema(),
		"id_field":   "url",
		"time_field": "published_at",
	})
	records, err := adapter.Fetch(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records", len(records))
	}
	if records[0].ExternalID != pageURL+"a" {
		t.Fatalf("ExternalID = %q", records[0].ExternalID)
	}
	if !records[0].ObservedAt.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) {
		t.Fatalf("ObservedAt = %s", records[0].ObservedAt)
	}
}

func TestWebAdapterFetchNextPage(t *testing.T) {
	t.Parallel()

	root := "https://example.com/"
	page2 := "https://example.com/page2"
	page1Result := []any{map[string]any{"url": root + "a", "title": "A", "published_at": "2024-01-02T03:04:05Z"}}
	page2Result := []any{map[string]any{"url": page2 + "/b", "title": "B", "published_at": "2024-01-03T03:04:05Z"}}

	fake := &fakeCrawl4AI{
		t: t,
		results: map[string]any{
			root:  page1Result,
			page2: page2Result,
		},
		links: map[string]string{
			root:  `<a rel="next" href="/page2">Next</a>`,
			page2: ``,
		},
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	adapter := &WebAdapter{Crawl4AIURL: server.URL, Token: "test-token"}
	raw, _ := json.Marshal(map[string]any{
		"url":        root,
		"schema":     webSchema(),
		"id_field":   "url",
		"time_field": "published_at",
		"pagination": map[string]any{"strategy": "next", "max_pages": 2},
	})
	records, err := adapter.Fetch(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
}

func TestWebAdapterFetchPageLinks(t *testing.T) {
	t.Parallel()

	root := "https://example.com/"
	page2 := "https://example.com/page/2"
	result := []any{map[string]any{"url": root + "a", "title": "A"}}

	fake := &fakeCrawl4AI{
		t:             t,
		results:       map[string]any{root: result, page2: result},
		links:         map[string]string{root: `<a href="/page/2">2</a><a href="/about">About</a>`, page2: ""},
		defaultResult: result,
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	adapter := &WebAdapter{Crawl4AIURL: server.URL, Token: "test-token"}
	raw, _ := json.Marshal(map[string]any{
		"url":        root,
		"schema":     webSchema(),
		"id_field":   "url",
		"pagination": map[string]any{"strategy": "page_links", "max_pages": 2},
	})
	records, err := adapter.Fetch(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	// page 1 and page 2 return the same external ID, so dedupe keeps one.
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
}

func TestWebAdapterFetchScroll(t *testing.T) {
	t.Parallel()

	root := "https://example.com/"
	html := `<html>scrolled</html>`
	result := []any{map[string]any{"url": root + "a", "title": "A"}}

	fake := &fakeCrawl4AI{
		t:             t,
		results:       map[string]any{"raw:" + html: result},
		links:         map[string]string{root: html},
		defaultResult: result,
	}
	server := httptest.NewServer(fake)
	defer server.Close()

	adapter := &WebAdapter{Crawl4AIURL: server.URL, Token: "test-token"}
	raw, _ := json.Marshal(map[string]any{
		"url":        root,
		"schema":     webSchema(),
		"id_field":   "url",
		"pagination": map[string]any{"strategy": "scroll", "max_scrolls": 2},
	})
	records, err := adapter.Fetch(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records", len(records))
	}
}

func TestWebAdapterErrors(t *testing.T) {
	t.Parallel()

	root := "https://example.com/"
	raw, _ := json.Marshal(map[string]any{
		"url":    root,
		"schema": webSchema(),
	})

	t.Run("missing token", func(t *testing.T) {
		adapter := &WebAdapter{Crawl4AIURL: "http://127.0.0.1:1"}
		if _, err := adapter.Fetch(context.Background(), raw, nil); err == nil {
			t.Fatal("expected token error")
		}
	})

	t.Run("http error", func(t *testing.T) {
		server := httptest.NewServer(&fakeCrawl4AI{t: t, failStatus: http.StatusUnauthorized})
		defer server.Close()
		adapter := &WebAdapter{Crawl4AIURL: server.URL, Token: "test-token"}
		if _, err := adapter.Fetch(context.Background(), raw, nil); err == nil {
			t.Fatal("expected HTTP error")
		}
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(&fakeCrawl4AI{t: t, pending: true})
		defer server.Close()
		adapter := &WebAdapter{Crawl4AIURL: server.URL, Token: "test-token"}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if _, err := adapter.Fetch(ctx, raw, nil); err == nil {
			t.Fatal("expected timeout")
		}
	})

	t.Run("empty extraction", func(t *testing.T) {
		server := httptest.NewServer(&fakeCrawl4AI{t: t, defaultResult: []any{}})
		defer server.Close()
		adapter := &WebAdapter{Crawl4AIURL: server.URL, Token: "test-token"}
		if _, err := adapter.Fetch(context.Background(), raw, nil); err == nil {
			t.Fatal("expected empty extraction error")
		}
	})
}

func TestWebAdapterEndpoint(t *testing.T) {
	t.Parallel()

	adapter := &WebAdapter{Crawl4AIURL: "http://crawl4ai:11235/llm/"}
	if got := adapter.endpoint("/llm/job"); got != "http://crawl4ai:11235/llm/job" {
		t.Fatalf("endpoint = %q", got)
	}
}
