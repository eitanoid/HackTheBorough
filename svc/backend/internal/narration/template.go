package narration

import (
	"strings"

	"hacktheburgh/backend/internal/contracts"
)

// Context is what the narrator knows about the project besides the findings.
type Context struct {
	Goal          string
	InsightPrompt string
	Series        map[string]SeriesContext // series id ->
}

type SeriesContext struct {
	Label         string
	Unit          *string
	Grain         string // hour | day | week
	SourceSummary string // the source profile's one-sentence summary; may be empty
}

// Text is the words on one card.
type Text struct {
	Headline     string
	Explanation  string
	Caveats      []string
	NarratedBy   string  // llm | template
	Plausibility *string // plausible | doubtful; nil for template text
}

func (c Context) label(id string) string {
	if s, ok := c.Series[id]; ok && s.Label != "" {
		return s.Label
	}
	return id
}

func (c Context) grain(id string) string {
	if s, ok := c.Series[id]; ok && s.Grain != "" {
		return s.Grain
	}
	return "period"
}

// Template writes card text without an LLM. It never prints a number: every
// figure on a card is rendered from stats. Template text has no verdict.
func Template(c Context, f contracts.Finding) Text {
	a, b := "", ""
	if len(f.Series) > 0 {
		a = c.label(f.Series[0])
	}
	if len(f.Series) > 1 {
		b = c.label(f.Series[1])
	}
	t := Text{NarratedBy: "template"}
	switch f.Kind {
	case "anomaly":
		word, side := "high", "above"
		if Direction(f) == "down" {
			word, side = "low", "below"
		}
		grain := "period"
		if len(f.Series) > 0 {
			grain = c.grain(f.Series[0])
		}
		t.Headline = a + " was unusually " + word
		t.Explanation = "The latest " + grain + " value of " + a + " is well " + side + " its recent typical range."
		t.Caveats = []string{"A single unusual period can be a data error."}
	case "custom":
		t.Headline = "A custom test on " + a
		if b != "" {
			t.Headline += " and " + b
		}
		t.Headline += " passed correction"
		t.Explanation = "The project's own test on these series held up after correcting for every test run on this project."
	default:
		way, ways := "move together", "the same way"
		if Direction(f) == "negative" {
			way, ways = "move in opposite directions", "opposite ways"
		}
		clause, clauseSentence := "", ""
		if l := Leader(f); l != nil {
			clause = ", with " + c.label(*l) + " moving first"
			clauseSentence = ", and " + c.label(*l) + " moves first"
		}
		t.Headline = a + " and " + b + " " + way + clause
		t.Explanation = "Changes in " + a + " and " + b + " tend to go " + ways + clauseSentence +
			". The link held up after correcting for every test run on this project."
		t.Caveats = []string{"A correlation does not show that one causes the other."}
	}
	t.Headline = strings.TrimSpace(t.Headline)
	return t
}
