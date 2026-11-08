package ingest

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Paging engine shared by every adapter. An adapter only knows how to fetch
// one page and which next-page candidates that page exposes; CollectPages
// decides which candidate to follow and when to stop. Nothing here is
// site-specific.

// Candidate methods, roughly in order of how trustworthy they are.
const (
	MethodLinkHeader = "link-header" // HTTP Link: <…>; rel="next" (RFC 8288)
	MethodRelNext    = "rel-next"    // <a>/<link>/Atom link with rel="next"
	MethodAnchor     = "anchor"      // a link labelled next/older/more/›/»
	MethodPageNumber = "page-number" // page number in the URL incremented, or a numbered page link
	MethodOffset     = "offset"      // offset/start/skip param advanced by the page size
	MethodCursor     = "cursor"      // after=<last id> style cursor
	MethodLLM        = "llm"         // next link located by the extraction LLM
	MethodScroll     = "scroll"      // infinite scroll of the first page
)

// Stop reasons recorded in PagingReport.Stopped.
const (
	StopLimit        = "limit"
	StopNoNext       = "no-next"
	StopNoNewRecords = "no-new-records"
	StopMaxPages     = "max-pages"
)

// NextCandidate is one way to reach the page after the current one.
type NextCandidate struct {
	URL    string
	Method string
	// Probe marks a guess the site did not declare (e.g. ?page=2). A probe is
	// only accepted if it yields records that were not seen before.
	Probe bool
	// Key identifies the probe kind (e.g. "cursor:after"). A probe kind that
	// fails once is not tried again during the same walk. Defaults to Method.
	Key string
	// Fallback marks a last resort (infinite scroll). It does not use the
	// probe budget, is accepted when it brings any new rows, and a failure is
	// a rejection rather than a backfill error.
	Fallback bool
}

func (c NextCandidate) probeKey() string {
	if c.Key != "" {
		return c.Key
	}
	return c.Method
}

// Page is one fetched page.
type Page struct {
	URL        string
	Records    []RawRecord
	Candidates []NextCandidate // best first
}

// PageFetcher fetches a single page. want is the number of records still
// needed (0 = no target).
type PageFetcher interface {
	FetchPage(ctx context.Context, pageURL string, want int) (Page, error)
}

type PagingLimits struct {
	Limit     int           // stop once this many unique records are collected (0 = no limit)
	MaxPages  int           // pages to accept, including the first (0 = no cap)
	MaxProbes int           // probe attempts allowed per page
	Delay     time.Duration // pause between page fetches
}

// PagingReport summarises one backfill walk. It is stored as
// data_sources.last_backfill and shown in the UI.
type PagingReport struct {
	Pages          int            `json:"pages"`
	Methods        map[string]int `json:"methods"`
	RejectedProbes int            `json:"rejected_probes"`
	Rows           int            `json:"rows"`
	Stopped        string         `json:"stopped"`
	// Error is set when a page after the first failed. The rows collected
	// before it are still returned, but the backfill is incomplete and should
	// be retried.
	Error      string    `json:"error,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

// CollectPages walks pages from start until the limit is reached or no usable
// next page remains. An error on the first page is returned; an error on a
// later page ends the walk and returns what was collected (with the error in
// the report). Context cancellation is always returned as an error.
func CollectPages(ctx context.Context, fetcher PageFetcher, start string, limits PagingLimits) ([]RawRecord, PagingReport, error) {
	report := PagingReport{Methods: map[string]int{}}
	finish := func(reason string) PagingReport {
		report.Stopped = reason
		report.FinishedAt = time.Now().UTC()
		return report
	}

	seen := map[string]bool{}
	var records []RawRecord
	add := func(page []RawRecord) int {
		added := 0
		for _, record := range page {
			if seen[record.ExternalID] {
				continue
			}
			seen[record.ExternalID] = true
			records = append(records, record)
			added++
		}
		return added
	}
	remaining := func() int {
		if limits.Limit <= 0 {
			return 0
		}
		return limits.Limit - len(records)
	}

	page, err := fetcher.FetchPage(ctx, start, limits.Limit)
	if err != nil {
		return nil, finish("error: " + err.Error()), err
	}
	visited := map[string]bool{start: true}
	report.Pages = 1
	add(page.Records)

	rejected := map[string]bool{} // probe methods that proved useless
	preferred := ""
	for {
		report.Rows = len(records)
		if limits.Limit > 0 && len(records) >= limits.Limit {
			return records, finish(StopLimit), nil
		}
		if limits.MaxPages > 0 && report.Pages >= limits.MaxPages {
			return records, finish(StopMaxPages), nil
		}
		if err := ctx.Err(); err != nil {
			return records, finish("cancelled"), err
		}

		candidates := orderCandidates(page.Candidates, preferred)
		advanced := false
		probes := 0
		lastErr := "" // last failure of a declared link on this page
		for _, candidate := range candidates {
			guessed := candidate.Probe || candidate.Fallback
			if candidate.URL == "" || visited[candidate.URL] || (guessed && rejected[candidate.probeKey()]) {
				continue
			}
			if candidate.Probe && !candidate.Fallback {
				if probes >= limits.MaxProbes {
					continue
				}
				probes++
			}
			if err := sleepCtx(ctx, limits.Delay); err != nil {
				return records, finish("cancelled"), err
			}
			visited[candidate.URL] = true
			next, err := fetcher.FetchPage(ctx, candidate.URL, remaining())
			if err != nil {
				if ctx.Err() != nil {
					return records, finish("cancelled"), ctx.Err()
				}
				if guessed {
					rejected[candidate.probeKey()] = true
					report.RejectedProbes++
					continue
				}
				// A declared link failed; another candidate may still work.
				lastErr = err.Error()
				continue
			}
			if candidate.Probe && !candidate.Fallback && !mostlyUnseen(next.Records, seen) {
				// Sites often ignore unknown parameters and return the same
				// listing (with a little ranking churn), so a probe must bring
				// a page that is mostly new to be trusted.
				rejected[candidate.probeKey()] = true
				report.RejectedProbes++
				continue
			}
			if add(next.Records) == 0 {
				if guessed {
					rejected[candidate.probeKey()] = true
					report.RejectedProbes++
					continue
				}
				report.Rows = len(records)
				return records, finish(StopNoNewRecords), nil
			}
			report.Pages++
			report.Methods[candidate.Method]++
			preferred = candidate.Method
			page = next
			advanced = true
			break
		}
		if !advanced {
			report.Rows = len(records)
			if lastErr != "" {
				report.Error = lastErr
				return records, finish("error: " + lastErr), nil
			}
			return records, finish(StopNoNext), nil
		}
	}
}

// mostlyUnseen reports whether at least half of the page's records are new.
func mostlyUnseen(page []RawRecord, seen map[string]bool) bool {
	if len(page) == 0 {
		return false
	}
	fresh := 0
	counted := map[string]bool{}
	for _, record := range page {
		if !seen[record.ExternalID] && !counted[record.ExternalID] {
			counted[record.ExternalID] = true
			fresh++
		}
	}
	return fresh*2 >= len(page)
}

// orderCandidates moves candidates of the preferred (last successful) method
// to the front, keeping the relative order otherwise.
func orderCandidates(candidates []NextCandidate, preferred string) []NextCandidate {
	if preferred == "" {
		return candidates
	}
	ordered := make([]NextCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Method == preferred {
			ordered = append(ordered, candidate)
		}
	}
	for _, candidate := range candidates {
		if candidate.Method != preferred {
			ordered = append(ordered, candidate)
		}
	}
	return ordered
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// --- candidate discovery helpers -------------------------------------------

var linkHeaderPart = regexp.MustCompile(`<([^>]*)>\s*((?:;\s*[^;,]+)*)`)
var linkHeaderRel = regexp.MustCompile(`(?i)rel\s*=\s*"?([^";,]+)"?`)

// linkHeaderNext returns the rel="next" target of HTTP Link headers (RFC 8288),
// resolved against base.
func linkHeaderNext(header http.Header, base string) string {
	for _, value := range header.Values("Link") {
		for _, match := range linkHeaderPart.FindAllStringSubmatch(value, -1) {
			rel := linkHeaderRel.FindStringSubmatch(match[2])
			if rel == nil || !hasRelToken(rel[1], "next") {
				continue
			}
			if resolved := resolveAgainst(base, match[1]); resolved != "" {
				return resolved
			}
		}
	}
	return ""
}

// relNext returns the first link whose rel attribute contains the "next"
// token (e.g. rel="next" or rel="nofollow next").
func relNext(links []pageLink) string {
	for _, link := range links {
		if hasRelToken(link.Rel, "next") {
			return link.Href
		}
	}
	return ""
}

func hasRelToken(rel, token string) bool {
	for _, part := range strings.Fields(strings.ToLower(rel)) {
		if part == token {
			return true
		}
	}
	return false
}

var nextSymbols = []string{"›", "»", "→", "⟩", "≫", ">", ">>"}
var nextPhrases = []string{
	"next", "older", "earlier", "more", "load more", "show more", "see more", "view more",
	"more posts", "more results", "more stories", "more articles", "more items",
}
var notNextWords = []string{"prev", "newer", "first", "last", "back", "‹", "«", "←"}

// anchorNext finds a link that looks like "go to the next page" by its label,
// aria-label or class. With a selector, only matching links are considered.
func anchorNext(links []pageLink, selector string) string {
	for _, link := range links {
		if selector != "" {
			if matchesPageSelector(link, selector) {
				return link.Href
			}
			continue
		}
		if looksLikeNextLink(link) {
			return link.Href
		}
	}
	return ""
}

func looksLikeNextLink(link pageLink) bool {
	text := strings.ToLower(strings.TrimSpace(link.Text))
	aria := strings.ToLower(link.Aria)
	class := strings.ToLower(link.Class)
	for _, word := range notNextWords {
		if strings.Contains(text, word) || strings.Contains(aria, word) {
			return false
		}
	}
	if hasRelToken(link.Rel, "next") {
		return true
	}
	label := strings.TrimSpace(strings.Trim(text, "›»→⟩≫>.… \t"))
	for _, phrase := range nextPhrases {
		if label == phrase || strings.HasPrefix(label, phrase+" page") {
			return true
		}
	}
	if strings.HasPrefix(label, "next") || strings.HasPrefix(label, "older") {
		return true
	}
	if label == "" {
		for _, symbol := range nextSymbols {
			if strings.TrimSpace(text) == symbol {
				return true
			}
		}
	}
	return strings.Contains(aria, "next") || strings.Contains(aria, "older") ||
		containsToken(class, "next") || strings.Contains(class, "pagination-next") ||
		strings.Contains(class, "next-page")
}

// Unambiguous page-number and offset parameters, followed as declared links.
var pageQueryKeys = []string{"page", "paged", "pg", "pagenum", "pageNumber", "page_number"}
var offsetQueryKeys = []string{"offset", "skip"}
var pathPagePattern = regexp.MustCompile(`(?i)/page/(\d+)(/|$)`)

// Ambiguous ones (?p= is often a post id, /p/<n> a product, ?start=/?from= a
// date or id filter) are only tried as probes.
var ambiguousPageKeys = []string{"p"}
var ambiguousOffsetKeys = []string{"start", "from"}
var ambiguousPathPattern = regexp.MustCompile(`(?i)/p/(\d+)(/|$)`)

// pageNumberNext increments a page number already present in the URL
// (?page=3 → ?page=4, /page/3/ → /page/4/).
func pageNumberNext(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	query := u.Query()
	for _, key := range pageQueryKeys {
		if n, err := strconv.Atoi(query.Get(key)); err == nil && n >= 0 {
			query.Set(key, strconv.Itoa(n+1))
			u.RawQuery = query.Encode()
			return u.String(), true
		}
	}
	if next, ok := incrementPath(u, pathPagePattern); ok {
		return next, true
	}
	return "", false
}

func incrementPath(u *url.URL, pattern *regexp.Regexp) (string, bool) {
	match := pattern.FindStringSubmatchIndex(u.Path)
	if match == nil {
		return "", false
	}
	n, _ := strconv.Atoi(u.Path[match[2]:match[3]])
	next := *u
	next.Path = u.Path[:match[2]] + strconv.Itoa(n+1) + u.Path[match[3]:]
	next.RawPath = ""
	return next.String(), true
}

// ambiguousNext guesses next-page URLs from parameters that may or may not
// be pagination (?p=3, /p/3, ?start=20, ?from=20). They are probes, so they
// are only kept when they bring mostly new rows.
func ambiguousNext(rawURL string, pageLen int) []probe {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	var probes []probe
	query := u.Query()
	for _, key := range ambiguousPageKeys {
		if n, err := strconv.Atoi(query.Get(key)); err == nil && n >= 0 {
			probes = append(probes, probe{Key: "page:" + key, URL: withQuery(u, key, strconv.Itoa(n+1))})
		}
	}
	if next, ok := incrementPath(u, ambiguousPathPattern); ok {
		probes = append(probes, probe{Key: "page:path-p", URL: next})
	}
	if pageLen > 0 {
		for _, key := range ambiguousOffsetKeys {
			if n, err := strconv.Atoi(query.Get(key)); err == nil && n >= 0 {
				probes = append(probes, probe{Key: "offset:" + key, URL: withQuery(u, key, strconv.Itoa(n+pageLen))})
			}
		}
	}
	return probes
}

// probe is a guessed next-page URL and the parameter it guesses.
type probe struct {
	Key string
	URL string
}

// withQuery returns rawURL with key set to value.
func withQuery(u *url.URL, key, value string) string {
	query := u.Query()
	query.Set(key, value)
	next := *u
	next.RawQuery = query.Encode()
	return next.String()
}

// pageProbes guesses "page 2" URLs for a first page that has no page number,
// one per query key (e.g. ?page=2, ?paged=2).
func pageProbes(rawURL string, keys ...string) []probe {
	if _, ok := pageNumberNext(rawURL); ok {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	var probes []probe
	for _, key := range keys {
		probes = append(probes, probe{Key: "page:" + key, URL: withQuery(u, key, "2")})
	}
	return probes
}

// offsetNext advances an offset-style parameter already present in the URL by
// the number of records on the current page.
func offsetNext(rawURL string, pageLen int) (string, bool) {
	if pageLen <= 0 {
		return "", false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	query := u.Query()
	for _, key := range offsetQueryKeys {
		if n, err := strconv.Atoi(query.Get(key)); err == nil && n >= 0 {
			query.Set(key, strconv.Itoa(n+pageLen))
			u.RawQuery = query.Encode()
			return u.String(), true
		}
	}
	return "", false
}

var hexHashPattern = regexp.MustCompile(`^[0-9a-f]{40,}$`)
var numericPattern = regexp.MustCompile(`^\d+$`)

// cursorProbes guesses cursor-style next-page URLs from the last record on the
// page: after=<id>, cursor=<id>, max_id=<id> (numeric ids) and
// until=<unix time>. Each is a probe and is only kept if it yields new rows.
func cursorProbes(rawURL string, last RawRecord) []probe {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	var probes []probe
	add := func(key, value string) {
		probes = append(probes, probe{Key: "cursor:" + key, URL: withQuery(u, key, value)})
	}
	id := strings.TrimSpace(last.ExternalID)
	if id != "" && !strings.Contains(id, "://") && !hexHashPattern.MatchString(id) && len(id) <= 128 {
		add("after", id)
		add("cursor", id)
		if numericPattern.MatchString(id) {
			add("max_id", id)
		}
	}
	if estimated, _ := last.Payload["_time_estimated"].(bool); !estimated && !last.ObservedAt.IsZero() {
		add("until", strconv.FormatInt(last.ObservedAt.Unix(), 10))
	}
	return probes
}

// maxRateLimitRetries and maxRetryWait bound how long a paged fetch waits
// when a site answers 429/503 (common when walking many pages quickly).
const maxRateLimitRetries = 2
const maxRetryWait = 30 * time.Second

// doWithRateLimitRetry sends the request built by newRequest and retries on
// 429 Too Many Requests / 503 Service Unavailable, honouring Retry-After
// (seconds or HTTP date, capped at maxRetryWait; 5s when absent).
func doWithRateLimitRetry(ctx context.Context, client *http.Client, newRequest func() (*http.Request, error)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := newRequest()
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		if (resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode != http.StatusServiceUnavailable) || attempt >= maxRateLimitRetries {
			return resp, nil
		}
		wait := retryAfter(resp.Header.Get("Retry-After"), time.Now())
		resp.Body.Close()
		if err := sleepCtx(ctx, wait); err != nil {
			return nil, err
		}
	}
}

func retryAfter(value string, now time.Time) time.Duration {
	wait := 5 * time.Second
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		wait = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(value); err == nil {
		wait = at.Sub(now)
	}
	if wait < 0 {
		wait = 0
	}
	if wait > maxRetryWait {
		wait = maxRetryWait
	}
	return wait
}

func resolveAgainst(base, href string) string {
	baseURL, err := url.Parse(base)
	if err != nil {
		return ""
	}
	target, err := baseURL.Parse(strings.TrimSpace(href))
	if err != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Host == "" {
		return ""
	}
	target.Fragment = ""
	return target.String()
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return a
	}
	return (a + b - 1) / b
}

// candidateList builds an ordered, de-duplicated candidate list.
type candidateList []NextCandidate

func (c *candidateList) add(rawURL, method string, isProbe bool) {
	c.addKeyed(rawURL, method, isProbe, "")
}

// addFallback adds a last-resort candidate (see NextCandidate.Fallback).
func (c *candidateList) addFallback(rawURL, method string) {
	if rawURL == "" {
		return
	}
	*c = append(*c, NextCandidate{URL: rawURL, Method: method, Fallback: true, Key: method})
}

func (c *candidateList) addKeyed(rawURL, method string, isProbe bool, key string) {
	if rawURL == "" {
		return
	}
	for _, existing := range *c {
		if existing.URL == rawURL {
			return
		}
	}
	*c = append(*c, NextCandidate{URL: rawURL, Method: method, Probe: isProbe, Key: key})
}

// urlCandidates adds the URL-derived candidates shared by all adapters:
// page-number/offset increments, then (on the first page only) page probes,
// then cursor probes from the last record.
func (c *candidateList) urlCandidates(pageURL string, records []RawRecord, firstPage bool, pageKeys []string, cursors bool) {
	if next, ok := pageNumberNext(pageURL); ok {
		c.add(next, MethodPageNumber, false)
	}
	if next, ok := offsetNext(pageURL, len(records)); ok {
		c.add(next, MethodOffset, false)
	}
	for _, guess := range ambiguousNext(pageURL, len(records)) {
		method := MethodPageNumber
		if strings.HasPrefix(guess.Key, "offset:") {
			method = MethodOffset
		}
		c.addKeyed(guess.URL, method, true, guess.Key)
	}
	if firstPage {
		for _, guess := range pageProbes(pageURL, pageKeys...) {
			c.addKeyed(guess.URL, MethodPageNumber, true, guess.Key)
		}
	}
	if cursors && len(records) > 0 {
		for _, guess := range cursorProbes(pageURL, records[len(records)-1]) {
			c.addKeyed(guess.URL, MethodCursor, true, guess.Key)
		}
	}
}

func describeReport(report *PagingReport) string {
	if report == nil {
		return ""
	}
	return fmt.Sprintf("pages=%d methods=%v rows=%d stopped=%s", report.Pages, report.Methods, report.Rows, report.Stopped)
}
