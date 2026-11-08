import type { JsonObject } from "@bufbuild/protobuf";
import type { VisualizationSpec } from "vega-embed";

// Generated protobuf Struct fields are already plain JSON objects.
export function specFromStruct(spec: JsonObject | undefined): VisualizationSpec | null {
  if (!spec) return null;

  const copy = JSON.parse(JSON.stringify(spec)) as MutableSpec;
  smoothLineMarks(copy);
  return copy as VisualizationSpec;
}

export function hasMissingChartData(spec: VisualizationSpec | null): boolean {
  return spec !== null && containsNull(spec);
}

type MutableMark = string | {
  [key: string]: unknown;
  type?: string;
};

type MutableSpec = {
  [key: string]: unknown;
  layer?: MutableSpec[];
  mark?: MutableMark;
};

function smoothLineMarks(spec: MutableSpec) {
  if (spec.mark && typeof spec.mark === "object" && spec.mark.type === "line") {
    spec.mark = { ...spec.mark, invalid: "filter", interpolate: "monotone" };
  }
  spec.layer?.forEach(smoothLineMarks);
}

function containsNull(value: unknown): boolean {
  if (value === null) return true;
  if (Array.isArray(value)) return value.some(containsNull);
  if (typeof value === "object") return Object.values(value).some(containsNull);
  return false;
}
