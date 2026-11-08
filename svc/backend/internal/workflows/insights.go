package workflows

import (
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"slices"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/narration"
)

// InsightRow is one statistical card, ready to insert.
type InsightRow struct {
	FindingKey   string
	Kind         string // correlation | anomaly | custom
	Headline     string
	Explanation  string
	Caveats      []string
	Stats        map[string]any  // Finding.Stats as-is
	ChartSpec    json.RawMessage // nil: no chart
	NarratedBy   string
	Plausibility *string // nil for template text
}

// InsightBuilder turns an analysis result into card rows: it reuses earlier
// LLM text, narrates the most important new findings in one call, and gives
// the rest template text.
type InsightBuilder struct {
	Store         InsightStore
	Narrator      *narration.Narrator
	MaxNarrations int
	// Chart turns a finding's chart into Vega-Lite. Owned by workstream A
	// (internal/charts); nil until then, and the card has no chart.
	Chart func(contracts.Chart) (json.RawMessage, error)
}

type reuseKey struct{ findingKey, direction string }

// Build never fails. If a store read fails, it narrates everything it can and
// uses templates for the rest. Rows are in finding_key order.
func (b *InsightBuilder) Build(ctx context.Context, projectID uuid.UUID, c narration.Context, res contracts.AnalysisResult) []InsightRow {
	if len(res.Findings) == 0 {
		return nil
	}
	prev := b.previous(ctx, projectID, res.Findings)

	texts := make(map[string]narration.Text, len(res.Findings))
	var fresh []contracts.Finding
	for _, f := range res.Findings {
		if p, ok := prev[reuseKey{f.FindingKey, narration.Direction(f)}]; ok {
			texts[f.FindingKey] = narration.Text{
				Headline: p.Headline, Explanation: p.Explanation, Caveats: p.Caveats,
				NarratedBy: "llm", Plausibility: p.Plausibility,
			}
			continue
		}
		fresh = append(fresh, f)
	}

	rank(fresh)
	limit := max(b.MaxNarrations, 0)
	toNarrate, rest := fresh[:min(limit, len(fresh))], fresh[min(limit, len(fresh)):]
	if len(toNarrate) > 0 {
		for k, t := range b.Narrator.Narrate(ctx, c, toNarrate) {
			texts[k] = t
		}
	}
	for _, f := range rest {
		texts[f.FindingKey] = narration.Template(c, f)
	}

	rows := make([]InsightRow, 0, len(res.Findings))
	for _, f := range res.Findings {
		t := texts[f.FindingKey]
		rows = append(rows, InsightRow{
			FindingKey: f.FindingKey, Kind: f.Kind, Headline: t.Headline, Explanation: t.Explanation,
			Caveats: t.Caveats, Stats: f.Stats, ChartSpec: b.chart(f),
			NarratedBy: t.NarratedBy, Plausibility: t.Plausibility,
		})
	}
	slices.SortFunc(rows, func(a, b InsightRow) int { return cmp.Compare(a.FindingKey, b.FindingKey) })
	return rows
}

// previous returns the newest LLM-written text for each (finding_key,
// direction). A failed read means every finding is treated as new.
func (b *InsightBuilder) previous(ctx context.Context, projectID uuid.UUID, fs []contracts.Finding) map[reuseKey]PrevText {
	out := map[reuseKey]PrevText{}
	if b.Store == nil {
		return out
	}
	keys := make([]string, len(fs))
	for i, f := range fs {
		keys[i] = f.FindingKey
	}
	rows, err := b.Store.PreviousTexts(ctx, projectID, keys)
	if err != nil {
		slog.Warn("could not read earlier insight texts; narrating everything", "project", projectID, "err", err)
		return out
	}
	for _, p := range rows { // newest first
		k := reuseKey{p.FindingKey, narration.Direction(contracts.Finding{Kind: p.Kind, Stats: p.Stats})}
		if _, seen := out[k]; !seen {
			out[k] = p
		}
	}
	return out
}

func (b *InsightBuilder) chart(f contracts.Finding) json.RawMessage {
	if b.Chart == nil || f.Chart == nil {
		return nil
	}
	spec, err := b.Chart(*f.Chart)
	if err != nil {
		slog.Warn("could not build chart", "finding", f.FindingKey, "err", err)
		return nil
	}
	return spec
}

// rank orders findings by importance: tests first by |effect|, then anomalies
// by |score| (the scales differ, so they are never mixed), ties by key.
func rank(fs []contracts.Finding) {
	slices.SortFunc(fs, func(a, b contracts.Finding) int {
		if ta, tb := a.Kind != "anomaly", b.Kind != "anomaly"; ta != tb {
			if ta {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(magnitude(b), magnitude(a)); c != 0 {
			return c
		}
		return cmp.Compare(a.FindingKey, b.FindingKey)
	})
}

func magnitude(f contracts.Finding) float64 {
	name := "effect"
	if f.Kind == "anomaly" {
		name = "score"
	}
	v, _ := narration.Number(f.Stats[name])
	if v < 0 {
		return -v
	}
	return v
}

// narrationContext builds the narrator's view of the project from what the
// author or the run loop already holds. Series come from the analysis health;
// each series' description is its source profile's summary.
func narrationContext(goal, insightPrompt string, sources []AuthorSource, res contracts.AnalysisResult) narration.Context {
	summaries := make(map[string]string, len(sources))
	for _, s := range sources {
		summaries[s.Key] = s.Profile.Summary
	}
	series := make(map[string]narration.SeriesContext, len(res.Health.Series))
	for _, s := range res.Health.Series {
		series[s.ID] = narration.SeriesContext{
			Label: s.Label, Unit: s.Unit, Grain: s.Grain, SourceSummary: summaries[s.Source],
		}
	}
	return narration.Context{Goal: goal, InsightPrompt: insightPrompt, Series: series}
}
