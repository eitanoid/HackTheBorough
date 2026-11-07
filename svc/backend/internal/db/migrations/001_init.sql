CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE data_sources (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name            text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('csv', 'rss')),  -- 'http_json' is stretch
  config          jsonb NOT NULL,        -- feed url or uploaded file ref
  poll_interval_s integer,               -- NULL for one-off CSV uploads
  profile         jsonb,                 -- SourceProfile, written by the profiler
  last_polled_at  timestamptz,
  last_error      text,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE data_records (
  id             bigserial PRIMARY KEY,
  data_source_id uuid NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
  external_id    text NOT NULL,          -- feed guid or row hash
  observed_at    timestamptz NOT NULL,   -- when it happened
  ingested_at    timestamptz NOT NULL DEFAULT now(),
  payload        jsonb NOT NULL,
  UNIQUE (data_source_id, external_id)
);
CREATE INDEX ON data_records (data_source_id, observed_at);

CREATE TABLE projects (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text NOT NULL,
  goal       text NOT NULL,              -- also the investigation workflow's question
  last_error text,                       -- generation failure, shown in the UI
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE project_sources (
  project_id     uuid REFERENCES projects(id) ON DELETE CASCADE,
  data_source_id uuid REFERENCES data_sources(id) ON DELETE CASCADE,
  PRIMARY KEY (project_id, data_source_id)
);

CREATE TABLE workflows (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  project_id     uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  kind           text NOT NULL CHECK (kind IN ('statistical', 'investigation')),
  version        integer NOT NULL,
  analysis_spec  jsonb,                  -- AnalysisSpec; statistical only
  report         jsonb,                  -- InvestigationReport; investigation only
  insight_prompt text NOT NULL,          -- narrator guidance or investigator brief
  rationale      text NOT NULL,          -- why this plan; shown in the UI
  is_active      boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, kind, version),
  CHECK ((kind = 'statistical') = (analysis_spec IS NOT NULL)),
  CHECK ((kind = 'investigation') = (report IS NOT NULL))
);
CREATE UNIQUE INDEX one_active_workflow ON workflows (project_id, kind) WHERE is_active;

CREATE TABLE workflow_steps (            -- an investigation's recorded tool calls
  workflow_id uuid NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  seq         integer NOT NULL,
  tool        text NOT NULL CHECK (tool IN ('describe_source', 'query_series', 'test_pair')),
  input       jsonb NOT NULL,            -- exact request; replayed on every run
  output      jsonb NOT NULL,            -- what the agent saw when it generated this version
  PRIMARY KEY (workflow_id, seq)
);

CREATE TABLE runs (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workflow_id uuid NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  data_up_to  timestamptz NOT NULL,      -- watermark: newest observed_at included
  status      text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  result      jsonb,                     -- AnalysisResult or ReplayResult
  error       text,
  started_at  timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);

CREATE TABLE reviews (
  id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workflow_id      uuid NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  run_id           uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  decision         text NOT NULL CHECK (decision IN ('keep', 'regenerate')),
  reason_category  text NOT NULL,        -- from the fixed lists in Reviewer
  reason           text NOT NULL,        -- one line, shown in the workflow history
  decided_by       text NOT NULL CHECK (decided_by IN ('rule', 'llm')),
  acted_on         boolean NOT NULL,     -- false when the daily cap blocked a regeneration
  next_workflow_id uuid REFERENCES workflows(id),
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE insights (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id      uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  origin      text NOT NULL CHECK (origin IN ('statistical', 'investigation')),
  finding_key text NOT NULL,             -- 'corr:rain_mm:rentals:lag+1' or 'inv:v3:2'
  kind        text NOT NULL CHECK (kind IN ('correlation', 'anomaly', 'observation')),
  headline    text NOT NULL,
  explanation text NOT NULL,
  caveats     jsonb NOT NULL DEFAULT '[]',
  stats       jsonb NOT NULL,            -- numbers from the worker; the UI renders figures from here
  evidence    jsonb,                     -- investigation: [{seq, path, value}] per claimed number
  chart_spec  jsonb,                     -- Vega-Lite
  narrated_by text NOT NULL CHECK (narrated_by IN ('llm', 'template')),
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, finding_key)
);

CREATE VIEW latest_insights AS
SELECT i.*
FROM insights i
JOIN (
  SELECT DISTINCT ON (w.project_id, w.kind) r.id AS run_id
  FROM runs r
  JOIN workflows w ON w.id = r.workflow_id
  WHERE r.status = 'succeeded'
  ORDER BY w.project_id, w.kind, r.finished_at DESC
) latest ON latest.run_id = i.run_id;
