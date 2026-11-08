package narration

import (
	"testing"

	"hacktheburgh/backend/internal/contracts"
)

func corr(effect float64, lag any) contracts.Finding {
	stats := map[string]any{"effect": effect}
	if lag != nil {
		stats["lag"] = lag
	}
	return contracts.Finding{FindingKey: "corr:a:b", Kind: "correlation", Series: []string{"a", "b"}, Stats: stats}
}

func TestDirection(t *testing.T) {
	cases := []struct {
		name string
		f    contracts.Finding
		want string
	}{
		{"positive", corr(0.4, nil), "positive"},
		{"negative", corr(-0.4, nil), "negative"},
		{"zero effect", corr(0, nil), "positive"},
		{"custom negative", contracts.Finding{Kind: "custom", Stats: map[string]any{"effect": -1.0}}, "negative"},
		{"anomaly up", contracts.Finding{Kind: "anomaly", Stats: map[string]any{"value": 9.0, "expected": 3.0}}, "up"},
		{"anomaly down", contracts.Finding{Kind: "anomaly", Stats: map[string]any{"value": 1.0, "expected": 3.0}}, "down"},
		{"anomaly equal", contracts.Finding{Kind: "anomaly", Stats: map[string]any{"value": 3.0, "expected": 3.0}}, "up"},
	}
	for _, c := range cases {
		if got := Direction(c.f); got != c.want {
			t.Errorf("%s: Direction = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestLeader(t *testing.T) {
	cases := []struct {
		name string
		f    contracts.Finding
		want string // "" means nil
	}{
		{"lag +2", corr(0.4, 2.0), "a"},
		{"lag -1", corr(0.4, -1.0), "b"},
		{"lag 0", corr(0.4, 0.0), ""},
		{"missing lag", corr(0.4, nil), ""},
		{"string lag", corr(0.4, "2"), ""},
		{"int lag", corr(0.4, 3), "a"},
		{"anomaly", contracts.Finding{Kind: "anomaly", Series: []string{"a"}, Stats: map[string]any{"lag": 2.0}}, ""},
	}
	for _, c := range cases {
		got := Leader(c.f)
		switch {
		case c.want == "" && got != nil:
			t.Errorf("%s: Leader = %q, want nil", c.name, *got)
		case c.want != "" && (got == nil || *got != c.want):
			t.Errorf("%s: Leader = %v, want %q", c.name, got, c.want)
		}
	}
}
