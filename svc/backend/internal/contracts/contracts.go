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

// ---- Analysis spec (statistical workflow) ----

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

type Pair struct {
	A string `json:"a"`
	B string `json:"b"`
}

type AnalysisSpec struct {
	Grain      string      `json:"grain" jsonschema:"enum=hour,enum=day,enum=week"`
	WindowDays int         `json:"window_days"` // 7..730
	Series     []SeriesDef `json:"series"`      // 2..12
	Pairs      []Pair      `json:"pairs"`       // empty = all pairs
	MaxLag     int         `json:"max_lag"`     // 0..14, in grain units
	Tests      []string    `json:"tests"`       // lagged_correlation | anomaly
}

type WorkflowDraft struct {
	AnalysisSpec  AnalysisSpec `json:"analysis_spec"`
	InsightPrompt string       `json:"insight_prompt"`
	Rationale     string       `json:"rationale"`
}

// ---- Analysis result (from the stats worker) ----

// Health is what the reviewer reads about a statistical run.
type Health struct {
	SkippedSeries  []string            `json:"skipped_series"`
	StaleSeries    []string            `json:"stale_series"`
	Coverage       map[string]float64  `json:"coverage"`
	LagsAtBoundary []string            `json:"lags_at_boundary"`
	UnprofiledKeys map[string][]string `json:"unprofiled_keys"` // source_id -> payload keys missing from its profile
	SpanPeriods    int                 `json:"span_periods"`
}

type Finding struct {
	FindingKey  string         `json:"finding_key"`
	Kind        string         `json:"kind"` // correlation | anomaly
	Series      []string       `json:"series"`
	Stats       map[string]any `json:"stats"`
	PeriodStart time.Time      `json:"period_start"`
	PeriodEnd   time.Time      `json:"period_end"`
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
