package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/pyrunner"
	"hacktheburgh/backend/internal/pyrunner/pyrunnertest"
)

var (
	day0      = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	watermark = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
)

// memStore is an in-memory Store. Records returns everything it holds for the
// requested sources, ignoring the window, so tests can check that BuildInput
// scopes the data itself.
type memStore struct {
	byProject map[uuid.UUID][]SourceMeta
	recs      map[uuid.UUID][]Record
	max       *time.Time
	err       error
}

func (m *memStore) ProjectSources(_ context.Context, p uuid.UUID) ([]SourceMeta, error) {
	return m.byProject[p], m.err
}

func (m *memStore) Records(_ context.Context, ids []uuid.UUID, _, _ time.Time) (map[uuid.UUID][]Record, error) {
	out := map[uuid.UUID][]Record{}
	for _, id := range ids {
		out[id] = m.recs[id]
	}
	return out, m.err
}

func (m *memStore) MaxObservedAt(context.Context, uuid.UUID) (*time.Time, error) { return m.max, m.err }

func source(name string, created int, fields ...string) SourceMeta {
	p := contracts.SourceProfile{Fields: []contracts.FieldProfile{}}
	for _, f := range fields {
		p.Fields = append(p.Fields, contracts.FieldProfile{Path: f, Role: "measure"})
	}
	return SourceMeta{ID: uuid.New(), Name: name, Kind: "csv", Profile: p, CreatedAt: day0.Add(time.Duration(created) * time.Hour)}
}

func rec(id string, at time.Time, payload map[string]any) Record {
	return Record{ExternalID: id, ObservedAt: at, Payload: payload}
}

// goodResult is a ScriptResult over the source keys "news" and "trips" that
// passes validation and yields one finding.
func goodResult() contracts.ScriptResult {
	return contracts.ScriptResult{
		Series: []contracts.SeriesInfo{
			{ID: "trips", Label: "Trips", Source: "trips", Grain: "day", Periods: 10, Points: 10},
			{ID: "news", Label: "News", Source: "news", Grain: "day", Periods: 10, Points: 10},
		},
		Tests: []contracts.Test{{
			Key: "corr:news:trips:lag+2", Kind: "correlation", Series: []string{"news", "trips"},
			P: 1e-6, Effect: 0.5, N: 100, Stats: map[string]any{"lag": 2.0},
		}},
	}
}

func newAnalyser(st Store, r pyrunner.Runner) *Analyser {
	return &Analyser{Store: st, Runner: r, Th: Thresholds{FDRQ: 0.05, MinN: 30},
		Timeout: 60 * time.Second, History: 90 * 24 * time.Hour, MaxTests: 2000}
}

// twoSources returns a project with a "News" and a "Trips" source.
func twoSources() (uuid.UUID, *memStore, SourceMeta, SourceMeta) {
	news, trips := source("News", 0, "title"), source("Trips", 1, "count")
	pid := uuid.New()
	return pid, &memStore{
		byProject: map[uuid.UUID][]SourceMeta{pid: {news, trips}},
		recs: map[uuid.UUID][]Record{
			news.ID:  {rec("n1", watermark.Add(-time.Hour), map[string]any{"title": "strike"})},
			trips.ID: {rec("t1", watermark.Add(-time.Hour), map[string]any{"count": 5.0})},
		},
	}, news, trips
}

func TestInputIsScopedToTheProjectAndWindow(t *testing.T) {
	pid, st, news, trips := twoSources()
	other := source("Other project's source", 2)
	st.byProject[uuid.New()] = []SourceMeta{other}
	st.recs[other.ID] = []Record{rec("o1", watermark, nil)}
	st.recs[news.ID] = []Record{
		rec("old", watermark.Add(-91*24*time.Hour), nil),     // before the history window
		rec("in", watermark.Add(-24*time.Hour), nil),         // inside
		rec("future", watermark.Add(time.Second), nil),       // after the watermark
		rec("edge", watermark, map[string]any{"title": "x"}), // exactly the watermark: kept
	}
	fake := pyrunnertest.Returns(goodResult())
	if _, err := newAnalyser(st, fake).Run(context.Background(), pid, "script", watermark); err != nil {
		t.Fatal(err)
	}
	reqs := fake.Requests()
	if len(reqs) != 1 {
		t.Fatalf("runner called %d times", len(reqs))
	}
	in := reqs[0].Input.(Input)
	if len(in.Sources) != 2 {
		t.Fatalf("INPUT has %d sources, want the project's 2", len(in.Sources))
	}
	for _, s := range in.Sources {
		if s.ID == other.ID.String() {
			t.Error("another project's source leaked into INPUT")
		}
	}
	got := in.Sources["news"].Records
	if len(got) != 2 || !got[0].ObservedAt.Equal(watermark.Add(-24*time.Hour)) || !got[1].ObservedAt.Equal(watermark) {
		t.Errorf("news records = %+v, want only the two in-window rows, oldest first", got)
	}
	if !in.DataUpTo.Equal(watermark) {
		t.Errorf("data_up_to = %v", in.DataUpTo)
	}
	if reqs[0].Timeout != 60*time.Second || reqs[0].Code != "script" {
		t.Errorf("request = %+v", reqs[0])
	}
	_ = trips
}

func TestInputRecordOrderAndEmptySources(t *testing.T) {
	a, b := source("A", 0), source("B", 1)
	at := watermark.Add(-time.Hour)
	in := BuildInput(watermark, 24*time.Hour, []SourceMeta{a, b}, map[uuid.UUID][]Record{
		a.ID: {rec("z", at, nil), rec("a", at, nil), rec("m", at.Add(-time.Minute), nil)},
	})
	ids := []string{}
	for _, r := range in.Sources["a"].Records {
		ids = append(ids, r.ObservedAt.Format("15:04:05"))
	}
	if len(in.Sources["a"].Records) != 3 || !in.Sources["a"].Records[0].ObservedAt.Before(in.Sources["a"].Records[1].ObservedAt) {
		t.Errorf("records not sorted by time: %v", ids)
	}
	b2, err := json.Marshal(in.Sources["b"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b2), `"records":[]`) || !strings.Contains(string(b2), `"fields":[]`) {
		t.Errorf("a source with no records must serialise empty lists, got %s", b2)
	}
}

func TestSourceKeys(t *testing.T) {
	first, second := source("News", 0), source("News", 1)
	punct := source("!!!", 2)
	third := source("  news  ", 3)
	// Passed out of order: keys follow created_at, not slice order.
	keys := SourceKeys([]SourceMeta{third, punct, second, first})
	want := map[uuid.UUID]string{first.ID: "news", second.ID: "news_2", punct.ID: "source", third.ID: "news_3"}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys = %v, want %v", keys, want)
	}
	w := source("Weather (Edinburgh) — daily", 0)
	if got := SourceKeys([]SourceMeta{w})[w.ID]; got != "weather_edinburgh_daily" {
		t.Errorf("slug = %q", got)
	}
}

func TestScriptErrors(t *testing.T) {
	pid, st, _, _ := twoSources()
	run := func(r pyrunner.Runner) error {
		_, err := newAnalyser(st, r).Run(context.Background(), pid, "script", watermark)
		return err
	}

	t.Run("raised", func(t *testing.T) {
		var se *ScriptError
		if err := run(pyrunnertest.Raises("Traceback...\nZeroDivisionError: division by zero")); !errors.As(err, &se) {
			t.Fatalf("err = %v", err)
		}
		if se.Kind != "raised" || !strings.Contains(se.Traceback, "ZeroDivisionError") {
			t.Errorf("err = %+v", se)
		}
		if !strings.Contains(se.ForAuthor(), "ZeroDivisionError") {
			t.Errorf("ForAuthor() = %q", se.ForAuthor())
		}
	})
	t.Run("timeout", func(t *testing.T) {
		var se *ScriptError
		if err := run(pyrunnertest.TimesOut()); !errors.As(err, &se) || se.Kind != "timeout" {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(se.Message, "60 s") {
			t.Errorf("message = %q", se.Message)
		}
	})
	t.Run("NaN in the result", func(t *testing.T) {
		var se *ScriptError
		err := run(pyrunnertest.ReturnsRaw([]byte(`{"series":[],"tests":[{"p":NaN}],"anomalies":[]}`)))
		if !errors.As(err, &se) || se.Kind != "invalid_result" {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(se.Message, "NaN and Infinity aren't valid JSON") {
			t.Errorf("missing hint: %q", se.Message)
		}
	})
	t.Run("fails validation", func(t *testing.T) {
		bad := goodResult()
		bad.Tests[0].P = 1.5
		var se *ScriptError
		if err := run(pyrunnertest.Returns(bad)); !errors.As(err, &se) || se.Kind != "invalid_result" {
			t.Fatalf("err = %v", err)
		}
		if !strings.Contains(se.Message, "tests[0].p") {
			t.Errorf("message should name the field: %q", se.Message)
		}
	})
	t.Run("unknown source key", func(t *testing.T) {
		bad := goodResult()
		bad.Series[0].Source = "weather"
		var se *ScriptError
		if err := run(pyrunnertest.Returns(bad)); !errors.As(err, &se) || !strings.Contains(se.Message, "series[0].source") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("runner failure is not a script error", func(t *testing.T) {
		boom := errors.New("runner unavailable")
		err := run(&pyrunnertest.Fake{Fn: func(pyrunner.Request) (pyrunner.Outcome, error) { return pyrunner.Outcome{}, boom }})
		var se *ScriptError
		if !errors.Is(err, boom) || errors.As(err, &se) {
			t.Errorf("err = %v; want the runner's error, unwrapped from ScriptError", err)
		}
	})
	t.Run("store failure is not a script error", func(t *testing.T) {
		broken := &memStore{err: errors.New("db down"), byProject: map[uuid.UUID][]SourceMeta{}}
		_, err := newAnalyser(broken, pyrunnertest.Returns(goodResult())).Run(context.Background(), pid, "s", watermark)
		var se *ScriptError
		if err == nil || errors.As(err, &se) {
			t.Errorf("err = %v", err)
		}
	})
}

func TestForAuthorNeverShowsResults(t *testing.T) {
	for _, e := range []*ScriptError{
		{Kind: "raised", Traceback: "ValueError: nope"},
		{Kind: "timeout", Message: "script ran past 60 s"},
		{Kind: "invalid_result", Message: "tests[0].p = 1.5: p must be between 0 and 1"},
	} {
		s := strings.ToLower(e.ForAuthor())
		for _, banned := range []string{"finding", "funnel", `"p"`, `"q"`} {
			if strings.Contains(s, banned) {
				t.Errorf("%s: ForAuthor() contains %q: %s", e.Kind, banned, s)
			}
		}
	}
}

func TestExtraOutputIsIgnored(t *testing.T) {
	pid, st, _, _ := twoSources()
	plain := pyrunnertest.Returns(goodResult())

	withExtras := &pyrunnertest.Fake{Fn: func(pyrunner.Request) (pyrunner.Outcome, error) {
		b, _ := json.Marshal(goodResult())
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		m["debug"] = map[string]any{"anything": []any{1, 2, 3}}
		m["findings"] = []any{"the script's own idea of findings"}
		raw, _ := json.Marshal(m)
		return pyrunner.Outcome{OK: true, Result: raw, Stdout: "lots of printed output"}, nil
	}}

	a, err := newAnalyser(st, plain).Run(context.Background(), pid, "s", watermark)
	if err != nil {
		t.Fatal(err)
	}
	b, err := newAnalyser(st, withExtras).Run(context.Background(), pid, "s", watermark)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(mustJSON(t, a), mustJSON(t, b)) {
		t.Errorf("extra keys or stdout changed the result:\n%s\n%s", mustJSON(t, a), mustJSON(t, b))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDeterminism(t *testing.T) {
	pid, st, _, _ := twoSources()
	first, err := newAnalyser(st, pyrunnertest.Returns(goodResult())).Run(context.Background(), pid, "s", watermark)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		again, err := newAnalyser(st, pyrunnertest.Returns(goodResult())).Run(context.Background(), pid, "s", watermark)
		if err != nil {
			t.Fatal(err)
		}
		if mustJSON(t, first) != mustJSON(t, again) {
			t.Fatalf("run %d differs:\n%s\n%s", i, mustJSON(t, first), mustJSON(t, again))
		}
	}
	if len(first.Findings) != 1 || first.Findings[0].FindingKey != "corr:news:trips:lag+2" {
		t.Errorf("findings = %+v", first.Findings)
	}
	if first.Health.Series[0].ID != "news" {
		t.Errorf("health series should be sorted by id: %+v", first.Health.Series)
	}
}

func TestHealth(t *testing.T) {
	pid, st, news, trips := twoSources()
	st.recs[news.ID] = []Record{
		rec("a", watermark.Add(-4*time.Hour), map[string]any{"title": "x", "author": "y", "meta": map[string]any{"lang": "en", "score": 1.0}}),
		rec("b", watermark.Add(-3*time.Hour), map[string]any{"title": "x"}),
		rec("c", watermark.Add(-time.Hour), map[string]any{"title": "x"}),
	}
	res, err := newAnalyser(st, pyrunnertest.Returns(goodResult())).Run(context.Background(), pid, "s", watermark)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Health.UnprofiledKeys[news.ID.String()]; !reflect.DeepEqual(got, []string{"author", "meta.lang", "meta.score"}) {
		t.Errorf("unprofiled keys = %v", got)
	}
	if _, ok := res.Health.UnprofiledKeys[trips.ID.String()]; ok {
		t.Error("a fully profiled source must be omitted from unprofiled_keys")
	}
	h := res.Health.Sources[news.ID.String()]
	if h.Records != 3 || !h.FirstAt.Equal(watermark.Add(-4*time.Hour)) || !h.LastAt.Equal(watermark.Add(-time.Hour)) {
		t.Errorf("news health = %+v", h)
	}
	if h.MedianGapS != 5400 { // gaps are 3600 and 7200
		t.Errorf("median gap = %v, want 5400", h.MedianGapS)
	}
	if one := res.Health.Sources[trips.ID.String()]; one.Records != 1 || one.MedianGapS != 0 {
		t.Errorf("single-record source health = %+v", one)
	}
}

func TestNilRunner(t *testing.T) {
	pid, st, _, _ := twoSources()
	_, err := newAnalyser(st, nil).Run(context.Background(), pid, "s", watermark)
	if !errors.Is(err, ErrNoRunner) {
		t.Errorf("err = %v, want ErrNoRunner", err)
	}
}

func TestWatermark(t *testing.T) {
	st := &memStore{}
	if _, ok, err := Watermark(context.Background(), st, uuid.New()); ok || err != nil {
		t.Errorf("no records: ok=%v err=%v", ok, err)
	}
	st.max = &watermark
	if got, ok, err := Watermark(context.Background(), st, uuid.New()); !ok || err != nil || !got.Equal(watermark) {
		t.Errorf("got %v ok=%v err=%v", got, ok, err)
	}
	st.err = errors.New("db down")
	if _, _, err := Watermark(context.Background(), st, uuid.New()); err == nil {
		t.Error("store errors must propagate")
	}
}
