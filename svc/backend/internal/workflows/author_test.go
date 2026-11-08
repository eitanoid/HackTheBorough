package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"hacktheburgh/backend/internal/analysis"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/db"
	"hacktheburgh/backend/internal/events"
	"hacktheburgh/backend/internal/llm"
	"hacktheburgh/backend/internal/pyrunner"
	"hacktheburgh/backend/internal/pyrunner/pyrunnertest"
)

const validScript = "df = INPUT['sources']['news']['records']\nresult = {'series': [], 'tests': [], 'anomalies': []}\n"

var watermark = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

// ---- fakes ----

// scriptedLLM answers each chat call with the next scripted reply and records
// every request it receives.
type scriptedLLM struct {
	mu      sync.Mutex
	replies []string
	reqs    []llm.ChatRequest
	block   chan struct{} // if set, Chat waits for it to close
	panics  bool
}

func (s *scriptedLLM) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	if s.block != nil {
		<-s.block
	}
	if s.panics {
		panic("provider exploded")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if len(s.reqs) > len(s.replies) {
		return llm.ChatResponse{}, errors.New("scriptedLLM: out of replies")
	}
	return llm.ChatResponse{Content: s.replies[len(s.reqs)-1]}, nil
}

func (s *scriptedLLM) calls() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.reqs) }

// userMessage is the first user message of the n-th request.
func (s *scriptedLLM) userMessage(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.reqs[n].Messages {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

func draftJSON(script string) string {
	b, _ := json.Marshal(contracts.WorkflowDraft{Script: script, InsightPrompt: "care about trips", Rationale: "because the goal says so"})
	return string(b)
}

func client(p llm.Provider) *llm.Client {
	return &llm.Client{Provider: p, Models: map[llm.Purpose]string{llm.Author: "test-model"}, Timeout: 5 * time.Second, MaxIter: 1}
}

type memAnalysisStore struct {
	sources []analysis.SourceMeta
	max     *time.Time
}

func (m *memAnalysisStore) ProjectSources(context.Context, uuid.UUID) ([]analysis.SourceMeta, error) {
	return m.sources, nil
}
func (m *memAnalysisStore) Records(_ context.Context, ids []uuid.UUID, _, _ time.Time) (map[uuid.UUID][]analysis.Record, error) {
	out := map[uuid.UUID][]analysis.Record{}
	for _, id := range ids {
		out[id] = []analysis.Record{{ExternalID: "r", ObservedAt: watermark, Payload: map[string]any{"title": "x"}}}
	}
	return out, nil
}
func (m *memAnalysisStore) MaxObservedAt(context.Context, uuid.UUID) (*time.Time, error) {
	return m.max, nil
}

type memAuthorStore struct {
	mu      sync.Mutex
	inputs  AuthorInputs
	saved   []SaveVersion
	errs    []string
	saveErr error
}

func (m *memAuthorStore) LoadInputs(context.Context, uuid.UUID) (AuthorInputs, error) {
	return m.inputs, nil
}
func (m *memAuthorStore) SaveVersion(_ context.Context, v SaveVersion) (uuid.UUID, uuid.UUID, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saveErr != nil {
		return uuid.UUID{}, uuid.UUID{}, m.saveErr
	}
	m.saved = append(m.saved, v)
	return uuid.New(), uuid.New(), nil
}
func (m *memAuthorStore) SetProjectError(_ context.Context, _ uuid.UUID, msg string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errs = append(m.errs, msg)
	return nil
}
func (m *memAuthorStore) lastError() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.errs) == 0 {
		return ""
	}
	return m.errs[len(m.errs)-1]
}

// ---- harness ----

type harness struct {
	author *Author
	prov   *scriptedLLM
	store  *memAuthorStore
	bus    *events.Bus
	first  chan uuid.UUID // workflow ids handed to OnFirstRun
	pid    uuid.UUID
}

// newHarness builds an Author whose analyser runs `runner`. The project has two
// sources keyed "news" and "trips", and data up to the watermark.
func newHarness(t *testing.T, runner pyrunner.Runner, replies ...string) *harness {
	t.Helper()
	news := analysis.SourceMeta{ID: uuid.New(), Name: "News", Kind: "rss", CreatedAt: watermark.Add(-48 * time.Hour)}
	trips := analysis.SourceMeta{ID: uuid.New(), Name: "Trips", Kind: "csv", CreatedAt: watermark.Add(-47 * time.Hour)}
	str := func(s string) *string { return &s }
	m := &memAnalysisStore{sources: []analysis.SourceMeta{news, trips}, max: &watermark}

	h := &harness{
		prov: &scriptedLLM{replies: replies},
		store: &memAuthorStore{inputs: AuthorInputs{
			Goal: "Do bus strikes reduce trips?",
			Sources: []AuthorSource{
				{Key: "news", ID: news.ID, Name: "News", Kind: "rss", Records: 10, FirstAt: watermark.Add(-72 * time.Hour), LastAt: watermark,
					Profile: contracts.SourceProfile{Summary: "headlines", Fields: []contracts.FieldProfile{{Path: "title", Role: "text", Description: "Headline"}}}},
				{Key: "trips", ID: trips.ID, Name: "Trips", Kind: "csv", Records: 10, FirstAt: watermark.Add(-72 * time.Hour), LastAt: watermark,
					Profile: contracts.SourceProfile{Fields: []contracts.FieldProfile{{Path: "count", Role: "measure", Unit: str("trips"), Aggregation: str("sum"), Description: "Daily trips"}}}},
			},
			SourceIDs: []uuid.UUID{news.ID, trips.ID},
		}},
		bus:   events.NewBus(),
		first: make(chan uuid.UUID, 4),
		pid:   uuid.New(),
	}
	h.author = &Author{
		LLM: client(h.prov),
		Analyser: &analysis.Analyser{Store: m, Runner: runner, Th: analysis.Thresholds{FDRQ: 0.05, MinN: 30},
			Timeout: time.Minute, History: 365 * 24 * time.Hour, MaxTests: 2000},
		Store:  h.store,
		Bus:    h.bus,
		Budget: time.Minute,
		OnFirstRun: func(_ context.Context, wf, _ uuid.UUID, _ contracts.AnalysisResult) {
			h.first <- wf
		},
	}
	return h
}

func goodResult() contracts.ScriptResult {
	return contracts.ScriptResult{
		Series: []contracts.SeriesInfo{
			{ID: "news", Label: "News", Source: "news", Grain: "day", Periods: 10, Points: 10},
			{ID: "trips", Label: "Trips", Source: "trips", Grain: "day", Periods: 10, Points: 10},
		},
		Tests: []contracts.Test{{Key: "corr:news:trips:lag+2", Kind: "correlation", Series: []string{"news", "trips"},
			P: 1e-6, Effect: -0.5, N: 100, Stats: map[string]any{"lag": 2.0}}},
	}
}

// sequence returns a runner that answers with the given fakes in order.
func sequence(fakes ...*pyrunnertest.Fake) *pyrunnertest.Fake {
	var mu sync.Mutex
	i := 0
	return &pyrunnertest.Fake{Fn: func(r pyrunner.Request) (pyrunner.Outcome, error) {
		mu.Lock()
		f := fakes[min(i, len(fakes)-1)]
		i++
		mu.Unlock()
		return f.Fn(r)
	}}
}

func collect(ch <-chan events.Event, n int) []events.Event {
	var out []events.Event
	for len(out) < n {
		select {
		case e := <-ch:
			out = append(out, e)
		case <-time.After(100 * time.Millisecond):
			return out
		}
	}
	return out
}

// ---- tests ----

func TestHappyPath(t *testing.T) {
	runner := pyrunnertest.Returns(goodResult())
	h := newHarness(t, runner, draftJSON(validScript))
	ch, cancel := h.bus.Subscribe()
	defer cancel()

	if err := h.author.Generate(context.Background(), h.pid, nil); err != nil {
		t.Fatal(err)
	}
	if h.prov.calls() != 1 || len(runner.Requests()) != 1 {
		t.Errorf("llm calls = %d, runs = %d; want 1 and 1", h.prov.calls(), len(runner.Requests()))
	}
	if len(h.store.saved) != 1 {
		t.Fatalf("saved %d versions", len(h.store.saved))
	}
	v := h.store.saved[0]
	if len(v.SourceIDs) != 2 || v.SourceIDs[0] != h.store.inputs.SourceIDs[0] {
		t.Errorf("source ids = %v", v.SourceIDs)
	}
	if !v.DataUpTo.Equal(watermark) || v.Draft.Script != validScript || v.ReviewID != nil {
		t.Errorf("saved = %+v", v)
	}
	if len(v.Result.Findings) != 1 {
		t.Errorf("result = %+v", v.Result)
	}
	if runner.Requests()[0].Code != validScript {
		t.Error("the runner should have been given the author's script")
	}
	evs := collect(ch, 1)
	if len(evs) != 1 || evs[0].Type != events.WorkflowRegenerated {
		t.Errorf("events = %+v", evs)
	}
	select {
	case wf := <-h.first:
		if evs[0].ID != wf.String() {
			t.Errorf("event id %s != workflow %s", evs[0].ID, wf)
		}
	default:
		t.Error("OnFirstRun was not called")
	}
	if h.store.lastError() != "" {
		t.Errorf("unexpected project error %q", h.store.lastError())
	}
}

func TestPromptDescribesGoalSourcesAndExample(t *testing.T) {
	h := newHarness(t, pyrunnertest.Returns(goodResult()), draftJSON(validScript))
	if err := h.author.Generate(context.Background(), h.pid, nil); err != nil {
		t.Fatal(err)
	}
	user := h.prov.userMessage(0)
	for _, want := range []string{"Do bus strikes reduce trips?", "## news", "## trips", "count · measure · trips · sum · Daily trips"} {
		if !strings.Contains(user, want) {
			t.Errorf("user prompt lacks %q:\n%s", want, user)
		}
	}
	if strings.Contains(user, "previous script") {
		t.Error("a first generation must not mention a previous script")
	}
	sys := h.prov.reqs[0].Messages[0].Content
	if !strings.Contains(sys, "Report **every** test") || !strings.Contains(sys, "robust z") && !strings.Contains(sys, "MAD") {
		t.Error("system prompt is missing the rules")
	}
	if !strings.Contains(sys, strings.TrimSpace(strings.Split(exampleScript, "\n")[0])) || !strings.Contains(sys, "def spearman") {
		t.Error("system prompt should embed the example script verbatim")
	}
	for _, want := range []string{"Start from the pairs the goal asks about", "possible cause first", "few"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system prompt lost the rule %q", want)
		}
	}
	if strings.Contains(sys, "scipy.stats import") {
		t.Error("the prompt must not tell the author to use scipy")
	}
}

func TestWithModelOverridesOnlyTheAuthorModel(t *testing.T) {
	h := newHarness(t, pyrunnertest.Returns(goodResult()), draftJSON(validScript))
	h.author.LLM.Models[llm.Narrator] = "narrator-model"
	other := h.author.WithModel("experimental-model")
	if err := other.Generate(context.Background(), h.pid, nil); err != nil {
		t.Fatal(err)
	}
	if got := h.prov.reqs[0].Model; got != "experimental-model" {
		t.Errorf("request used model %q", got)
	}
	if h.author.LLM.Models[llm.Author] != "test-model" || other.LLM.Models[llm.Narrator] != "narrator-model" {
		t.Errorf("the original author's models changed, or other purposes were lost: %v / %v", h.author.LLM.Models, other.LLM.Models)
	}
}

func TestExampleScriptPassesTheDraftLint(t *testing.T) {
	d := contracts.WorkflowDraft{Script: exampleScript, Rationale: "r", InsightPrompt: "i"}
	if err := d.Validate(); err != nil {
		t.Errorf("the example in the prompt breaks the prompt's own rules: %v", err)
	}
}

func TestInvalidDraftTwiceFails(t *testing.T) {
	runner := pyrunnertest.Returns(goodResult())
	h := newHarness(t, runner, draftJSON(""), draftJSON("import os\nINPUT\nresult = {}\n"))
	err := h.author.Generate(context.Background(), h.pid, nil)
	if err == nil || !strings.Contains(err.Error(), "author output invalid") {
		t.Fatalf("err = %v", err)
	}
	if h.prov.calls() != 2 {
		t.Errorf("llm calls = %d, want 2 (the original and llm.Generate's one retry)", h.prov.calls())
	}
	if len(runner.Requests()) != 0 || len(h.store.saved) != 0 {
		t.Error("an invalid draft must not be run or saved")
	}
	if !strings.Contains(h.store.lastError(), "author output invalid") {
		t.Errorf("last_error = %q", h.store.lastError())
	}
}

func TestScriptRaisesThenRetrySucceeds(t *testing.T) {
	runner := sequence(pyrunnertest.Raises("Traceback (most recent call last):\nKeyError: 'headline'"), pyrunnertest.Returns(goodResult()))
	failing := "x = INPUT['sources']['news']['records'][0]['payload']['headline']\nresult = {}\n"
	h := newHarness(t, runner, draftJSON(failing), draftJSON(validScript))
	if err := h.author.Generate(context.Background(), h.pid, nil); err != nil {
		t.Fatal(err)
	}
	if h.prov.calls() != 2 || len(runner.Requests()) != 2 {
		t.Errorf("llm calls = %d, runs = %d", h.prov.calls(), len(runner.Requests()))
	}
	second := h.prov.userMessage(1)
	if !strings.Contains(second, "KeyError: 'headline'") || !strings.Contains(second, failing) {
		t.Errorf("the retry prompt should show the traceback and the failed script:\n%s", second)
	}
	lower := strings.ToLower(second)
	for _, banned := range []string{`"p"`, `"q"`, "findings", "funnel"} {
		if strings.Contains(lower, banned) {
			t.Errorf("the retry prompt leaks results (%s):\n%s", banned, second)
		}
	}
	if len(h.store.saved) != 1 || h.store.saved[0].Draft.Script != validScript {
		t.Errorf("the corrected script should be the one saved: %+v", h.store.saved)
	}
}

func TestScriptRaisesTwice(t *testing.T) {
	runner := pyrunnertest.Raises("Traceback...\nZeroDivisionError: division by zero")
	h := newHarness(t, runner, draftJSON(validScript), draftJSON(validScript))
	err := h.author.Generate(context.Background(), h.pid, nil)
	if err == nil || !strings.Contains(err.Error(), "script failed twice") || !strings.Contains(err.Error(), "ZeroDivisionError") {
		t.Fatalf("err = %v", err)
	}
	if h.prov.calls() > 4 || len(runner.Requests()) != 2 {
		t.Errorf("llm calls = %d (max 4), runs = %d (want 2)", h.prov.calls(), len(runner.Requests()))
	}
	if len(h.store.saved) != 0 {
		t.Error("nothing may be saved when the script fails twice, so the previous version stays active")
	}
	if !strings.Contains(h.store.lastError(), "script failed twice") {
		t.Errorf("last_error = %q", h.store.lastError())
	}
}

func TestRunnerFailureDoesNotSpendTheRetry(t *testing.T) {
	boom := &pyrunnertest.Fake{Fn: func(pyrunner.Request) (pyrunner.Outcome, error) {
		return pyrunner.Outcome{}, errors.New("runner unreachable")
	}}
	for name, runner := range map[string]pyrunner.Runner{"runner returns an error": boom, "no runner wired": nil} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, runner, draftJSON(validScript), draftJSON(validScript))
			err := h.author.Generate(context.Background(), h.pid, nil)
			if err == nil || !strings.Contains(err.Error(), "analysis unavailable") {
				t.Fatalf("err = %v", err)
			}
			if h.prov.calls() != 1 {
				t.Errorf("llm calls = %d; an infrastructure failure must not trigger a second draft", h.prov.calls())
			}
			if !strings.Contains(h.store.lastError(), "analysis unavailable") || len(h.store.saved) != 0 {
				t.Errorf("last_error = %q, saved = %d", h.store.lastError(), len(h.store.saved))
			}
		})
	}
}

func TestRegenerationTrigger(t *testing.T) {
	h := newHarness(t, pyrunnertest.Returns(goodResult()), draftJSON(validScript))
	prev := "# the previous script\nresult = INPUT\n"
	h.store.inputs.PrevScript, h.store.inputs.PrevVersion = &prev, 3
	review := uuid.New()

	err := h.author.Generate(context.Background(), h.pid, &Trigger{ReviewID: &review, Reason: "trips series went stale"})
	if err != nil {
		t.Fatal(err)
	}
	user := h.prov.userMessage(0)
	if !strings.Contains(user, prev) || !strings.Contains(user, "trips series went stale") {
		t.Errorf("regeneration prompt lacks the previous script or the reason:\n%s", user)
	}
	if got := h.store.saved[0].ReviewID; got == nil || *got != review {
		t.Errorf("review id = %v, want %v", got, review)
	}
}

func TestNoDataYet(t *testing.T) {
	h := newHarness(t, pyrunnertest.Returns(goodResult()), draftJSON(validScript))
	h.author.Analyser.Store.(*memAnalysisStore).max = nil
	if err := h.author.Generate(context.Background(), h.pid, nil); !errors.Is(err, ErrNoData) {
		t.Fatalf("err = %v, want ErrNoData", err)
	}
	if h.prov.calls() != 0 {
		t.Error("no data must mean no LLM call")
	}
	if h.store.lastError() != "" {
		t.Errorf("ErrNoData is not a failure to show on the project: %q", h.store.lastError())
	}
}

func TestSaveFailureIsRecorded(t *testing.T) {
	h := newHarness(t, pyrunnertest.Returns(goodResult()), draftJSON(validScript))
	h.store.saveErr = errors.New("disk full")
	err := h.author.Generate(context.Background(), h.pid, nil)
	if err == nil || !strings.Contains(h.store.lastError(), "disk full") {
		t.Errorf("err = %v, last_error = %q", err, h.store.lastError())
	}
	select {
	case <-h.first:
		t.Error("OnFirstRun must not run when nothing was saved")
	default:
	}
}

func waitIdle(t *testing.T, a *Author) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		n := len(a.inFlight)
		a.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("author never became idle")
}

func TestStartRejectsAConcurrentGeneration(t *testing.T) {
	h := newHarness(t, pyrunnertest.Returns(goodResult()), draftJSON(validScript))
	h.prov.block = make(chan struct{})

	if err := h.author.Start(h.pid, nil); err != nil {
		t.Fatal(err)
	}
	if err := h.author.Start(h.pid, nil); !errors.Is(err, ErrBusy) {
		t.Errorf("second Start = %v, want ErrBusy", err)
	}
	if err := h.author.Start(uuid.New(), nil); err != nil {
		t.Errorf("a different project must not be blocked: %v", err)
	}
	close(h.prov.block)
	waitIdle(t, h.author)
	if err := h.author.Start(h.pid, nil); err != nil {
		t.Errorf("Start after completion = %v", err)
	}
	waitIdle(t, h.author)
}

func TestStartRecoversFromPanics(t *testing.T) {
	h := newHarness(t, pyrunnertest.Returns(goodResult()), draftJSON(validScript))
	h.prov.panics = true
	if err := h.author.Start(h.pid, nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, h.author)
	if !strings.Contains(h.store.lastError(), "provider exploded") {
		t.Errorf("last_error = %q; a panic must become a project error", h.store.lastError())
	}
	if err := h.author.Start(h.pid, nil); err != nil {
		t.Errorf("the in-flight flag must be released after a panic: %v", err)
	}
	waitIdle(t, h.author)
}

// TestSaveVersionPostgres needs a database with the repo's migrations
// available: TEST_DATABASE_URL=postgres://... go test ./internal/workflows
func TestSaveVersionPostgres(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var pid, srcID uuid.UUID
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(pool.QueryRow(ctx, `INSERT INTO projects (name, goal, last_error) VALUES ('t', 'goal text', 'old failure') RETURNING id`).Scan(&pid))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, pid) })
	must(pool.QueryRow(ctx, `INSERT INTO data_sources (name, kind, config, profile) VALUES ('Trips', 'csv', '{}',
		'{"fields":[{"path":"count","role":"measure","unit":null,"aggregation":"sum","description":"d"}],"timestamp_path":null,"summary":"s"}') RETURNING id`).Scan(&srcID))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM data_sources WHERE id = $1`, srcID) })
	_, err = pool.Exec(ctx, `INSERT INTO project_sources VALUES ($1, $2)`, pid, srcID)
	must(err)
	_, err = pool.Exec(ctx, `INSERT INTO data_records (data_source_id, external_id, observed_at, payload) VALUES ($1, 'a', $2, '{"count": 4}')`, srcID, watermark)
	must(err)

	store := &PgAuthorStore{Pool: pool, Sources: &analysis.PgStore{Pool: pool}}
	in, err := store.LoadInputs(ctx, pid)
	must(err)
	if in.Goal != "goal text" || len(in.Sources) != 1 || in.Sources[0].Key != "trips" || in.Sources[0].Records != 1 || in.PrevScript != nil {
		t.Fatalf("inputs = %+v", in)
	}
	if got, ok, err := analysis.Watermark(ctx, &analysis.PgStore{Pool: pool}, pid); err != nil || !ok || !got.Equal(watermark) {
		t.Fatalf("watermark = %v %v %v", got, ok, err)
	}

	recs, err := (&analysis.PgStore{Pool: pool}).Records(ctx, []uuid.UUID{srcID}, watermark.Add(-time.Hour), watermark)
	must(err)
	if len(recs[srcID]) != 1 || recs[srcID][0].Payload["count"] != 4.0 || !recs[srcID][0].ObservedAt.Equal(watermark) {
		t.Fatalf("records = %+v", recs)
	}
	if recs, err = (&analysis.PgStore{Pool: pool}).Records(ctx, []uuid.UUID{srcID}, watermark.Add(time.Hour), watermark.Add(2*time.Hour)); err != nil || len(recs[srcID]) != 0 {
		t.Fatalf("an empty window should return no records: %+v %v", recs, err)
	}

	save := func(script string) (uuid.UUID, uuid.UUID) {
		wf, run, err := store.SaveVersion(ctx, SaveVersion{
			ProjectID: pid, Draft: contracts.WorkflowDraft{Script: script, InsightPrompt: "i", Rationale: "r"},
			SourceIDs: []uuid.UUID{srcID}, DataUpTo: watermark, StartedAt: time.Now(),
			Result: contracts.AnalysisResult{DataUpTo: watermark, Findings: []contracts.Finding{}, Rejected: []contracts.Finding{},
				Funnel: map[string]int{"tests_run": 1}, Health: contracts.Health{Sources: map[string]contracts.SourceHealth{}}},
		})
		must(err)
		return wf, run
	}
	wf1, _ := save("v1")
	wf2, run2 := save("v2")

	var v1Active, v2Active bool
	var v2Version int
	var sourceIDs []uuid.UUID
	must(pool.QueryRow(ctx, `SELECT is_active FROM workflows WHERE id = $1`, wf1).Scan(&v1Active))
	must(pool.QueryRow(ctx, `SELECT is_active, version, source_ids FROM workflows WHERE id = $1`, wf2).Scan(&v2Active, &v2Version, &sourceIDs))
	if v1Active || !v2Active || v2Version != 2 || len(sourceIDs) != 1 || sourceIDs[0] != srcID {
		t.Errorf("v1 active=%v, v2 active=%v version=%d source_ids=%v", v1Active, v2Active, v2Version, sourceIDs)
	}
	var status string
	var result []byte
	must(pool.QueryRow(ctx, `SELECT status, result FROM runs WHERE id = $1`, run2).Scan(&status, &result))
	var stored contracts.AnalysisResult
	must(json.Unmarshal(result, &stored))
	if status != "succeeded" || stored.Funnel["tests_run"] != 1 {
		t.Errorf("run = %s %s", status, result)
	}
	var lastErr *string
	must(pool.QueryRow(ctx, `SELECT last_error FROM projects WHERE id = $1`, pid).Scan(&lastErr))
	if lastErr != nil {
		t.Errorf("a successful save must clear last_error, got %q", *lastErr)
	}

	in, err = store.LoadInputs(ctx, pid)
	must(err)
	if in.PrevScript == nil || *in.PrevScript != "v2" || in.PrevVersion != 2 {
		t.Errorf("previous version = %v %d", in.PrevScript, in.PrevVersion)
	}
	must(store.SetProjectError(ctx, pid, "boom"))
	must(pool.QueryRow(ctx, `SELECT last_error FROM projects WHERE id = $1`, pid).Scan(&lastErr))
	if lastErr == nil || *lastErr != "boom" {
		t.Errorf("last_error = %v", lastErr)
	}
}
