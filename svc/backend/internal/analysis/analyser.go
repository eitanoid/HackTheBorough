package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
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

type sourceInputDiagnostic struct {
	Key           string   `json:"source_key"`
	ID            string   `json:"source_id"`
	Records       int      `json:"records"`
	ProfileFields []string `json:"profile_fields"`
	PayloadFields []string `json:"payload_fields"`
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
	result := contracts.AnalysisResult{
		DataUpTo: dataUpTo,
		Findings: findings,
		Funnel:   funnel,
		Rejected: rejected,
		Health:   buildHealth(in, sr.Series),
	}
	slog.Info("analysis script completed",
		"project_id", projectID,
		"source_count", len(in.Sources),
		"record_count", inputRecordCount(in),
		"series_count", len(sr.Series),
		"tests_run", len(sr.Tests),
		"anomalies", len(sr.Anomalies),
		"findings", len(result.Findings),
		"rejected", len(result.Rejected),
	)
	if len(sr.Tests) == 0 {
		slog.Warn("analysis script reported no tests",
			"project_id", projectID,
			"reported_series", seriesIDs(sr.Series),
			"input_sources", sourceInputDiagnostics(in),
		)
	}
	if len(result.Findings) == 0 {
		slog.Warn("analysis completed with no findings",
			"project_id", projectID,
			"tests_run", len(sr.Tests),
			"funnel", result.Funnel,
			"fdr_q", a.Th.FDRQ,
			"min_n", a.Th.MinN,
		)
	}
	return result, nil
}

func inputRecordCount(input Input) int {
	count := 0
	for _, source := range input.Sources {
		count += len(source.Records)
	}
	return count
}

func seriesIDs(series []contracts.SeriesInfo) []string {
	ids := make([]string, 0, len(series))
	for _, item := range series {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return ids
}

// sourceInputDiagnostics reports field names and counts only, never record values.
func sourceInputDiagnostics(input Input) []sourceInputDiagnostic {
	keys := make([]string, 0, len(input.Sources))
	for key := range input.Sources {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	out := make([]sourceInputDiagnostic, 0, len(keys))
	for _, key := range keys {
		source := input.Sources[key]
		profileFields := make(map[string]struct{}, len(source.Fields))
		payloadFields := make(map[string]struct{})
		for _, field := range source.Fields {
			profileFields[field.Path] = struct{}{}
		}
		for _, record := range source.Records {
			for field := range record.Payload {
				payloadFields[field] = struct{}{}
			}
		}
		out = append(out, sourceInputDiagnostic{
			Key:           key,
			ID:            source.ID,
			Records:       len(source.Records),
			ProfileFields: sortedKeys(profileFields),
			PayloadFields: sortedKeys(payloadFields),
		})
	}
	return out
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
