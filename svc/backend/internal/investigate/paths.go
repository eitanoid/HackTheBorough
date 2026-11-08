package investigate

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// maxClaimString is the longest string a claim may point at.
const maxClaimString = 80

// Resolve walks a dotted path ("result.lags.3.rho") into a step's output. Each
// segment is a map key or a non-negative array index. It reports false for an
// empty path, a missing key, an index out of range, or a segment applied to a
// scalar.
func Resolve(output json.RawMessage, path string) (any, bool) {
	if path == "" || len(output) == 0 {
		return nil, false
	}
	var cur any
	if err := json.Unmarshal(output, &cur); err != nil {
		return nil, false
	}
	for _, seg := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// ClaimValue accepts what a card may show: a finite number or a string of at
// most 80 characters. Maps, arrays, booleans and nulls are not claimable.
func ClaimValue(v any) (any, bool) {
	switch x := v.(type) {
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, false
		}
		return x, true
	case string:
		if x == "" || len([]rune(x)) > maxClaimString {
			return nil, false
		}
		return x, true
	}
	return nil, false
}
