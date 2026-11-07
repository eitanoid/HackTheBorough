package contracts

import "fmt"

var knownTests = map[string]bool{"lagged_correlation": true, "anomaly": true}

// Validate checks the numeric bounds that structured outputs can't enforce.
// TODO(workstream C): semantic checks against source profiles.
func (s AnalysisSpec) Validate() error {
	if s.WindowDays < 7 || s.WindowDays > 730 {
		return fmt.Errorf("window_days %d out of range 7..730", s.WindowDays)
	}
	if n := len(s.Series); n < 2 || n > 12 {
		return fmt.Errorf("series count %d out of range 2..12", n)
	}
	if s.MaxLag < 0 || s.MaxLag > 14 {
		return fmt.Errorf("max_lag %d out of range 0..14", s.MaxLag)
	}
	ids := map[string]bool{}
	for _, se := range s.Series {
		if ids[se.ID] {
			return fmt.Errorf("duplicate series id %q", se.ID)
		}
		ids[se.ID] = true
	}
	for _, p := range s.Pairs {
		if !ids[p.A] || !ids[p.B] {
			return fmt.Errorf("pair (%s,%s) names an undefined series", p.A, p.B)
		}
	}
	for _, t := range s.Tests {
		if !knownTests[t] {
			return fmt.Errorf("unknown test %q", t)
		}
	}
	return nil
}

// Reason categories valid per workflow kind. The rule-decided categories
// (sources_changed, claim_contradicted) never come from the LLM, so they are
// not in the ReviewDecision enum.
var reasonsByKind = map[string]map[string]bool{
	"statistical":   {"schema_drift": true, "stale_series": true, "coverage": true, "lag_boundary": true},
	"investigation": {"effect_faded": true, "explanation_mismatch": true, "new_scope": true},
}

// Validate rejects regenerate with category none, and categories that don't
// belong to the workflow's kind. Callers store a rejected decision as keep.
func (d ReviewDecision) Validate(workflowKind string) error {
	switch d.Decision {
	case "keep":
		return nil
	case "regenerate":
		if !reasonsByKind[workflowKind][d.ReasonCategory] {
			return fmt.Errorf("category %q is not valid for a %s workflow", d.ReasonCategory, workflowKind)
		}
		return nil
	}
	return fmt.Errorf("unknown decision %q", d.Decision)
}
