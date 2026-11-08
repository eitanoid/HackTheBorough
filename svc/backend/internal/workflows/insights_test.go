package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/charts"
	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/llm"
	"hacktheburgh/backend/internal/narration"
	"hacktheburgh/backend/internal/pyrunner/pyrunnertest"
)

// fakeNarrator answers every finding in the prompt, echoing the direction it
// was given, so tests can count calls and vary the verdicts.
type fakeNarrator struct {
	mu       sync.Mutex
	asked    [][]string // finding keys per call
	users    []string
	doubtful map[string]bool
	fail     bool
}

func (f *fakeNarrator) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return llm.ChatResponse{}, errors.New("provider down")
	}
	var user string
	for _, m := range req.Messages {
		if m.Role == "user" {
			user = m.Content
			break
		}
	}
	var in struct {
		Findings []struct {
			FindingKey string `json:"finding_key"`
			Direction  string `json:"direction"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(user), &in); err != nil {
		return llm.ChatResponse{}, err
	}
	var keys []string
	var out contracts.Narration
	for _, x := range in.Findings {
		keys = append(keys, x.FindingKey)
		d := contracts.InsightDraft{
			FindingKey: x.FindingKey, Headline: "llm " + x.FindingKey + " " + x.Direction, Explanation: "Explained.",
			Direction: x.Direction, Plausibility: "plausible", Caveats: []string{},
		}
		if f.doubtful[x.FindingKey] {
			d.Plausibility, d.Caveats = "doubtful", []string{"Because of the calendar."}
		}
		out.Insights = append(out.Insights, d)
	}
	f.asked = append(f.asked, keys)
	f.users = append(f.users, user)
	b, _ := json.Marshal(out)
	return llm.ChatResponse{Content: string(b)}, nil
}

func (f *fakeNarrator) calls() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.asked) }

func (f *fakeNarrator) lastAsked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[len(f.asked)-1]
}

type memInsightStore struct {
	rows []PrevText // newest first
	err  error
}

func (m *memInsightStore) PreviousTexts(_ context.Context, _ uuid.UUID, keys []string) ([]PrevText, error) {
	if m.err != nil {
		return nil, m.err
	}
	var out []PrevText
	for _, r := range m.rows {
		for _, k := range keys {
			if r.FindingKey == k {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// remember stores a run's LLM-narrated rows, newest first, as the database would.
func (m *memInsightStore) remember(rows []InsightRow) {
	var added []PrevText
	for _, r := range rows {
		if r.NarratedBy == "llm" {
			added = append(added, PrevText{FindingKey: r.FindingKey, Kind: r.Kind, Headline: r.Headline,
				Explanation: r.Explanation, Caveats: r.Caveats, Stats: r.Stats, Plausibility: r.Plausibility})
		}
	}
	m.rows = append(added, m.rows...)
}

type builderHarness struct {
	b     *InsightBuilder
	llm   *fakeNarrator
	store *memInsightStore
}

func newBuilder(maxNarrations int) *builderHarness {
	f := &fakeNarrator{doubtful: map[string]bool{}}
	c := &llm.Client{Provider: f, Models: map[llm.Purpose]string{llm.Narrator: "m"}, Timeout: 5 * time.Second}
	s := &memInsightStore{}
	return &builderHarness{
		llm: f, store: s,
		b: &InsightBuilder{Store: s, Narrator: &narration.Narrator{LLM: c}, MaxNarrations: maxNarrations},
	}
}

func (h *builderHarness) build(fs ...contracts.Finding) []InsightRow {
	rows := h.b.Build(context.Background(), uuid.New(), narration.Context{Goal: "goal", InsightPrompt: "focus"},
		contracts.AnalysisResult{Findings: fs})
	h.store.remember(rows)
	return rows
}

func test(key string, effect float64) contracts.Finding {
	return contracts.Finding{FindingKey: key, Kind: "correlation", Series: []string{"a", "b"},
		Stats: map[string]any{"effect": effect, "lag": 1.0, "q": 0.01}}
}

func anomaly(key string, score float64) contracts.Finding {
	return contracts.Finding{FindingKey: key, Kind: "anomaly", Series: []string{"a"},
		Stats: map[string]any{"value": 10.0, "expected": 4.0, "score": score}}
}

func byKey(rows []InsightRow) map[string]InsightRow {
	m := map[string]InsightRow{}
	for _, r := range rows {
		m[r.FindingKey] = r
	}
	return m
}

func TestBuildNarratesNewFinding(t *testing.T) {
	h := newBuilder(5)
	rows := h.build(test("corr:a:b", 0.5))
	if h.llm.calls() != 1 || len(rows) != 1 || rows[0].NarratedBy != "llm" || rows[0].Plausibility == nil {
		t.Fatalf("calls = %d, rows = %+v", h.llm.calls(), rows)
	}
	if rows[0].Kind != "correlation" || rows[0].Stats["effect"] != 0.5 {
		t.Errorf("row = %+v", rows[0])
	}
}

func TestBuildReusesTextWithFreshStats(t *testing.T) {
	h := newBuilder(5)
	first := h.build(test("corr:a:b", 0.5))
	second := h.build(test("corr:a:b", 0.6))
	if h.llm.calls() != 1 {
		t.Fatalf("calls = %d, want 1", h.llm.calls())
	}
	if second[0].Headline != first[0].Headline || second[0].NarratedBy != "llm" {
		t.Errorf("text changed: %+v vs %+v", first[0], second[0])
	}
	if second[0].Stats["effect"] != 0.6 {
		t.Errorf("stats are stale: %v", second[0].Stats)
	}
}

func TestBuildSignFlipRenarratesAndFlipBackReuses(t *testing.T) {
	h := newBuilder(5)
	pos := h.build(test("corr:a:b", 0.5))
	neg := h.build(test("corr:a:b", -0.5))
	if h.llm.calls() != 2 || neg[0].Headline == pos[0].Headline {
		t.Fatalf("calls = %d, headlines %q / %q", h.llm.calls(), pos[0].Headline, neg[0].Headline)
	}
	back := h.build(test("corr:a:b", 0.4))
	if h.llm.calls() != 2 || back[0].Headline != pos[0].Headline {
		t.Errorf("calls = %d, headline %q, want the old text %q", h.llm.calls(), back[0].Headline, pos[0].Headline)
	}
}

func TestBuildDoubtfulIsCachedUntilSignChanges(t *testing.T) {
	h := newBuilder(5)
	h.llm.doubtful["corr:a:b"] = true
	first := h.build(test("corr:a:b", 0.5))
	if first[0].Plausibility == nil || *first[0].Plausibility != "doubtful" || first[0].Caveats[0] != "Because of the calendar." {
		t.Fatalf("row = %+v", first[0])
	}
	again := h.build(test("corr:a:b", 0.5))
	if h.llm.calls() != 1 || *again[0].Plausibility != "doubtful" {
		t.Fatalf("calls = %d, row = %+v", h.llm.calls(), again[0])
	}
	h.llm.doubtful["corr:a:b"] = false
	flipped := h.build(test("corr:a:b", -0.5))
	if h.llm.calls() != 2 || *flipped[0].Plausibility != "plausible" {
		t.Errorf("calls = %d, row = %+v", h.llm.calls(), flipped[0])
	}
}

func TestBuildCapsNarrationsAndCatchesUpNextRun(t *testing.T) {
	h := newBuilder(5)
	var fs []contracts.Finding
	for i, k := range []string{"c1", "c2", "c3", "c4", "c5", "c6", "c7"} {
		fs = append(fs, test(k, 0.9-float64(i)*0.1)) // c1 strongest ... c7 weakest
	}
	rows := byKey(h.build(fs...))
	if h.llm.calls() != 1 {
		t.Fatalf("calls = %d, want 1", h.llm.calls())
	}
	for _, k := range []string{"c1", "c2", "c3", "c4", "c5"} {
		if rows[k].NarratedBy != "llm" {
			t.Errorf("%s should be llm", k)
		}
	}
	for _, k := range []string{"c6", "c7"} {
		if rows[k].NarratedBy != "template" || rows[k].Plausibility != nil {
			t.Errorf("%s should be template: %+v", k, rows[k])
		}
	}
	rows = byKey(h.build(fs...))
	if h.llm.calls() != 2 {
		t.Fatalf("calls = %d, want 2", h.llm.calls())
	}
	if got := h.llm.lastAsked(); len(got) != 2 {
		t.Errorf("second call asked for %v, want just the 2 templated findings", got)
	}
	for k, r := range rows {
		if r.NarratedBy != "llm" {
			t.Errorf("%s should be llm on the second run", k)
		}
	}
}

func TestBuildRankingTestsBeforeAnomaliesAndIgnoresPlausibility(t *testing.T) {
	h := newBuilder(1)
	h.llm.doubtful["weak"] = true
	rows := byKey(h.build(anomaly("anom", 9), test("weak", 0.3)))
	if rows["weak"].NarratedBy != "llm" || rows["anom"].NarratedBy != "template" {
		t.Errorf("a rho of 0.3 must outrank a z of 9: %+v", rows)
	}
	if got := h.llm.lastAsked(); len(got) != 1 || got[0] != "weak" {
		t.Errorf("asked %v", got)
	}
	// Among equals, the key decides; the doubtful one is not demoted.
	h2 := newBuilder(1)
	h2.llm.doubtful["a"] = true
	rows = byKey(h2.build(test("b", 0.4), test("a", 0.4)))
	if rows["a"].NarratedBy != "llm" || rows["b"].NarratedBy != "template" {
		t.Errorf("ties go by key: %+v", rows)
	}
}

func TestBuildAnomaliesRankedByScore(t *testing.T) {
	h := newBuilder(1)
	rows := byKey(h.build(anomaly("low", 4), anomaly("high", -8)))
	if rows["high"].NarratedBy != "llm" || rows["low"].NarratedBy != "template" {
		t.Errorf("|score| decides: %+v", rows)
	}
}

func TestBuildNarratorFailureStillGivesEveryFindingARow(t *testing.T) {
	h := newBuilder(5)
	h.llm.fail = true
	rows := h.build(test("a", 0.5), anomaly("b", 5))
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	for _, r := range rows {
		if r.NarratedBy != "template" || r.Headline == "" || r.Plausibility != nil {
			t.Errorf("row = %+v", r)
		}
	}
}

func TestBuildStoreFailureTreatsEverythingAsNew(t *testing.T) {
	h := newBuilder(5)
	h.build(test("a", 0.5))
	h.store.err = errors.New("db down")
	rows := h.build(test("a", 0.5))
	if h.llm.calls() != 2 || len(rows) != 1 || rows[0].NarratedBy != "llm" {
		t.Errorf("calls = %d, rows = %+v", h.llm.calls(), rows)
	}
}

func TestBuildZeroFindings(t *testing.T) {
	h := newBuilder(5)
	if rows := h.build(); len(rows) != 0 || h.llm.calls() != 0 {
		t.Errorf("rows = %v, calls = %d", rows, h.llm.calls())
	}
}

func TestBuildSortsByKeyAndBuildsCharts(t *testing.T) {
	h := newBuilder(5)
	f1, f2 := test("z", 0.5), test("m", 0.4)
	f1.Chart = &contracts.Chart{Type: "dual_line"}
	h.b.Chart = func(c contracts.Chart, _ charts.Meta) (json.RawMessage, error) {
		return json.RawMessage(`{"mark":"` + c.Type + `"}`), nil
	}
	rows := h.build(f1, f2)
	if rows[0].FindingKey != "m" || rows[1].FindingKey != "z" {
		t.Errorf("order = %s, %s", rows[0].FindingKey, rows[1].FindingKey)
	}
	if rows[0].ChartSpec != nil || string(rows[1].ChartSpec) != `{"mark":"dual_line"}` {
		t.Errorf("charts = %s / %s", rows[0].ChartSpec, rows[1].ChartSpec)
	}
	h.b.Chart = func(contracts.Chart, charts.Meta) (json.RawMessage, error) { return nil, errors.New("bad chart") }
	rows = h.build(f1)
	if len(rows) != 1 || rows[0].ChartSpec != nil {
		t.Errorf("a chart error must leave the chart empty: %+v", rows)
	}
}

func TestNarrationContext(t *testing.T) {
	unit := "trips"
	res := contracts.AnalysisResult{Health: contracts.Health{Series: []contracts.SeriesInfo{
		{ID: "t", Label: "Trips", Source: "trips", Unit: &unit, Grain: "day"},
	}}}
	sources := []AuthorSource{{Key: "trips", Profile: contracts.SourceProfile{Summary: "Daily trips"}}}
	c := NarrationContext("goal", "focus", sources, res)
	s := c.Series["t"]
	if c.Goal != "goal" || c.InsightPrompt != "focus" || s.Label != "Trips" || s.Grain != "day" || s.SourceSummary != "Daily trips" || *s.Unit != "trips" {
		t.Errorf("context = %+v", c)
	}
}

// ---- Author integration ----

func narratingAuthor(t *testing.T, narrator *fakeNarrator, replies ...string) *harness {
	t.Helper()
	h := newHarness(t, pyrunnertest.Returns(goodResult()), replies...)
	c := &llm.Client{Provider: narrator, Models: map[llm.Purpose]string{llm.Narrator: "m"}, Timeout: 5 * time.Second}
	h.author.Insights = &InsightBuilder{
		Store: &memInsightStore{}, Narrator: &narration.Narrator{LLM: c}, MaxNarrations: 5,
	}
	return h
}

func TestAuthorSavesInsightRowsWithTheRun(t *testing.T) {
	n := &fakeNarrator{doubtful: map[string]bool{}}
	h := narratingAuthor(t, n, draftJSON(validScript))
	ch, cancel := h.bus.Subscribe()
	defer cancel()
	if err := h.author.Generate(context.Background(), h.pid, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.store.saved) != 1 {
		t.Fatalf("saved %d", len(h.store.saved))
	}
	rows := h.store.saved[0].Insights
	if len(rows) != 1 || rows[0].FindingKey != "corr:news:trips:lag+2" || rows[0].NarratedBy != "llm" {
		t.Fatalf("rows = %+v", rows)
	}
	if n.calls() != 1 {
		t.Fatalf("narrator calls = %d", n.calls())
	}
	for _, want := range []string{"care about trips", "Do bus strikes reduce trips?", "headlines", "News", `"direction": "negative"`, `"leader": "News"`} {
		if !strings.Contains(n.users[0], want) {
			t.Errorf("narrator prompt lacks %q:\n%s", want, n.users[0])
		}
	}
	evs := collect(ch, 2)
	if len(evs) != 2 || !(evs[1].Type == "project_updated" && evs[1].ID == h.pid.String()) {
		t.Errorf("events = %+v", evs)
	}
}

func TestAuthorWithoutInsightsWritesNone(t *testing.T) {
	h := newHarness(t, pyrunnertest.Returns(goodResult()), draftJSON(validScript))
	if err := h.author.Generate(context.Background(), h.pid, nil); err != nil {
		t.Fatal(err)
	}
	if len(h.store.saved[0].Insights) != 0 {
		t.Errorf("rows = %+v", h.store.saved[0].Insights)
	}
}

func TestAuthorStillSavesWhenTheNarratorFails(t *testing.T) {
	n := &fakeNarrator{fail: true}
	h := narratingAuthor(t, n, draftJSON(validScript))
	if err := h.author.Generate(context.Background(), h.pid, nil); err != nil {
		t.Fatal(err)
	}
	rows := h.store.saved[0].Insights
	if len(rows) != 1 || rows[0].NarratedBy != "template" {
		t.Errorf("rows = %+v", rows)
	}
}

func TestWithModelKeepsInsights(t *testing.T) {
	h := narratingAuthor(t, &fakeNarrator{}, draftJSON(validScript))
	if h.author.WithModel("x").Insights != h.author.Insights {
		t.Error("WithModel dropped the insight builder")
	}
}
