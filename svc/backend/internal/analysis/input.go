package analysis

import (
	"context"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/contracts"
)

// SourceMeta is a data source as the analysis sees it.
type SourceMeta struct {
	ID        uuid.UUID
	Name      string
	Kind      string
	Summary   string
	Profile   contracts.SourceProfile
	CreatedAt time.Time
}

// Record is one data_records row.
type Record struct {
	ExternalID string
	ObservedAt time.Time
	Payload    map[string]any
}

// Input is what the script sees as the global INPUT. It is a struct so the
// serialised field order is fixed; maps are keyed by source key and marshal
// sorted.
type Input struct {
	DataUpTo time.Time              `json:"data_up_to"`
	Sources  map[string]InputSource `json:"sources"`
}

type InputSource struct {
	ID      string                   `json:"id"`
	Name    string                   `json:"name"`
	Kind    string                   `json:"kind"`
	Summary string                   `json:"summary"`
	Fields  []contracts.FieldProfile `json:"fields"`
	Records []InputRecord            `json:"records"`
}

type InputRecord struct {
	ObservedAt time.Time      `json:"observed_at"`
	Payload    map[string]any `json:"payload"`
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slug lowercases a name, replaces each run of non-alphanumerics with "_" and
// trims "_"; an empty result becomes "source".
func slug(name string) string {
	s := strings.Trim(nonAlnum.ReplaceAllString(strings.ToLower(name), "_"), "_")
	if s == "" {
		return "source"
	}
	return s
}

// sortedByCreation returns the sources ordered by (created_at, id).
func sortedByCreation(sources []SourceMeta) []SourceMeta {
	s := append([]SourceMeta(nil), sources...)
	sort.SliceStable(s, func(i, j int) bool {
		if !s[i].CreatedAt.Equal(s[j].CreatedAt) {
			return s[i].CreatedAt.Before(s[j].CreatedAt)
		}
		return s[i].ID.String() < s[j].ID.String()
	})
	return s
}

// SourceKeys names each source with a script-friendly key. Sources are taken in
// (created_at, id) order; the first with a given slug keeps it and later ones
// get _2, _3 and so on. A key stays stable as long as earlier sources are not
// removed; if one is, the reviewer's sources_changed rule regenerates the
// script anyway.
func SourceKeys(sources []SourceMeta) map[uuid.UUID]string {
	keys := make(map[uuid.UUID]string, len(sources))
	used := map[string]bool{}
	for _, s := range sortedByCreation(sources) {
		base := slug(s.Name)
		key := base
		for n := 2; used[key]; n++ {
			key = base + "_" + strconv.Itoa(n)
		}
		used[key] = true
		keys[s.ID] = key
	}
	return keys
}

// BuildInput assembles INPUT. Records are sorted by (observed_at, external_id)
// and limited to [dataUpTo − history, dataUpTo] even if the store returned
// more. Sources with no records are still present, with an empty list.
func BuildInput(dataUpTo time.Time, history time.Duration, sources []SourceMeta, recs map[uuid.UUID][]Record) Input {
	keys := SourceKeys(sources)
	from := dataUpTo.Add(-history)
	in := Input{DataUpTo: dataUpTo, Sources: make(map[string]InputSource, len(sources))}
	for _, s := range sources {
		rs := make([]Record, 0, len(recs[s.ID]))
		for _, r := range recs[s.ID] {
			if r.ObservedAt.Before(from) || r.ObservedAt.After(dataUpTo) {
				continue
			}
			rs = append(rs, r)
		}
		sort.SliceStable(rs, func(i, j int) bool {
			if !rs[i].ObservedAt.Equal(rs[j].ObservedAt) {
				return rs[i].ObservedAt.Before(rs[j].ObservedAt)
			}
			return rs[i].ExternalID < rs[j].ExternalID
		})
		out := make([]InputRecord, len(rs))
		for i, r := range rs {
			p := r.Payload
			if p == nil {
				p = map[string]any{}
			}
			out[i] = InputRecord{ObservedAt: r.ObservedAt.UTC(), Payload: p}
		}
		fields := s.Profile.Fields
		if fields == nil {
			fields = []contracts.FieldProfile{}
		}
		in.Sources[keys[s.ID]] = InputSource{
			ID: s.ID.String(), Name: s.Name, Kind: s.Kind, Summary: s.Summary, Fields: fields, Records: out,
		}
	}
	return in
}

// Watermark is the newest observed_at across the project's sources; false
// means there are no records yet.
func Watermark(ctx context.Context, s Store, projectID uuid.UUID) (time.Time, bool, error) {
	t, err := s.MaxObservedAt(ctx, projectID)
	if err != nil || t == nil {
		return time.Time{}, false, err
	}
	return t.UTC(), true, nil
}
