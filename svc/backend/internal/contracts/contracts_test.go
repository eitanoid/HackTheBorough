package contracts

import (
	"bytes"
	"encoding/json"
	"flag"
	"hacktheburgh/backend/internal/llm"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

func ptr[T any](v T) *T { return &v }

func okScriptResult() ScriptResult {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	return ScriptResult{
		Series: []SeriesInfo{
			{ID: "strike_news", Label: "Strike news", Source: "news", Unit: ptr("articles"), Grain: "day", Periods: 90, Points: 90},
			{ID: "trips", Label: "Trips", Source: "trips", Unit: nil, Grain: "day", Periods: 90, Points: 88},
		},
		Tests: []Test{{
			Key: "corr:strike_news:trips:lag+2", Kind: "correlation", Series: []string{"strike_news", "trips"},
			P: 0.0004, Effect: -0.42, N: 88,
			Stats: map[string]any{"lag": 2.0, "rho": -0.42},
			Chart: &Chart{Type: "lag_scatter", Points: []map[string]any{{"x": 1.5, "y": nil}, {"x": 2.0, "y": 3.25}}},
		}},
		Anomalies: []Anomaly{{
			Key: "anom:trips:2026-11-01", Series: "trips", Period: t0, Value: 120, Expected: 300, Score: -4.1,
			Chart: &Chart{Type: "anomaly_band", Points: []map[string]any{
				{"flagged": true, "hi": 330.0, "lo": 270.0, "t": "2026-11-01T00:00:00Z", "v": 120.0},
			}},
		}},
	}
}

func okAnalysisResult() AnalysisResult {
	t0 := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	sr := okScriptResult()
	return AnalysisResult{
		DataUpTo: t0,
		Findings: []Finding{{
			FindingKey: "corr:strike_news:trips:lag+2", Kind: "correlation", Series: []string{"strike_news", "trips"},
			Stats: map[string]any{"effect": -0.42, "lag": 2.0, "n": 88.0, "p": 0.0004, "q": 0.004, "rho": -0.42},
			Chart: sr.Tests[0].Chart,
		}},
		Funnel:   map[string]int{"kept": 1, "passed_fdr": 1, "raw_p_lt_05": 1, "tests_run": 15},
		Rejected: []Finding{},
		Health: Health{
			Series: sr.Series,
			Sources: map[string]SourceHealth{
				"news": {Records: 400, FirstAt: t0.AddDate(0, 0, -90), LastAt: t0, MedianGapS: 3600},
			},
			UnprofiledKeys: map[string][]string{"news": {"extra.field"}},
		},
	}
}

func marshalGolden(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

func TestGoldenRoundTrip(t *testing.T) {
	cases := []struct {
		file string
		fix  any
		into func() any
	}{
		{"script_result_ok.json", okScriptResult(), func() any { return new(ScriptResult) }},
		{"analysis_result.json", okAnalysisResult(), func() any { return new(AnalysisResult) }},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			path := filepath.Join("testdata", c.file)
			if *update {
				if err := os.MkdirAll("testdata", 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, marshalGolden(t, c.fix), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")) // Windows checkouts
			v := c.into()
			if err := json.Unmarshal(want, v); err != nil {
				t.Fatal(err)
			}
			if got := marshalGolden(t, v); !bytes.Equal(got, want) {
				t.Errorf("round trip changed %s:\n got: %s\nwant: %s", c.file, got, want)
			}
		})
	}
}

func TestGoldenScriptResultValidates(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "script_result_ok.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r ScriptResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(map[string]bool{"news": true, "trips": true}, 2000); err != nil {
		t.Errorf("golden script result should validate: %v", err)
	}
}

func TestNarrationSchemaRequiresPlausibility(t *testing.T) {
	_, schema := llm.SchemaFor[Narration]()
	items := schema["properties"].(map[string]any)["insights"].(map[string]any)["items"].(map[string]any)
	required, _ := items["required"].([]string)
	found := false
	for _, r := range required {
		found = found || r == "plausibility"
	}
	if !found {
		t.Fatalf("plausibility is not required: %v", required)
	}
	p := items["properties"].(map[string]any)["plausibility"].(map[string]any)
	enum, _ := p["enum"].([]any)
	if len(enum) != 2 || enum[0] != "plausible" || enum[1] != "doubtful" {
		t.Fatalf("plausibility enum = %v", enum)
	}
}

func TestReplayResultRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	msg := "step raised"
	in := ReplayResult{DataUpTo: at, Steps: []StepOutput{
		{Seq: 1, Output: json.RawMessage(`{"result":{"rho":0.4}}`), AsOf: at},
		{Seq: 2, Output: json.RawMessage(`{"result":{"rho":0.1}}`), Error: &msg, AsOf: at.Add(-24 * time.Hour)},
	}}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out ReplayResult
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Steps[0].Error != nil || out.Steps[1].Error == nil || *out.Steps[1].Error != msg || !out.Steps[1].AsOf.Equal(at.Add(-24*time.Hour)) {
		t.Fatalf("round trip lost fields: %+v", out)
	}
	c := Claim{Seq: 1, Path: "result.rho", Label: "rho"}
	cb, _ := json.Marshal(c)
	if string(cb) != `{"seq":1,"path":"result.rho","label":"rho"}` {
		t.Fatalf("claim json = %s", cb)
	}
}
