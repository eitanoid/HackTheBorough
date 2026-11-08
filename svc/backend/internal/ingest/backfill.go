package ingest

import (
	"errors"
	"sort"
	"time"
)

// ErrInvalidBackfillRows is returned for a negative backfill target.
var ErrInvalidBackfillRows = errors.New("backfill_rows must not be negative (0 selects the default)")

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

// hasRealTime reports whether a record's timestamp came from the source
// rather than being estimated at ingest time.
func hasRealTime(record RawRecord) bool {
	estimated, _ := record.Payload["_time_estimated"].(bool)
	return !estimated
}

// BackfillFloor returns the timestamp below which a capped backfill dropped
// rows: the oldest kept row's time, when the cap actually dropped something
// and that row has a real timestamp. kept must be newest first. Otherwise nil.
func BackfillFloor(kept []RawRecord, available int) *time.Time {
	if len(kept) == 0 || available <= len(kept) {
		return nil
	}
	oldest := kept[len(kept)-1]
	if !hasRealTime(oldest) {
		return nil
	}
	floor := oldest.ObservedAt
	return &floor
}

// TrimBelowFloor drops timestamped records older than the backfill floor:
// sources keep re-serving rows a capped backfill deliberately left out (a long
// RSS page, a whole CSV file), and those must not leak back in. Records
// without a real timestamp, and everything when there is no floor, are kept;
// the database deduplicates them.
func TrimBelowFloor(records []RawRecord, floor *time.Time) []RawRecord {
	if floor == nil {
		return records
	}
	kept := records[:0:0]
	for _, record := range records {
		if hasRealTime(record) && record.ObservedAt.Before(*floor) {
			continue
		}
		kept = append(kept, record)
	}
	return kept
}
