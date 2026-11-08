package narration

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/llm"
)

// scripted replays canned replies and records every request.
type scripted struct {
	mu      sync.Mutex
	replies []string // a reply starting with "error:" makes the call fail
	reqs    []llm.ChatRequest
}

func (s *scripted) Chat(_ context.Context, req llm.ChatRequest) (llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reqs = append(s.reqs, req)
	if len(s.reqs) > len(s.replies) {
		return llm.ChatResponse{}, errors.New("scripted: out of replies")
	}
	r := s.replies[len(s.reqs)-1]
	if msg, ok := strings.CutPrefix(r, "error:"); ok {
		return llm.ChatResponse{}, errors.New(msg)
	}
	return llm.ChatResponse{Content: r}, nil
}

func (s *scripted) calls() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.reqs) }

func (s *scripted) userMessage() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.reqs[0].Messages {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

func newNarrator(p llm.Provider) *Narrator {
	return &Narrator{LLM: &llm.Client{Provider: p, Models: map[llm.Purpose]string{llm.Narrator: "test-model"}, Timeout: 5 * time.Second}}
}

func narrationContext() Context {
	return Context{
		Goal: "Does strike news lead bus trips?", InsightPrompt: "care about commuters",
		Series: map[string]SeriesContext{
			"a": {Label: "Strike news", Grain: "day", SourceSummary: "Local transport news"},
			"b": {Label: "Bus trips", Grain: "day"},
		},
	}
}

func finding(key string, effect float64) contracts.Finding {
	f := corr(effect, 2.0)
	f.FindingKey = key
	return f
}

func draft(f contracts.Finding) contracts.InsightDraft {
	return contracts.InsightDraft{
		FindingKey: f.FindingKey, Headline: "Headline " + f.FindingKey, Explanation: "Explanation.",
		Direction: Direction(f), Plausibility: "plausible", Caveats: []string{"Be careful."},
	}
}

func reply(ds ...contracts.InsightDraft) string {
	b, _ := json.Marshal(contracts.Narration{Insights: ds})
	return string(b)
}

func three() []contracts.Finding {
	return []contracts.Finding{finding("k1", 0.5), finding("k2", -0.4), finding("k3", 0.3)}
}

func TestNarrateHappyPath(t *testing.T) {
	fs := three()
	p := &scripted{replies: []string{reply(draft(fs[0]), draft(fs[1]), draft(fs[2]))}}
	got := newNarrator(p).Narrate(context.Background(), narrationContext(), fs)
	if p.calls() != 1 {
		t.Fatalf("calls = %d, want 1", p.calls())
	}
	for _, f := range fs {
		g := got[f.FindingKey]
		if g.NarratedBy != "llm" || g.Plausibility == nil || *g.Plausibility != "plausible" {
			t.Errorf("%s: %+v", f.FindingKey, g)
		}
		if g.Headline != "Headline "+f.FindingKey {
			t.Errorf("%s: headline %q", f.FindingKey, g.Headline)
		}
	}
	user := p.userMessage()
	for _, want := range []string{"Does strike news lead bus trips?", "care about commuters", "Strike news", "Local transport news", `"direction": "negative"`} {
		if !strings.Contains(user, want) {
			t.Errorf("prompt is missing %q:\n%s", want, user)
		}
	}
}

func TestNarrateMissingCardFallsBackAlone(t *testing.T) {
	fs := three()
	p := &scripted{replies: []string{reply(draft(fs[0]), draft(fs[2]))}}
	got := newNarrator(p).Narrate(context.Background(), narrationContext(), fs)
	if p.calls() != 1 {
		t.Fatalf("calls = %d, want 1", p.calls())
	}
	if got["k2"].NarratedBy != "template" || got["k1"].NarratedBy != "llm" || got["k3"].NarratedBy != "llm" {
		t.Errorf("got %+v", got)
	}
}

func TestNarrateDuplicateKeyTwiceFallsBackAll(t *testing.T) {
	fs := three()
	bad := reply(draft(fs[0]), draft(fs[0]), draft(fs[1]), draft(fs[2]))
	p := &scripted{replies: []string{bad, bad}}
	got := newNarrator(p).Narrate(context.Background(), narrationContext(), fs)
	if p.calls() != 2 {
		t.Fatalf("calls = %d, want 2", p.calls())
	}
	assertAllTemplate(t, got, fs)
}

func TestNarrateUnknownKeyIsRetried(t *testing.T) {
	fs := three()
	stray := draft(fs[0])
	stray.FindingKey = "other"
	p := &scripted{replies: []string{reply(stray), reply(draft(fs[0]), draft(fs[1]), draft(fs[2]))}}
	got := newNarrator(p).Narrate(context.Background(), narrationContext(), fs)
	if p.calls() != 2 || got["k1"].NarratedBy != "llm" {
		t.Fatalf("calls = %d, got %+v", p.calls(), got)
	}
}

func TestNarrateProviderErrors(t *testing.T) {
	fs := three()
	p := &scripted{replies: []string{"error:boom", "error:boom"}}
	got := newNarrator(p).Narrate(context.Background(), narrationContext(), fs)
	assertAllTemplate(t, got, fs)
}

func TestNarrateWrongDirection(t *testing.T) {
	fs := three()
	wrong := draft(fs[1])
	wrong.Direction = "positive" // k2 is negative
	p := &scripted{replies: []string{reply(draft(fs[0]), wrong, draft(fs[2]))}}
	got := newNarrator(p).Narrate(context.Background(), narrationContext(), fs)
	if got["k2"].NarratedBy != "template" || got["k1"].NarratedBy != "llm" || got["k3"].NarratedBy != "llm" {
		t.Errorf("got %+v", got)
	}
	if !strings.Contains(got["k2"].Headline, "opposite") {
		t.Errorf("template headline = %q", got["k2"].Headline)
	}
}

func TestNarrateDoubtfulKeepsReason(t *testing.T) {
	fs := three()
	d := draft(fs[0])
	d.Plausibility = "doubtful"
	d.Caveats = []string{"Both series dip at weekends.", "Second caveat."}
	p := &scripted{replies: []string{reply(d, draft(fs[1]), draft(fs[2]))}}
	got := newNarrator(p).Narrate(context.Background(), narrationContext(), fs)
	g := got["k1"]
	if g.NarratedBy != "llm" || g.Plausibility == nil || *g.Plausibility != "doubtful" || g.Caveats[0] != "Both series dip at weekends." {
		t.Errorf("got %+v", g)
	}
}

func TestNarrateDoubtfulWithoutCaveats(t *testing.T) {
	fs := three()
	d := draft(fs[0])
	d.Plausibility = "doubtful"
	d.Caveats = nil
	p := &scripted{replies: []string{reply(d, draft(fs[1]), draft(fs[2]))}}
	got := newNarrator(p).Narrate(context.Background(), narrationContext(), fs)
	if g := got["k1"]; g.NarratedBy != "template" || g.Plausibility != nil {
		t.Errorf("k1 = %+v", g)
	}
	if got["k2"].NarratedBy != "llm" || got["k3"].NarratedBy != "llm" {
		t.Errorf("others affected: %+v", got)
	}
}

func TestNarrateLengthLimits(t *testing.T) {
	fs := three()
	long := draft(fs[0])
	long.Headline = strings.Repeat("x", maxHeadline+1)
	empty := draft(fs[1])
	empty.Explanation = ""
	p := &scripted{replies: []string{reply(long, empty, draft(fs[2]))}}
	got := newNarrator(p).Narrate(context.Background(), narrationContext(), fs)
	if got["k1"].NarratedBy != "template" || got["k2"].NarratedBy != "template" || got["k3"].NarratedBy != "llm" {
		t.Errorf("got %+v", got)
	}
}

func TestNarrateNilLLM(t *testing.T) {
	fs := three()
	assertAllTemplate(t, (&Narrator{}).Narrate(context.Background(), narrationContext(), fs), fs)
	var n *Narrator
	assertAllTemplate(t, n.Narrate(context.Background(), narrationContext(), fs), fs)
}

func TestNarrateEmptyFindings(t *testing.T) {
	p := &scripted{}
	got := newNarrator(p).Narrate(context.Background(), narrationContext(), nil)
	if p.calls() != 0 || len(got) != 0 {
		t.Errorf("calls = %d, got %v", p.calls(), got)
	}
}

func assertAllTemplate(t *testing.T, got map[string]Text, fs []contracts.Finding) {
	t.Helper()
	if len(got) != len(fs) {
		t.Fatalf("got %d texts for %d findings", len(got), len(fs))
	}
	for _, f := range fs {
		g := got[f.FindingKey]
		if g.NarratedBy != "template" || g.Plausibility != nil || g.Headline == "" {
			t.Errorf("%s: %+v", f.FindingKey, g)
		}
	}
}
