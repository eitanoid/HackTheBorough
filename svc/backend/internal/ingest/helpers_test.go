package ingest

import (
	"context"
	"encoding/json"
)

// fetchRecords runs a normal (non-backfill) poll and returns its records.
func fetchRecords(adapter SourceAdapter, ctx context.Context, raw json.RawMessage) ([]RawRecord, error) {
	result, err := adapter.Fetch(ctx, raw, FetchOptions{})
	return result.Records, err
}
