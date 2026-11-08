package narration

import (
	"encoding/json"

	"hacktheburgh/backend/internal/contracts"
)

// Direction is computed from executed numbers, never taken from the model:
// positive | negative for tests, up | down for anomalies.
func Direction(f contracts.Finding) string {
	if f.Kind == "anomaly" {
		value, _ := Number(f.Stats["value"])
		expected, _ := Number(f.Stats["expected"])
		if value >= expected {
			return "up"
		}
		return "down"
	}
	if effect, _ := Number(f.Stats["effect"]); effect < 0 {
		return "negative"
	}
	return "positive"
}

// Leader is the series id that moves first, or nil. Only correlations with a
// numeric lag have one. A lag k > 0 compares series[0] at period t with
// series[1] at period t+k, so series[0] moves first.
func Leader(f contracts.Finding) *string {
	if f.Kind != "correlation" || len(f.Series) < 2 {
		return nil
	}
	lag, ok := Number(f.Stats["lag"])
	switch {
	case !ok || lag == 0:
		return nil
	case lag > 0:
		return &f.Series[0]
	default:
		return &f.Series[1]
	}
}

// Number reads a stats value as a float. Stats come from JSON, so they are
// normally float64, but rows built in Go may hold other numeric types.
func Number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
