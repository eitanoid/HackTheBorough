package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/pyrunner"
)

const nanHint = "NaN and Infinity aren't valid JSON; skip tests whose statistic is undefined"

type Analyser struct {
	Store    Store
	Runner   pyrunner.Runner
	Th       Thresholds
	Timeout  time.Duration // passed to the runner for each script run
	History  time.Duration // how far back INPUT reaches from the watermark
	MaxTests int
}

// A *ScriptError means the script failed (raised, timed out, or reported an
// invalid result). Any other error is infrastructure: the store or the runner
// failed, and the script is not at fault.
func (a *Analyser) Run(ctx context.Context, projectID uuid.UUID, script string, dataUpTo time.Time) (contracts.AnalysisResult, error) {
	var zero contracts.AnalysisResult
	if a.Runner == nil {
		return zero, ErrNoRunner
	}

	sources, err := a.Store.ProjectSources(ctx, projectID)
	if err != nil {
		return zero, fmt.Errorf("load sources: %w", err)
	}
	ids := make([]uuid.UUID, len(sources))
	for i, s := range sources {
		ids[i] = s.ID
	}
	recs, err := a.Store.Records(ctx, ids, dataUpTo.Add(-a.History), dataUpTo)
	if err != nil {
		return zero, fmt.Errorf("load records: %w", err)
	}
	in := BuildInput(dataUpTo, a.History, sources, recs)

	out, err := a.Runner.Run(ctx, pyrunner.Request{Code: script, Input: in, Timeout: a.Timeout})
	if err != nil {
		return zero, err
	}
	switch {
	case !out.OK && out.TimedOut:
		return zero, &ScriptError{Kind: "timeout", Message: fmt.Sprintf("script ran past %d s", int(a.Timeout.Seconds()))}
	case !out.OK:
		return zero, &ScriptError{Kind: "raised", Traceback: out.Error}
	}

	var sr contracts.ScriptResult
	if err := json.Unmarshal(out.Result, &sr); err != nil {
		return zero, &ScriptError{Kind: "invalid_result", Message: fmt.Sprintf("%v. %s", err, nanHint)}
	}
	keys := map[string]bool{}
	for _, k := range SourceKeys(sources) {
		keys[k] = true
	}
	if err := sr.Validate(keys, a.MaxTests); err != nil {
		return zero, &ScriptError{Kind: "invalid_result", Message: err.Error()}
	}

	findings, rejected, funnel := Correct(sr, a.Th)
	return contracts.AnalysisResult{
		DataUpTo: dataUpTo,
		Findings: findings,
		Funnel:   funnel,
		Rejected: rejected,
		Health:   buildHealth(in, sr.Series),
	}, nil
}
