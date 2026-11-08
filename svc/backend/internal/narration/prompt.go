package narration

import (
	_ "embed"
	"encoding/json"

	"hacktheburgh/backend/internal/contracts"
)

//go:embed prompts/narrator_system.md
var narratorSystem string

func systemPrompt() string { return narratorSystem }

type promptSeries struct {
	ID     string  `json:"id"`
	Label  string  `json:"label"`
	Unit   *string `json:"unit"`
	Grain  string  `json:"grain"`
	Source string  `json:"source,omitempty"`
}

type promptFinding struct {
	FindingKey string         `json:"finding_key"`
	Kind       string         `json:"kind"`
	Direction  string         `json:"direction"`
	Leader     *string        `json:"leader"`
	Series     []promptSeries `json:"series"`
	Stats      map[string]any `json:"stats"`
}

type promptInput struct {
	Goal     string          `json:"goal"`
	Focus    string          `json:"focus"`
	Findings []promptFinding `json:"findings"`
}

// userPrompt is one JSON object. The model sees no raw records, only stats.
func userPrompt(c Context, fs []contracts.Finding) string {
	in := promptInput{Goal: c.Goal, Focus: c.InsightPrompt, Findings: make([]promptFinding, 0, len(fs))}
	for _, f := range fs {
		pf := promptFinding{
			FindingKey: f.FindingKey, Kind: f.Kind, Direction: Direction(f),
			Series: make([]promptSeries, 0, len(f.Series)), Stats: f.Stats,
		}
		if l := Leader(f); l != nil {
			label := c.label(*l)
			pf.Leader = &label
		}
		for _, id := range f.Series {
			s := c.Series[id]
			pf.Series = append(pf.Series, promptSeries{ID: id, Label: c.label(id), Unit: s.Unit, Grain: s.Grain, Source: s.SourceSummary})
		}
		in.Findings = append(in.Findings, pf)
	}
	b, _ := json.MarshalIndent(in, "", "  ")
	return string(b)
}
