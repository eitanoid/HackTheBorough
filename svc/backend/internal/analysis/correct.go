// Package analysis turns a project's records into an AnalysisResult: build
// INPUT, run the author's script through the Python runner, validate what it
// reports, and apply multiple-testing correction in Go. The script never
// decides what counts as a finding.
package analysis

import (
	"math"
	"sort"
	"strings"
	"time"

	"hacktheburgh/backend/internal/contracts"
)

type Thresholds struct {
	FDRQ float64 // Benjamini–Hochberg level
	MinN int     // fewest observations a test may rest on
}

const (
	maxAnomaliesPerSeries = 3
	maxRejected           = 3
)

// BH returns Benjamini–Hochberg q-values in input order.
//
// Sort ascending by p; q at rank i is the minimum over ranks j >= i of
// (m/j)·p_j, capped at 1. Equal p values receive equal q, so the output does
// not depend on how ties are ordered.
func BH(p []float64) []float64 {
	m := len(p)
	q := make([]float64, m)
	if m == 0 {
		return q
	}
	idx := make([]int, m)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return p[idx[a]] < p[idx[b]] })

	running := 1.0
	for rank := m; rank >= 1; rank-- {
		i := idx[rank-1]
		running = math.Min(running, float64(m)/float64(rank)*p[i])
		q[i] = running
	}
	return q
}

func pairKey(series []string) string {
	s := append([]string(nil), series...)
	sort.Strings(s)
	return strings.Join(s, "|")
}

// Correct turns a validated ScriptResult into findings, rejected tests and the
// funnel. The family for correction is every test the script reported,
// including those with too few observations, so the script cannot shrink the
// family by omission.
func Correct(r contracts.ScriptResult, th Thresholds) (findings, rejected []contracts.Finding, funnel map[string]int) {
	ps := make([]float64, len(r.Tests))
	for i, t := range r.Tests {
		ps[i] = t.P
	}
	qs := BH(ps)

	// failedBy[i] is "" for a candidate and "fdr" or "min_n" for a rejected test.
	failedBy := make([]string, len(r.Tests))
	rawLt05, nCandidates := 0, 0
	for i, t := range r.Tests {
		if t.P < 0.05 {
			rawLt05++
		}
		switch {
		case t.N < th.MinN:
			failedBy[i] = "min_n"
		case qs[i] > th.FDRQ:
			failedBy[i] = "fdr"
		default:
			nCandidates++
		}
	}

	// One finding per correlated pair: the largest |effect| wins; ties go to
	// the lower q, then the lower key. Custom tests are not grouped.
	winner := map[string]int{}
	for i, t := range r.Tests {
		if failedBy[i] != "" || t.Kind != "correlation" {
			continue
		}
		pk := pairKey(t.Series)
		if w, seen := winner[pk]; !seen || beats(r.Tests, qs, i, w) {
			winner[pk] = i
		}
	}

	findings = []contracts.Finding{}
	kept := 0
	for i, t := range r.Tests {
		if failedBy[i] != "" {
			continue
		}
		if t.Kind == "correlation" && winner[pairKey(t.Series)] != i {
			continue
		}
		findings = append(findings, testFinding(t, qs[i], ""))
		kept++
	}

	findings = append(findings, anomalyFindings(r.Anomalies)...)
	sort.SliceStable(findings, func(a, b int) bool { return findings[a].FindingKey < findings[b].FindingKey })

	order := make([]int, 0, len(r.Tests))
	for i := range r.Tests {
		if failedBy[i] != "" {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(a, b int) bool {
		ta, tb := r.Tests[order[a]], r.Tests[order[b]]
		if ta.P != tb.P {
			return ta.P < tb.P
		}
		return ta.Key < tb.Key
	})
	rejected = []contracts.Finding{}
	seenPair := map[string]bool{}
	for _, i := range order {
		if len(rejected) == maxRejected {
			break
		}
		t := r.Tests[i]
		pk := pairKey(t.Series)
		if seenPair[pk] {
			continue
		}
		seenPair[pk] = true
		rejected = append(rejected, testFinding(t, qs[i], failedBy[i]))
	}

	funnel = map[string]int{
		"tests_run":   len(r.Tests),
		"raw_p_lt_05": rawLt05,
		"passed_fdr":  nCandidates,
		"kept":        kept,
	}
	return findings, rejected, funnel
}

func beats(tests []contracts.Test, q []float64, i, w int) bool {
	ei, ew := math.Abs(tests[i].Effect), math.Abs(tests[w].Effect)
	if ei != ew {
		return ei > ew
	}
	if q[i] != q[w] {
		return q[i] < q[w]
	}
	return tests[i].Key < tests[w].Key
}

// testFinding copies the script's stats, then sets p, q, n and effect from Go's
// numbers so a script cannot supply its own corrected values.
func testFinding(t contracts.Test, q float64, rejectedBy string) contracts.Finding {
	stats := make(map[string]any, len(t.Stats)+5)
	for k, v := range t.Stats {
		stats[k] = v
	}
	stats["p"] = t.P
	stats["q"] = q
	stats["n"] = float64(t.N)
	stats["effect"] = t.Effect
	if rejectedBy != "" {
		stats["rejected_by"] = rejectedBy
	}
	return contracts.Finding{
		FindingKey: t.Key,
		Kind:       t.Kind,
		Series:     append([]string(nil), t.Series...),
		Stats:      stats,
		Chart:      t.Chart,
	}
}

func anomalyFindings(all []contracts.Anomaly) []contracts.Finding {
	bySeries := map[string][]contracts.Anomaly{}
	for _, a := range all {
		bySeries[a.Series] = append(bySeries[a.Series], a)
	}
	var out []contracts.Finding
	for _, as := range bySeries {
		sort.SliceStable(as, func(i, j int) bool {
			si, sj := math.Abs(as[i].Score), math.Abs(as[j].Score)
			if si != sj {
				return si > sj
			}
			return as[i].Key < as[j].Key
		})
		if len(as) > maxAnomaliesPerSeries {
			as = as[:maxAnomaliesPerSeries]
		}
		for _, a := range as {
			out = append(out, contracts.Finding{
				FindingKey: a.Key,
				Kind:       "anomaly",
				Series:     []string{a.Series},
				Stats: map[string]any{
					"value":    a.Value,
					"expected": a.Expected,
					"score":    a.Score,
					"period":   a.Period.UTC().Format(time.RFC3339),
				},
				Chart: a.Chart,
			})
		}
	}
	return out
}
