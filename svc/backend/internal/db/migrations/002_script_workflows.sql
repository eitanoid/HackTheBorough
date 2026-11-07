-- Statistical workflows are now an agent-written Python script, not a JSON spec
-- (see claude-plan.md, "Analysis script and stats library").

-- Old spec-based statistical workflows can't be converted to scripts. Removing
-- them cascades to their runs, reviews and (via runs) insights; projects stay,
-- and the author regenerates a script for them.
DELETE FROM workflows WHERE kind = 'statistical';

-- Dropping the column also drops the table CHECK that references it.
ALTER TABLE workflows DROP COLUMN analysis_spec;
ALTER TABLE workflows ADD COLUMN analysis_script text;
ALTER TABLE workflows ADD CONSTRAINT workflows_script_matches_kind
  CHECK ((kind = 'statistical') = (analysis_script IS NOT NULL));

-- Investigations may run model-written Python.
ALTER TABLE workflow_steps DROP CONSTRAINT IF EXISTS workflow_steps_tool_check;
ALTER TABLE workflow_steps ADD CONSTRAINT workflow_steps_tool_check
  CHECK (tool IN ('describe_source', 'query_series', 'test_pair', 'run_python'));

-- Custom tests registered by scripts produce 'custom' findings.
ALTER TABLE insights DROP CONSTRAINT IF EXISTS insights_kind_check;
ALTER TABLE insights ADD CONSTRAINT insights_kind_check
  CHECK (kind IN ('correlation', 'anomaly', 'custom', 'observation'));
