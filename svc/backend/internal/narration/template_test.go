package narration

import (
	"strings"
	"testing"
	"unicode"

	"hacktheburgh/backend/internal/contracts"
)

func testContext() Context {
	return Context{Series: map[string]SeriesContext{
		"a": {Label: "Strike news", Grain: "day"},
		"b": {Label: "Bus trips", Grain: "day"},
	}}
}

func TestTemplate(t *testing.T) {
	anomalyUp := contracts.Finding{Kind: "anomaly", Series: []string{"b"}, Stats: map[string]any{"value": 9.0, "expected": 3.0}}
	anomalyDown := contracts.Finding{Kind: "anomaly", Series: []string{"b"}, Stats: map[string]any{"value": 1.0, "expected": 3.0}}
	custom := contracts.Finding{Kind: "custom", Series: []string{"a", "b"}, Stats: map[string]any{"effect": 0.2}}
	cases := []struct {
		name  string
		f     contracts.Finding
		wants []string
	}{
		{"positive with leader", corr(0.4, 2.0), []string{"Strike news", "Bus trips", "move together", "Strike news moving first"}},
		{"negative", corr(-0.4, nil), []string{"opposite"}},
		{"negative leader b", corr(-0.4, -1.0), []string{"Bus trips moving first"}},
		{"anomaly up", anomalyUp, []string{"Bus trips was unusually high", "day value", "above"}},
		{"anomaly down", anomalyDown, []string{"unusually low", "below"}},
		{"custom", custom, []string{"Strike news and Bus trips", "passed correction"}},
	}
	for _, c := range cases {
		got := Template(testContext(), c.f)
		if got.Headline == "" || got.Explanation == "" {
			t.Fatalf("%s: empty text: %+v", c.name, got)
		}
		all := got.Headline + " | " + got.Explanation
		for _, w := range c.wants {
			if !strings.Contains(all, w) {
				t.Errorf("%s: %q missing from %q", c.name, w, all)
			}
		}
		if got.NarratedBy != "template" || got.Plausibility != nil {
			t.Errorf("%s: NarratedBy=%q Plausibility=%v", c.name, got.NarratedBy, got.Plausibility)
		}
		if strings.IndexFunc(all+strings.Join(got.Caveats, ""), unicode.IsDigit) >= 0 {
			t.Errorf("%s: template text has a digit: %q", c.name, all)
		}
	}
}

func TestTemplateFallsBackToIDs(t *testing.T) {
	got := Template(Context{}, corr(0.4, nil))
	if !strings.Contains(got.Headline, "a and b") {
		t.Errorf("headline = %q", got.Headline)
	}
}

func TestTemplateCaveats(t *testing.T) {
	if c := Template(testContext(), corr(0.4, nil)).Caveats; len(c) != 1 {
		t.Errorf("correlation caveats = %v", c)
	}
	if c := Template(testContext(), contracts.Finding{Kind: "custom", Series: []string{"a"}, Stats: map[string]any{}}).Caveats; len(c) != 0 {
		t.Errorf("custom caveats = %v", c)
	}
}
