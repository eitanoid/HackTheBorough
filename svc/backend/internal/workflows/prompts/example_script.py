import math

import numpy as np
import pandas as pd

MAX_LAG = 7
WINDOW = 28          # periods of history for the anomaly baseline
RECENT = 3           # only the last few periods can be anomalous
Z_MIN = 3.5
CHART_ROWS = 200


def num(x):
    """A plain float, or None when the value is missing or not finite."""
    return None if x is None or not np.isfinite(x) else float(x)


def spearman(x, y):
    """Spearman rho and a two-sided p-value (Fisher z approximation, n > 3).

    Only pandas and numpy are available, so there is no scipy.stats.
    """
    n = len(x)
    rho = x.rank().corr(y.rank())   # Spearman = Pearson on ranks (pandas' own method="spearman" needs scipy)
    if n <= 3 or not np.isfinite(rho) or abs(rho) >= 1:
        return None, None
    z = math.atanh(rho) * math.sqrt((n - 3) / 1.06)   # 1.06: variance inflation for rank correlation
    return float(rho), float(math.erfc(abs(z) / math.sqrt(2)))


def rfc3339(ts):
    return ts.strftime("%Y-%m-%dT%H:%M:%SZ")


def frame(key):
    """Records of one source as a time-indexed frame (empty if none)."""
    recs = INPUT["sources"].get(key, {}).get("records", [])
    if not recs:
        return pd.DataFrame()
    df = pd.DataFrame([{"t": r["observed_at"], **r["payload"]} for r in recs])
    df["t"] = pd.to_datetime(df["t"], utc=True)
    return df.set_index("t").sort_index()


end = pd.Timestamp(INPUT["data_up_to"]).floor("D")
news, trips = frame("news"), frame("trips")
starts = [d.index.min() for d in (news, trips) if len(d)]
index = pd.date_range(min(starts).floor("D"), end, freq="D") if starts else pd.DatetimeIndex([])

series = {}   # id -> (info, values)

if len(news) and "title" in news:
    mentions = news["title"].fillna("").str.contains("strike", case=False)
    # A count of events: periods with no event are zero, not missing.
    values = mentions.resample("D").sum().reindex(index, fill_value=0).astype(float)
    series["strike_news"] = ("Strike headlines", "news", "headlines", values)

if len(trips) and "count" in trips:
    # A measure: periods with no readings stay missing.
    values = trips["count"].resample("D").sum(min_count=1).reindex(index)
    series["trips"] = ("Bus trips", "trips", "trips", values)

result = {"series": [], "tests": [], "anomalies": []}
for sid, (label, source, unit, values) in series.items():
    result["series"].append({
        "id": sid, "label": label, "source": source, "unit": unit, "grain": "day",
        "periods": int(len(values)), "points": int(values.notna().sum()),
    })

# Correlate day-to-day changes; every lag is its own reported test.
if "strike_news" in series and "trips" in series:
    a, b = series["strike_news"][3].diff(), series["trips"][3].diff()
    for lag in range(0, MAX_LAG + 1):
        pair = pd.concat([a, b.shift(-lag)], axis=1, keys=["a", "b"]).dropna()
        if len(pair) < 3 or pair["a"].nunique() < 2 or pair["b"].nunique() < 2:
            continue   # the statistic is undefined; skip rather than report NaN
        rho, p = spearman(pair["a"], pair["b"])
        if rho is None:
            continue
        tail = pair.tail(CHART_ROWS)
        result["tests"].append({
            "key": f"corr:strike_news:trips:lag{lag:+d}",
            "kind": "correlation",
            "series": ["strike_news", "trips"],
            "p": float(p),
            "effect": float(rho),
            "n": int(len(pair)),
            "stats": {"rho": float(rho), "lag": lag},
            "chart": {"type": "lag_scatter",
                      "points": [{"x": num(x), "y": num(y)} for x, y in zip(tail["a"], tail["b"])]},
        })

# Robust z-score against the previous WINDOW periods, last RECENT periods only.
for sid, (label, source, unit, values) in series.items():
    for i in range(max(0, len(values) - RECENT), len(values)):
        base = values.iloc[max(0, i - WINDOW):i].dropna()
        if len(base) < 14 or np.isnan(values.iloc[i]):
            continue
        median = float(base.median())
        scale = 1.4826 * float((base - median).abs().median())
        if scale == 0:
            continue
        z = (float(values.iloc[i]) - median) / scale
        if abs(z) <= Z_MIN:
            continue
        period = values.index[i]
        shown = values.iloc[max(0, i - 60):i + 1]
        result["anomalies"].append({
            "key": f"anom:{sid}:{period.strftime('%Y-%m-%d')}",
            "series": sid,
            "period": rfc3339(period),
            "value": float(values.iloc[i]),
            "expected": median,
            "score": float(z),
            "chart": {"type": "anomaly_band", "points": [
                {"t": rfc3339(t), "v": num(v), "lo": median - Z_MIN * scale,
                 "hi": median + Z_MIN * scale, "flagged": bool(t == period)}
                for t, v in shown.items()]},
        })
