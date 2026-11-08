package ingest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseCSVConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		wantURL string
		wantErr bool
	}{
		{name: "https", raw: `{"url":"https://example.com/data.csv"}`, wantURL: "https://example.com/data.csv"},
		{name: "http", raw: `{"url":"http://example.com/data.csv"}`, wantURL: "http://example.com/data.csv"},
		{name: "missing scheme", raw: `{"url":"example.com/data.csv"}`, wantErr: true},
		{name: "relative", raw: `{"url":"/data.csv"}`, wantErr: true},
		{name: "bad json", raw: `{`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseCSVConfig(json.RawMessage(tt.raw))
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

func TestParseCSVTypesAndDefaults(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	records, err := ParseCSV([]byte("name,count,price,active,empty\nA,3,1.5,true,\n"), "", now)
	if err != nil {
		t.Fatalf("ParseCSV: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("got %d records, want 1", len(records))
	}
	record := records[0]
	if record.Payload["name"] != "A" {
		t.Fatalf("name = %#v", record.Payload["name"])
	}
	if record.Payload["count"] != int64(3) {
		t.Fatalf("count = %#v (%T)", record.Payload["count"], record.Payload["count"])
	}
	if record.Payload["price"] != float64(1.5) {
		t.Fatalf("price = %#v (%T)", record.Payload["price"], record.Payload["price"])
	}
	if record.Payload["active"] != true {
		t.Fatalf("active = %#v", record.Payload["active"])
	}
	if record.Payload["empty"] != nil {
		t.Fatalf("empty = %#v", record.Payload["empty"])
	}
	if record.Payload["_time_estimated"] != true {
		t.Fatal("expected _time_estimated when no time column is present")
	}
	if !record.ObservedAt.Equal(now) {
		t.Fatalf("ObservedAt = %s, want %s", record.ObservedAt, now)
	}
	if record.ExternalID == "" {
		t.Fatal("expected a stable external ID")
	}

	other, err := ParseCSV([]byte("name,count,price,active,empty\nA,3,1.5,true,\n"), "", now)
	if err != nil {
		t.Fatalf("ParseCSV second run: %v", err)
	}
	if other[0].ExternalID != record.ExternalID {
		t.Fatalf("external ID is not deterministic: %q != %q", other[0].ExternalID, record.ExternalID)
	}
}

func TestParseCSVTimeFields(t *testing.T) {
	t.Parallel()

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		body      string
		timeField string
		wantTime  time.Time
	}{
		{
			name:      "iso date",
			body:      "date,value\n2024-01-02,5\n",
			timeField: "date",
			wantTime:  time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		},
		{
			name:      "rfc3339",
			body:      "published_at,value\n2024-01-02T03:04:05Z,5\n",
			timeField: "published_at",
			wantTime:  time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC),
		},
		{
			name:      "year",
			body:      "year,value\n2020,5\n",
			timeField: "year",
			wantTime:  time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		{
			name:     "detected date column",
			body:     "Date,value\n2024-01-02,5\n",
			wantTime: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			records, err := ParseCSV([]byte(tt.body), tt.timeField, now)
			if err != nil {
				t.Fatalf("ParseCSV: %v", err)
			}
			if !records[0].ObservedAt.Equal(tt.wantTime) {
				t.Fatalf("ObservedAt = %s, want %s", records[0].ObservedAt, tt.wantTime)
			}
			if records[0].Payload["_time_estimated"] == true {
				t.Fatal("did not expect _time_estimated")
			}
		})
	}
}

func TestParseCSVBOMAndErrors(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	records, err := ParseCSV([]byte("\xef\xbb\xbfname\nA\n"), "", now)
	if err != nil {
		t.Fatalf("BOM parse: %v", err)
	}
	if records[0].Payload["name"] != "A" {
		t.Fatalf("name = %#v", records[0].Payload["name"])
	}

	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "header only", body: "a,b\n"},
		{name: "empty header", body: ",b\n1,2\n"},
		{name: "ragged row", body: "a,b\n1\n"},
		{name: "bad quote", body: "a,b\n\"1,2\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseCSV([]byte(tt.body), "", now); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestCSVAdapterFetch(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") == "" {
			t.Error("missing Accept header")
		}
		_, _ = w.Write([]byte("name,value\nA,1\n"))
	}))
	defer server.Close()

	adapter := &CSVAdapter{}
	raw, _ := json.Marshal(map[string]string{"url": server.URL + "/data.csv"})
	records, err := fetchRecords(adapter, context.Background(), raw)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(records) != 1 || records[0].Payload["name"] != "A" {
		t.Fatalf("unexpected records: %#v", records)
	}
}

func TestCSVAdapterHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer server.Close()

	adapter := &CSVAdapter{}
	raw, _ := json.Marshal(map[string]string{"url": server.URL})
	if _, err := adapter.Fetch(context.Background(), raw, FetchOptions{}); err == nil {
		t.Fatal("expected HTTP error")
	}
}
