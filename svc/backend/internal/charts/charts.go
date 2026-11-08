package charts

import (
	"cmp"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"time"

	"hacktheburgh/backend/internal/contracts"
)

const schemaURL = "https://vega.github.io/schema/vega-lite/v6.json"

// invalidBreak makes a null leave a gap in a line or area instead of being
// bridged. Vega-Lite 6 spells this on the mark; the golden files pin it, and
// the frontend compile check (pnpm check:charts) would warn if it were wrong.
const invalidBreak = "break-paths-filter-domains"

// Axis names one series for titles and legends.
type Axis struct {
	ID    string `json:"id"`
	Label string `json:"label"` // the id when empty
	Unit  string `json:"unit"`  // "" for none
}

// Meta is what the points don't carry.
type Meta struct {
	Series []Axis   `json:"series"` // Finding.Series order: [0] is a / x, [1] is b / y
	Lag    *int     `json:"lag"`    // correlations; nil or 0 means no shift
	Grain  string   `json:"grain"`  // hour | day | week
	Effect *float64 `json:"effect"` // tests only; the sign the trend line must agree with
}

// Build returns a Vega-Lite spec with the data inlined. It errors only on an
// unknown type or on points that fail the contract checks (which the contract
// validation already rejects, so in practice never). Specs are theme-free: the
// frontend supplies colours, fonts and dark mode through the Vega config.
func Build(c contracts.Chart, m Meta) (json.RawMessage, error) {
	var s spec
	var err error
	switch c.Type {
	case "dual_line":
		s, err = dualLine(c.Points, m)
	case "lag_scatter":
		s, err = lagScatter(c.Points, m)
	case "series_line":
		s, err = seriesLine(c.Points, m)
	case "anomaly_band":
		s, err = anomalyBand(c.Points, m)
	default:
		return nil, fmt.Errorf("unknown chart type %q", c.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("%s chart: %w", c.Type, err)
	}
	return json.Marshal(s)
}

// ---- spec shape ----

// Structs (and maps, which encoding/json writes in key order) keep the output
// byte-for-byte deterministic.
type spec struct {
	Schema    string    `json:"$schema"`
	Width     string    `json:"width"`
	Height    int       `json:"height"`
	Autosize  autosize  `json:"autosize"`
	Data      dataBlock `json:"data"`
	Transform []any     `json:"transform,omitempty"`
	Layer     []layer   `json:"layer"`
}

type autosize struct {
	Type     string `json:"type"`
	Contains string `json:"contains"`
}

type dataBlock struct {
	Values any `json:"values"`
}

type layer struct {
	Mark      map[string]any `json:"mark"`
	Transform []any          `json:"transform,omitempty"`
	Encoding  map[string]any `json:"encoding"`
}

type obj = map[string]any

func newSpec(values any) spec {
	return spec{
		Schema: schemaURL, Width: "container", Height: 200,
		Autosize: autosize{Type: "fit", Contains: "padding"},
		Data:     dataBlock{Values: values},
	}
}

// ---- encodings shared by the builders ----

func timeAxis() obj {
	return obj{"field": "t", "type": "temporal", "axis": obj{"title": nil}, "scale": obj{"nice": false}}
}

func quantY(field, title string) obj {
	return obj{"field": field, "type": "quantitative", "axis": obj{"title": title}, "scale": obj{"zero": false}}
}

func tipTime(grain string) obj {
	title := "Date"
	if grain == "hour" {
		title = "Time"
	}
	return obj{"field": "t", "type": "temporal", "title": title, "format": timeFormat(grain)}
}

func tipNum(field, title string) obj {
	return obj{"field": field, "type": "quantitative", "title": title, "format": ".3~g"}
}

// hover is an invisible point layer that gives line charts a tooltip at every
// point (a tooltip on a line mark only describes the line's first point).
func hover(encoding obj) layer {
	return layer{Mark: obj{"type": "point", "opacity": 0, "size": 80}, Encoding: encoding}
}

func timeFormat(grain string) string {
	if grain == "hour" {
		return "%d %b %H:%M"
	}
	return "%d %b %Y"
}

// ---- labels ----

// axis returns series i with its label filled in: the id, then the column name.
func (m Meta) axis(i int, col string) Axis {
	var a Axis
	if i < len(m.Series) {
		a = m.Series[i]
	}
	a.Label = cmp.Or(a.Label, a.ID, col)
	return a
}

// pair returns the two series of a correlation chart. When both have the same
// label, each gets its id appended so a legend or pair of axes can tell them apart.
func (m Meta) pair(colA, colB string) (Axis, Axis) {
	a, b := m.axis(0, colA), m.axis(1, colB)
	if a.Label == b.Label {
		for _, p := range []struct {
			ax  *Axis
			col string
		}{{&a, colA}, {&b, colB}} {
			suffix := p.ax.ID
			if suffix == "" || suffix == p.ax.Label {
				suffix = p.col
			}
			p.ax.Label += " (" + suffix + ")"
		}
	}
	return a, b
}

func axisTitle(a Axis) string {
	if a.Unit == "" {
		return a.Label
	}
	return a.Label + " (" + a.Unit + ")"
}

func (m Meta) lag() int {
	if m.Lag == nil {
		return 0
	}
	return *m.Lag
}

// periods writes a count of grain periods: "1 day", "2 hours", "3 weeks".
func periods(n int, grain string) string {
	unit := cmp.Or(grain, "day")
	abs := n
	if abs < 0 {
		abs = -abs
	}
	if abs != 1 {
		unit += "s"
	}
	return strconv.Itoa(n) + " " + unit
}

// ---- reading points ----
//
// Columns are copied by allow-list: only the type's columns are read, and
// anything else the script sent never reaches the browser.

func checkCount(pts []map[string]any) error {
	if len(pts) < 1 || len(pts) > contracts.MaxChartPoints {
		return fmt.Errorf("%d points: a chart needs 1 to %d", len(pts), contracts.MaxChartPoints)
	}
	return nil
}

func timeCol(row map[string]any, i int) (string, error) {
	s, ok := row["t"].(string)
	if !ok {
		return "", fmt.Errorf("points[%d].t = %v: t must be an RFC 3339 string", i, row["t"])
	}
	if _, err := time.Parse(time.RFC3339, s); err != nil {
		return "", fmt.Errorf("points[%d].t = %q: not an RFC 3339 timestamp", i, s)
	}
	return s, nil
}

// numCol returns nil for a missing or null value.
func numCol(row map[string]any, col string, i int) (*float64, error) {
	v, ok := row[col]
	if !ok {
		return nil, fmt.Errorf("points[%d] has no %q", i, col)
	}
	if v == nil {
		return nil, nil
	}
	var f float64
	switch n := v.(type) {
	case float64:
		f = n
	case int:
		f = float64(n)
	case int64:
		f = float64(n)
	case json.Number:
		var err error
		if f, err = n.Float64(); err != nil {
			return nil, fmt.Errorf("points[%d].%s = %v: must be a finite number or null", i, col, v)
		}
	default:
		return nil, fmt.Errorf("points[%d].%s = %v: must be a finite number or null", i, col, v)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return nil, fmt.Errorf("points[%d].%s = %v: must be a finite number or null", i, col, v)
	}
	return &f, nil
}

func boolCol(row map[string]any, col string, i int) (bool, error) {
	b, ok := row[col].(bool)
	if !ok {
		return false, fmt.Errorf("points[%d].%s = %v: must be true or false", i, col, row[col])
	}
	return b, nil
}
