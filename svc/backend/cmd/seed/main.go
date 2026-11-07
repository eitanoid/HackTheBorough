// Command seed registers the live demo sources. It is idempotent by name.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"time"

	"hacktheburgh/backend/internal/config"
	"hacktheburgh/backend/internal/db"
	"hacktheburgh/backend/internal/ingest"
)

const webSchema = `{
  "type": "array",
  "items": {
    "type": "object",
    "properties": {
      "title": {"type": "string"},
      "url": {"type": "string"},
      "published_at": {"type": "string"},
      "summary": {"type": "string"}
    },
    "required": ["title", "url"]
  }
}`

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg := config.Load()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database connection failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		slog.Error("migrations failed", "err", err)
		os.Exit(1)
	}
	store := ingest.NewStore(pool)
	sources := []struct {
		name     string
		kind     string
		interval int
		config   any
	}{
		{
			name: "BleepingComputer", kind: "web", interval: 300,
			config: map[string]any{
				// Cloudflare blocks Crawl4AI directly; Jina Reader serves the same page.
				"url":        "https://r.jina.ai/https://www.bleepingcomputer.com/",
				"query":      "Extract up to 10 latest BleepingComputer articles. Use absolute links and RFC3339 publication dates when present.",
				"schema":     json.RawMessage(webSchema),
				"id_field":   "url",
				"time_field": "published_at",
			},
		},
		{
			name: "Yahoo Finance", kind: "web", interval: 300,
			config: map[string]any{
				"url":        "https://finance.yahoo.com/news/",
				"query":      "Extract up to 10 latest Yahoo Finance news items. Use absolute links and RFC3339 publication dates when present.",
				"schema":     json.RawMessage(webSchema),
				"id_field":   "url",
				"time_field": "published_at",
			},
		},
		{
			name: "Crude Oil Prices (OWID)", kind: "csv", interval: 3600,
			config: map[string]any{
				"url":        "https://ourworldindata.org/grapher/crude-oil-prices.csv?v=1&csvType=full&useColumnShortNames=true",
				"time_field": "year",
			},
		},
		{
			name: "Trump's Truth RSS", kind: "rss", interval: 300,
			config: map[string]any{
				"url": "https://www.trumpstruth.org/feed",
			},
		},
	}
	for _, item := range sources {
		raw, err := json.Marshal(item.config)
		if err != nil {
			slog.Error("encode source config", "source", item.name, "err", err)
			os.Exit(1)
		}
		interval := item.interval
		source, err := store.EnsureSource(ctx, item.name, item.kind, raw, &interval)
		if err != nil {
			slog.Error("register source", "source", item.name, "err", err)
			os.Exit(1)
		}
		slog.Info("source registered", "id", source.ID, "name", source.Name, "kind", source.Kind)
	}
}
