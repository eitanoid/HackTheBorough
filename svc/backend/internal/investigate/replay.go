package investigate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/insightrows"
	"hacktheburgh/backend/internal/pyrunner"
)

// ReplayError means every cited run_python step failed on the new data:
// nothing was recomputed, and showing only carried values would hide the
// breakage. The run fails.
type ReplayError struct{ Reasons []string }

func (e *ReplayError) Error() string {
	return fmt.Sprintf("every cited step failed on the new data (%d): %s", len(e.Reasons), e.Reasons[0])
}

// Replayer re-executes the recorded steps that findings cite. It makes no LLM
// call.
type Replayer struct {
	Store   ReplayStore
	Inputs  *InputBuilder
	Runner  pyrunner.Runner
	Timeout time.Duration // per run_python step
	Chart   ChartBuilder
}

// Replay runs the cited steps of one investigation workflow at dataUpTo and
// returns the run's result and its cards.
//
// A step that raised, timed out or returned an unusable result keeps the
// previous run's output and gets Error set. A runner failure (as opposed to a
// code failure) is returned as an error.
func (r *Replayer) Replay(ctx context.Context, workflowID uuid.UUID, dataUpTo time.Time) (contracts.ReplayResult, []insightrows.InsightRow, error) {
	empty := contracts.ReplayResult{DataUpTo: dataUpTo, Steps: []contracts.StepOutput{}}
	wf, err := r.Store.LoadWorkflow(ctx, workflowID)
	if err != nil {
		return empty, nil, err
	}
	cited := CitedSeqs(wf.Report)
	if len(cited) == 0 {
		return empty, nil, nil
	}
	prev, err := r.Store.PreviousReplay(ctx, workflowID)
	if err != nil {
		return empty, nil, err
	}
	prevBySeq := map[int]contracts.StepOutput{}
	if prev != nil {
		for _, s := range prev.Steps {
			prevBySeq[s.Seq] = s
		}
	}
	steps := make(map[int]Step, len(wf.Steps))
	for _, s := range wf.Steps {
		steps[s.Seq] = s
	}

	in, err := r.Inputs.Build(ctx, wf.ProjectID, dataUpTo)
	if err != nil {
		return empty, nil, err
	}

	res := contracts.ReplayResult{DataUpTo: dataUpTo, Steps: make([]contracts.StepOutput, 0, len(cited))}
	var pythonSteps int
	var reasons []string
	for _, seq := range cited {
		if err := ctx.Err(); err != nil {
			return empty, nil, err
		}
		step, ok := steps[seq]
		if !ok {
			return empty, nil, fmt.Errorf("report cites step %d, which was not recorded", seq)
		}
		var output json.RawMessage
		var reason string
		switch step.Tool {
		case ToolDescribeSource:
			var args struct {
				Source string `json:"source"`
			}
			_ = json.Unmarshal(step.Input, &args)
			var found bool
			if output, found = describeSource(in, args.Source); !found {
				reason = fmt.Sprintf("source %q is no longer part of the project", args.Source)
			}
		case ToolRunPython:
			pythonSteps++
			var args struct {
				Code string `json:"code"`
			}
			if err := json.Unmarshal(step.Input, &args); err != nil {
				return empty, nil, fmt.Errorf("step %d has unreadable input: %w", seq, err)
			}
			out, err := execPython(ctx, r.Runner, in, args.Code, r.Timeout)
			if err != nil {
				return empty, nil, fmt.Errorf("python runner: %w", err)
			}
			output = out
			if why, failed := failedReason(out); failed {
				reason = why
			}
		default:
			return empty, nil, fmt.Errorf("step %d uses unknown tool %q", seq, step.Tool)
		}

		if reason == "" {
			res.Steps = append(res.Steps, contracts.StepOutput{Seq: seq, Output: output, AsOf: dataUpTo})
			continue
		}
		if step.Tool == ToolRunPython {
			reasons = append(reasons, reason)
		}
		carried := contracts.StepOutput{Seq: seq, Output: step.Output, AsOf: wf.CreatedAt}
		if p, ok := prevBySeq[seq]; ok {
			carried.Output, carried.AsOf = p.Output, p.AsOf
		}
		carried.Error = &reason
		res.Steps = append(res.Steps, carried)
	}
	if pythonSteps > 0 && len(reasons) == pythonSteps {
		return empty, nil, &ReplayError{Reasons: reasons}
	}
	return res, Cards(wf.Report, wf.Version, res, prev, r.Chart), nil
}
