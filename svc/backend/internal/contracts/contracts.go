// Package contracts holds the shared JSON contracts. Frozen after M0:
// propose changes rather than making them unilaterally. The Python stats
// worker mirrors these in stats/spec.py.
package contracts

import "time"

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

// ---- Analysis spec ----

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
	Tests      []string    `json:"tests"`       // lagged_correlation | anomaly | trend_change
}

type WorkflowDraft struct {
	AnalysisSpec  AnalysisSpec `json:"analysis_spec"`
	InsightPrompt string       `json:"insight_prompt"`
	Rationale     string       `json:"rationale"`
}

// ---- Analysis result (from the stats worker) ----

type Finding struct {
	FindingKey  string         `json:"finding_key"`
	Kind        string         `json:"kind"` // correlation | anomaly | trend_change
	Series      []string       `json:"series"`
	Stats       map[string]any `json:"stats"`
	PeriodStart time.Time      `json:"period_start"`
	PeriodEnd   time.Time      `json:"period_end"`
}

type AnalysisResult struct {
	DataUpTo  time.Time                   `json:"data_up_to"`
	Findings  []Finding                   `json:"findings"`
	Funnel    map[string]int              `json:"funnel"`
	Skipped   []string                    `json:"skipped"`
	Rejected  []Finding                   `json:"rejected"`
	ChartData map[string][]map[string]any `json:"chart_data"`
}

// ---- Narration ----

type InsightDraft struct {
	FindingKey  string   `json:"finding_key"`
	Headline    string   `json:"headline"`
	Explanation string   `json:"explanation"`
	Direction   string   `json:"direction" jsonschema:"enum=positive,enum=negative,enum=up,enum=down"`
	Leader      *string  `json:"leader"`
	Caveats     []string `json:"caveats"`
	Chart       string   `json:"chart" jsonschema:"enum=dual_line,enum=lag_scatter,enum=anomaly_band,enum=trend_split"`
}

type Narration struct {
	Insights []InsightDraft `json:"insights"`
}

// ---- Review ----

type ReviewItem struct {
	FindingKey string        `json:"finding_key"`
	Decision   string        `json:"decision" jsonschema:"enum=keep,enum=revise"`
	Reason     string        `json:"reason"`   // one line, shown on the card
	Revision   *InsightDraft `json:"revision"` // required when decision = revise
}

type Review struct {
	Items []ReviewItem `json:"items"`
}
