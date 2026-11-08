package investigate

import (
	"encoding/json"
	"strings"

	"hacktheburgh/backend/internal/charts"
	"hacktheburgh/backend/internal/contracts"
)

// Step is one recorded tool call.
type Step struct {
	Seq    int
	Tool   string // describe_source | run_python
	Input  json.RawMessage
	Output json.RawMessage
}

// Failed reports whether the step's code raised, timed out or was rejected
// after running: such a step has an `error` string or `timed_out` set.
func (s Step) Failed() bool {
	_, failed := failedReason(s.Output)
	return failed
}

// invChart is the chart object at a finding's chart_path: the Chart shape plus
// optional display metadata for charts.Build. It is read here, not a contract.
type invChart struct {
	contracts.Chart
	Series []charts.Axis `json:"series"` // [0] is a / x, [1] is b / y
	Grain  string        `json:"grain"`  // default "day"
	Lag    *int          `json:"lag"`
}

// parseChart reads the chart at path in the output and validates it.
func parseChart(output json.RawMessage, path string) (invChart, bool) {
	var c invChart
	v, ok := Resolve(output, path)
	if !ok {
		return c, false
	}
	if _, isObj := v.(map[string]any); !isObj {
		return c, false
	}
	b, err := json.Marshal(v)
	if err != nil || json.Unmarshal(b, &c) != nil {
		return c, false
	}
	if c.Validate() != nil {
		return c, false
	}
	return c, true
}

// Meta is what charts.Build needs besides the points.
func (c invChart) Meta() charts.Meta {
	grain := c.Grain
	if grain == "" {
		grain = "day"
	}
	return charts.Meta{Series: c.Series, Grain: grain, Lag: c.Lag}
}

// Clean drops what the recorded outputs don't back up: claims whose step is
// missing or failed, whose path doesn't resolve to a claimable value, or whose
// label is empty or repeated; charts that don't resolve to a valid chart; and
// findings left with nothing to show. At most MaxInvestigationFindings are
// kept, in report order. A report with no findings left is still returned.
func Clean(r contracts.InvestigationReport, steps []Step) contracts.InvestigationReport {
	bySeq := make(map[int]Step, len(steps))
	for _, s := range steps {
		bySeq[s.Seq] = s
	}
	out := contracts.InvestigationReport{Rationale: r.Rationale, Findings: []contracts.InvestigationFinding{}}
	for _, f := range r.Findings {
		if len(out.Findings) >= contracts.MaxInvestigationFindings {
			break
		}
		if strings.TrimSpace(f.Headline) == "" {
			continue
		}
		kept := f
		kept.Claims = nil
		labels := map[string]bool{}
		for _, c := range f.Claims {
			label := strings.TrimSpace(c.Label)
			s, ok := bySeq[c.Seq]
			if !ok || s.Failed() || label == "" || labels[label] {
				continue
			}
			v, ok := Resolve(s.Output, c.Path)
			if !ok {
				continue
			}
			if _, ok := ClaimValue(v); !ok {
				continue
			}
			labels[label] = true
			c.Label = label
			kept.Claims = append(kept.Claims, c)
		}
		kept.ChartSeq, kept.ChartPath = nil, nil
		if f.ChartSeq != nil && f.ChartPath != nil {
			if s, ok := bySeq[*f.ChartSeq]; ok && !s.Failed() {
				if _, ok := parseChart(s.Output, *f.ChartPath); ok {
					kept.ChartSeq, kept.ChartPath = f.ChartSeq, f.ChartPath
				}
			}
		}
		if len(kept.Claims) == 0 && kept.ChartSeq == nil {
			continue
		}
		if kept.Caveats == nil {
			kept.Caveats = []string{}
		}
		out.Findings = append(out.Findings, kept)
	}
	return out
}
