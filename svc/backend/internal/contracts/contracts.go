// Package contracts holds the shared JSON contracts. Frozen after M0:
// propose changes rather than making them unilaterally. The author's Python
// script produces ScriptResult as plain JSON; Go validates it here, so there
// is no Python-side mirror of these types.
package contracts

import (
	"encoding/json"
	"time"
)

// ---- Source profiling ----

type FieldProfile struct {
	Path        string  `json:"path"`
	Role        string  `json:"role" jsonschema:"enum=timestamp,enum=measure,enum=category,enum=text,enum=id,enum=ignore"`
	Unit        *string `json:"unit"`
	Aggregation *string `json:"aggregation" jsonschema:"enum=mean,enum=sum,enum=last,enum=count"`
	Description string  `json:"description"`
}

type SourceProfile struct {
	Fields        []FieldProfile `json:"fields"`
	TimestampPath *string        `json:"timestamp_path"`
	Summary       string         `json:"summary"`
}

// ---- Statistical workflow ----

// WorkflowDraft is the author's output: a pandas script that the Python runner
// executes against INPUT, plus narrator guidance. It is the only new
// LLM-facing type in this area, and it has no maps (llm.SchemaFor panics on
// them).
type WorkflowDraft struct {
	Script        string `json:"script"`         // Python; reads INPUT, sets result
	InsightPrompt string `json:"insight_prompt"` // what the user cares about, for the narrator
	Rationale     string `json:"rationale"`      // why these series and tests; shown in the UI
}

// ---- Script result (what the author's script puts in `result`) ----

type SeriesInfo struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Source  string  `json:"source"` // key in INPUT.sources
	Unit    *string `json:"unit"`
	Grain   string  `json:"grain"`   // hour | day | week
	Periods int     `json:"periods"` // periods in the analysed window
	Points  int     `json:"points"`  // periods with a value
}

type Chart struct {
	Type   string           `json:"type"`   // dual_line | lag_scatter | series_line | anomaly_band
	Points []map[string]any `json:"points"` // <= 500
}

type Test struct {
	Key    string         `json:"key"`
	Kind   string         `json:"kind"` // correlation | custom
	Series []string       `json:"series"`
	P      float64        `json:"p"`
	Effect float64        `json:"effect"`
	N      int            `json:"n"`
	Stats  map[string]any `json:"stats"`
	Chart  *Chart         `json:"chart"`
}

type Anomaly struct {
	Key      string    `json:"key"`
	Series   string    `json:"series"`
	Period   time.Time `json:"period"`
	Value    float64   `json:"value"`
	Expected float64   `json:"expected"`
	Score    float64   `json:"score"`
	Chart    *Chart    `json:"chart"`
}

type ScriptResult struct {
	Series    []SeriesInfo `json:"series"`
	Tests     []Test       `json:"tests"`
	Anomalies []Anomaly    `json:"anomalies"`
}

// ---- Analysis result (stored in runs.result) ----

// ScriptResult and AnalysisResult are never passed to llm.SchemaFor.

type SourceHealth struct {
	Records    int       `json:"records"`
	FirstAt    time.Time `json:"first_at"`
	LastAt     time.Time `json:"last_at"`
	MedianGapS float64   `json:"median_gap_s"`
}

type Health struct {
	Series         []SeriesInfo            `json:"series"`
	Sources        map[string]SourceHealth `json:"sources"`         // source_id ->
	UnprofiledKeys map[string][]string     `json:"unprofiled_keys"` // source_id ->
}

type Finding struct {
	FindingKey string         `json:"finding_key"`
	Kind       string         `json:"kind"` // correlation | anomaly | custom
	Series     []string       `json:"series"`
	Stats      map[string]any `json:"stats"`
	Chart      *Chart         `json:"chart"`
}

type AnalysisResult struct {
	DataUpTo time.Time      `json:"data_up_to"`
	Findings []Finding      `json:"findings"`
	Funnel   map[string]int `json:"funnel"` // tests_run, raw_p_lt_05, passed_fdr, kept
	Rejected []Finding      `json:"rejected"`
	Health   Health         `json:"health"`
}

// ---- Narration (statistical findings) ----

type InsightDraft struct {
	FindingKey  string   `json:"finding_key"`
	Headline    string   `json:"headline"`
	Explanation string   `json:"explanation"`
	Direction   string   `json:"direction" jsonschema:"enum=positive,enum=negative,enum=up,enum=down"`
	Leader      *string  `json:"leader"`
	Caveats     []string `json:"caveats"`
}

type Narration struct {
	Insights []InsightDraft `json:"insights"`
}

// ---- Investigation workflow ----

// Claim points at one number in a recorded step's output.
type Claim struct {
	Seq  int    `json:"seq"`  // workflow_steps.seq this number comes from
	Path string `json:"path"` // dotted path into that step's output, e.g. "lags.3.rho"
}

type InvestigationFinding struct {
	Headline    string   `json:"headline"`
	Explanation string   `json:"explanation"`
	Claims      []Claim  `json:"claims"`
	Caveats     []string `json:"caveats"`
	ChartSeq    *int     `json:"chart_seq"`  // step whose output the chart is built from
	ChartPath   *string  `json:"chart_path"` // dotted path to the chart object in that step's output
}

type InvestigationReport struct {
	Rationale string                 `json:"rationale"`
	Findings  []InvestigationFinding `json:"findings"`
}

// StepOutput is one replayed step.
type StepOutput struct {
	Seq    int             `json:"seq"`
	Output json.RawMessage `json:"output"`
}

// ReplayResult is returned by the worker's POST /replay.
// NOTE: not specified field-by-field in the plan; proposed shape.
type ReplayResult struct {
	DataUpTo time.Time    `json:"data_up_to"`
	Steps    []StepOutput `json:"steps"` // one output per recorded step
}

// ---- Reviewer ----

type ReviewDecision struct {
	Decision       string `json:"decision" jsonschema:"enum=keep,enum=regenerate"`
	ReasonCategory string `json:"reason_category" jsonschema:"enum=none,enum=schema_drift,enum=stale_series,enum=coverage,enum=effect_faded,enum=explanation_mismatch,enum=new_scope"`
	Reason         string `json:"reason"` // one line, shown in the workflow history
}
