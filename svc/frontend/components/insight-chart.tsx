"use client";

import { useEffect, useRef, useState } from "react";
import embed, { type Result, type VisualizationSpec } from "vega-embed";

const chartConfig = {
  background: "transparent",
  font: "var(--font-geist-sans), system-ui, sans-serif",
  axis: {
    labelColor: "#7b8d83",
    titleColor: "#3b5d4b",
    gridColor: "#edf1ee",
    domainColor: "#dce7df",
    tickColor: "#dce7df",
    labelFontSize: 9,
    titleFontSize: 9,
  },
  legend: {
    labelColor: "#3b5d4b",
    titleColor: "#3b5d4b",
    labelFontSize: 9,
    titleFontSize: 9,
  },
  range: {
    category: ["#17745d", "#b4653a", "#4c9670", "#8a6f4d"],
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
