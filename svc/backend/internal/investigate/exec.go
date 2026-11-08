package investigate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/pyrunner"
)

const (
	ToolDescribeSource = "describe_source"
	ToolRunPython      = "run_python"

	// maxResultBytes caps a run_python result. Bigger results are recorded as
	// an error step so one step can't bloat workflow_steps and the runs.
	maxResultBytes = 256 << 10
)

// InputBuilder builds the INPUT that run_python and describe_source read: one
// per generation or replay, at a fixed data_up_to.
type InputBuilder struct {
	Store   analysis.Store
	History time.Duration
}

func (b *InputBuilder) Build(ctx context.Context, projectID uuid.UUID, dataUpTo time.Time) (analysis.Input, error) {
	sources, err := b.Store.ProjectSources(ctx, projectID)
	if err != nil {
		return analysis.Input{}, fmt.Errorf("load sources: %w", err)
	}
	ids := make([]uuid.UUID, len(sources))
	for i, s := range sources {
		ids[i] = s.ID
	}
	recs, err := b.Store.Records(ctx, ids, dataUpTo.Add(-b.History), dataUpTo)
	if err != nil {
		return analysis.Input{}, fmt.Errorf("load records: %w", err)
	}
	return analysis.BuildInput(dataUpTo, b.History, sources, recs), nil
}

// pythonOutput is the recorded output of a run_python step.
type pythonOutput struct {
	Result   json.RawMessage `json:"result"` // the code's `result` dict; null on failure
	Stdout   string          `json:"stdout"`
	Error    string          `json:"error"`
	TimedOut bool            `json:"timed_out"`
}

// execPython runs code against in and returns the output to record. A code
// failure (raised, timed out, bad or oversize result) is a recorded output
// with Error set; only a runner failure is an error.
func execPython(ctx context.Context, run pyrunner.Runner, in analysis.Input, code string, timeout time.Duration) (json.RawMessage, error) {
	if run == nil {
		return nil, fmt.Errorf("no python runner")
	}
	out, err := run.Run(ctx, pyrunner.Request{Code: code, Input: in, Timeout: timeout})
	if err != nil {
		return nil, err
	}
	rec := pythonOutput{Result: json.RawMessage("null"), Stdout: out.Stdout}
	switch {
	case !out.OK && out.TimedOut:
		rec.TimedOut = true
		rec.Error = fmt.Sprintf("code ran past %d s", int(timeout.Seconds()))
	case !out.OK:
		rec.Error = out.Error
		if rec.Error == "" {
			rec.Error = "code failed"
		}
	case len(out.Result) > maxResultBytes:
		rec.Error = "result too large; summarise, and keep charts to 500 points"
	case !json.Valid(out.Result):
		rec.Error = "result is not valid JSON; NaN and Infinity are not allowed"
	case !isObject(out.Result):
		rec.Error = "result must be a dict"
	default:
		rec.Result = out.Result
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func isObject(b json.RawMessage) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\n', '\r':
			continue
		}
		return c == '{'
	}
	return false
}

// describeOutput is the recorded output of a describe_source step.
type describeOutput struct {
	Key     string     `json:"key"`
	Name    string     `json:"name"`
	Kind    string     `json:"kind"`
	Summary string     `json:"summary"`
	Fields  any        `json:"fields"`
	Records int        `json:"records"`
	FirstAt *time.Time `json:"first_at"`
	LastAt  *time.Time `json:"last_at"`
	Columns []string   `json:"payload_keys"`
}

// describeSource reads a source's facts from INPUT, so they come from the same
// snapshot as run_python's data. False means the key is not in INPUT.
func describeSource(in analysis.Input, key string) (json.RawMessage, bool) {
	s, ok := in.Sources[key]
	if !ok {
		return nil, false
	}
	o := describeOutput{Key: key, Name: s.Name, Kind: s.Kind, Summary: s.Summary, Fields: s.Fields, Records: len(s.Records)}
	if n := len(s.Records); n > 0 {
		first, last := s.Records[0].ObservedAt, s.Records[n-1].ObservedAt
		o.FirstAt, o.LastAt = &first, &last
	}
	seen := map[string]bool{}
	for _, r := range s.Records {
		for k := range r.Payload {
			seen[k] = true
		}
	}
	o.Columns = make([]string, 0, len(seen))
	for k := range seen {
		o.Columns = append(o.Columns, k)
	}
	sort.Strings(o.Columns)
	b, err := json.Marshal(o)
	if err != nil {
		return nil, false
	}
	return b, true
}

// failedReason returns a one-line reason when the output records a failure.
func failedReason(output json.RawMessage) (string, bool) {
	var o struct {
		Error    string `json:"error"`
		TimedOut bool   `json:"timed_out"`
	}
	if json.Unmarshal(output, &o) != nil {
		return "unreadable output", true
	}
	if o.Error == "" && !o.TimedOut {
		return "", false
	}
	return firstLine(o.Error, o.TimedOut), true
}

func firstLine(s string, timedOut bool) string {
	if timedOut && s == "" {
		return "timed out"
	}
	// The last line of a traceback names the exception.
	lines := splitLines(s)
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i] != "" {
			if len(lines[i]) > 200 {
				return lines[i][:200]
			}
			return lines[i]
		}
	}
	return "failed"
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
