package contracts

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

const MaxScriptChars = 20000

const MaxAnomalies = 50

const MaxChartPoints = 500

// Limits on an investigation report.
const (
	MaxInvestigationFindings = 4
	MaxClaimsPerFinding      = 8
)

var (
	scriptAssignsResult = regexp.MustCompile(`(?m)^[ \t]*result[ \t]*=[^=]`)
	scriptMentionsInput = regexp.MustCompile(`\bINPUT\b`)

	// forbiddenInScript is a lint that catches mistakes; it is not a security
	// boundary (the runner is). Unseeded randomness isn't caught here: the
	// prompt asks for np.random.default_rng(<const>).
	forbiddenInScript = []struct {
		re   *regexp.Regexp
		what string
	}{
		{regexp.MustCompile(`\bopen\(`), "open("},
		{regexp.MustCompile(`\bimport os\b`), "import os"},
		{regexp.MustCompile(`\bfrom os\b`), "from os"},
		{regexp.MustCompile(`\bsubprocess\b`), "subprocess"},
		{regexp.MustCompile(`\bsocket\b`), "socket"},
		{regexp.MustCompile(`\brequests\b`), "requests"},
		{regexp.MustCompile(`\burllib\b`), "urllib"},
		{regexp.MustCompile(`\bdatetime\.now\b`), "datetime.now"},
		{regexp.MustCompile(`\bdatetime\.utcnow\b`), "datetime.utcnow"},
		{regexp.MustCompile(`\btime\.time\b`), "time.time"},
		{regexp.MustCompile(`\bnp\.random\.seed\(`), "np.random.seed("},
	}
)

func (d WorkflowDraft) Validate() error {
	if strings.TrimSpace(d.Script) == "" {
		return fmt.Errorf("script is empty")
	}
	if strings.TrimSpace(d.Rationale) == "" {
		return fmt.Errorf("rationale is empty")
	}
	return CheckScript(d.Script)
}

// CheckScript is the lint shared by the author's scripts and the
// investigator's run_python code: size, reads INPUT, assigns result, and none
// of the forbidden tokens.
func CheckScript(code string) error {
	if len(code) > MaxScriptChars {
		return fmt.Errorf("script is %d characters; the limit is %d", len(code), MaxScriptChars)
	}
	if !scriptMentionsInput.MatchString(code) {
		return fmt.Errorf("script never reads INPUT")
	}
	if !scriptAssignsResult.MatchString(code) {
		return fmt.Errorf("script never assigns `result`")
	}
	for _, f := range forbiddenInScript {
		if f.re.MatchString(code) {
			return fmt.Errorf("script uses %q, which is not allowed: use only data from INPUT, no files, network, clock or os", f.what)
		}
	}
	return nil
}

var keyRe = regexp.MustCompile(`^[a-z0-9_:+\-]{1,120}$`)

var grains = map[string]bool{"hour": true, "day": true, "week": true}

var chartColumns = map[string][]string{
	"dual_line":    {"t", "a", "b"},
	"lag_scatter":  {"x", "y"},
	"series_line":  {"t", "v"},
	"anomaly_band": {"t", "v", "lo", "hi", "flagged"},
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Validate returns the first problem in the script's result, worded for the
// author, e.g. "tests[3].p = 1.5: p must be between 0 and 1". sourceKeys is
// the set of keys in INPUT.sources; maxTests <= 0 means no limit.
func (r ScriptResult) Validate(sourceKeys map[string]bool, maxTests int) error {
	if len(r.Series) == 0 {
		return fmt.Errorf("series: report at least one series")
	}
	seriesIDs := map[string]bool{}
	for i, s := range r.Series {
		at := fmt.Sprintf("series[%d]", i)
		if !keyRe.MatchString(s.ID) {
			return fmt.Errorf("%s.id = %q: id must match %s", at, s.ID, keyRe)
		}
		if seriesIDs[s.ID] {
			return fmt.Errorf("%s.id = %q: duplicate series id", at, s.ID)
		}
		seriesIDs[s.ID] = true
		if !sourceKeys[s.Source] {
			return fmt.Errorf("%s.source = %q: not a key in INPUT['sources']", at, s.Source)
		}
		if !grains[s.Grain] {
			return fmt.Errorf("%s.grain = %q: grain must be hour, day or week", at, s.Grain)
		}
		if s.Points < 0 || s.Points > s.Periods {
			return fmt.Errorf("%s.points = %d: points must be between 0 and periods (%d)", at, s.Points, s.Periods)
		}
	}

	if maxTests > 0 && len(r.Tests) > maxTests {
		return fmt.Errorf("tests: %d tests reported; the limit is %d", len(r.Tests), maxTests)
	}
	if len(r.Anomalies) > MaxAnomalies {
		return fmt.Errorf("anomalies: %d anomalies reported; the limit is %d", len(r.Anomalies), MaxAnomalies)
	}

	keys := map[string]bool{}
	for i, t := range r.Tests {
		at := fmt.Sprintf("tests[%d]", i)
		if err := checkKey(keys, at, t.Key); err != nil {
			return err
		}
		switch t.Kind {
		case "correlation":
			if len(t.Series) != 2 {
				return fmt.Errorf("%s.series has %d entries: a correlation needs exactly 2 series", at, len(t.Series))
			}
			if t.Series[0] == t.Series[1] {
				return fmt.Errorf("%s.series = [%q, %q]: a correlation needs two different series", at, t.Series[0], t.Series[1])
			}
		case "custom":
		default:
			return fmt.Errorf("%s.kind = %q: kind must be correlation or custom", at, t.Kind)
		}
		for j, id := range t.Series {
			if !seriesIDs[id] {
				return fmt.Errorf("%s.series[%d] = %q: not a reported series", at, j, id)
			}
		}
		if !finite(t.P) || t.P < 0 || t.P > 1 {
			return fmt.Errorf("%s.p = %v: p must be between 0 and 1", at, t.P)
		}
		if t.N < 0 {
			return fmt.Errorf("%s.n = %d: n must not be negative", at, t.N)
		}
		if !finite(t.Effect) {
			return fmt.Errorf("%s.effect = %v: effect must be a finite number", at, t.Effect)
		}
		if err := checkStats(at+".stats", t.Stats); err != nil {
			return err
		}
		if err := checkChart(at+".chart", t.Chart); err != nil {
			return err
		}
	}

	for i, a := range r.Anomalies {
		at := fmt.Sprintf("anomalies[%d]", i)
		if err := checkKey(keys, at, a.Key); err != nil {
			return err
		}
		if !seriesIDs[a.Series] {
			return fmt.Errorf("%s.series = %q: not a reported series", at, a.Series)
		}
		for _, f := range []struct {
			name string
			v    float64
		}{{"value", a.Value}, {"expected", a.Expected}, {"score", a.Score}} {
			if !finite(f.v) {
				return fmt.Errorf("%s.%s = %v: %s must be a finite number", at, f.name, f.v, f.name)
			}
		}
		if err := checkChart(at+".chart", a.Chart); err != nil {
			return err
		}
	}
	return nil
}

func checkKey(seen map[string]bool, at, key string) error {
	if !keyRe.MatchString(key) {
		return fmt.Errorf("%s.key = %q: key must match %s", at, key, keyRe)
	}
	if seen[key] {
		return fmt.Errorf("%s.key = %q: duplicate key", at, key)
	}
	seen[key] = true
	return nil
}

func checkStats(at string, stats map[string]any) error {
	for k, v := range stats {
		switch x := v.(type) {
		case float64:
			if !finite(x) {
				return fmt.Errorf("%s[%q] = %v: numbers must be finite", at, k, x)
			}
		case bool:
		case string:
			if len(x) > 200 {
				return fmt.Errorf("%s[%q]: strings are limited to 200 characters", at, k)
			}
		default:
			return fmt.Errorf("%s[%q]: values must be numbers, bools or short strings, not %T", at, k, v)
		}
	}
	return nil
}

// Validate checks a chart the way a script result's charts are checked.
func (c Chart) Validate() error { return checkChart("chart", &c) }

func checkChart(at string, c *Chart) error {
	if c == nil {
		return nil
	}
	cols, ok := chartColumns[c.Type]
	if !ok {
		return fmt.Errorf("%s.type = %q: type must be dual_line, lag_scatter, series_line or anomaly_band", at, c.Type)
	}
	if len(c.Points) < 1 || len(c.Points) > MaxChartPoints {
		return fmt.Errorf("%s.points has %d rows: a chart needs 1 to %d", at, len(c.Points), MaxChartPoints)
	}
	for i, row := range c.Points {
		for _, col := range cols {
			v, present := row[col]
			if !present {
				return fmt.Errorf("%s.points[%d] has no %q: %s charts need columns %s", at, i, col, c.Type, strings.Join(cols, ", "))
			}
			switch {
			case col == "t":
				s, isStr := v.(string)
				if !isStr {
					return fmt.Errorf("%s.points[%d].t = %v: t must be an RFC 3339 string", at, i, v)
				}
				if _, err := time.Parse(time.RFC3339, s); err != nil {
					return fmt.Errorf("%s.points[%d].t = %q: t must be an RFC 3339 timestamp", at, i, s)
				}
			case col == "flagged":
				if _, isBool := v.(bool); !isBool {
					return fmt.Errorf("%s.points[%d].flagged = %v: flagged must be true or false", at, i, v)
				}
			default:
				if v == nil {
					continue
				}
				f, isNum := v.(float64)
				if !isNum || !finite(f) {
					return fmt.Errorf("%s.points[%d].%s = %v: must be a finite number or null", at, i, col, v)
				}
			}
		}
	}
	return nil
}

// Reason categories valid per workflow kind. The rule-decided categories
// (sources_changed, claim_contradicted) never come from the LLM, so they are
// not in the ReviewDecision enum.
var reasonsByKind = map[string]map[string]bool{
	"statistical":   {"schema_drift": true, "stale_series": true, "coverage": true},
	"investigation": {"effect_faded": true, "explanation_mismatch": true, "new_scope": true},
}

// Validate rejects regenerate with category none, and categories that don't
// belong to the workflow's kind. Callers store a rejected decision as keep.
func (d ReviewDecision) Validate(workflowKind string) error {
	switch d.Decision {
	case "keep":
		return nil
	case "regenerate":
		if !reasonsByKind[workflowKind][d.ReasonCategory] {
			return fmt.Errorf("category %q is not valid for a %s workflow", d.ReasonCategory, workflowKind)
		}
		return nil
	}
	return fmt.Errorf("unknown decision %q", d.Decision)
}
