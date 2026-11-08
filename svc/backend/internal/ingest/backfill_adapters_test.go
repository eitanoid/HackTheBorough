package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- RSS ---------------------------------------------------------------------

// atomFeed renders an Atom feed with one entry per id (newest first) and an
// optional feed-level rel="next" link.
func atomFeed(next string, ids ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom">`)
	if next != "" {
		fmt.Fprintf(&b, `<link rel="next" href="%s"/>`, next)
	}
	for _, id := range ids {
		n, _ := strconv.Atoi(strings.TrimLeft(id, "t3_item"))
		updated := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(n) * time.Hour)
		fmt.Fprintf(&b, `<entry><id>%s</id><title>%s</title><updated>%s</updated></entry>`, id, id, updated.Format(time.RFC3339))
	}
	b.WriteString(`</feed>`)
	return b.String()
}

func itemIDs(prefix string, from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, fmt.Sprintf("%s%d", prefix, i))
	}
	return out
}

type requestLog struct {
	mu   sync.Mutex
	urls []string
}

func (l *requestLog) add(r *http.Request) {
	l.mu.Lock()
	l.urls = append(l.urls, r.URL.RequestURI())
	l.mu.Unlock()
}

func (l *requestLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.urls)
}

func backfillRSS(t *testing.T, feedURL string, limit int) FetchResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"url": feedURL})
	result, err := (&RSSAdapter{}).Fetch(context.Background(), raw, FetchOptions{Backfill: true, Limit: limit})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Report == nil {
		t.Fatal("backfill must return a paging report")
	}
	return result
}

func TestRSSBackfillFollowsRelNext(t *testing.T) {
	t.Parallel()
	var log requestLog
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		switch r.URL.Path {
		case "/feed":
			_, _ = w.Write([]byte(atomFeed(server.URL+"/feed/2", itemIDs("item", 1, 10)...)))
		case "/feed/2":
			_, _ = w.Write([]byte(atomFeed(server.URL+"/feed/3", itemIDs("item", 11, 20)...)))
		case "/feed/3":
			_, _ = w.Write([]byte(atomFeed("", itemIDs("item", 21, 30)...)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result := backfillRSS(t, server.URL+"/feed", 15)
	if len(result.Records) < 15 {
		t.Fatalf("got %d records, want >= 15", len(result.Records))
	}
	if result.Report.Methods[MethodRelNext] != 1 || result.Report.Stopped != StopLimit {
		t.Fatalf("report = %+v", result.Report)
	}
	if log.count() != 2 {
		t.Fatalf("requests = %v, want 2 (stop at the limit)", log.urls)
	}
}

func TestRSSBackfillFollowsLinkHeader(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte(atomFeed("", itemIDs("item", 11, 20)...)))
			return
		}
		w.Header().Set("Link", `</feed?page=2>; rel="next"`)
		_, _ = w.Write([]byte(atomFeed("", itemIDs("item", 1, 10)...)))
	}))
	defer server.Close()

	result := backfillRSS(t, server.URL+"/feed", 100)
	if len(result.Records) != 20 || result.Report.Methods[MethodLinkHeader] != 1 {
		t.Fatalf("records = %d, report = %+v", len(result.Records), result.Report)
	}
}

func TestRSSBackfillCursorProbe(t *testing.T) {
	t.Parallel()
	// A listing that pages with ?after=<last id> and ignores ?page / ?paged.
	all := itemIDs("t3_", 1, 30)
	var log requestLog
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		start := 0
		if after := r.URL.Query().Get("after"); after != "" {
			for i, id := range all {
				if id == after {
					start = i + 1
				}
			}
		}
		end := start + 10
		if end > len(all) {
			end = len(all)
		}
		_, _ = w.Write([]byte(atomFeed("", all[start:end]...)))
	}))
	defer server.Close()

	result := backfillRSS(t, server.URL+"/r/news/.rss", 25)
	if len(result.Records) < 25 {
		t.Fatalf("got %d records, want >= 25; requests = %v", len(result.Records), log.urls)
	}
	if result.Report.Methods[MethodCursor] < 2 || result.Report.RejectedProbes != 2 {
		t.Fatalf("report = %+v; requests = %v", result.Report, log.urls)
	}
}

func TestRSSBackfillWithoutPagingStopsAfterProbes(t *testing.T) {
	t.Parallel()
	var log requestLog
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		_, _ = w.Write([]byte(atomFeed("", itemIDs("item", 1, 10)...)))
	}))
	defer server.Close()

	result := backfillRSS(t, server.URL+"/feed", 100)
	if len(result.Records) != 10 || result.Report.Stopped != StopNoNext {
		t.Fatalf("records = %d, report = %+v", len(result.Records), result.Report)
	}
	if got := log.count(); got > 5 {
		t.Fatalf("requests = %d (%v), want at most 1 + MaxProbes", got, log.urls)
	}
}

func TestRSSNormalPollIsSingleRequest(t *testing.T) {
	t.Parallel()
	var log requestLog
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		_, _ = w.Write([]byte(atomFeed("/feed/2", itemIDs("item", 1, 10)...)))
	}))
	defer server.Close()

	raw, _ := json.Marshal(map[string]string{"url": server.URL + "/feed"})
	result, err := (&RSSAdapter{}).Fetch(context.Background(), raw, FetchOptions{})
	if err != nil || len(result.Records) != 10 || result.Report != nil || log.count() != 1 {
		t.Fatalf("records = %d, report = %+v, requests = %v, err = %v", len(result.Records), result.Report, log.urls, err)
	}
}

func TestParseFeedPageLinks(t *testing.T) {
	rss := `<?xml version="1.0"?><rss version="2.0" xmlns:atom="http://www.w3.org/2005/Atom"><channel>
		<title>t</title><link>https://example.com/</link>
		<atom:link rel="next" href="https://example.com/feed?paged=2"/>
		<item><guid>g1</guid><title>one</title></item></channel></rss>`
	records, links, err := ParseFeedPage([]byte(rss), time.Now())
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %v, err = %v", records, err)
	}
	if got := relNext(links); got != "https://example.com/feed?paged=2" {
		t.Fatalf("relNext = %q, links = %+v", got, links)
	}
}

// --- CSV ---------------------------------------------------------------------

func TestCSVBackfillNewestFirstAndLinkHeader(t *testing.T) {
	t.Parallel()
	var log requestLog
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		if r.URL.Query().Get("page") == "2" {
			_, _ = w.Write([]byte("name\nc\nd\n"))
			return
		}
		w.Header().Set("Link", `<?page=2>; rel="next"`)
		_, _ = w.Write([]byte("name\na\nb\n"))
	}))
	defer server.Close()

	raw, _ := json.Marshal(map[string]string{"url": server.URL + "/data.csv"})
	result, err := (&CSVAdapter{}).Fetch(context.Background(), raw, FetchOptions{Backfill: true, Limit: 10})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	var names []string
	for _, record := range result.Records {
		names = append(names, fmt.Sprint(record.Payload["name"]))
	}
	// Timeless rows are reversed per page so the bottom rows count as newest.
	if strings.Join(names, ",") != "b,a,d,c" {
		t.Fatalf("names = %v", names)
	}
	// page=2 carries a page number, so page=3 is tried and ends the walk
	// when it brings nothing new.
	if result.Report.Methods[MethodLinkHeader] != 1 || log.count() != 3 || result.Report.Stopped != StopNoNewRecords {
		t.Fatalf("report = %+v, requests = %v", result.Report, log.urls)
	}
}

func TestCSVBackfillNeverProbes(t *testing.T) {
	t.Parallel()
	var log requestLog
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		_, _ = w.Write([]byte("name\na\nb\n"))
	}))
	defer server.Close()

	raw, _ := json.Marshal(map[string]string{"url": server.URL + "/data.csv"})
	if _, err := (&CSVAdapter{}).Fetch(context.Background(), raw, FetchOptions{Backfill: true, Limit: 10}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if log.count() != 1 {
		t.Fatalf("requests = %v, want a single download", log.urls)
	}
}

// --- Web ---------------------------------------------------------------------

// backfillCrawl4AI is a fake Crawl4AI for backfill tests. Each page's HTML
// carries a <!--page:KEY--> marker; LLM jobs on raw HTML answer with
// results[KEY]. Scroll crawls (scan_full_page) are served scrollHTML.
type backfillCrawl4AI struct {
	t          *testing.T
	pages      map[string]string // page URL → HTML
	internal   map[string][]crawlLink
	results    map[string]any // marker → extraction answer
	nextURL    any            // answer to the LLM next-link question
	scrollHTML string

	mu        sync.Mutex
	crawls    []string
	scrolls   int
	questions []string
}

var pageMarker = regexp.MustCompile(`<!--page:([^>]+?)-->`)

func (f *backfillCrawl4AI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/crawl":
		var req struct {
			URLs          []string `json:"urls"`
			CrawlerConfig struct {
				Params map[string]any `json:"params"`
			} `json:"crawler_config"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		pageURL := req.URLs[0]
		html := f.pages[pageURL]
		f.mu.Lock()
		f.crawls = append(f.crawls, pageURL)
		if req.CrawlerConfig.Params["scan_full_page"] == true {
			f.scrolls++
			html = f.scrollHTML
		}
		f.mu.Unlock()
		result := map[string]any{"success": true, "url": pageURL, "html": html}
		if links := f.internal[pageURL]; links != nil {
			result["links"] = map[string]any{"internal": links}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "results": []any{result}})
	case r.URL.Path == "/llm/job":
		var req llmJobRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.questions = append(f.questions, req.Question)
		f.mu.Unlock()
		var answer any
		if strings.Contains(req.Question, "pagination controls") {
			answer = []any{map[string]any{"next_url": f.nextURL}}
		} else {
			key := ""
			if match := pageMarker.FindStringSubmatch(req.URL); match != nil {
				key = match[1]
			}
			answer = f.results[key]
		}
		encoded, _ := json.Marshal(answer)
		_ = json.NewEncoder(w).Encode(map[string]any{"result": json.RawMessage(encoded)})
	default:
		http.NotFound(w, r)
	}
}

func webItems(prefix string, from, to int) []any {
	var out []any
	for i := from; i <= to; i++ {
		out = append(out, map[string]any{
			"url":          fmt.Sprintf("https://posts.example/%s/%d", prefix, i),
			"title":        fmt.Sprintf("%s %d", prefix, i),
			"published_at": time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Hour).Format(time.RFC3339),
		})
	}
	return out
}

func runWebBackfill(t *testing.T, fake *backfillCrawl4AI, pageURL string, limit int, extra map[string]any) FetchResult {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	cfg := map[string]any{"url": pageURL, "schema": webSchema(), "id_field": "url"}
	for k, v := range extra {
		cfg[k] = v
	}
	raw, _ := json.Marshal(cfg)
	adapter := &WebAdapter{Crawl4AIURL: server.URL, Token: "test-token"}
	result, err := adapter.Fetch(context.Background(), raw, FetchOptions{Backfill: true, Limit: limit})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if result.Report == nil {
		t.Fatal("missing report")
	}
	return result
}

func TestWebBackfillFollowsHeadRelNextAndStopsAtLimit(t *testing.T) {
	t.Parallel()
	fake := &backfillCrawl4AI{t: t,
		pages: map[string]string{
			"https://site.example/news":        `<html><head><link rel="next" href="/news?page=2"></head><body><!--page:1--></body></html>`,
			"https://site.example/news?page=2": `<html><head><link rel="next" href="/news?page=3"></head><body><!--page:2--></body></html>`,
			"https://site.example/news?page=3": `<html><body><!--page:3--></body></html>`,
		},
		results: map[string]any{"1": webItems("a", 1, 10), "2": webItems("a", 11, 20), "3": webItems("a", 21, 30)},
	}
	result := runWebBackfill(t, fake, "https://site.example/news", 15, nil)
	if len(result.Records) != 20 || result.Report.Stopped != StopLimit || result.Report.Methods[MethodRelNext] != 1 {
		t.Fatalf("records = %d, report = %+v", len(result.Records), result.Report)
	}
	if len(fake.crawls) != 2 {
		t.Fatalf("crawls = %v, want exactly one per page and no third page", fake.crawls)
	}
	if !strings.Contains(fake.questions[0], "up to 15 records") || !strings.Contains(fake.questions[1], "up to 5 records") {
		t.Fatalf("questions must carry the remaining row hint: %q", fake.questions)
	}
}

func TestWebBackfillAnchorWithCrawlerLinks(t *testing.T) {
	t.Parallel()
	// Crawl4AI's own link list has no rel/class, so the HTML must be parsed too.
	fake := &backfillCrawl4AI{t: t,
		pages: map[string]string{
			"https://old.site.example/r/x/":                     `<!--page:1--><a href="/r/x/?count=25&after=t3_b" rel="nofollow next">next ›</a>`,
			"https://old.site.example/r/x/?count=25&after=t3_b": `<!--page:2-->`,
		},
		internal: map[string][]crawlLink{
			"https://old.site.example/r/x/": {{Href: "https://old.site.example/about", Text: "About"}},
		},
		results: map[string]any{"1": webItems("b", 1, 25), "2": webItems("b", 26, 50)},
	}
	result := runWebBackfill(t, fake, "https://old.site.example/r/x/", 40, nil)
	if len(result.Records) != 50 || result.Report.Methods[MethodRelNext] != 1 {
		t.Fatalf("records = %d, report = %+v", len(result.Records), result.Report)
	}
}

func TestWebBackfillFallsBackToScroll(t *testing.T) {
	t.Parallel()
	// An infinite-scroll page: no links, ?page=2 is ignored, the LLM finds no
	// next link, so the walk must still reach the scroll fallback.
	fake := &backfillCrawl4AI{t: t,
		pages: map[string]string{
			"https://feed.example/":        `<!--page:1-->`,
			"https://feed.example/?page=2": `<!--page:1-->`,
		},
		results:    map[string]any{"1": webItems("c", 1, 20), "scroll": webItems("c", 1, 80)},
		nextURL:    nil,
		scrollHTML: `<!--page:scroll-->`,
	}
	result := runWebBackfill(t, fake, "https://feed.example/", 60, nil)
	if fake.scrolls != 1 {
		t.Fatalf("scrolls = %d, crawls = %v, report = %+v", fake.scrolls, fake.crawls, result.Report)
	}
	if len(result.Records) != 80 || result.Report.Methods[MethodScroll] != 1 {
		t.Fatalf("records = %d, report = %+v", len(result.Records), result.Report)
	}
}

func TestWebBackfillUsesLLMNextLink(t *testing.T) {
	t.Parallel()
	fake := &backfillCrawl4AI{t: t,
		pages: map[string]string{
			"https://shop.example/list":         `<!--page:1--><button onclick="go()">→ further</button>`,
			"https://shop.example/list?page=2":  `<!--page:1-->`,
			"https://shop.example/list/older-1": `<!--page:2-->`,
		},
		results: map[string]any{"1": webItems("d", 1, 10), "2": webItems("d", 11, 20)},
		nextURL: "/list/older-1",
	}
	result := runWebBackfill(t, fake, "https://shop.example/list", 20, nil)
	if len(result.Records) != 20 || result.Report.Methods[MethodLLM] != 1 {
		t.Fatalf("records = %d, report = %+v, crawls = %v", len(result.Records), result.Report, fake.crawls)
	}
}

func TestWebBackfillExplicitScrollStrategy(t *testing.T) {
	t.Parallel()
	fake := &backfillCrawl4AI{t: t,
		pages:      map[string]string{},
		results:    map[string]any{"scroll": webItems("e", 1, 30)},
		scrollHTML: `<!--page:scroll-->`,
	}
	result := runWebBackfill(t, fake, "https://feed.example/", 25, map[string]any{
		"pagination": map[string]any{"strategy": "scroll"},
	})
	if fake.scrolls != 1 || len(fake.crawls) != 1 || len(result.Records) != 30 {
		t.Fatalf("scrolls = %d, crawls = %v, records = %d", fake.scrolls, fake.crawls, len(result.Records))
	}
}

func TestWebNormalPollUnchanged(t *testing.T) {
	t.Parallel()
	// Without a backfill the adapter does one URL-based LLM job and no crawl.
	fake := &backfillCrawl4AI{t: t, pages: map[string]string{}, results: map[string]any{"": webItems("f", 1, 3)}}
	server := httptest.NewServer(fake)
	defer server.Close()
	raw, _ := json.Marshal(map[string]any{"url": "https://site.example/", "schema": webSchema(), "id_field": "url"})
	result, err := (&WebAdapter{Crawl4AIURL: server.URL, Token: "t"}).Fetch(context.Background(), raw, FetchOptions{})
	if err != nil || len(result.Records) != 3 || result.Report != nil {
		t.Fatalf("records = %d, report = %+v, err = %v", len(result.Records), result.Report, err)
	}
	if len(fake.crawls) != 0 || strings.Contains(fake.questions[0], "up to") {
		t.Fatalf("crawls = %v, question = %q", fake.crawls, fake.questions[0])
	}
}
