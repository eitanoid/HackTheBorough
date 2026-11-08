package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxFeedBytes = 10 * 1024 * 1024

// RSSConfig is stored in data_sources.config for rss sources.
type RSSConfig struct {
	URL string `json:"url"`
}

func ParseRSSConfig(raw json.RawMessage) (RSSConfig, error) {
	var cfg RSSConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return cfg, fmt.Errorf("invalid rss config: %w", err)
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return cfg, errors.New("rss config.url must be an absolute http(s) URL")
	}
	return cfg, nil
}

type RSSAdapter struct {
	Client *http.Client
	// PageDelay is the pause between pages during a backfill.
	PageDelay time.Duration
}

func (a *RSSAdapter) Kind() string { return "rss" }

// Fetch reads one feed page on normal polls. During a backfill it follows
// next pages (Link header, rel="next", page/offset parameters, cursor probes)
// until opts.Limit records are collected.
func (a *RSSAdapter) Fetch(ctx context.Context, raw json.RawMessage, opts FetchOptions) (FetchResult, error) {
	cfg, err := ParseRSSConfig(raw)
	if err != nil {
		return FetchResult{}, err
	}
	if !opts.Backfill || opts.Limit <= 0 {
		body, _, err := a.fetchPage(ctx, cfg.URL)
		if err != nil {
			return FetchResult{}, err
		}
		records, err := ParseFeed(body, time.Now().UTC())
		return FetchResult{Records: records}, err
	}
	pager := &rssPager{adapter: a, first: cfg.URL}
	records, report, err := CollectPages(ctx, pager, cfg.URL, PagingLimits{
		Limit:     opts.Limit,
		MaxPages:  clampInt(ceilDiv(opts.Limit, 10)+2, 1, 100),
		MaxProbes: 4,
		Delay:     a.PageDelay,
	})
	if err != nil {
		return FetchResult{}, err
	}
	return FetchResult{Records: records, Report: &report}, nil
}

func (a *RSSAdapter) fetchPage(ctx context.Context, pageURL string) ([]byte, http.Header, error) {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := doWithRateLimitRetry(ctx, client, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/atom+xml, application/rss+xml, application/xml, text/xml")
		req.Header.Set("User-Agent", "AnduinDataPipeline/1.0")
		return req, nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("fetch rss: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, nil, fmt.Errorf("rss HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFeedBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read rss: %w", err)
	}
	if len(body) > maxFeedBytes {
		return nil, nil, fmt.Errorf("rss feed exceeds %d bytes", maxFeedBytes)
	}
	return body, resp.Header, nil
}

// rssPager fetches one feed page for CollectPages.
type rssPager struct {
	adapter *RSSAdapter
	first   string
}

func (p *rssPager) FetchPage(ctx context.Context, pageURL string, _ int) (Page, error) {
	body, header, err := p.adapter.fetchPage(ctx, pageURL)
	if err != nil {
		return Page{}, err
	}
	records, links, err := ParseFeedPage(body, time.Now().UTC())
	if err != nil {
		return Page{}, err
	}
	var candidates candidateList
	candidates.add(linkHeaderNext(header, pageURL), MethodLinkHeader, false)
	if next := relNext(links); next != "" {
		candidates.add(resolveAgainst(pageURL, next), MethodRelNext, false)
	}
	candidates.urlCandidates(pageURL, records, pageURL == p.first, []string{"page", "paged"}, true)
	return Page{URL: pageURL, Records: records, Candidates: candidates}, nil
}

type feedDocument struct {
	Channel struct {
		Items []rssItem `xml:"item"`
		// <atom:link rel="next"> inside an RSS channel (RFC 5005).
		AtomLinks []atomLink `xml:"http://www.w3.org/2005/Atom link"`
	} `xml:"channel"`
	Items   []rssItem   `xml:"item"`
	Entries []atomEntry `xml:"entry"`
	// Feed-level Atom links, e.g. <link rel="next" href="…"/>.
	Links []atomLink `xml:"link"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        string   `xml:"guid"`
	Description string   `xml:"description"`
	Summary     string   `xml:"summary"`
	Content     string   `xml:"encoded"`
	PubDate     string   `xml:"pubDate"`
	Published   string   `xml:"published"`
	Updated     string   `xml:"updated"`
	Creator     string   `xml:"creator"`
	Author      string   `xml:"author"`
	Categories  []string `xml:"category"`
}

type atomEntry struct {
	ID         string         `xml:"id"`
	Title      string         `xml:"title"`
	Links      []atomLink     `xml:"link"`
	Summary    string         `xml:"summary"`
	Content    string         `xml:"content"`
	Published  string         `xml:"published"`
	Updated    string         `xml:"updated"`
	Authors    []atomAuthor   `xml:"author"`
	Categories []atomCategory `xml:"category"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

// ParseFeed accepts RSS 2.0, RSS 1.0 (RDF) and Atom documents.
func ParseFeed(body []byte, now time.Time) ([]RawRecord, error) {
	records, _, err := ParseFeedPage(body, now)
	return records, err
}

// ParseFeedPage parses a feed and also returns its feed-level links (with
// their rel attributes) so callers can find the next page.
func ParseFeedPage(body []byte, now time.Time) ([]RawRecord, []pageLink, error) {
	var doc feedDocument
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, nil, fmt.Errorf("invalid RSS/Atom XML: %w", err)
	}
	var links []pageLink
	for _, link := range append(append([]atomLink{}, doc.Links...), doc.Channel.AtomLinks...) {
		if href := strings.TrimSpace(link.Href); href != "" {
			links = append(links, pageLink{Href: href, Rel: link.Rel})
		}
	}
	records, err := parseFeedRecords(doc, now)
	return records, links, err
}

func parseFeedRecords(doc feedDocument, now time.Time) ([]RawRecord, error) {
	items := append(doc.Channel.Items, doc.Items...)
	if len(items) == 0 && len(doc.Entries) == 0 {
		return nil, errors.New("document does not contain an RSS or Atom feed")
	}

	records := make([]RawRecord, 0, len(items)+len(doc.Entries))
	for _, item := range items {
		payload := map[string]any{}
		if item.Title != "" {
			payload["title"] = strings.TrimSpace(item.Title)
		}
		link := strings.TrimSpace(item.Link)
		if link != "" {
			payload["link"] = link
		}
		summary := firstNonEmpty(item.Summary, item.Description, item.Content)
		if summary != "" {
			payload["summary"] = strings.TrimSpace(summary)
		}
		author := firstNonEmpty(item.Creator, item.Author)
		if author != "" {
			payload["authors"] = []string{strings.TrimSpace(author)}
		}
		if len(item.Categories) > 0 {
			payload["categories"] = item.Categories
		}
		externalID := firstNonEmpty(item.GUID, link)
		observed, ok := parseTimestamp(firstNonEmpty(item.Published, item.Updated, item.PubDate))
		if !ok {
			observed = now
			payload["_time_estimated"] = true
		}
		if externalID == "" {
			externalID = hashRecord(payload)
		}
		payload["id"] = externalID
		if v, ok := parseTimestamp(firstNonEmpty(item.Published, item.Updated, item.PubDate)); ok {
			payload["published_at"] = v.Format(time.RFC3339)
		}
		records = append(records, RawRecord{ExternalID: externalID, ObservedAt: observed, Payload: payload})
	}
	for _, entry := range doc.Entries {
		payload := map[string]any{}
		if entry.Title != "" {
			payload["title"] = strings.TrimSpace(entry.Title)
		}
		link := firstAlternateLink(entry.Links)
		if link != "" {
			payload["link"] = link
		}
		summary := firstNonEmpty(entry.Summary, entry.Content)
		if summary != "" {
			payload["summary"] = strings.TrimSpace(summary)
		}
		var authors []string
		for _, author := range entry.Authors {
			if author.Name != "" {
				authors = append(authors, strings.TrimSpace(author.Name))
			}
		}
		if len(authors) > 0 {
			payload["authors"] = authors
		}
		var categories []string
		for _, category := range entry.Categories {
			if category.Term != "" {
				categories = append(categories, category.Term)
			}
		}
		if len(categories) > 0 {
			payload["categories"] = categories
		}
		externalID := firstNonEmpty(entry.ID, link)
		observed, ok := parseTimestamp(firstNonEmpty(entry.Published, entry.Updated))
		if !ok {
			observed = now
			payload["_time_estimated"] = true
		} else {
			payload["published_at"] = observed.Format(time.RFC3339)
		}
		if externalID == "" {
			externalID = hashRecord(payload)
		}
		payload["id"] = externalID
		records = append(records, RawRecord{ExternalID: externalID, ObservedAt: observed, Payload: payload})
	}
	return records, nil
}

func firstAlternateLink(links []atomLink) string {
	for _, link := range links {
		if link.Rel == "" || link.Rel == "alternate" {
			return strings.TrimSpace(link.Href)
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func hashRecord(payload map[string]any) string {
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

var timestampLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	time.RFC1123Z,
	time.RFC1123,
	time.RFC822Z,
	time.RFC822,
	"Mon, 02 Jan 2006 15:04:05 MST",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

func parseTimestamp(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	lower := strings.ToLower(value)
	if lower == "just now" || lower == "now" {
		return time.Now().UTC(), true
	}
	if lower == "yesterday" {
		return time.Now().UTC().Add(-24 * time.Hour), true
	}
	if n, unit, ok := parseRelativeAge(lower); ok {
		return time.Now().UTC().Add(-time.Duration(n) * unit), true
	}
	for _, layout := range timestampLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func parseRelativeAge(value string) (int, time.Duration, bool) {
	var n int
	var unit string
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return 0, 0, false
	}
	if _, err := fmt.Sscanf(value, "%d%s", &n, &unit); err != nil || n < 0 {
		return 0, 0, false
	}
	switch unit {
	case "s", "sec", "secs", "second", "seconds":
		return n, time.Second, true
	case "m", "min", "mins", "minute", "minutes":
		return n, time.Minute, true
	case "h", "hr", "hrs", "hour", "hours":
		return n, time.Hour, true
	case "d", "day", "days":
		return n, 24 * time.Hour, true
	}
	return 0, 0, false
}
