package narration

import (
	"context"
	"fmt"
	"log/slog"
	"unicode/utf8"

	"hacktheburgh/backend/internal/contracts"
	"hacktheburgh/backend/internal/llm"
)

const (
	maxHeadline    = 120
	maxExplanation = 600
)

type Narrator struct {
	LLM *llm.Client // nil: every finding gets template text, no calls
}

// Narrate writes text for each finding. It never fails: any finding the model
// doesn't narrate acceptably gets Template text. At most one Generate call
// (two model calls with its internal retry).
func (n *Narrator) Narrate(ctx context.Context, c Context, fs []contracts.Finding) map[string]Text {
	out := make(map[string]Text, len(fs))
	if len(fs) == 0 {
		return out
	}
	fallback := func() map[string]Text {
		for _, f := range fs {
			out[f.FindingKey] = Template(c, f)
		}
		return out
	}
	if n == nil || n.LLM == nil {
		return fallback()
	}

	asked := make(map[string]bool, len(fs))
	for _, f := range fs {
		asked[f.FindingKey] = true
	}
	reply, err := llm.Generate(ctx, n.LLM, llm.Request{
		Purpose: llm.Narrator, System: systemPrompt(), User: userPrompt(c, fs),
	}, func(r contracts.Narration) error { return validateReply(r, asked) })
	if err != nil {
		slog.Warn("narration failed; using templates", "findings", len(fs), "err", err)
		return fallback()
	}

	drafts := make(map[string]contracts.InsightDraft, len(reply.Insights))
	for _, d := range reply.Insights {
		drafts[d.FindingKey] = d
	}
	for _, f := range fs {
		d, ok := drafts[f.FindingKey]
		if !ok {
			slog.Warn("narrator left out a finding; using template", "finding", f.FindingKey)
			out[f.FindingKey] = Template(c, f)
			continue
		}
		if err := checkCard(d, f); err != nil {
			slog.Warn("narrator card rejected; using template", "finding", f.FindingKey, "err", err)
			out[f.FindingKey] = Template(c, f)
			continue
		}
		plausibility := d.Plausibility
		out[f.FindingKey] = Text{
			Headline: d.Headline, Explanation: d.Explanation, Caveats: nonNil(d.Caveats),
			NarratedBy: "llm", Plausibility: &plausibility,
		}
	}
	return out
}

// validateReply is the whole-reply check passed to llm.Generate: only
// structural problems, so one bad card never costs the others their text.
func validateReply(r contracts.Narration, asked map[string]bool) error {
	seen := make(map[string]bool, len(r.Insights))
	for _, d := range r.Insights {
		if !asked[d.FindingKey] {
			return fmt.Errorf("finding_key %q was not asked for", d.FindingKey)
		}
		if seen[d.FindingKey] {
			return fmt.Errorf("finding_key %q appears more than once", d.FindingKey)
		}
		seen[d.FindingKey] = true
	}
	return nil
}

// checkCard is the per-card check. A failure means template text, no retry.
// The draft's leader is ignored: Go's value is used.
func checkCard(d contracts.InsightDraft, f contracts.Finding) error {
	if h := utf8.RuneCountInString(d.Headline); h < 1 || h > maxHeadline {
		return fmt.Errorf("headline is %d characters", h)
	}
	if e := utf8.RuneCountInString(d.Explanation); e < 1 || e > maxExplanation {
		return fmt.Errorf("explanation is %d characters", e)
	}
	if want := Direction(f); d.Direction != want {
		return fmt.Errorf("direction %q, computed %q", d.Direction, want)
	}
	switch d.Plausibility {
	case "plausible":
	case "doubtful":
		if len(d.Caveats) == 0 || d.Caveats[0] == "" {
			return fmt.Errorf("doubtful without a reason")
		}
	default:
		return fmt.Errorf("plausibility %q", d.Plausibility)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
