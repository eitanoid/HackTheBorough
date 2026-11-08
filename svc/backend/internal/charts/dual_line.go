package charts

import "fmt"

type dualRow struct {
	T string   `json:"t"`
	S string   `json:"s"`
	V *float64 `json:"v"`
}

// dualLine draws two series on one standardised axis. Go only reshapes the
// {t,a,b} points into long rows {t,s,v}; Vega computes the z-scores.
func dualLine(pts []map[string]any, m Meta) (spec, error) {
	if err := checkCount(pts); err != nil {
		return spec{}, err
	}
	a, b := m.pair("a", "b")
	la, lb := a.Label, b.Label
	if k := m.lag(); k != 0 {
		lb = fmt.Sprintf("%s (shifted %s)", lb, periods(k, m.Grain))
	}
	rows := make([]dualRow, 0, 2*len(pts))
	for i, p := range pts {
		t, err := timeCol(p, i)
		if err != nil {
			return spec{}, err
		}
		va, err := numCol(p, "a", i)
		if err != nil {
			return spec{}, err
		}
		vb, err := numCol(p, "b", i)
		if err != nil {
			return spec{}, err
		}
		rows = append(rows, dualRow{t, la, va}, dualRow{t, lb, vb})
	}

	s := newSpec(rows)
	s.Transform = []any{
		obj{
			"joinaggregate": []obj{
				{"op": "mean", "field": "v", "as": "mean"},
				{"op": "stdev", "field": "v", "as": "sd"},
			},
			"groupby": []string{"s"},
		},
		obj{
			"calculate": "datum.v == null ? null : (datum.sd > 0 ? (datum.v - datum.mean) / datum.sd : 0)",
			"as":        "z",
		},
	}
	colour := obj{
		"field": "s", "type": "nominal",
		"scale":  obj{"domain": []string{la, lb}},
		"legend": obj{"title": nil},
	}
	s.Layer = []layer{
		{
			Mark: obj{"type": "line", "invalid": invalidBreak},
			Encoding: obj{
				"x":     timeAxis(),
				"y":     quantY("z", "Standardised value"),
				"color": colour,
			},
		},
		hover(obj{
			"x":     timeAxis(),
			"y":     quantY("z", "Standardised value"),
			"color": colour,
			"tooltip": []obj{
				tipTime(m.Grain),
				{"field": "s", "type": "nominal", "title": "Series"},
				tipNum("v", "Value"),
			},
		}),
	}
	return s, nil
}
