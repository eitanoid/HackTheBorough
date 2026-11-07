CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE data_sources (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name            text NOT NULL,
  kind            text NOT NULL CHECK (kind IN ('csv', 'rss', 'http_json')),
  config          jsonb NOT NULL,        -- url, field paths, uploaded file ref
  poll_interval_s integer,               -- NULL for one-off CSV uploads
  profile         jsonb,                 -- SourceProfile, written by the profiler
  last_polled_at  timestamptz,
  last_error      text,
  created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE data_records (
  id             bigserial PRIMARY KEY,
  data_source_id uuid NOT NULL REFERENCES data_sources(id) ON DELETE CASCADE,
  external_id    text NOT NULL,          -- feed guid, row hash, time bucket
  observed_at    timestamptz NOT NULL,   -- when it happened
  ingested_at    timestamptz NOT NULL DEFAULT now(),
  payload        jsonb NOT NULL,
  UNIQUE (data_source_id, external_id)
);
CREATE INDEX ON data_records (data_source_id, observed_at);

CREATE TABLE projects (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text NOT NULL,
  goal       text NOT NULL,
  last_error text,                   -- authoring failure, shown in the UI
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
  version        integer NOT NULL,
  analysis_spec  jsonb NOT NULL,         -- AnalysisSpec
  insight_prompt text NOT NULL,
  rationale      text NOT NULL,
  is_active      boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, version)
);
CREATE UNIQUE INDEX one_active_workflow ON workflows (project_id) WHERE is_active;

CREATE TABLE runs (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  workflow_id  uuid NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  data_up_to   timestamptz NOT NULL,     -- watermark: newest observed_at included
  status       text NOT NULL CHECK (status IN ('running', 'succeeded', 'failed')),
  result       jsonb,                    -- AnalysisResult
  error        text,
  started_at   timestamptz NOT NULL DEFAULT now(),
  finished_at  timestamptz
);

CREATE TABLE insights (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id      uuid NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
  project_id  uuid NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  finding_key text NOT NULL,             -- e.g. 'corr:rain_mm:rentals:lag+1'
  kind        text NOT NULL CHECK (kind IN ('correlation', 'anomaly', 'trend_change')),
  headline    text NOT NULL,
  explanation text NOT NULL,
  caveats     jsonb NOT NULL DEFAULT '[]',
  stats       jsonb NOT NULL,            -- copied from AnalysisResult; the UI renders numbers from here
  chart_spec  jsonb,                     -- Vega-Lite
  narrated_by text NOT NULL CHECK (narrated_by IN ('llm', 'template')),
  narrated_stats jsonb NOT NULL,         -- the stats the text was written against
  reviewed_at    timestamptz,
  review_reason  text,                   -- reviewer's one-line reason, shown on the card
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (run_id, finding_key)
);

CREATE VIEW latest_insights AS
SELECT i.*
FROM insights i
JOIN (
  SELECT DISTINCT ON (w.project_id) r.id AS run_id
  FROM runs r
  JOIN workflows w ON w.id = r.workflow_id
  WHERE r.status = 'succeeded'
  ORDER BY w.project_id, r.finished_at DESC
) latest ON latest.run_id = i.run_id;
