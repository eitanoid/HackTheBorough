package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// scriptedPages is a PageFetcher backed by a map of URL → page.
type scriptedPages struct {
	pages   map[string]Page
	errs    map[string]error
	fetched []string
}

func (s *scriptedPages) FetchPage(_ context.Context, pageURL string, _ int) (Page, error) {
	s.fetched = append(s.fetched, pageURL)
	if err := s.errs[pageURL]; err != nil {
		return Page{}, err
	}
	page, ok := s.pages[pageURL]
	if !ok {
		return Page{}, fmt.Errorf("no page %s", pageURL)
	}
	page.URL = pageURL
	return page, nil
}

func recs(ids ...string) []RawRecord {
	out := make([]RawRecord, 0, len(ids))
	for _, id := range ids {
		out = append(out, RawRecord{ExternalID: id, Payload: map[string]any{"id": id}})
	}
	return out
}

func ids(records []RawRecord) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, record.ExternalID)
	}
	return out
}

func declared(url, method string) NextCandidate { return NextCandidate{URL: url, Method: method} }
func guess(url, method, key string) NextCandidate {
	return NextCandidate{URL: url, Method: method, Probe: true, Key: key}
}

func TestCollectPagesFollowsDeclaredLinksUntilLimit(t *testing.T) {
	fetcher := &scriptedPages{pages: map[string]Page{
		"p1": {Records: recs("a", "b"), Candidates: []NextCandidate{declared("p2", MethodRelNext)}},
		"p2": {Records: recs("c", "d"), Candidates: []NextCandidate{declared("p3", MethodRelNext)}},
		"p3": {Records: recs("e", "f"), Candidates: []NextCandidate{declared("p4", MethodRelNext)}},
	}}
	records, report, err := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 5})
	if err != nil {
		t.Fatalf("CollectPages: %v", err)
	}
	if got := ids(records); !reflect.DeepEqual(got, []string{"a", "b", "c", "d", "e", "f"}) {
		t.Fatalf("records = %v", got)
	}
	if report.Stopped != StopLimit || report.Pages != 3 || report.Methods[MethodRelNext] != 2 {
		t.Fatalf("report = %+v", report)
	}
	if !reflect.DeepEqual(fetcher.fetched, []string{"p1", "p2", "p3"}) {
		t.Fatalf("fetched = %v (p4 must not be fetched once the limit is reached)", fetcher.fetched)
	}
}

func TestCollectPagesStopReasons(t *testing.T) {
	t.Run("no next", func(t *testing.T) {
		fetcher := &scriptedPages{pages: map[string]Page{"p1": {Records: recs("a")}}}
		_, report, err := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10})
		if err != nil || report.Stopped != StopNoNext {
			t.Fatalf("report = %+v, err = %v", report, err)
		}
	})
	t.Run("max pages", func(t *testing.T) {
		fetcher := &scriptedPages{pages: map[string]Page{
			"p1": {Records: recs("a"), Candidates: []NextCandidate{declared("p2", MethodAnchor)}},
			"p2": {Records: recs("b"), Candidates: []NextCandidate{declared("p3", MethodAnchor)}},
		}}
		records, report, _ := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10, MaxPages: 2})
		if report.Stopped != StopMaxPages || len(records) != 2 {
			t.Fatalf("report = %+v, records = %v", report, ids(records))
		}
	})
	t.Run("declared page with nothing new", func(t *testing.T) {
		fetcher := &scriptedPages{pages: map[string]Page{
			"p1": {Records: recs("a", "b"), Candidates: []NextCandidate{declared("p2", MethodRelNext)}},
			"p2": {Records: recs("a", "b"), Candidates: []NextCandidate{declared("p3", MethodRelNext)}},
		}}
		_, report, _ := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10})
		if report.Stopped != StopNoNewRecords {
			t.Fatalf("report = %+v", report)
		}
	})
	t.Run("visited urls are not refetched", func(t *testing.T) {
		fetcher := &scriptedPages{pages: map[string]Page{
			"p1": {Records: recs("a"), Candidates: []NextCandidate{declared("p2", MethodRelNext)}},
			"p2": {Records: recs("b"), Candidates: []NextCandidate{declared("p1", MethodRelNext)}},
		}}
		_, report, _ := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10})
		if report.Stopped != StopNoNext || len(fetcher.fetched) != 2 {
			t.Fatalf("report = %+v, fetched = %v", report, fetcher.fetched)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		fetcher := &scriptedPages{pages: map[string]Page{
			"p1": {Records: recs("a"), Candidates: []NextCandidate{declared("p2", MethodRelNext)}},
		}}
		cancel()
		_, _, err := CollectPages(ctx, fetcher, "p1", PagingLimits{Limit: 10})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	})
}

func TestCollectPagesErrors(t *testing.T) {
	t.Run("first page error is returned", func(t *testing.T) {
		fetcher := &scriptedPages{errs: map[string]error{"p1": errors.New("boom")}}
		if _, _, err := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10}); err == nil {
			t.Fatal("expected first-page error")
		}
	})
	t.Run("later page error keeps collected rows", func(t *testing.T) {
		fetcher := &scriptedPages{
			pages: map[string]Page{"p1": {Records: recs("a"), Candidates: []NextCandidate{declared("p2", MethodRelNext)}}},
			errs:  map[string]error{"p2": errors.New("boom")},
		}
		records, report, err := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10})
		if err != nil || len(records) != 1 || !strings.HasPrefix(report.Stopped, "error: ") {
			t.Fatalf("records = %v, report = %+v, err = %v", ids(records), report, err)
		}
	})
}

func TestCollectPagesProbes(t *testing.T) {
	t.Run("rejected probe falls through to the next candidate", func(t *testing.T) {
		fetcher := &scriptedPages{pages: map[string]Page{
			"p1": {Records: recs("a", "b"), Candidates: []NextCandidate{
				guess("p1?page=2", MethodPageNumber, "page:page"),
				guess("p1?after=b", MethodCursor, "cursor:after"),
			}},
			"p1?page=2":  {Records: recs("a", "b")}, // site ignores ?page
			"p1?after=b": {Records: recs("c", "d")},
		}}
		records, report, _ := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10, MaxProbes: 4})
		if got := ids(records); !reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
			t.Fatalf("records = %v", got)
		}
		if report.Methods[MethodCursor] != 1 || report.RejectedProbes != 1 {
			t.Fatalf("report = %+v", report)
		}
	})
	t.Run("probe with only a little churn is rejected", func(t *testing.T) {
		fetcher := &scriptedPages{pages: map[string]Page{
			"p1":        {Records: recs("a", "b", "c", "d"), Candidates: []NextCandidate{guess("p1?page=2", MethodPageNumber, "page:page")}},
			"p1?page=2": {Records: recs("a", "b", "c", "x")}, // 1 of 4 new: ranking churn, not a page
		}}
		records, report, _ := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10, MaxProbes: 4})
		if len(records) != 4 || report.RejectedProbes != 1 || report.Stopped != StopNoNext {
			t.Fatalf("records = %v, report = %+v", ids(records), report)
		}
	})
	t.Run("rejected probe kind is not retried", func(t *testing.T) {
		fetcher := &scriptedPages{pages: map[string]Page{
			"p1": {Records: recs("a"), Candidates: []NextCandidate{
				declared("p2", MethodRelNext),
				guess("p1?page=2", MethodPageNumber, "page:page"),
			}},
			"p1?page=2": {Records: recs("a")},
			"p2":        {Records: recs("b"), Candidates: []NextCandidate{guess("p2?page=2", MethodPageNumber, "page:page")}},
		}}
		// rel-next wins on p1, so the probe on p1 is never tried; on p2 the
		// probe is tried once.
		_, _, _ = CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10, MaxProbes: 4})
		for _, fetched := range fetcher.fetched {
			if fetched == "p1?page=2" {
				t.Fatalf("p1 probe should not be tried when a declared link works: %v", fetcher.fetched)
			}
		}
	})
	t.Run("accepted probe method becomes preferred", func(t *testing.T) {
		fetcher := &scriptedPages{pages: map[string]Page{
			"p1": {Records: recs("a"), Candidates: []NextCandidate{guess("c2", MethodCursor, "cursor:after")}},
			"c2": {Records: recs("b"), Candidates: []NextCandidate{
				guess("x", MethodPageNumber, "page:page"),
				guess("c3", MethodCursor, "cursor:after"),
			}},
			"c3": {Records: recs("c")},
			"x":  {Records: recs("z")},
		}}
		records, _, _ := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10, MaxProbes: 4})
		if got := ids(records); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
			t.Fatalf("records = %v, fetched = %v", got, fetcher.fetched)
		}
	})
	t.Run("probe budget does not block a declared fallback", func(t *testing.T) {
		// Mirrors an infinite-scroll site: guesses all fail, scroll works.
		fetcher := &scriptedPages{pages: map[string]Page{
			"p1": {Records: recs("a", "b"), Candidates: []NextCandidate{
				guess("anchor", MethodAnchor, "anchor"),
				guess("p1?page=2", MethodPageNumber, "page:page"),
				guess("llmnext:p1", MethodLLM, "llm"),
				declared("scroll:p1", MethodScroll),
			}},
			"anchor":    {Records: recs("a", "b")},
			"p1?page=2": {Records: recs("a", "b")},
			"scroll:p1": {Records: recs("a", "b", "c", "d", "e")},
		}, errs: map[string]error{"llmnext:p1": errors.New("LLM found no next page link")}}
		records, report, err := CollectPages(context.Background(), fetcher, "p1", PagingLimits{Limit: 10, MaxProbes: 2})
		if err != nil {
			t.Fatalf("CollectPages: %v", err)
		}
		if len(records) != 5 || report.Methods[MethodScroll] != 1 {
			t.Fatalf("records = %v, report = %+v, fetched = %v", ids(records), report, fetcher.fetched)
		}
	})
}

func TestLinkHeaderNext(t *testing.T) {
	header := http.Header{}
	header.Add("Link", `<https://api.example.com/items?page=1>; rel="prev", <https://api.example.com/items?page=3>; rel="next"`)
	if got := linkHeaderNext(header, "https://api.example.com/items?page=2"); got != "https://api.example.com/items?page=3" {
		t.Fatalf("got %q", got)
	}
	relative := http.Header{}
	relative.Add("Link", `</feed?page=2>; rel=next`)
	if got := linkHeaderNext(relative, "https://example.com/feed"); got != "https://example.com/feed?page=2" {
		t.Fatalf("relative got %q", got)
	}
	if got := linkHeaderNext(http.Header{}, "https://example.com"); got != "" {
		t.Fatalf("empty got %q", got)
	}
}

func TestRelNextAndAnchorNext(t *testing.T) {
	links := []pageLink{
		{Href: "https://e.com/prev", Text: "‹ prev", Rel: "prev"},
		{Href: "https://e.com/article", Text: "Read more"},
		{Href: "https://e.com/2", Text: "next ›", Rel: "nofollow next"},
	}
	if got := relNext(links); got != "https://e.com/2" {
		t.Fatalf("relNext = %q", got)
	}
	cases := []struct {
		link pageLink
		want bool
	}{
		{pageLink{Text: "Next"}, true},
		{pageLink{Text: "Older posts »"}, true},
		{pageLink{Text: "»"}, true},
		{pageLink{Text: "Load more"}, true},
		{pageLink{Aria: "Next page"}, true},
		{pageLink{Class: "pagination next"}, true},
		{pageLink{Text: "Read more"}, false},
		{pageLink{Text: "« Previous"}, false},
		{pageLink{Text: "Newer posts"}, false},
		{pageLink{Text: "Last"}, false},
		{pageLink{Text: "About"}, false},
	}
	for _, tc := range cases {
		if got := looksLikeNextLink(tc.link); got != tc.want {
			t.Errorf("looksLikeNextLink(%+v) = %v, want %v", tc.link, got, tc.want)
		}
	}
}

func TestPageNumberAndOffsetNext(t *testing.T) {
	cases := map[string]string{
		"https://e.com/list?page=3":         "https://e.com/list?page=4",
		"https://e.com/feed?paged=1":        "https://e.com/feed?paged=2",
		"https://e.com/blog/page/2/":        "https://e.com/blog/page/3/",
		"https://e.com/news/p/9":            "https://e.com/news/p/10",
		"https://e.com/list?q=x&pg=5&s=new": "https://e.com/list?pg=6&q=x&s=new",
	}
	for in, want := range cases {
		got, ok := pageNumberNext(in)
		if !ok || got != want {
			t.Errorf("pageNumberNext(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := pageNumberNext("https://e.com/list"); ok {
		t.Error("URL without a page number must not increment")
	}
	if got, ok := offsetNext("https://e.com/api?offset=20&limit=20", 20); !ok || got != "https://e.com/api?limit=20&offset=40" {
		t.Errorf("offsetNext = %q, %v", got, ok)
	}
	if probes := pageProbes("https://e.com/feed", "page", "paged"); len(probes) != 2 || probes[1].URL != "https://e.com/feed?paged=2" {
		t.Errorf("pageProbes = %+v", probes)
	}
	if probes := pageProbes("https://e.com/feed?page=3", "page"); probes != nil {
		t.Errorf("pageProbes with an existing page number = %+v", probes)
	}
}

func TestCursorProbes(t *testing.T) {
	last := RawRecord{ExternalID: "t3_abc123", ObservedAt: time.Unix(1700000000, 0).UTC(), Payload: map[string]any{}}
	probes := cursorProbes("https://www.example.com/r/news/.rss?limit=100", last)
	var urls []string
	for _, probe := range probes {
		urls = append(urls, probe.URL)
	}
	want := []string{
		"https://www.example.com/r/news/.rss?after=t3_abc123&limit=100",
		"https://www.example.com/r/news/.rss?cursor=t3_abc123&limit=100",
		"https://www.example.com/r/news/.rss?limit=100&until=1700000000",
	}
	if !reflect.DeepEqual(urls, want) {
		t.Fatalf("cursorProbes = %v", urls)
	}
	hashed := RawRecord{ExternalID: strings.Repeat("ab", 32), Payload: map[string]any{"_time_estimated": true}}
	if probes := cursorProbes("https://e.com/feed", hashed); len(probes) != 0 {
		t.Fatalf("hash ids and estimated times must not produce probes: %+v", probes)
	}
	numeric := RawRecord{ExternalID: "12345", Payload: map[string]any{"_time_estimated": true}}
	if probes := cursorProbes("https://e.com/feed", numeric); len(probes) != 3 || probes[2].Key != "cursor:max_id" {
		t.Fatalf("numeric probes = %+v", probes)
	}
}

func TestSortNewestFirstAndCapRecords(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	records := []RawRecord{
		{ExternalID: "old", ObservedAt: base},
		{ExternalID: "new", ObservedAt: base.Add(2 * time.Hour)},
		{ExternalID: "tie-a", ObservedAt: base.Add(time.Hour)},
		{ExternalID: "tie-b", ObservedAt: base.Add(time.Hour)},
		{ExternalID: "new", ObservedAt: base.Add(2 * time.Hour)},
	}
	SortNewestFirst(records)
	if got := ids(records); !reflect.DeepEqual(got, []string{"new", "new", "tie-a", "tie-b", "old"}) {
		t.Fatalf("sorted = %v", got)
	}
	if got := ids(CapRecords(records, 3)); !reflect.DeepEqual(got, []string{"new", "tie-a", "tie-b"}) {
		t.Fatalf("capped = %v", got)
	}
	if got := len(CapRecords(records, 0)); got != 4 {
		t.Fatalf("uncapped dedup length = %d", got)
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              5 * time.Second,
		"3":                             3 * time.Second,
		"600":                           maxRetryWait,
		"Mon, 01 Jan 2024 00:00:10 GMT": 10 * time.Second,
		"garbage":                       5 * time.Second,
	}
	for value, want := range cases {
		if got := retryAfter(value, now); got != want {
			t.Errorf("retryAfter(%q) = %s, want %s", value, got, want)
		}
	}
}

func TestRSSFetchRetriesRateLimit(t *testing.T) {
	t.Parallel()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(atomFeed("", itemIDs("item", 1, 3)...)))
	}))
	defer server.Close()
	raw, _ := json.Marshal(map[string]string{"url": server.URL + "/feed"})
	result, err := (&RSSAdapter{}).Fetch(context.Background(), raw, FetchOptions{})
	if err != nil || len(result.Records) != 3 || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("records = %d, calls = %d, err = %v", len(result.Records), calls, err)
	}
}

func TestTrimKnownTail(t *testing.T) {
	cases := []struct {
		name  string
		in    []string
		known map[string]bool
		want  []string
	}{
		{"nothing stored keeps all", []string{"n1", "n2"}, map[string]bool{}, []string{"n1", "n2"}},
		{"drops rows below the oldest known", []string{"new", "k1", "left-out", "k2", "older"}, map[string]bool{"k1": true, "k2": true}, []string{"new", "k1", "left-out", "k2"}},
		{"all known", []string{"k1", "k2"}, map[string]bool{"k1": true, "k2": true}, []string{"k1", "k2"}},
		{"only new above known", []string{"new", "k1", "old1", "old2"}, map[string]bool{"k1": true}, []string{"new", "k1"}},
	}
	for _, tc := range cases {
		if got := ids(TrimKnownTail(recs(tc.in...), tc.known)); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveBackfillRows(t *testing.T) {
	if got, _ := ResolveBackfillRows(0, 0); got != DefaultBackfillRows {
		t.Errorf("default = %d", got)
	}
	if got, _ := ResolveBackfillRows(0, 900); got != 900 {
		t.Errorf("server default = %d", got)
	}
	if got, _ := ResolveBackfillRows(42, 900); got != 42 {
		t.Errorf("explicit = %d", got)
	}
	if _, err := ResolveBackfillRows(-1, 900); err == nil {
		t.Error("negative must be rejected")
	}
}
