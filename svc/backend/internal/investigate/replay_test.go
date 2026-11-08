package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/charts"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/pyrunner"
	"hacktheburgh/backend/internal/pyrunner/pyrunnertest"
)

type memReplayStore struct {
	wf   Workflow
	prev *contracts.ReplayResult
}

func (m *memReplayStore) LoadWorkflow(context.Context, uuid.UUID) (Workflow, error) { return m.wf, nil }
func (m *memReplayStore) PreviousReplay(context.Context, uuid.UUID) (*contracts.ReplayResult, error) {
	return m.prev, nil
}

var (
	gen   = watermark.Add(-48 * time.Hour) // when the workflow was generated
	later = watermark.Add(7 * 24 * time.Hour)
)

func pyStep(seq int, code, result string) Step {
	in, _ := json.Marshal(map[string]string{"code": code})
	return Step{Seq: seq, Tool: ToolRunPython, Input: in, Output: okStep(seq, result).Output}
}

// replayFixture: step 1 describes weather, steps 2 and 3 are cited, step 4 is not.
func replayFixture() (*memReplayStore, *contracts.ReplayResult) {
	steps := []Step{
		{Seq: 1, Tool: ToolDescribeSource, Input: raw(`{"source":"weather"}`), Output: raw(`{"key":"weather","records":2}`)},
		pyStep(2, "a = INPUT\nresult = {'rho': 0.5}", `{"rho":0.5,"series":{"t":1}}`),
		pyStep(3, "b = INPUT\nresult = {'rho': 0.3}", `{"rho":0.3}`),
		pyStep(4, "c = INPUT\nresult = {'unused': 1}", `{"unused":1}`),
	}
	report := contracts.InvestigationReport{Rationale: "r", Findings: []contracts.InvestigationFinding{{
		Headline: "H", Explanation: "E", Caveats: []string{"c1"},
		Claims: []contracts.Claim{
			{Seq: 2, Path: "result.rho", Label: "winter"},
			{Seq: 3, Path: "result.rho", Label: "summer"},
		},
	}}}
	wf := Workflow{ID: uuid.New(), ProjectID: uuid.New(), Version: 2, CreatedAt: gen, Report: report, Steps: steps}
	prev := &contracts.ReplayResult{DataUpTo: watermark, Steps: []contracts.StepOutput{
		{Seq: 2, Output: steps[1].Output, AsOf: watermark},
		{Seq: 3, Output: steps[2].Output, AsOf: watermark},
	}}
	return &memReplayStore{wf: wf, prev: prev}, prev
}

func newReplayer(store ReplayStore, run pyrunner.Runner) *Replayer {
	w := later
	return &Replayer{Store: store, Inputs: &InputBuilder{Store: &memAnalysis{max: &w}, History: 365 * 24 * time.Hour},
		Runner: run, Timeout: time.Second}
}

// byCode answers each step by its code, like the real thing would.
func byCode(m map[string]string) *pyrunnertest.Fake {
	return &pyrunnertest.Fake{Fn: func(r pyrunner.Request) (pyrunner.Outcome, error) {
		for k, v := range m {
			if strings.Contains(r.Code, k) {
				if strings.HasPrefix(v, "raise:") {
					return pyrunner.Outcome{Error: "Traceback...\n" + strings.TrimPrefix(v, "raise:")}, nil
				}
				return pyrunner.Outcome{OK: true, Result: json.RawMessage(v)}, nil
			}
		}
		return pyrunner.Outcome{}, errors.New("unexpected code")
	}}
}

func TestReplayAtSameWatermarkReproducesRecordedOutputs(t *testing.T) {
	store, _ := replayFixture()
	run := byCode(map[string]string{"a = INPUT": `{"rho":0.5,"series":{"t":1}}`, "b = INPUT": `{"rho":0.3}`})
	res, rows, err := newReplayer(store, run).Replay(context.Background(), store.wf.ID, watermark)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Steps) != 2 {
		t.Fatalf("steps = %d, want 2 cited", len(res.Steps))
	}
	for _, s := range res.Steps {
		want := store.wf.Steps[s.Seq-1].Output
		if string(s.Output) != string(want) {
			t.Errorf("step %d output differs:\n got %s\nwant %s", s.Seq, s.Output, want)
		}
		if s.Error != nil || !s.AsOf.Equal(watermark) {
			t.Errorf("step %d error/as_of = %v/%v", s.Seq, s.Error, s.AsOf)
		}
	}
	if len(rows) != 1 || rows[0].FindingKey != "inv:v2:0" || len(rows[0].Caveats) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
}

func TestReplayRunsOnlyCitedStepsOnOneInput(t *testing.T) {
	store, _ := replayFixture()
	run := byCode(map[string]string{"a = INPUT": `{"rho":0.6}`, "b = INPUT": `{"rho":0.2}`})
	res, rows, err := newReplayer(store, run).Replay(context.Background(), store.wf.ID, later)
	if err != nil {
		t.Fatal(err)
	}
	reqs := run.Requests()
	if len(reqs) != 2 {
		t.Fatalf("runner calls = %d, want 2 (step 4 is not cited, step 1 is not python)", len(reqs))
	}
	a, _ := json.Marshal(reqs[0].Input)
	b, _ := json.Marshal(reqs[1].Input)
	if string(a) != string(b) {
		t.Fatal("INPUT differs between steps")
	}
	if got := rows[0].Stats["winter"]; got != 0.6 {
		t.Fatalf("winter = %v, want the fresh 0.6", got)
	}
	for _, s := range res.Steps {
		if !s.AsOf.Equal(later) {
			t.Errorf("as_of = %v", s.AsOf)
		}
	}
	if len(rows[0].Caveats) != 1 {
		t.Fatalf("a fully fresh card got caveats %v", rows[0].Caveats)
	}
}

func TestReplayDegradesOneRaisedStep(t *testing.T) {
	store, _ := replayFixture()
	run := byCode(map[string]string{"a = INPUT": `{"rho":0.6}`, "b = INPUT": "raise:KeyError: 'rain'"})
	res, rows, err := newReplayer(store, run).Replay(context.Background(), store.wf.ID, later)
	if err != nil {
		t.Fatalf("one failed step must not fail the run: %v", err)
	}
	var carried *contracts.StepOutput
	for i := range res.Steps {
		if res.Steps[i].Seq == 3 {
			carried = &res.Steps[i]
		}
	}
	if carried == nil || carried.Error == nil || !strings.Contains(*carried.Error, "KeyError") || !carried.AsOf.Equal(watermark) {
		t.Fatalf("carried step = %+v", carried)
	}
	if string(carried.Output) != string(store.wf.Steps[2].Output) {
		t.Fatalf("carried output = %s", carried.Output)
	}
	r := rows[0]
	if r.Stats["winter"] != 0.6 || r.Stats["summer"] != 0.3 {
		t.Fatalf("stats = %v", r.Stats)
	}
	if len(r.Caveats) != 2 || !strings.Contains(r.Caveats[1], "Couldn't recompute") || !strings.Contains(r.Caveats[1], watermark.Format("2 Jan 2006")) {
		t.Fatalf("caveats = %v", r.Caveats)
	}
	ev := r.Evidence.([]Evidence)
	if ev[1].Label != "summer" || !ev[1].AsOf.Equal(watermark) || !ev[0].AsOf.Equal(later) {
		t.Fatalf("evidence = %+v", ev)
	}
}

func TestReplayCarriesAClaimWhosePathDisappeared(t *testing.T) {
	store, _ := replayFixture()
	// Step 3 succeeds but no longer has result.rho.
	run := byCode(map[string]string{"a = INPUT": `{"rho":0.6}`, "b = INPUT": `{"other":1}`})
	_, rows, err := newReplayer(store, run).Replay(context.Background(), store.wf.ID, later)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Stats["summer"] != 0.3 || len(rows[0].Caveats) != 2 {
		t.Fatalf("stats=%v caveats=%v", rows[0].Stats, rows[0].Caveats)
	}
}

func TestReplayLeavesOffAClaimNeitherRunHas(t *testing.T) {
	store, prev := replayFixture()
	prev.Steps[1].Output = raw(`{"result":{"other":1}}`) // the previous run lacks it too
	run := byCode(map[string]string{"a = INPUT": `{"rho":0.6}`, "b = INPUT": `{"other":1}`})
	_, rows, err := newReplayer(store, run).Replay(context.Background(), store.wf.ID, later)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := rows[0].Stats["summer"]; has || len(rows[0].Stats) != 1 {
		t.Fatalf("stats = %v", rows[0].Stats)
	}
}

func TestReplayFailsWhenEveryCitedPythonStepFails(t *testing.T) {
	store, _ := replayFixture()
	run := byCode(map[string]string{"a = INPUT": "raise:ValueError: x", "b = INPUT": "raise:ValueError: y"})
	_, _, err := newReplayer(store, run).Replay(context.Background(), store.wf.ID, later)
	var re *ReplayError
	if !errors.As(err, &re) || len(re.Reasons) != 2 {
		t.Fatalf("err = %v, want *ReplayError with 2 reasons", err)
	}
}

func TestReplayRunnerErrorIsAPlainError(t *testing.T) {
	store, _ := replayFixture()
	run := &pyrunnertest.Fake{Fn: func(pyrunner.Request) (pyrunner.Outcome, error) { return pyrunner.Outcome{}, errors.New("runner down") }}
	_, _, err := newReplayer(store, run).Replay(context.Background(), store.wf.ID, later)
	var re *ReplayError
	if err == nil || errors.As(err, &re) || !strings.Contains(err.Error(), "runner down") {
		t.Fatalf("err = %v", err)
	}
}

func TestReplayTimeoutAndOversizeAreCodeFailures(t *testing.T) {
	store, _ := replayFixture()
	run := &pyrunnertest.Fake{Fn: func(r pyrunner.Request) (pyrunner.Outcome, error) {
		if strings.Contains(r.Code, "a = INPUT") {
			return pyrunner.Outcome{TimedOut: true}, nil
		}
		return pyrunner.Outcome{OK: true, Result: json.RawMessage(`{"rho":0.2}`)}, nil
	}}
	res, _, err := newReplayer(store, run).Replay(context.Background(), store.wf.ID, later)
	if err != nil {
		t.Fatal(err)
	}
	if res.Steps[0].Error == nil || !strings.Contains(*res.Steps[0].Error, "ran past") {
		t.Fatalf("timeout step = %+v", res.Steps[0])
	}

	out, err := execPython(context.Background(), pyrunnertest.ReturnsRaw([]byte(`{"x":"`+strings.Repeat("a", maxResultBytes)+`"}`)),
		okInput(), "result = INPUT", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if reason, failed := failedReason(out); !failed || !strings.Contains(reason, "too large") {
		t.Fatalf("oversize output = %s", out)
	}
}

func TestReplayWithNothingCitedDoesNothing(t *testing.T) {
	store, _ := replayFixture()
	store.wf.Report.Findings = nil
	run := byCode(nil)
	res, rows, err := newReplayer(store, run).Replay(context.Background(), store.wf.ID, later)
	if err != nil || len(res.Steps) != 0 || rows != nil || len(run.Requests()) != 0 {
		t.Fatalf("res=%+v rows=%v err=%v runs=%d", res, rows, err, len(run.Requests()))
	}
	if res.Steps == nil {
		t.Fatal("Steps must encode as [] not null")
	}
}

func TestReplayDescribeSourceComesFromInput(t *testing.T) {
	store, _ := replayFixture()
	store.wf.Report.Findings[0].Claims = []contracts.Claim{{Seq: 1, Path: "records", Label: "rows"}}
	store.prev.Steps = []contracts.StepOutput{{Seq: 1, Output: store.wf.Steps[0].Output, AsOf: watermark}}
	run := byCode(nil)
	res, rows, err := newReplayer(store, run).Replay(context.Background(), store.wf.ID, later)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Requests()) != 0 {
		t.Fatal("describe_source must not use the runner")
	}
	if rows[0].Stats["rows"] != 2.0 || res.Steps[0].Error != nil {
		t.Fatalf("rows=%v step=%+v", rows[0].Stats, res.Steps[0])
	}
}

func TestCardsBuildChartFromFreshestOutput(t *testing.T) {
	chartOut := func(v int) json.RawMessage {
		return raw(`{"result":{"c":{"type":"series_line","points":[{"t":"2026-10-01T00:00:00Z","v":` + string(rune('0'+v)) + `}],"series":[{"id":"a","label":"A","unit":"mm"}],"grain":"week"}}}`)
	}
	r := contracts.InvestigationReport{Findings: []contracts.InvestigationFinding{{Headline: "h", Explanation: "e", ChartSeq: ip(1), ChartPath: sp("result.c")}}}
	var gotMeta charts.Meta
	var gotPoints int
	build := func(c contracts.Chart, m charts.Meta) (json.RawMessage, error) {
		gotMeta, gotPoints = m, len(c.Points)
		return raw(`{"spec":true}`), nil
	}
	cur := contracts.ReplayResult{DataUpTo: later, Steps: []contracts.StepOutput{{Seq: 1, Output: chartOut(2), AsOf: later}}}
	rows := Cards(r, 3, cur, nil, build)
	if string(rows[0].ChartSpec) != `{"spec":true}` || gotPoints != 1 || gotMeta.Grain != "week" || gotMeta.Series[0].Unit != "mm" {
		t.Fatalf("chart=%s meta=%+v", rows[0].ChartSpec, gotMeta)
	}
	if rows[0].Origin != "investigation" || rows[0].Kind != "observation" || rows[0].NarratedBy != "llm" || rows[0].Plausibility != nil {
		t.Fatalf("row = %+v", rows[0])
	}
	if got := Cards(r, 3, cur, nil, nil)[0].ChartSpec; got != nil {
		t.Fatalf("nil builder gave a chart: %s", got)
	}
}

func okInput() analysis.Input {
	return analysis.Input{DataUpTo: watermark, Sources: map[string]analysis.InputSource{}}
}
