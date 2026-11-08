package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseRSSConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantURL string
		wantErr bool
	}{
		{name: "https", raw: `{"url":"https://example.com/feed.xml"}`, wantURL: "https://example.com/feed.xml"},
		{name: "http", raw: `{"url":"http://example.com/feed.xml"}`, wantURL: "http://example.com/feed.xml"},
		{name: "missing scheme", raw: `{"url":"example.com/feed.xml"}`, wantErr: true},
		{name: "relative", raw: `{"url":"/feed.xml"}`, wantErr: true},
		{name: "bad json", raw: `{`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseRSSConfig(json.RawMessage(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.URL != tt.wantURL {
				t.Fatalf("URL = %q, want %q", cfg.URL, tt.wantURL)
			}
		})
	}
}

const rss2Fixture = `<?xml version="1.0"?>
<rss version="2.0">
  <channel>
    <title>Example</title>
    <item>
      <title>First post</title>
      <link>https://example.com/first</link>
      <guid>guid-1</guid>
      <description>First summary</description>
      <author>Alice</author>
      <category>news</category>
      <category>tech</category>
      <pubDate>Mon, 02 Jan 2006 15:04:05 GMT</pubDate>
    </item>
  </channel>
</rss>`

const atomFixture = `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Example</title>
  <entry>
    <id>atom-1</id>
    <title>Atom post</title>
    <link rel="alternate" href="https://example.com/atom"/>
    <summary>Atom summary</summary>
    <author><name>Bob</name></author>
    <category term="news"/>
    <updated>2024-01-02T03:04:05Z</updated>
  </entry>
</feed>`

const rdfFixture = `<?xml version="1.0"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"
         xmlns="http://purl.org/rss/1.0/"
         xmlns:dc="http://purl.org/dc/elements/1.1/">
  <channel>
    <title>RDF Example</title>
  </channel>
  <item>
    <title>RDF post</title>
    <link>https://example.com/rdf</link>
    <dc:creator>Carol</dc:creator>
    <description>RDF summary</description>
    <dc:date>2024-01-02T03:04:05Z</dc:date>
  </item>
</rdf:RDF>`

func TestParseFeedRSS2(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	records, err := ParseFeed([]byte(rss2Fixture), now)
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	record := records[0]
	if record.ExternalID != "guid-1" {
		t.Fatalf("ExternalID = %q", record.ExternalID)
	}
	if record.Payload["title"] != "First post" {
		t.Fatalf("title = %#v", record.Payload["title"])
	}
	if record.Payload["link"] != "https://example.com/first" {
		t.Fatalf("link = %#v", record.Payload["link"])
	}
	if record.Payload["summary"] != "First summary" {
		t.Fatalf("summary = %#v", record.Payload["summary"])
	}
	if record.Payload["id"] != "guid-1" {
		t.Fatalf("id = %#v", record.Payload["id"])
	}
	if record.Payload["published_at"] != "2006-01-02T15:04:05Z" {
		t.Fatalf("published_at = %#v", record.Payload["published_at"])
	}
	authors, ok := record.Payload["authors"].([]string)
	if !ok || len(authors) != 1 || authors[0] != "Alice" {
		t.Fatalf("authors = %#v", record.Payload["authors"])
	}
	categories, ok := record.Payload["categories"].([]string)
	if !ok || len(categories) != 2 {
		t.Fatalf("categories = %#v", record.Payload["categories"])
	}
	if !record.ObservedAt.Equal(time.Date(2006, 1, 2, 15, 4, 5, 0, time.UTC)) {
		t.Fatalf("ObservedAt = %s", record.ObservedAt)
	}
}

func TestParseFeedAtom(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	records, err := ParseFeed([]byte(atomFixture), now)
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	record := records[0]
	if record.ExternalID != "atom-1" {
		t.Fatalf("ExternalID = %q", record.ExternalID)
	}
	if record.Payload["link"] != "https://example.com/atom" {
		t.Fatalf("link = %#v", record.Payload["link"])
	}
	if record.Payload["summary"] != "Atom summary" {
		t.Fatalf("summary = %#v", record.Payload["summary"])
	}
	if record.Payload["published_at"] != "2024-01-02T03:04:05Z" {
		t.Fatalf("published_at = %#v", record.Payload["published_at"])
	}
	authors, ok := record.Payload["authors"].([]string)
	if !ok || len(authors) != 1 || authors[0] != "Bob" {
		t.Fatalf("authors = %#v", record.Payload["authors"])
	}
	categories, ok := record.Payload["categories"].([]string)
	if !ok || len(categories) != 1 || categories[0] != "news" {
		t.Fatalf("categories = %#v", record.Payload["categories"])
	}
}

func TestParseFeedRDF(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	records, err := ParseFeed([]byte(rdfFixture), now)
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	record := records[0]
	if record.ExternalID != "https://example.com/rdf" {
		t.Fatalf("ExternalID = %q", record.ExternalID)
	}
	if record.Payload["title"] != "RDF post" {
		t.Fatalf("title = %#v", record.Payload["title"])
	}
	authors, ok := record.Payload["authors"].([]string)
	if !ok || len(authors) != 1 || authors[0] != "Carol" {
		t.Fatalf("authors = %#v", record.Payload["authors"])
	}
}

func TestParseFeedFallbacks(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	body := `<rss version="2.0"><channel><item><title>No identifiers</title></item></channel></rss>`
	records, err := ParseFeed([]byte(body), now)
	if err != nil {
		t.Fatalf("ParseFeed: %v", err)
	}
	if records[0].ExternalID == "" {
		t.Fatal("expected hashed external ID")
	}
	if records[0].Payload["_time_estimated"] != true {
		t.Fatal("expected _time_estimated")
	}
	if !records[0].ObservedAt.Equal(now) {
		t.Fatalf("ObservedAt = %s, want %s", records[0].ObservedAt, now)
	}
}

func TestParseFeedErrors(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	for name, body := range map[string]string{
		"invalid xml": `<rss`,
		"empty":       `<rss version="2.0"><channel></channel></rss>`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFeed([]byte(body), now); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseTimestamp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  time.Time
		ok    bool
	}{
		{name: "rfc3339", value: "2024-01-02T03:04:05Z", want: time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), ok: true},
		{name: "rfc3339 nano", value: "2024-01-02T03:04:05.123456789Z", want: time.Date(2024, 1, 2, 3, 4, 5, 123456789, time.UTC), ok: true},
		{name: "rfc1123z", value: "Mon, 02 Jan 2006 15:04:05 -0700", want: time.Date(2006, 1, 2, 22, 4, 5, 0, time.UTC), ok: true},
		{name: "rfc822", value: "02 Jan 06 15:04 MST", ok: true},
		{name: "date only", value: "2024-01-02", want: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), ok: true},
		{name: "empty", value: "", ok: false},
		{name: "garbage", value: "not a date", ok: false},
		{name: "one week", value: "1 week ago", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseTimestamp(tt.value)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if tt.ok && !tt.want.IsZero() && !got.Equal(tt.want) {
				t.Fatalf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestParseRelativeAge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		value string
		n     int
		unit  time.Duration
		ok    bool
	}{
		{value: "2d", n: 2, unit: 24 * time.Hour, ok: true},
		{value: "2 days ago", n: 2, unit: 24 * time.Hour, ok: true},
		{value: "3 hours ago", n: 3, unit: time.Hour, ok: true},
		{value: "15 minutes ago", n: 15, unit: time.Minute, ok: true},
		{value: "30 seconds ago", n: 30, unit: time.Second, ok: true},
		{value: "1 week ago", ok: false},
		{value: "not a duration", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			n, unit, ok := parseRelativeAge(tt.value)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if tt.ok && (n != tt.n || unit != tt.unit) {
				t.Fatalf("got %d %s, want %d %s", n, unit, tt.n, tt.unit)
			}
		})
	}
}

func TestRSSAdapterFetch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "" {
			t.Error("missing Accept header")
		}
		_, _ = w.Write([]byte(rss2Fixture))
	}))
	defer server.Close()

	adapter := &RSSAdapter{}
	raw, _ := json.Marshal(map[string]string{"url": server.URL + "/feed.xml"})
	records, err := adapter.Fetch(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(records) != 1 || records[0].ExternalID != "guid-1" {
		t.Fatalf("unexpected records: %#v", records)
	}
}

func TestRSSAdapterHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()

	adapter := &RSSAdapter{}
	raw, _ := json.Marshal(map[string]string{"url": server.URL})
	if _, err := adapter.Fetch(context.Background(), raw, nil); err == nil {
		t.Fatal("expected HTTP error")
	}
}
