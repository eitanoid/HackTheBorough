package investigate

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"hacktheburgh/backend/internal/charts"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/insightrows"
)

// ChartBuilder turns a chart into Vega-Lite (charts.Build in production).
// Nil means no card has a chart.
type ChartBuilder func(contracts.Chart, charts.Meta) (json.RawMessage, error)

// Evidence is one claimed number on a card, with where it came from.
type Evidence struct {
	Seq   int       `json:"seq"`
	Path  string    `json:"path"`
	Label string    `json:"label"`
	Value any       `json:"value"`
	AsOf  time.Time `json:"as_of"`
}

// CitedSeqs is every step a claim or a chart points at, ascending and unique.
func CitedSeqs(r contracts.InvestigationReport) []int {
	seen := map[int]bool{}
	for _, f := range r.Findings {
		for _, c := range f.Claims {
			seen[c.Seq] = true
		}
		if f.ChartSeq != nil {
			seen[*f.ChartSeq] = true
		}
	}
	out := make([]int, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}

// FirstRun builds the result of a generation's first run from the recorded
// outputs of the cited steps, so no code is re-executed.
func FirstRun(r contracts.InvestigationReport, steps []Step, dataUpTo time.Time) contracts.ReplayResult {
	bySeq := make(map[int]Step, len(steps))
	for _, s := range steps {
		bySeq[s.Seq] = s
	}
	res := contracts.ReplayResult{DataUpTo: dataUpTo, Steps: []contracts.StepOutput{}}
	for _, seq := range CitedSeqs(r) {
		if s, ok := bySeq[seq]; ok {
			res.Steps = append(res.Steps, contracts.StepOutput{Seq: seq, Output: s.Output, AsOf: dataUpTo})
		}
	}
	return res
}

// lookup finds the freshest output of a step in which a path resolves: this
// run's output first, then the previous run's.
type lookup struct {
	cur, prev map[int]contracts.StepOutput
	dataUpTo  time.Time
}

func newLookup(cur contracts.ReplayResult, prev *contracts.ReplayResult) lookup {
	l := lookup{cur: map[int]contracts.StepOutput{}, prev: map[int]contracts.StepOutput{}, dataUpTo: cur.DataUpTo}
	for _, s := range cur.Steps {
		l.cur[s.Seq] = s
	}
	if prev != nil {
		for _, s := range prev.Steps {
			l.prev[s.Seq] = s
		}
	}
	return l
}

// find returns the step output that resolves path, and whether it is older
// than this run (carried).
func (l lookup) find(seq int, resolves func(json.RawMessage) bool) (contracts.StepOutput, bool, bool) {
	for _, m := range []map[int]contracts.StepOutput{l.cur, l.prev} {
		if s, ok := m[seq]; ok && len(s.Output) > 0 && resolves(s.Output) {
			return s, s.AsOf.Before(l.dataUpTo), true
		}
	}
	return contracts.StepOutput{}, false, false
}

// Cards builds one investigation card per finding. Numbers come from the
// replayed outputs; where a path no longer resolves (or the step couldn't be
// recomputed) the value is carried from the previous run and the card says so.
// The text never changes between replays.
func Cards(r contracts.InvestigationReport, version int, replay contracts.ReplayResult, prev *contracts.ReplayResult, build ChartBuilder) []insightrows.InsightRow {
	l := newLookup(replay, prev)
	rows := make([]insightrows.InsightRow, 0, len(r.Findings))
	for i, f := range r.Findings {
		stats := map[string]any{}
		evidence := []Evidence{}
		var carriedFrom time.Time
		note := func(asOf time.Time) {
			if carriedFrom.IsZero() || asOf.Before(carriedFrom) {
				carriedFrom = asOf
			}
		}
		for _, c := range f.Claims {
			var value any
			s, carried, ok := l.find(c.Seq, func(o json.RawMessage) bool {
				v, ok := Resolve(o, c.Path)
				if !ok {
					return false
				}
				value, ok = ClaimValue(v)
				return ok
			})
			if !ok {
				continue // neither run has it: left off the card
			}
			if carried {
				note(s.AsOf)
			}
			stats[c.Label] = value
			evidence = append(evidence, Evidence{Seq: c.Seq, Path: c.Path, Label: c.Label, Value: value, AsOf: s.AsOf})
		}

		var chart json.RawMessage
		if f.ChartSeq != nil && f.ChartPath != nil && build != nil {
			var parsed invChart
			s, carried, ok := l.find(*f.ChartSeq, func(o json.RawMessage) bool {
				c, ok := parseChart(o, *f.ChartPath)
				parsed = c
				return ok
			})
			if ok {
				spec, err := build(parsed.Chart, parsed.Meta())
				if err != nil {
					slog.Warn("could not build investigation chart", "finding", i, "err", err)
				} else {
					chart = spec
					if carried {
						note(s.AsOf)
					}
				}
			}
		}

		caveats := append([]string{}, f.Caveats...)
		if !carriedFrom.IsZero() {
			caveats = append(caveats, fmt.Sprintf("Couldn't recompute on the latest data; some figures are from %s.", carriedFrom.UTC().Format("2 Jan 2006")))
		}
		rows = append(rows, insightrows.InsightRow{
			Origin: insightrows.OriginInvestigation, FindingKey: fmt.Sprintf("inv:v%d:%d", version, i), Kind: "observation",
			Headline: f.Headline, Explanation: f.Explanation, Caveats: caveats,
			Stats: stats, Evidence: evidence, ChartSpec: chart, NarratedBy: "llm",
		})
	}
	return rows
}
