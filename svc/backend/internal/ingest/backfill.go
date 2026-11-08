package ingest

import (
	"errors"
	"sort"
)

// ErrInvalidBackfillRows is returned for a negative backfill target.
var ErrInvalidBackfillRows = errors.New("backfill_rows must be at least 1")

// ResolveBackfillRows turns a requested backfill target into the one to
// store: 0 selects serverDefault (DefaultBackfillRows when that is unset),
// negative values are rejected. API handlers call this; the store only keeps
// a last-resort fallback for direct callers such as the seed command.
func ResolveBackfillRows(requested, serverDefault int) (int, error) {
	if requested < 0 {
		return 0, ErrInvalidBackfillRows
	}
	if requested > 0 {
		return requested, nil
	}
	if serverDefault > 0 {
		return serverDefault, nil
	}
	return DefaultBackfillRows, nil
}

// SortNewestFirst orders records by ObservedAt descending, keeping the
// adapter's order for ties.
func SortNewestFirst(records []RawRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		return records[i].ObservedAt.After(records[j].ObservedAt)
	})
}

// CapRecords drops duplicate external IDs (keeping the first occurrence) and
// truncates the result to n records. n <= 0 means no cap.
func CapRecords(records []RawRecord, n int) []RawRecord {
	seen := make(map[string]bool, len(records))
	out := make([]RawRecord, 0, len(records))
	for _, record := range records {
		if seen[record.ExternalID] {
			continue
		}
		seen[record.ExternalID] = true
		out = append(out, record)
		if n > 0 && len(out) == n {
			break
		}
	}
	return out
}

// recordsHaveTimes reports whether at least one record carries a real
// timestamp (adapters mark guessed times with payload["_time_estimated"]).
func recordsHaveTimes(records []RawRecord) bool {
	for _, record := range records {
		if estimated, _ := record.Payload["_time_estimated"].(bool); !estimated {
			return true
		}
	}
	return false
}

func reverseRecords(records []RawRecord) {
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
}

// TrimKnownTail keeps only the records above the last one already stored.
// records must be ordered newest first. Sources re-serve rows a backfill
// deliberately left out (an RSS page longer than the target, a whole CSV
// file); everything at or below the oldest already-stored row is such an old
// row, while rows above it are genuinely new. With nothing stored yet, all
// records are kept.
func TrimKnownTail(records []RawRecord, known map[string]bool) []RawRecord {
	last := -1
	for i, record := range records {
		if known[record.ExternalID] {
			last = i
		}
	}
	if last < 0 {
		return records
	}
	return records[:last+1]
}
