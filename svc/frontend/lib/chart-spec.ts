import type { JsonObject } from "@bufbuild/protobuf";
import type { VisualizationSpec } from "vega-embed";

// Generated protobuf Struct fields are already plain JSON objects.
export function specFromStruct(spec: JsonObject | undefined): VisualizationSpec | null {
  return (spec as VisualizationSpec | undefined) ?? null;
}
