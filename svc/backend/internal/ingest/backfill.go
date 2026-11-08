package ingest

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// ErrInvalidBackfillRows is returned for a negative backfill target.
var ErrInvalidBackfillRows = errors.New("backfill_rows must not be negative (0 selects the default)")

// MaxBackfillRows is the largest storable target (the column is an integer).
// Paging caps, not this number, bound how much work a backfill does.
const MaxBackfillRows = math.MaxInt32

// ErrBackfillRowsTooLarge is returned for a target above MaxBackfillRows.
var ErrBackfillRowsTooLarge = fmt.Errorf("backfill_rows must be at most %d", MaxBackfillRows)

// ResolveBackfillRows turns a requested backfill target into the one to
// store: 0 selects serverDefault (DefaultBackfillRows when that is unset),
// negative values are rejected. API handlers call this; the store only keeps
// a last-resort fallback for direct callers such as the seed command.
func ResolveBackfillRows(requested, serverDefault int) (int, error) {
	if requested < 0 {
		return 0, ErrInvalidBackfillRows
	}
	if requested > MaxBackfillRows {
		return 0, ErrBackfillRowsTooLarge
	}
	if requested > 0 {
		return requested, nil
	}
	if serverDefault > 0 {
		return serverDefault, nil
	}
	return DefaultBackfillRows, nil
}

// SortNewestFirst orders records with a real timestamp newest first, followed
// by records whose time was only estimated (they would otherwise all sort as
// "now" and crowd out genuinely recent rows), keeping the adapter's order for
// ties and among undated records.
func SortNewestFirst(records []RawRecord) {
	sort.SliceStable(records, func(i, j int) bool {
		ri, rj := hasRealTime(records[i]), hasRealTime(records[j])
		if ri != rj {
			return ri
		}
		return ri && records[i].ObservedAt.After(records[j].ObservedAt)
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
// timestamp.
func recordsHaveTimes(records []RawRecord) bool {
	for _, record := range records {
		if hasRealTime(record) {
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

// Dropped returns the ids of records that a backfill cap left out: every
// unique id in all that is not in kept.
func Dropped(all, kept []RawRecord) []string {
	keep := make(map[string]bool, len(kept))
	for _, record := range kept {
		keep[record.ExternalID] = true
	}
	var dropped []string
	for _, record := range CapRecords(all, 0) {
		if !keep[record.ExternalID] {
			dropped = append(dropped, record.ExternalID)
		}
	}
	return dropped
}

// WithoutIDs returns records whose ids are not in skip.
func WithoutIDs(records []RawRecord, skip map[string]bool) []RawRecord {
	if len(skip) == 0 {
		return records
	}
	kept := records[:0:0]
	for _, record := range records {
		if !skip[record.ExternalID] {
			kept = append(kept, record)
		}
	}
	return kept
}
