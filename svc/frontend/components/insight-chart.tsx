"use client";

import { useEffect, useRef, useState } from "react";
import embed, { type Result, type VisualizationSpec } from "vega-embed";

export default function InsightChart({ spec }: { spec: VisualizationSpec }) {
  const host = useRef<HTMLDivElement>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    const element = host.current;
    if (!element) return;

    let cancelled = false;
    let result: Result | undefined;
    embed(element, spec, { actions: false, renderer: "canvas" })
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
