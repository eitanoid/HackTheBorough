// Package contracts holds the shared JSON contracts. Frozen after M0:
// propose changes rather than making them unilaterally. The Python stats
// worker mirrors these in stats/spec.py.
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

// ---- Series definitions and statistical workflow ----

type Filter struct {
	Path   string `json:"path"`
	Equals string `json:"equals"`
}

type SeriesDef struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	SourceID string   `json:"source_id"`
	Field    *string  `json:"field"`
	Agg      string   `json:"agg" jsonschema:"enum=mean,enum=sum,enum=last,enum=count"`
	Filters  []Filter `json:"filters"`
	Keywords []string `json:"keywords"`
	Unit     *string  `json:"unit"`
}

// WorkflowDraft is the author's output: a Python script run by the stats
// worker against its stats library, plus narrator guidance.
type WorkflowDraft struct {
	Script        string `json:"script"`         // Python using the stats library
	InsightPrompt string `json:"insight_prompt"` // what the user cares about, for the narrator
	Rationale     string `json:"rationale"`      // why these series and tests; shown in the UI
}

// ---- Analysis result (from the stats worker) ----

// Health is what the reviewer reads about a statistical run.
type Health struct {
	LoadedSeries   map[string]string   `json:"loaded_series"` // series id -> source_id, from load_series calls
	SkippedSeries  []string            `json:"skipped_series"`
	StaleSeries    []string            `json:"stale_series"`
	Coverage       map[string]float64  `json:"coverage"`
	LagsAtBoundary []string            `json:"lags_at_boundary"`
	UnprofiledKeys map[string][]string `json:"unprofiled_keys"` // source_id -> payload keys missing from its profile
	SpanPeriods    int                 `json:"span_periods"`
}

type Finding struct {
	FindingKey string         `json:"finding_key"`
	Kind       string         `json:"kind"` // correlation | anomaly | custom
	Series     []string       `json:"series"`
	Stats      map[string]any `json:"stats"`
	// StabilityChecked is false for custom tests (no half-window check).
	StabilityChecked bool      `json:"stability_checked"`
	PeriodStart      time.Time `json:"period_start"`
	PeriodEnd        time.Time `json:"period_end"`
}

type AnalysisResult struct {
	DataUpTo  time.Time                   `json:"data_up_to"`
	Findings  []Finding                   `json:"findings"`
	Funnel    map[string]int              `json:"funnel"`
	Rejected  []Finding                   `json:"rejected"`
	ChartData map[string][]map[string]any `json:"chart_data"`
	Health    Health                      `json:"health"`
}

// ---- Narration (statistical findings) ----

type InsightDraft struct {
	FindingKey  string   `json:"finding_key"`
	Headline    string   `json:"headline"`
	Explanation string   `json:"explanation"`
	Direction   string   `json:"direction" jsonschema:"enum=positive,enum=negative,enum=up,enum=down"`
	Leader      *string  `json:"leader"`
	Caveats     []string `json:"caveats"`
	Chart       string   `json:"chart" jsonschema:"enum=dual_line,enum=lag_scatter,enum=anomaly_band"`
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
	Chart       string   `json:"chart" jsonschema:"enum=dual_line,enum=lag_scatter,enum=series_line"`
	ChartSeq    int      `json:"chart_seq"` // step whose output the chart is built from
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
	ReasonCategory string `json:"reason_category" jsonschema:"enum=none,enum=schema_drift,enum=stale_series,enum=coverage,enum=lag_boundary,enum=effect_faded,enum=explanation_mismatch,enum=new_scope"`
	Reason         string `json:"reason"` // one line, shown in the workflow history
}
