package charts

import "fmt"

type scatterRow struct {
	X *float64 `json:"x"`
	Y *float64 `json:"y"`
}

// lagScatter draws y against x with a least-squares trend line. The line is
// left out when its slope disagrees with the finding's effect: the effect is a
// Spearman rho and the line is least-squares, so they can differ, and a chart
// must never contradict its card.
func lagScatter(pts []map[string]any, m Meta) (spec, error) {
	if err := checkCount(pts); err != nil {
		return spec{}, err
	}
	rows := make([]scatterRow, 0, len(pts))
	for i, p := range pts {
		x, err := numCol(p, "x", i)
		if err != nil {
			return spec{}, err
		}
		y, err := numCol(p, "y", i)
		if err != nil {
			return spec{}, err
		}
		rows = append(rows, scatterRow{x, y})
	}
	a, b := m.pair("x", "y")
	xt, yt := axisTitle(a), axisTitle(b)
	if k := m.lag(); k != 0 {
		yt = fmt.Sprintf("%s, %s later", yt, periods(k, m.Grain))
	}
	x := obj{"field": "x", "type": "quantitative", "axis": obj{"title": xt}, "scale": obj{"zero": false}}
	y := obj{"field": "y", "type": "quantitative", "axis": obj{"title": yt}, "scale": obj{"zero": false}}

	s := newSpec(rows)
	s.Layer = []layer{{
		Mark: obj{"type": "point"},
		Encoding: obj{"x": x, "y": y, "tooltip": []obj{
			tipNum("x", a.Label), tipNum("y", b.Label),
		}},
	}}
	if showTrend(rows, m.Effect) {
		s.Layer = append(s.Layer, layer{
			Mark:      obj{"type": "line", "style": "trend"},
			Transform: []any{obj{"regression": "y", "on": "x"}},
			Encoding:  obj{"x": x, "y": y},
		})
	}
	return s, nil
}

// showTrend reports whether the least-squares line over the non-null pairs
// exists and agrees in sign with effect. With no effect, existing is enough.
func showTrend(rows []scatterRow, effect *float64) bool {
	slope, ok := slope(rows)
	if !ok {
		return false
	}
	if effect == nil {
		return true
	}
	return slope != 0 && *effect != 0 && (slope > 0) == (*effect > 0)
}

// slope is the least-squares slope of y on x over rows where both are present.
// It is not defined with fewer than two pairs or with no spread in x.
func slope(rows []scatterRow) (float64, bool) {
	var n, sx, sy float64
	for _, r := range rows {
		if r.X != nil && r.Y != nil {
			n++
			sx += *r.X
			sy += *r.Y
		}
	}
	if n < 2 {
		return 0, false
	}
	mx, my := sx/n, sy/n
	var sxx, sxy float64
	for _, r := range rows {
		if r.X != nil && r.Y != nil {
			sxx += (*r.X - mx) * (*r.X - mx)
			sxy += (*r.X - mx) * (*r.Y - my)
		}
	}
	if sxx == 0 {
		return 0, false
	}
	return sxy / sxx, true
}
