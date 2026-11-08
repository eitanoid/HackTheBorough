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

// DefaultBackfillRows is used when a source is created without an explicit
// backfill target.
const DefaultBackfillRows = 500

// FetchOptions tells an adapter what kind of poll this is.
type FetchOptions struct {
	// Since is the previous poll time (nil on the first poll). Adapters may
	// ignore it.
	Since *time.Time
	// Backfill is true for an initial or re-requested backfill. Adapters then
	// page through the source until Limit records are collected.
	Backfill bool
	// Limit is the backfill target. Zero means a normal single-page fetch.
	Limit int
}

// FetchResult is what one adapter Fetch produced.
type FetchResult struct {
	Records []RawRecord
	// Report describes how pages were walked; nil on normal polls.
	Report *PagingReport
}

// SourceAdapter implementations are pure: scheduling, dedup and errors
// belong to the loop.
type SourceAdapter interface {
	Kind() string
	Fetch(ctx context.Context, cfg json.RawMessage, opts FetchOptions) (FetchResult, error)
}
