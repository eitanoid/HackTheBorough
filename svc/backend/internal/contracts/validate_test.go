package contracts

import (
	"math"
	"strings"
	"testing"
)

const cleanScript = `import numpy as np
import pandas as pd

df = pd.DataFrame(INPUT['sources']['news']['records'])
rng = np.random.default_rng(7)
result = {'series': [], 'tests': [], 'anomalies': []}
`

func TestWorkflowDraftValidate(t *testing.T) {
	draft := func(script string) WorkflowDraft {
		return WorkflowDraft{Script: script, Rationale: "why", InsightPrompt: "p"}
	}
	rows := []struct {
		name string
		d    WorkflowDraft
		want string // substring of the error; "" means valid
	}{
		{"clean example", draft(cleanScript), ""},
		{"empty script", draft("  "), "script is empty"},
		{"empty rationale", WorkflowDraft{Script: cleanScript}, "rationale"},
		{"missing INPUT", draft("result = {}\n"), "INPUT"},
		{"missing result", draft("x = INPUT\n"), "result"},
		{"comparison is not assignment", draft("x = INPUT\nif result == 1: pass\n"), "result"},
		{"import os", draft(cleanScript + "import os\n"), "import os"},
		{"from os", draft(cleanScript + "from os import path\n"), "from os"},
		{"open(", draft(cleanScript + "open('x')\n"), "open("},
		{"subprocess", draft(cleanScript + "import subprocess\n"), "subprocess"},
		{"datetime.now", draft(cleanScript + "t = datetime.now()\n"), "datetime.now"},
		{"datetime.utcnow", draft(cleanScript + "t = datetime.utcnow()\n"), "datetime.utcnow"},
		{"time.time", draft(cleanScript + "t = time.time()\n"), "time.time"},
		{"np.random.seed", draft(cleanScript + "np.random.seed(1)\n"), "np.random.seed("},
		{"reopen is not open(", draft(cleanScript + "x = reopen(1)\n"), ""},
		{"too long", draft(cleanScript + strings.Repeat("#", MaxScriptChars)), "limit"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) { expectErr(t, r.d.Validate(), r.want) })
	}
}

func expectErr(t *testing.T, err error, want string) {
	t.Helper()
	switch {
	case want == "" && err != nil:
		t.Errorf("unexpected error: %v", err)
	case want != "" && err == nil:
		t.Errorf("expected an error containing %q, got none", want)
	case want != "" && !strings.Contains(err.Error(), want):
		t.Errorf("error %q does not name %q", err, want)
	}
}

func TestScriptResultValidate(t *testing.T) {
	keys := map[string]bool{"news": true, "trips": true}
	rows := []struct {
		name string
		mut  func(r *ScriptResult)
		max  int
		want string
	}{
		{"ok", func(r *ScriptResult) {}, 2000, ""},
		{"no series", func(r *ScriptResult) { r.Series = nil }, 2000, "series"},
		{"bad series id", func(r *ScriptResult) { r.Series[0].ID = "Strike News" }, 2000, "series[0].id"},
		{"duplicate series id", func(r *ScriptResult) { r.Series[1].ID = r.Series[0].ID }, 2000, "series[1].id"},
		{"unknown source", func(r *ScriptResult) { r.Series[0].Source = "nope" }, 2000, "series[0].source"},
		{"bad grain", func(r *ScriptResult) { r.Series[0].Grain = "year" }, 2000, "series[0].grain"},
		{"points > periods", func(r *ScriptResult) { r.Series[0].Points = 91 }, 2000, "series[0].points"},
		{"negative points", func(r *ScriptResult) { r.Series[0].Points = -1 }, 2000, "series[0].points"},
		{"bad test key", func(r *ScriptResult) { r.Tests[0].Key = "has space" }, 2000, "tests[0].key"},
		{"duplicate test key", func(r *ScriptResult) { r.Anomalies[0].Key = r.Tests[0].Key }, 2000, "anomalies[0].key"},
		{"unknown test series", func(r *ScriptResult) { r.Tests[0].Series[1] = "ghost" }, 2000, "tests[0].series[1]"},
		{"unknown anomaly series", func(r *ScriptResult) { r.Anomalies[0].Series = "ghost" }, 2000, "anomalies[0].series"},
		{"bad kind", func(r *ScriptResult) { r.Tests[0].Kind = "magic" }, 2000, "tests[0].kind"},
		{"correlation with 1 series", func(r *ScriptResult) { r.Tests[0].Series = r.Tests[0].Series[:1] }, 2000, "tests[0].series"},
		{"correlation with same series twice", func(r *ScriptResult) { r.Tests[0].Series[1] = r.Tests[0].Series[0] }, 2000, "tests[0].series"},
		{"p above 1", func(r *ScriptResult) { r.Tests[0].P = 1.5 }, 2000, "tests[0].p = 1.5: p must be between 0 and 1"},
		{"p NaN", func(r *ScriptResult) { r.Tests[0].P = math.NaN() }, 2000, "tests[0].p"},
		{"negative n", func(r *ScriptResult) { r.Tests[0].N = -1 }, 2000, "tests[0].n"},
		{"infinite effect", func(r *ScriptResult) { r.Tests[0].Effect = math.Inf(1) }, 2000, "tests[0].effect"},
		{"NaN anomaly value", func(r *ScriptResult) { r.Anomalies[0].Value = math.NaN() }, 2000, "anomalies[0].value"},
		{"infinite anomaly expected", func(r *ScriptResult) { r.Anomalies[0].Expected = math.Inf(-1) }, 2000, "anomalies[0].expected"},
		{"NaN anomaly score", func(r *ScriptResult) { r.Anomalies[0].Score = math.NaN() }, 2000, "anomalies[0].score"},
		{"stats NaN", func(r *ScriptResult) { r.Tests[0].Stats["rho"] = math.NaN() }, 2000, "tests[0].stats"},
		{"stats list", func(r *ScriptResult) { r.Tests[0].Stats["xs"] = []any{1.0} }, 2000, "tests[0].stats"},
		{"stats long string", func(r *ScriptResult) { r.Tests[0].Stats["s"] = strings.Repeat("x", 201) }, 2000, "tests[0].stats"},
		{"stats ok types", func(r *ScriptResult) { r.Tests[0].Stats["flag"] = true; r.Tests[0].Stats["s"] = "short" }, 2000, ""},
		{"unknown chart type", func(r *ScriptResult) { r.Tests[0].Chart.Type = "pie" }, 2000, "tests[0].chart.type"},
		{"empty chart", func(r *ScriptResult) { r.Tests[0].Chart.Points = nil }, 2000, "tests[0].chart.points"},
		{"chart too big", func(r *ScriptResult) {
			r.Tests[0].Chart.Points = make([]map[string]any, MaxChartPoints+1)
			for i := range r.Tests[0].Chart.Points {
				r.Tests[0].Chart.Points[i] = map[string]any{"x": 1.0, "y": 1.0}
			}
		}, 2000, "tests[0].chart.points"},
		{"chart missing column", func(r *ScriptResult) { delete(r.Tests[0].Chart.Points[0], "y") }, 2000, `"y"`},
		{"chart NaN column", func(r *ScriptResult) { r.Tests[0].Chart.Points[0]["x"] = math.NaN() }, 2000, "points[0].x"},
		{"chart null is fine", func(r *ScriptResult) { r.Tests[0].Chart.Points[0]["y"] = nil }, 2000, ""},
		{"chart bad t", func(r *ScriptResult) { r.Anomalies[0].Chart.Points[0]["t"] = "yesterday" }, 2000, "anomalies[0].chart.points[0].t"},
		{"chart flagged not bool", func(r *ScriptResult) { r.Anomalies[0].Chart.Points[0]["flagged"] = 1.0 }, 2000, "flagged"},
		{"over maxTests", func(r *ScriptResult) { r.Tests = append(r.Tests, Test{Key: "extra", Kind: "custom", P: 1}) }, 1, "tests"},
		{"too many anomalies", func(r *ScriptResult) {
			for i := 0; i < MaxAnomalies; i++ {
				r.Anomalies = append(r.Anomalies, Anomaly{Key: "a" + strings.Repeat("x", i+1), Series: "trips"})
			}
		}, 2000, "anomalies"},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			res := okScriptResult()
			r.mut(&res)
			expectErr(t, res.Validate(keys, r.max), r.want)
		})
	}
}

func TestCheckScript(t *testing.T) {
	if err := CheckScript(cleanScript); err != nil {
		t.Fatalf("clean script: %v", err)
	}
	for _, c := range []struct{ name, code, want string }{
		{"no INPUT", "result = {}\n", "INPUT"},
		{"no result", "x = INPUT\n", "result"},
		{"open", "x = INPUT\nopen('f')\nresult = {}\n", "open("},
		{"too long", "result = INPUT\n" + strings.Repeat("#", MaxScriptChars), "limit"},
	} {
		err := CheckScript(c.code)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want substring %q", c.name, err, c.want)
		}
	}
}

func TestChartValidate(t *testing.T) {
	ok := Chart{Type: "series_line", Points: []map[string]any{{"t": "2026-11-01T00:00:00Z", "v": 1.0}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid chart: %v", err)
	}
	if err := (Chart{Type: "pie", Points: ok.Points}).Validate(); err == nil {
		t.Error("unknown type accepted")
	}
	if err := (Chart{Type: "series_line"}).Validate(); err == nil {
		t.Error("empty chart accepted")
	}
}
