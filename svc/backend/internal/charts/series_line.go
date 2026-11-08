package charts

type lineRow struct {
	T string   `json:"t"`
	V *float64 `json:"v"`
}

// seriesLine draws one series. There is a single line, so no legend.
func seriesLine(pts []map[string]any, m Meta) (spec, error) {
	if err := checkCount(pts); err != nil {
		return spec{}, err
	}
	rows := make([]lineRow, 0, len(pts))
	for i, p := range pts {
		t, err := timeCol(p, i)
		if err != nil {
			return spec{}, err
		}
		v, err := numCol(p, "v", i)
		if err != nil {
			return spec{}, err
		}
		rows = append(rows, lineRow{t, v})
	}
	a := m.axis(0, "v")
	title := axisTitle(a)

	s := newSpec(rows)
	s.Layer = []layer{
		{
			Mark:     obj{"type": "line", "invalid": invalidBreak},
			Encoding: obj{"x": timeAxis(), "y": quantY("v", title)},
		},
		hover(obj{
			"x": timeAxis(), "y": quantY("v", title),
			"tooltip": []obj{tipTime(m.Grain), tipNum("v", a.Label)},
		}),
	}
	return s, nil
}
