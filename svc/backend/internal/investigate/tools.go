package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/llm"
	"hacktheburgh/backend/internal/pyrunner"
)

var errBudget = errors.New("tool budget exhausted; write your report")

// recorder holds the one INPUT for a generation and records every tool call
// that passes input validation as a step.
type recorder struct {
	in       analysis.Input
	run      pyrunner.Runner
	timeout  time.Duration
	maxSteps int

	mu    sync.Mutex
	steps []Step
}

func (r *recorder) Steps() []Step {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Step(nil), r.steps...)
}

// record appends a step and returns what the model sees: {"seq": n, ...output}.
func (r *recorder) record(tool string, input, output json.RawMessage) any {
	r.steps = append(r.steps, Step{Seq: len(r.steps) + 1, Tool: tool, Input: input, Output: output})
	return withSeq(len(r.steps), output)
}

func withSeq(seq int, output json.RawMessage) json.RawMessage {
	var m map[string]json.RawMessage
	if json.Unmarshal(output, &m) != nil {
		m = map[string]json.RawMessage{}
	}
	s, _ := json.Marshal(seq)
	m["seq"] = s
	b, _ := json.Marshal(m)
	return b
}

func (r *recorder) sourceKeys() []string {
	keys := make([]string, 0, len(r.in.Sources))
	for k := range r.in.Sources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (r *recorder) tools() []llm.Tool {
	return []llm.Tool{
		{
			Name: ToolDescribeSource,
			Description: "Describe one source: its profile, fields, record count, first and last timestamps and the payload keys " +
				"seen. Cheap; use it before writing code against a source.",
			InputSchema: objectSchema(map[string]any{
				"source": map[string]any{"type": "string", "description": "a key in INPUT['sources']"},
			}, "source"),
			Handler: r.describe,
		},
		{
			Name: ToolRunPython,
			Description: "Run pandas/numpy code against INPUT in a fresh process. The code must assign a JSON-serialisable dict to " +
				"`result`. Returns {seq, result, stdout, error, timed_out}. Cite numbers from it by seq and path.",
			InputSchema: objectSchema(map[string]any{
				"code": map[string]any{"type": "string", "description": "complete Python source; reads INPUT, assigns result"},
			}, "code"),
			Handler: r.python,
		},
	}
}

func objectSchema(props map[string]any, required ...string) map[string]any {
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

func (r *recorder) describe(_ context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Source string `json:"source"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("bad arguments: %v", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steps) >= r.maxSteps {
		return nil, errBudget
	}
	out, ok := describeSource(r.in, args.Source)
	if !ok {
		return nil, fmt.Errorf("unknown source %q; the keys are: %s", args.Source, strings.Join(r.sourceKeys(), ", "))
	}
	input, _ := json.Marshal(args)
	return r.record(ToolDescribeSource, input, out), nil
}

func (r *recorder) python(ctx context.Context, raw json.RawMessage) (any, error) {
	var args struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("bad arguments: %v", err)
	}
	if err := contracts.CheckScript(args.Code); err != nil {
		return nil, err
	}
	// Held across the run: calls are serial in RunTools, and the budget check
	// and the append must not interleave.
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steps) >= r.maxSteps {
		return nil, errBudget
	}
	out, err := execPython(ctx, r.run, r.in, args.Code, r.timeout)
	if err != nil {
		// The runner failed, not the code: tell the model, record nothing.
		return map[string]string{"error": "runner unavailable"}, nil
	}
	input, _ := json.Marshal(args)
	return r.record(ToolRunPython, input, out), nil
}
