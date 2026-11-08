package investigate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/llm"
	"hacktheburgh/backend/internal/pyrunner"
	"hacktheburgh/backend/internal/pyrunner/pyrunnertest"
)

var watermark = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// ---- scripted provider ----

type turn struct {
	calls   []llm.ToolCall
	content string
}

type scripted struct {
	mu    sync.Mutex
	turns []turn
	reqs  []llm.ChatRequest
}

func (s *scripted) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if len(s.reqs) > len(s.turns) {
		return llm.ChatResponse{}, errors.New("scripted: out of turns")
	}
	t := s.turns[len(s.reqs)-1]
	return llm.ChatResponse{Content: t.content, ToolCalls: t.calls}, nil
}

func (s *scripted) toolResults() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	if len(s.reqs) == 0 {
		return nil
	}
	for _, m := range s.reqs[len(s.reqs)-1].Messages {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

func call(id, name, args string) llm.ToolCall {
	return llm.ToolCall{ID: id, Name: name, Arguments: args}
}

func pyArgs(code string) string {
	b, _ := json.Marshal(map[string]string{"code": code})
	return string(b)
}

const goodCode = "df = INPUT['sources']['weather']['records']\nresult = {'rho': 0.5}\n"

func reportJSON(r contracts.InvestigationReport) string { b, _ := json.Marshal(r); return string(b) }

// ---- in-memory stores ----

type memAnalysis struct{ max *time.Time }

func (m *memAnalysis) ProjectSources(context.Context, uuid.UUID) ([]analysis.SourceMeta, error) {
	return []analysis.SourceMeta{{ID: uuid.New(), Name: "Weather", Kind: "csv", Summary: "daily weather",
		CreatedAt: watermark}}, nil
}
func (m *memAnalysis) Records(_ context.Context, ids []uuid.UUID, _, _ time.Time) (map[uuid.UUID][]analysis.Record, error) {
	out := map[uuid.UUID][]analysis.Record{}
	for _, id := range ids {
		out[id] = []analysis.Record{
			{ExternalID: "a", ObservedAt: watermark.Add(-24 * time.Hour), Payload: map[string]any{"rain": 1.0}},
			{ExternalID: "b", ObservedAt: watermark, Payload: map[string]any{"rain": 2.0, "temp": 9.0}},
		}
	}
	return out, nil
}
func (m *memAnalysis) MaxObservedAt(context.Context, uuid.UUID) (*time.Time, error) {
	return m.max, nil
}

type memStore struct {
	mu       sync.Mutex
	inputs   Inputs
	has      bool
	saved    []SaveVersion
	cards    [][]string // finding keys per save
	errs     []string
	saveErr  error
	hasCalls int
}

func (m *memStore) LoadInputs(context.Context, uuid.UUID) (Inputs, error) { return m.inputs, nil }
func (m *memStore) HasInvestigation(context.Context, uuid.UUID) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hasCalls++
	return m.has, nil
}
func (m *memStore) SaveVersion(_ context.Context, v SaveVersion) (Saved, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return Saved{}, m.saveErr
	}
	m.saved = append(m.saved, v)
	var keys []string
	if v.Cards != nil {
		for _, r := range v.Cards(len(m.saved)) {
			keys = append(keys, r.FindingKey)
		}
	}
	m.cards = append(m.cards, keys)
	m.has = true
	return Saved{WorkflowID: uuid.New(), RunID: uuid.New(), Version: len(m.saved)}, nil
}
func (m *memStore) SetProjectError(_ context.Context, _ uuid.UUID, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errs = append(m.errs, msg)
	return nil
}
func (m *memStore) lastErr() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.errs) == 0 {
		return ""
	}
	return m.errs[len(m.errs)-1]
}

func newInvestigator(t *testing.T, p llm.Provider, run pyrunner.Runner, store *memStore) *Investigator {
	t.Helper()
	w := watermark
	return &Investigator{
		LLM:    &llm.Client{Provider: p, Models: map[llm.Purpose]string{llm.Investigator: "m"}, Timeout: 5 * time.Second, MaxIter: 12},
		Runner: run, Store: store,
		Inputs: &InputBuilder{Store: &memAnalysis{max: &w}, History: 365 * 24 * time.Hour},
	}
}

func okRunner() *pyrunnertest.Fake {
	return pyrunnertest.Returns(map[string]any{"rho": 0.5, "n": 40})
}

func goodReport(seq int) contracts.InvestigationReport {
	return contracts.InvestigationReport{Rationale: "tried it", Findings: []contracts.InvestigationFinding{{
		Headline: "Rain moves with trips", Explanation: "They move together.",
		Claims:  []contracts.Claim{{Seq: seq, Path: "result.rho", Label: "rank correlation"}},
		Caveats: []string{"exploratory"},
	}}}
}

// ---- tests ----

func TestGenerateRecordsStepsAndSavesCleanedReport(t *testing.T) {
	p := &scripted{turns: []turn{
		{calls: []llm.ToolCall{call("1", "describe_source", `{"source":"weather"}`)}},
		{calls: []llm.ToolCall{call("2", "run_python", pyArgs(goodCode)), call("3", "run_python", pyArgs(goodCode))}},
		{content: reportJSON(contracts.InvestigationReport{Rationale: "tried it", Findings: []contracts.InvestigationFinding{
			goodReport(2).Findings[0],
			{Headline: "Bad", Explanation: "e", Claims: []contracts.Claim{{Seq: 3, Path: "result.nope", Label: "x"}}},
		}})},
	}}
	store := &memStore{}
	run := okRunner()
	inv := newInvestigator(t, p, run, store)

	if err := inv.Generate(context.Background(), uuid.New(), nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(store.saved) != 1 {
		t.Fatalf("saved %d versions, want 1", len(store.saved))
	}
	v := store.saved[0]
	if len(v.Steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(v.Steps))
	}
	for i, s := range v.Steps {
		if s.Seq != i+1 {
			t.Errorf("step %d has seq %d", i, s.Seq)
		}
	}
	if v.Steps[0].Tool != "describe_source" || v.Steps[1].Tool != "run_python" {
		t.Errorf("tools = %s, %s", v.Steps[0].Tool, v.Steps[1].Tool)
	}
	if _, ok := Resolve(v.Steps[1].Output, "result.rho"); !ok {
		t.Errorf("full output not recorded: %s", v.Steps[1].Output)
	}
	if strings.Contains(string(v.Steps[1].Output), `"seq"`) {
		t.Errorf("recorded output must not include seq: %s", v.Steps[1].Output)
	}
	// The model saw the seq of each result.
	got := strings.Join(p.toolResults(), "\n")
	for _, want := range []string{`"seq":1`, `"seq":2`, `"seq":3`} {
		if !strings.Contains(got, want) {
			t.Errorf("model did not see %s in %s", want, got)
		}
	}
	// The unresolvable finding was cleaned out, the good one stayed.
	if len(v.Report.Findings) != 1 || v.Report.Findings[0].Headline != "Rain moves with trips" {
		t.Fatalf("report = %+v", v.Report)
	}
	// First run is built from the recorded output of the cited step.
	if len(v.Replay.Steps) != 1 || v.Replay.Steps[0].Seq != 2 || !v.Replay.Steps[0].AsOf.Equal(watermark) || !v.DataUpTo.Equal(watermark) {
		t.Fatalf("first run = %+v", v.Replay)
	}
	if len(store.cards[0]) != 1 || store.cards[0][0] != "inv:v1:0" {
		t.Fatalf("cards = %v", store.cards)
	}
	if len(run.Requests()) != 2 {
		t.Errorf("runner calls = %d, want 2 (the first run must not re-execute code)", len(run.Requests()))
	}
}

func TestToolResultOverFourKBIsTruncatedForModelButRecordedInFull(t *testing.T) {
	big := map[string]any{"blob": strings.Repeat("x", 6000), "rho": 0.5}
	p := &scripted{turns: []turn{
		{calls: []llm.ToolCall{call("1", "run_python", pyArgs(goodCode))}},
		{content: reportJSON(contracts.InvestigationReport{Rationale: "r"})},
	}}
	store := &memStore{}
	inv := newInvestigator(t, p, pyrunnertest.Returns(big), store)
	if err := inv.Generate(context.Background(), uuid.New(), nil); err != nil {
		t.Fatal(err)
	}
	if res := p.toolResults(); len(res) != 1 || len(res[0]) > 4200 {
		t.Fatalf("model saw %d bytes", len(p.toolResults()[0]))
	}
	if !strings.Contains(string(store.saved[0].Steps[0].Output), strings.Repeat("x", 6000)) {
		t.Fatal("recorded output was truncated")
	}
}

func TestRejectedCallsAreNotRecordedAndNeverReachTheRunner(t *testing.T) {
	p := &scripted{turns: []turn{
		{calls: []llm.ToolCall{
			call("1", "describe_source", `{"source":"nope"}`),
			call("2", "run_python", pyArgs("f = open('x')\nresult = INPUT\n")),
			call("3", "run_python", pyArgs("x = 1")),
		}},
		{content: reportJSON(contracts.InvestigationReport{Rationale: "r"})},
	}}
	store := &memStore{}
	run := okRunner()
	inv := newInvestigator(t, p, run, store)
	if err := inv.Generate(context.Background(), uuid.New(), nil); err != nil {
		t.Fatal(err)
	}
	if len(store.saved[0].Steps) != 0 || len(run.Requests()) != 0 {
		t.Fatalf("steps=%d runner calls=%d, want 0/0", len(store.saved[0].Steps), len(run.Requests()))
	}
	res := strings.Join(p.toolResults(), "\n")
	for _, want := range []string{"unknown source", "open(", "never reads INPUT"} {
		if !strings.Contains(res, want) {
			t.Errorf("model was not told %q: %s", want, res)
		}
	}
}

func TestRaisedStepIsRecordedAndItsClaimsAreDropped(t *testing.T) {
	p := &scripted{turns: []turn{
		{calls: []llm.ToolCall{call("1", "run_python", pyArgs(goodCode))}},
		{content: reportJSON(goodReport(1))},
	}}
	store := &memStore{}
	inv := newInvestigator(t, p, pyrunnertest.Raises("Traceback...\nKeyError: 'rain'"), store)
	if err := inv.Generate(context.Background(), uuid.New(), nil); err != nil {
		t.Fatal(err)
	}
	v := store.saved[0]
	if len(v.Steps) != 1 || !strings.Contains(string(v.Steps[0].Output), "KeyError") {
		t.Fatalf("steps = %+v", v.Steps)
	}
	if len(v.Report.Findings) != 0 {
		t.Fatalf("finding citing a raised step survived: %+v", v.Report.Findings)
	}
	if v.Report.Rationale == "" {
		t.Fatal("an empty report must keep its rationale")
	}
}

func TestToolBudgetStopsRecording(t *testing.T) {
	var calls []llm.ToolCall
	for n := 1; n <= 13; n++ {
		calls = append(calls, call(fmt.Sprint(n), "run_python", pyArgs(goodCode)))
	}
	p := &scripted{turns: []turn{{calls: calls}, {content: reportJSON(contracts.InvestigationReport{Rationale: "r"})}}}
	store := &memStore{}
	run := okRunner()
	inv := newInvestigator(t, p, run, store)
	if err := inv.Generate(context.Background(), uuid.New(), nil); err != nil {
		t.Fatal(err)
	}
	if len(store.saved[0].Steps) != 12 || len(run.Requests()) != 12 {
		t.Fatalf("steps=%d runs=%d, want 12/12", len(store.saved[0].Steps), len(run.Requests()))
	}
	res := p.toolResults()
	if !strings.Contains(res[12], "tool budget exhausted") {
		t.Fatalf("13th result = %s", res[12])
	}
}

func TestEveryRunPythonCallSeesTheSameInput(t *testing.T) {
	p := &scripted{turns: []turn{
		{calls: []llm.ToolCall{call("1", "run_python", pyArgs(goodCode)), call("2", "run_python", pyArgs(goodCode))}},
		{content: reportJSON(contracts.InvestigationReport{Rationale: "r"})},
	}}
	run := okRunner()
	inv := newInvestigator(t, p, run, &memStore{})
	if err := inv.Generate(context.Background(), uuid.New(), nil); err != nil {
		t.Fatal(err)
	}
	reqs := run.Requests()
	a, _ := json.Marshal(reqs[0].Input)
	b, _ := json.Marshal(reqs[1].Input)
	if string(a) != string(b) {
		t.Fatal("INPUT differs between calls")
	}
	in := reqs[0].Input.(analysis.Input)
	if len(in.Sources) != 1 || !in.DataUpTo.Equal(watermark) {
		t.Fatalf("INPUT = %+v", in)
	}
}

func TestRunnerFailureIsNotRecorded(t *testing.T) {
	p := &scripted{turns: []turn{
		{calls: []llm.ToolCall{call("1", "run_python", pyArgs(goodCode))}},
		{content: reportJSON(contracts.InvestigationReport{Rationale: "r"})},
	}}
	store := &memStore{}
	run := &pyrunnertest.Fake{Fn: func(pyrunner.Request) (pyrunner.Outcome, error) { return pyrunner.Outcome{}, errors.New("boom") }}
	inv := newInvestigator(t, p, run, store)
	if err := inv.Generate(context.Background(), uuid.New(), nil); err != nil {
		t.Fatal(err)
	}
	if len(store.saved[0].Steps) != 0 {
		t.Fatal("a runner failure was recorded as a step")
	}
	if !strings.Contains(strings.Join(p.toolResults(), ""), "runner unavailable") {
		t.Fatal("model was not told the runner is unavailable")
	}
}

func TestGenerateFailureKeepsOldVersionAndRecordsPrefixedError(t *testing.T) {
	store := &memStore{}
	inv := newInvestigator(t, &scripted{}, okRunner(), store) // no turns: the LLM call fails
	if err := inv.Generate(context.Background(), uuid.New(), nil); err == nil {
		t.Fatal("want an error")
	}
	if len(store.saved) != 0 {
		t.Fatal("something was saved")
	}
	if !strings.HasPrefix(store.lastErr(), ErrorPrefix) {
		t.Fatalf("last_error = %q", store.lastErr())
	}
}

func TestValidateReport(t *testing.T) {
	if err := validateReport(contracts.InvestigationReport{Rationale: "r"}); err != nil {
		t.Errorf("zero findings must be allowed: %v", err)
	}
	if validateReport(contracts.InvestigationReport{}) == nil {
		t.Error("empty rationale accepted")
	}
	f := contracts.InvestigationFinding{Headline: "h", Explanation: "e", Claims: make([]contracts.Claim, 9)}
	if validateReport(contracts.InvestigationReport{Rationale: "r", Findings: []contracts.InvestigationFinding{f}}) == nil {
		t.Error("9 claims accepted")
	}
}

func TestStartIfNone(t *testing.T) {
	now := time.Now()
	waitFor := func(cond func() bool) {
		t.Helper()
		for i := 0; i < 200 && !cond(); i++ {
			time.Sleep(5 * time.Millisecond)
		}
		if !cond() {
			t.Fatal("timed out")
		}
	}
	project := uuid.New()

	t.Run("does nothing when any investigation version exists", func(t *testing.T) {
		store := &memStore{has: true}
		p := &scripted{}
		inv := newInvestigator(t, p, okRunner(), store)
		if err := inv.StartIfNone(context.Background(), project); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Millisecond)
		if len(p.reqs) != 0 {
			t.Fatal("the LLM was called")
		}
	})

	t.Run("stops after three failed attempts, with backoff", func(t *testing.T) {
		store := &memStore{}
		p := &scripted{} // every generation fails
		inv := newInvestigator(t, p, okRunner(), store)
		inv.Now = func() time.Time { return now }
		attemptsSeen := func() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.reqs) }
		failed := func() int { inv.mu.Lock(); defer inv.mu.Unlock(); return inv.attempts[project].failures }

		for n, step := range []time.Duration{0, 9 * time.Minute, time.Minute, 29 * time.Minute, time.Minute, time.Hour} {
			now = now.Add(step)
			before := attemptsSeen()
			if err := inv.StartIfNone(context.Background(), project); err != nil {
				t.Fatal(err)
			}
			time.Sleep(30 * time.Millisecond)
			// n=0 starts (fail 1); n=1 is 9 min later: too soon; n=2 is 10 min after: runs (fail 2);
			// n=3 is 29 min after: too soon; n=4 is 30 min after: runs (fail 3); n=5: no more.
			wantRun := n == 0 || n == 2 || n == 4
			if ran := attemptsSeen() > before; ran != wantRun {
				t.Fatalf("step %d: ran = %v, want %v", n, ran, wantRun)
			}
			if wantRun {
				waitFor(func() bool { return failed() == n/2+1 })
			}
		}
	})
}

func TestStartRejectsSecondGenerationForSameProject(t *testing.T) {
	block := make(chan struct{})
	p := &blocking{block: block}
	inv := newInvestigator(t, p, okRunner(), &memStore{})
	project := uuid.New()
	if err := inv.Start(project, nil); err != nil {
		t.Fatal(err)
	}
	if err := inv.Start(project, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Start = %v, want ErrBusy", err)
	}
	close(block)
}

type blocking struct{ block chan struct{} }

func (b *blocking) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	<-b.block
	return llm.ChatResponse{}, errors.New("done")
}
