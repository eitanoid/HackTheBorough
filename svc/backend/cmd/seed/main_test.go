package main

import (
	"encoding/json"
	"strings"
	"testing"

	"hacktheburgh/backend/internal/ingest"
)

func TestDemoSources(t *testing.T) {
	t.Parallel()

	sources := demoSources()
	if len(sources) != 3 {
		t.Fatalf("got %d demo sources, want 3", len(sources))
	}

	kinds := map[string]bool{}
	for _, source := range sources {
		if source.name == "" {
			t.Fatal("source name is empty")
		}
		if source.interval <= 0 {
			t.Fatalf("source %q has invalid interval %d", source.name, source.interval)
		}

		raw, err := json.Marshal(source.config)
		if err != nil {
			t.Fatalf("marshal %q: %v", source.name, err)
		}
		lower := strings.ToLower(string(raw))
		if strings.Contains(lower, "jina") {
			t.Fatalf("source %q still uses Jina Reader", source.name)
		}
		if strings.Contains(lower, "openrouter") {
			t.Fatalf("source %q still references OpenRouter", source.name)
		}

		switch source.kind {
		case "rss":
			if _, err := ingest.ParseRSSConfig(raw); err != nil {
				t.Fatalf("rss source %q: %v", source.name, err)
			}
		case "csv":
			if _, err := ingest.ParseCSVConfig(raw); err != nil {
				t.Fatalf("csv source %q: %v", source.name, err)
			}
		case "web":
			if _, err := ingest.ParseWebConfig(raw); err != nil {
				t.Fatalf("web source %q: %v", source.name, err)
			}
		default:
			t.Fatalf("source %q has unsupported kind %q", source.name, source.kind)
		}
		kinds[source.kind] = true
	}

	for _, kind := range []string{"web", "csv", "rss"} {
		if !kinds[kind] {
			t.Fatalf("missing demo source kind %q", kind)
		}
	}
}
