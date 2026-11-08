package analysis

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"

	"hacktheburgh/backend/internal/contracts"
)

var th = Thresholds{FDRQ: 0.05, MinN: 30}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func corr(key, a, b string, p, effect float64, n int) contracts.Test {
	return contracts.Test{Key: key, Kind: "correlation", Series: []string{a, b}, P: p, Effect: effect, N: n,
		Stats: map[string]any{"rho": effect}}
}

func result(tests ...contracts.Test) contracts.ScriptResult {
	return contracts.ScriptResult{Tests: tests}
}

func TestBH(t *testing.T) {
	t.Run("hand computed", func(t *testing.T) {
		got := BH([]float64{0.01, 0.04, 0.03, 0.005})
		want := []float64{0.02, 0.04, 0.04, 0.02}
		for i := range want {
			if !near(got[i], want[i]) {
				t.Errorf("q[%d] = %v, want %v", i, got[i], want[i])
			}
		}
	})
	t.Run("empty", func(t *testing.T) {
		if got := BH(nil); len(got) != 0 {
			t.Errorf("got %v", got)
		}
	})
	t.Run("single test is its own p", func(t *testing.T) {
		if got := BH([]float64{0.03}); !near(got[0], 0.03) {
			t.Errorf("got %v", got)
		}
	})
	t.Run("all ones", func(t *testing.T) {
		for _, q := range BH([]float64{1, 1, 1}) {
			if q != 1 {
				t.Errorf("q = %v, want 1", q)
			}
		}
	})
	t.Run("capped at 1", func(t *testing.T) {
		for _, q := range BH([]float64{0.9, 0.95}) {
			if q > 1 {
				t.Errorf("q = %v exceeds 1", q)
			}
		}
	})
	t.Run("ties share a q whatever their order", func(t *testing.T) {
		a := BH([]float64{0.02, 0.02, 0.5, 0.02})
		b := BH([]float64{0.5, 0.02, 0.02, 0.02})
		if a[0] != a[1] || a[1] != a[3] {
			t.Errorf("tied p values got different q: %v", a)
		}
		if a[0] != b[1] || a[2] != b[0] {
			t.Errorf("q depends on input order: %v vs %v", a, b)
		}
	})
}

func TestNullTestsGiveNoFindings(t *testing.T) {
	var tests []contracts.Test
	for k := 1; k <= 40; k++ {
		tests = append(tests, corr(fmt.Sprintf("t%02d", k), fmt.Sprintf("a%d", k), "b", float64(k)/40, 0.1, 100))
	}
	tests = append(tests, corr("extra", "x", "y", 0.04, 0.2, 100))
	findings, _, funnel := Correct(result(tests...), th)
	if len(findings) != 0 {
		t.Errorf("got %d findings from null tests: %+v", len(findings), findings)
	}
	if funnel["tests_run"] != 41 || funnel["passed_fdr"] != 0 || funnel["kept"] != 0 {
		t.Errorf("funnel = %v", funnel)
	}
}

func TestOneStrongTest(t *testing.T) {
	tests := []contracts.Test{corr("strong", "a", "b", 0.0001, 0.6, 100)}
	for k := 1; k <= 20; k++ {
		tests = append(tests, corr(fmt.Sprintf("null%02d", k), fmt.Sprintf("n%d", k), "z", 0.2+float64(k)/30, 0.05, 100))
	}
	findings, _, funnel := Correct(result(tests...), th)
	if len(findings) != 1 || findings[0].FindingKey != "strong" {
		t.Fatalf("findings = %+v", findings)
	}
	if q := findings[0].Stats["q"].(float64); q > 0.05 {
		t.Errorf("q = %v, want <= 0.05", q)
	}
	if funnel["kept"] != 1 || funnel["passed_fdr"] != 1 {
		t.Errorf("funnel = %v", funnel)
	}
}

func TestSmallNIsCountedButNotKept(t *testing.T) {
	findings, rejected, funnel := Correct(result(corr("tiny", "a", "b", 0.0001, 0.9, 10)), th)
	if len(findings) != 0 {
		t.Errorf("findings = %+v", findings)
	}
	if funnel["tests_run"] != 1 || funnel["raw_p_lt_05"] != 1 || funnel["passed_fdr"] != 0 {
		t.Errorf("funnel = %v", funnel)
	}
	if len(rejected) != 1 || rejected[0].Stats["rejected_by"] != "min_n" {
		t.Errorf("rejected = %+v", rejected)
	}
}

func TestSmallNTestStillCountsInTheFamily(t *testing.T) {
	// Two tests at p = 0.03. Alone, q would be 0.03; with a third test in the
	// family it rises to 0.045, still passing. With a fourth (n too small) it
	// rises past 0.05, so the small-n test must count in m.
	base := []contracts.Test{
		corr("a", "a1", "b1", 0.03, 0.5, 100),
		corr("b", "a2", "b2", 0.03, 0.5, 100),
		corr("c", "a3", "b3", 0.9, 0.1, 100),
	}
	if f, _, _ := Correct(result(base...), th); len(f) != 2 {
		t.Fatalf("setup: expected 2 findings, got %d", len(f))
	}
	with := append(base, corr("tiny", "a4", "b4", 0.9, 0.1, 5))
	if f, _, _ := Correct(result(with...), th); len(f) != 0 {
		t.Errorf("a small-n test should widen the family; got %d findings", len(f))
	}
}

func TestOneFindingPerPair(t *testing.T) {
	tests := []contracts.Test{
		corr("lag1", "a", "b", 0.0001, 0.30, 100),
		corr("lag2", "a", "b", 0.0001, 0.55, 100),
		corr("lag3", "a", "b", 0.0001, 0.40, 100),
	}
	findings, _, funnel := Correct(result(tests...), th)
	if len(findings) != 1 || findings[0].FindingKey != "lag2" {
		t.Fatalf("findings = %+v", findings)
	}
	if funnel["passed_fdr"] != 3 || funnel["kept"] != 1 {
		t.Errorf("funnel = %v", funnel)
	}
}

func TestPairGroupingIsOrderIndependent(t *testing.T) {
	ab := corr("ab", "a", "b", 0.0001, 0.3, 100)
	ba := corr("ba", "b", "a", 0.0001, 0.5, 100)
	findings, _, _ := Correct(result(ab, ba), th)
	if len(findings) != 1 || findings[0].FindingKey != "ba" {
		t.Errorf("findings = %+v", findings)
	}
}

func TestPairTieBreaks(t *testing.T) {
	// Equal |effect|, different p: the lower q wins.
	f, _, _ := Correct(result(
		corr("x", "a", "b", 0.001, 0.5, 100),
		corr("y", "a", "b", 0.0001, -0.5, 100),
	), th)
	if len(f) != 1 || f[0].FindingKey != "y" {
		t.Errorf("lower q should win: %+v", f)
	}
	// Equal effect and p: the lower key wins.
	f, _, _ = Correct(result(
		corr("k2", "a", "b", 0.0001, 0.5, 100),
		corr("k1", "a", "b", 0.0001, 0.5, 100),
	), th)
	if len(f) != 1 || f[0].FindingKey != "k1" {
		t.Errorf("lower key should win: %+v", f)
	}
}

func TestCustomTestsAreNotGrouped(t *testing.T) {
	mk := func(key string) contracts.Test {
		return contracts.Test{Key: key, Kind: "custom", Series: []string{"a", "b"}, P: 0.0001, Effect: 1, N: 100}
	}
	f, _, _ := Correct(result(mk("c1"), mk("c2")), th)
	if len(f) != 2 {
		t.Errorf("custom tests should both be kept: %+v", f)
	}
}

func TestScriptCannotFakeCorrectedValues(t *testing.T) {
	tc := corr("k", "a", "b", 0.0001, 0.5, 100)
	tc.Stats = map[string]any{"q": 0.0, "p": 0.0, "n": 1e9, "effect": 99.0, "lag": 2.0}
	f, _, _ := Correct(result(tc), th)
	if len(f) != 1 {
		t.Fatalf("findings = %+v", f)
	}
	s := f[0].Stats
	if s["q"].(float64) == 0 || s["p"].(float64) != 0.0001 || s["n"].(float64) != 100 || s["effect"].(float64) != 0.5 {
		t.Errorf("script values leaked through: %v", s)
	}
	if s["lag"].(float64) != 2 {
		t.Errorf("script's own stats should survive: %v", s)
	}
}

func TestAnomalyCap(t *testing.T) {
	r := contracts.ScriptResult{}
	for i := 0; i < 5; i++ {
		r.Anomalies = append(r.Anomalies, contracts.Anomaly{
			Key: fmt.Sprintf("an%d", i), Series: "trips", Period: time.Date(2026, 11, 1+i, 0, 0, 0, 0, time.UTC),
			Value: 1, Expected: 2, Score: float64(i + 4),
		})
	}
	r.Anomalies = append(r.Anomalies, contracts.Anomaly{Key: "other", Series: "rain", Score: 4})
	f, _, funnel := Correct(r, th)
	if len(f) != 4 {
		t.Fatalf("want 3 for trips + 1 for rain, got %d: %+v", len(f), f)
	}
	var keys []string
	for _, x := range f {
		keys = append(keys, x.FindingKey)
	}
	want := []string{"an2", "an3", "an4", "other"}
	for i := range want {
		if keys[i] != want[i] {
			t.Errorf("keys = %v, want %v", keys, want)
			break
		}
	}
	if funnel["kept"] != 0 {
		t.Errorf("anomalies must not count as kept tests: %v", funnel)
	}
	if f[0].Stats["period"] != "2026-11-03T00:00:00Z" {
		t.Errorf("period = %v", f[0].Stats["period"])
	}
}

func TestRejectedIsCappedAndOnePerPair(t *testing.T) {
	var tests []contracts.Test
	// Two failures on pair a|b (keep the better), plus 4 more pairs.
	tests = append(tests, corr("ab1", "a", "b", 0.30, 0.1, 100), corr("ab2", "a", "b", 0.20, 0.1, 100))
	for i, p := range []float64{0.5, 0.4, 0.35, 0.6} {
		tests = append(tests, corr(fmt.Sprintf("p%d", i), fmt.Sprintf("s%d", i), "z", p, 0.1, 100))
	}
	_, rej, _ := Correct(result(tests...), th)
	if len(rej) != 3 {
		t.Fatalf("rejected = %d, want 3", len(rej))
	}
	got := []string{rej[0].FindingKey, rej[1].FindingKey, rej[2].FindingKey}
	// Sorted by p: ab2 (0.20), ab1 skipped (same pair), p2 (0.35), p1 (0.4).
	want := []string{"ab2", "p2", "p1"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("rejected = %v, want %v", got, want)
			break
		}
	}
	for _, r := range rej {
		if r.Stats["rejected_by"] != "fdr" {
			t.Errorf("rejected_by = %v", r.Stats["rejected_by"])
		}
	}
}

func TestFunnelOnMixedFixture(t *testing.T) {
	_, _, funnel := Correct(result(
		corr("strong1", "a", "b", 0.00001, 0.5, 100), // kept
		corr("strong2", "a", "b", 0.00002, 0.4, 100), // passes, loses pair
		corr("small", "c", "d", 0.00001, 0.9, 5),     // raw < .05, min_n
		corr("weak", "e", "f", 0.045, 0.2, 100),      // raw < .05, fails fdr (q = 0.056)
		corr("null", "g", "h", 0.8, 0.0, 100),
	), th)
	want := map[string]int{"tests_run": 5, "raw_p_lt_05": 4, "passed_fdr": 2, "kept": 1}
	for k, v := range want {
		if funnel[k] != v {
			t.Errorf("funnel[%s] = %d, want %d (%v)", k, funnel[k], v, funnel)
		}
	}
}

func TestInputOrderDoesNotMatter(t *testing.T) {
	var tests []contracts.Test
	for i := 0; i < 30; i++ {
		tests = append(tests, corr(fmt.Sprintf("t%02d", i), fmt.Sprintf("a%d", i%7), fmt.Sprintf("b%d", i%3),
			math.Pow(10, -float64(i%9)-1.5), 0.1+float64(i)/100, 40+i))
	}
	var anoms []contracts.Anomaly
	for i := 0; i < 6; i++ {
		anoms = append(anoms, contracts.Anomaly{Key: fmt.Sprintf("an%d", i), Series: fmt.Sprintf("s%d", i%2), Score: float64(i)})
	}
	encode := func(tests []contracts.Test, anoms []contracts.Anomaly) string {
		f, r, fn := Correct(contracts.ScriptResult{Tests: tests, Anomalies: anoms}, th)
		b, err := json.Marshal(map[string]any{"f": f, "r": r, "fn": fn})
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	want := encode(tests, anoms)
	rng := rand.New(rand.NewSource(1))
	for trial := 0; trial < 20; trial++ {
		st := append([]contracts.Test(nil), tests...)
		sa := append([]contracts.Anomaly(nil), anoms...)
		rng.Shuffle(len(st), func(i, j int) { st[i], st[j] = st[j], st[i] })
		rng.Shuffle(len(sa), func(i, j int) { sa[i], sa[j] = sa[j], sa[i] })
		if got := encode(st, sa); got != want {
			t.Fatalf("shuffled input changed the output (trial %d)", trial)
		}
	}
}

func TestEmptyResultGivesEmptySlicesNotNil(t *testing.T) {
	f, r, fn := Correct(contracts.ScriptResult{}, th)
	if f == nil || r == nil {
		t.Errorf("findings and rejected must be non-nil so they marshal as []")
	}
	if fn["tests_run"] != 0 {
		t.Errorf("funnel = %v", fn)
	}
}
