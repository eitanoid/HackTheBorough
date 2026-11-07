// Package ingest fetches records from external sources.
package ingest

import (
	"context"
	"encoding/json"
	"time"
)

type RawRecord struct {
	ExternalID string // stable per item
	ObservedAt time.Time
	Payload    map[string]any
}

// SourceAdapter implementations are pure: scheduling, dedup and errors
// belong to the loop.
type SourceAdapter interface {
	Kind() string
	Fetch(ctx context.Context, cfg json.RawMessage, since *time.Time) ([]RawRecord, error)
}
