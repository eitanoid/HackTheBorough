-- The narrator's verdict on a statistical finding. NULL for template text and
-- for investigation cards. A doubtful card is still shown, with a badge.
ALTER TABLE insights ADD COLUMN plausibility text
  CHECK (plausibility IN ('plausible', 'doubtful'));

-- The view was created with i.*, which Postgres expanded at creation time;
-- recreate it so it includes the new column (appended last, so OR REPLACE works).
CREATE OR REPLACE VIEW latest_insights AS
SELECT i.*
FROM insights i
JOIN (
  SELECT DISTINCT ON (w.project_id, w.kind) r.id AS run_id
  FROM runs r
  JOIN workflows w ON w.id = r.workflow_id
  WHERE r.status = 'succeeded'
  ORDER BY w.project_id, w.kind, r.finished_at DESC
) latest ON latest.run_id = i.run_id;
