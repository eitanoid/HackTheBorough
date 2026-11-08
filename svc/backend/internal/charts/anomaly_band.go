package charts

type bandRow struct {
	T       string   `json:"t"`
	V       *float64 `json:"v"`
	Lo      *float64 `json:"lo"`
	Hi      *float64 `json:"hi"`
	Flagged bool     `json:"flagged"`
}

// anomalyBand draws the series, its expected band, and the flagged points.
// The band may vary over time; nothing assumes it is constant.
func anomalyBand(pts []map[string]any, m Meta) (spec, error) {
	if err := checkCount(pts); err != nil {
		return spec{}, err
	}
	rows := make([]bandRow, 0, len(pts))
	for i, p := range pts {
		t, err := timeCol(p, i)
		if err != nil {
			return spec{}, err
		}
		r := bandRow{T: t}
		for _, c := range []struct {
			col string
			dst **float64
		}{{"v", &r.V}, {"lo", &r.Lo}, {"hi", &r.Hi}} {
			if *c.dst, err = numCol(p, c.col, i); err != nil {
				return spec{}, err
			}
		}
		if r.Flagged, err = boolCol(p, "flagged", i); err != nil {
			return spec{}, err
		}
		rows = append(rows, r)
	}
	a := m.axis(0, "v")
	title := axisTitle(a)
	tooltip := []obj{
		tipTime(m.Grain), tipNum("v", a.Label),
		tipNum("lo", "Expected low"), tipNum("hi", "Expected high"),
	}

	s := newSpec(rows)
	s.Layer = []layer{
		{
			Mark: obj{"type": "area", "style": "band", "invalid": invalidBreak},
			Encoding: obj{
				"x":  timeAxis(),
				"y":  quantY("lo", title),
				"y2": obj{"field": "hi"},
			},
		},
		{
			Mark:     obj{"type": "line", "invalid": invalidBreak},
			Encoding: obj{"x": timeAxis(), "y": quantY("v", title)},
		},
		hover(obj{"x": timeAxis(), "y": quantY("v", title), "tooltip": tooltip}),
		{
			Mark:      obj{"type": "point", "style": "flagged"},
			Transform: []any{obj{"filter": "datum.flagged === true"}},
			Encoding:  obj{"x": timeAxis(), "y": quantY("v", title), "tooltip": tooltip},
		},
	}
	return s, nil
}
