package investigate

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"hacktheburgh/backend/internal/contracts"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestResolve(t *testing.T) {
	out := raw(`{"result":{"rho":0.4,"lags":[{"rho":0.1},{"rho":0.2}],"name":"x"},"error":""}`)
	for _, c := range []struct {
		path string
		want any
		ok   bool
	}{
		{"result.rho", 0.4, true},
		{"result.lags.1.rho", 0.2, true},
		{"result.name", "x", true},
		{"result.lags.2.rho", nil, false}, // past the end
		{"result.lags.-1.rho", nil, false},
		{"result.lags.a", nil, false},
		{"result.rho.deeper", nil, false}, // key on a number
		{"result.missing", nil, false},
		{"", nil, false},
	} {
		got, ok := Resolve(out, c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("Resolve(%q) = %v, %v; want %v, %v", c.path, got, ok, c.want, c.ok)
		}
	}
}

func TestClaimValue(t *testing.T) {
	for _, c := range []struct {
		v  any
		ok bool
	}{
		{0.5, true}, {"fine", true}, {strings.Repeat("a", 81), false}, {"", false},
		{true, false}, {nil, false}, {map[string]any{}, false}, {[]any{1.0}, false},
	} {
		if _, ok := ClaimValue(c.v); ok != c.ok {
			t.Errorf("ClaimValue(%v) ok = %v, want %v", c.v, ok, c.ok)
		}
	}
}

func okStep(seq int, result string) Step {
	return Step{Seq: seq, Tool: "run_python", Output: raw(fmt.Sprintf(`{"result":%s,"stdout":"","error":"","timed_out":false}`, result))}
}

func chartJSON(n int) string {
	pts := make([]string, n)
	for i := range pts {
		pts[i] = fmt.Sprintf(`{"t":"2026-10-%02dT00:00:00Z","v":%d}`, i%28+1, i)
	}
	return fmt.Sprintf(`{"type":"series_line","points":[%s],"series":[{"id":"a","label":"A","unit":"mm"}]}`, strings.Join(pts, ","))
}

func ip(i int) *int       { return &i }
func sp(s string) *string { return &s }

func finding(h string, claims ...contracts.Claim) contracts.InvestigationFinding {
	return contracts.InvestigationFinding{Headline: h, Explanation: "e", Claims: claims}
}

func TestCleanClaims(t *testing.T) {
	steps := []Step{
		okStep(1, `{"rho":0.4,"n":"abc","arr":[1],"nan":null}`),
		{Seq: 2, Tool: "run_python", Output: raw(`{"result":null,"stdout":"","error":"Traceback...","timed_out":false}`)},
	}
	r := contracts.InvestigationReport{Rationale: "why", Findings: []contracts.InvestigationFinding{
		finding("ok", contracts.Claim{Seq: 1, Path: "result.rho", Label: "rho"}),
		finding("unknown step", contracts.Claim{Seq: 9, Path: "result.rho", Label: "rho"}),
		finding("failed step", contracts.Claim{Seq: 2, Path: "result.rho", Label: "rho"}),
		finding("bad path", contracts.Claim{Seq: 1, Path: "result.nope", Label: "rho"}),
		finding("array value", contracts.Claim{Seq: 1, Path: "result.arr", Label: "x"}),
		finding("null value", contracts.Claim{Seq: 1, Path: "result.nan", Label: "x"}),
		finding("no label", contracts.Claim{Seq: 1, Path: "result.rho", Label: "  "}),
		finding("", contracts.Claim{Seq: 1, Path: "result.rho", Label: "rho"}),
	}}
	got := Clean(r, steps)
	if len(got.Findings) != 1 || got.Findings[0].Headline != "ok" {
		t.Fatalf("findings = %+v, want only \"ok\"", got.Findings)
	}
	if got.Rationale != "why" {
		t.Fatalf("rationale lost")
	}
}

func TestCleanDuplicateLabelKeepsFirst(t *testing.T) {
	steps := []Step{okStep(1, `{"a":1,"b":2}`)}
	r := contracts.InvestigationReport{Findings: []contracts.InvestigationFinding{finding("h",
		contracts.Claim{Seq: 1, Path: "result.a", Label: "same"}, contracts.Claim{Seq: 1, Path: "result.b", Label: "same"})}}
	got := Clean(r, steps)
	if len(got.Findings[0].Claims) != 1 || got.Findings[0].Claims[0].Path != "result.a" {
		t.Fatalf("claims = %+v", got.Findings[0].Claims)
	}
}

func TestCleanCharts(t *testing.T) {
	steps := []Step{
		okStep(1, fmt.Sprintf(`{"good":%s,"huge":%s,"unknown":{"type":"pie","points":[{"t":"2026-10-01T00:00:00Z","v":1}]},"scalar":3}`,
			chartJSON(10), chartJSON(501))),
	}
	mk := func(path string) contracts.InvestigationFinding {
		f := finding("h", contracts.Claim{Seq: 1, Path: "result.scalar", Label: "n"})
		f.ChartSeq, f.ChartPath = ip(1), sp(path)
		return f
	}
	r := contracts.InvestigationReport{Findings: []contracts.InvestigationFinding{
		mk("result.good"), mk("result.huge"), mk("result.unknown"), mk("result.scalar"),
	}}
	got := Clean(r, steps)
	if len(got.Findings) != 4 {
		t.Fatalf("findings = %d, want 4 (claims keep them)", len(got.Findings))
	}
	if got.Findings[0].ChartSeq == nil || *got.Findings[0].ChartPath != "result.good" {
		t.Errorf("valid chart was dropped")
	}
	for i := 1; i < 4; i++ {
		if got.Findings[i].ChartSeq != nil || got.Findings[i].ChartPath != nil {
			t.Errorf("finding %d kept an invalid chart", i)
		}
	}
}

func TestCleanChartOnlyFindingSurvives(t *testing.T) {
	steps := []Step{okStep(1, fmt.Sprintf(`{"c":%s}`, chartJSON(5)))}
	f := finding("chart only")
	f.ChartSeq, f.ChartPath = ip(1), sp("result.c")
	got := Clean(contracts.InvestigationReport{Findings: []contracts.InvestigationFinding{f}}, steps)
	if len(got.Findings) != 1 {
		t.Fatalf("chart-only finding dropped")
	}
}

func TestCleanKeepsAtMostFourAndReturnsEmptyReport(t *testing.T) {
	steps := []Step{okStep(1, `{"a":1}`)}
	var fs []contracts.InvestigationFinding
	for i := 0; i < 6; i++ {
		fs = append(fs, finding(fmt.Sprintf("f%d", i), contracts.Claim{Seq: 1, Path: "result.a", Label: "a"}))
	}
	got := Clean(contracts.InvestigationReport{Findings: fs}, steps)
	if len(got.Findings) != contracts.MaxInvestigationFindings || got.Findings[3].Headline != "f3" {
		t.Fatalf("findings = %+v", got.Findings)
	}

	empty := Clean(contracts.InvestigationReport{Rationale: "kept", Findings: []contracts.InvestigationFinding{finding("x")}}, steps)
	if empty.Findings == nil || len(empty.Findings) != 0 || empty.Rationale != "kept" {
		t.Fatalf("empty report = %+v (Findings must be non-nil so it encodes as [])", empty)
	}
}
