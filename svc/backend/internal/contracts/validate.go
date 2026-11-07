package contracts

import (
	"fmt"
	"strings"
)

// Validate checks the draft is usable. Semantic checks (the script runs and
// loads series) happen in the author's dry run through the worker.
func (d WorkflowDraft) Validate() error {
	if strings.TrimSpace(d.Script) == "" {
		return fmt.Errorf("script is empty")
	}
	if strings.TrimSpace(d.Rationale) == "" {
		return fmt.Errorf("rationale is empty")
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
