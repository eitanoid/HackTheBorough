package analysis

import (
	"sort"

	"hacktheburgh/backend/internal/contracts"
)

func sourceHealth(recs []InputRecord) contracts.SourceHealth {
	h := contracts.SourceHealth{Records: len(recs)}
	if len(recs) == 0 {
		return h
	}
	h.FirstAt, h.LastAt = recs[0].ObservedAt, recs[len(recs)-1].ObservedAt
	if len(recs) < 2 {
		return h
	}
	gaps := make([]float64, len(recs)-1)
	for i := 1; i < len(recs); i++ {
		gaps[i-1] = recs[i].ObservedAt.Sub(recs[i-1].ObservedAt).Seconds()
	}
	sort.Float64s(gaps)
	if n := len(gaps); n%2 == 1 {
		h.MedianGapS = gaps[n/2]
	} else {
		h.MedianGapS = (gaps[n/2-1] + gaps[n/2]) / 2
	}
	return h
}

// payloadPaths adds the dotted path of every leaf in v to out. Nested objects
// extend the path; anything else (including arrays and empty objects) is a leaf.
func payloadPaths(prefix string, v map[string]any, out map[string]bool) {
	for k, x := range v {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		if nested, ok := x.(map[string]any); ok && len(nested) > 0 {
			payloadPaths(p, nested, out)
			continue
		}
		out[p] = true
	}
}

func unprofiledKeys(src InputSource) []string {
	profiled := make(map[string]bool, len(src.Fields))
	for _, f := range src.Fields {
		profiled[f.Path] = true
	}
	seen := map[string]bool{}
	for _, r := range src.Records {
		payloadPaths("", r.Payload, seen)
	}
	var out []string
	for p := range seen {
		if !profiled[p] {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func buildHealth(in Input, series []contracts.SeriesInfo) contracts.Health {
	h := contracts.Health{
		Series:         append([]contracts.SeriesInfo{}, series...),
		Sources:        make(map[string]contracts.SourceHealth, len(in.Sources)),
		UnprofiledKeys: map[string][]string{},
	}
	sort.SliceStable(h.Series, func(i, j int) bool { return h.Series[i].ID < h.Series[j].ID })
	for _, src := range in.Sources {
		h.Sources[src.ID] = sourceHealth(src.Records)
		if keys := unprofiledKeys(src); len(keys) > 0 {
			h.UnprofiledKeys[src.ID] = keys
		}
	}
	return h
}
