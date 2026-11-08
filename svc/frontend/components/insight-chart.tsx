"use client";

import { useEffect, useRef, useState } from "react";
import embed, { type Result, type VisualizationSpec } from "vega-embed";

const chartConfig = {
  background: "transparent",
  font: "var(--font-geist-sans), system-ui, sans-serif",
  axis: {
    labelColor: "#718a98",
    titleColor: "#315d70",
    gridColor: "#e5eef2",
    domainColor: "#cfdee5",
    tickColor: "#cfdee5",
    labelFontSize: 9,
    titleFontSize: 9,
  },
  legend: {
    labelColor: "#315d70",
    titleColor: "#315d70",
    labelFontSize: 9,
    titleFontSize: 9,
  },
  range: {
    category: ["#167b9b", "#bf6846", "#57a9bd", "#8a7b55"],
  },
  line: { color: "#167b9b", strokeWidth: 2.4 },
  point: { color: "#167b9b", filled: true, size: 48 },
  style: {
    band: { fill: "#62b6c9", fillOpacity: 0.14, stroke: "#62aebf", strokeWidth: 0.7 },
    flagged: { fill: "#d65d55", stroke: "#ffffff", strokeWidth: 2, shape: "diamond", size: 150 },
  },
  view: { stroke: null },
};

export default function InsightChart({ spec }: { spec: VisualizationSpec }) {
  const host = useRef<HTMLDivElement>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    const element = host.current;
    if (!element) return;

    let cancelled = false;
    let result: Result | undefined;
    embed(element, spec, { actions: false, renderer: "svg", config: chartConfig })
      .then((value) => {
        if (cancelled) {
          value.finalize();
          return;
        }
        result = value;
        setError("");
      })
      .catch((reason: unknown) => {
        if (!cancelled) {
          setError(reason instanceof Error ? reason.message : "Chart failed to render.");
        }
      });

    return () => {
      cancelled = true;
      result?.finalize();
    };
  }, [spec]);

  return (
    <>
      <div className="insight-chart" ref={host} />
      {error && <p className="insight-chart-error" role="alert">{error}</p>}
    </>
  );
}
