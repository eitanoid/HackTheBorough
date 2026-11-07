# Live Insights Platform: Implementation Plan

Nov 7, 2026 · @Tymur

## Overview

In 24 hours, 3–4 people build a service where each project has two agent-generated workflows, one statistical and one investigative. Both re-run deterministically whenever data lands, and a reviewer agent decides when either needs regenerating.

**MVP (must ship):**

- Two source types: CSV upload and RSS/Atom feed.
- Projects: a goal in plain language plus a set of sources.
- A statistical workflow per project: an agent-written `AnalysisSpec`, executed by a deterministic stats engine with false-discovery control (lagged correlation and anomalies).
- An investigation workflow per project: an agent explores the data through read-only worker tools to answer the goal; its tool calls are recorded and replayed on new data.
- A reviewer agent that, after each run, decides whether the spec still suits the data and whether investigation findings still hold, and triggers regeneration when they don't.
- A React dashboard with statistical and exploratory finding cards, Vega-Lite charts, workflow history with review reasons, and live updates over SSE.
- A replay mode that streams historical data in fast, for the demo.

**Stretch (only after milestone M3 passes):** an `http_json` source adapter, the `trend_change` test, ad-hoc user questions, investigations started automatically by anomalies, a permutation test for surviving findings, a deterministic verifier for card text.

**Non-goals:** authentication, multi-tenancy, web scraping, X/Twitter, running model-written code, production deployment.

**For the coding agent:** build the milestones in the order given in the work plan. Each milestone ends with acceptance checks; do not start the next one until they pass. The SQL migrations, the Go structs in `internal/contracts/` and the stats worker's matching Pydantic models are shared by every workstream: propose changes to them, don't make them unilaterally.

## Design decisions

The sketch's shape stays: ingestor, data store, project store, workflow manager, analysis, reporter. Eight changes make it safe to run and statistically honest.

| # | Area | Sketch | Plan | Why |
| --- | --- | --- | --- | --- |
| 1 | Statistical analysis | LLM-written "Statistics Code" | `AnalysisSpec` JSON, validated, executed by our engine | No model-written code runs on the server; the same corrections apply on every run; every number traces to a known computation |
| 2 | Workflows per project | One Workflows table with statistics code and an AI insights prompt | One statistical and one investigation workflow per project, each a versioned artifact re-run deterministically on new data | Re-runs are cheap and comparable; an LLM is called to regenerate only when the reviewer says so |
| 3 | Regeneration | — | Only for reasons from a fixed list, capped per day; the spec reviewer never sees results | Regenerating a spec until something passes would be p-hacking |
| 4 | Time | One `CreatedAt`, set at ingestion | `observed_at` (event time) plus `ingested_at` | Correlations must run on when things happened, not when we fetched them |
| 5 | Duplicates | None | Unique `(data_source_id, external_id)`; insert with `ON CONFLICT DO NOTHING` | Polling re-fetches the same items, so counts would inflate on every poll |
| 6 | Results | JSON columns on Project and an Insights table | Insights only, written by runs; a `reviews` table; a `latest_insights` view per workflow kind | One source of truth, and provenance for every claim |
| 7 | Polling | On the source and on the project | On the source only; a project's workflows re-run when its sources' newest `observed_at` passes their last run | Two projects sharing a source can't disagree |
| 8 | Project ↔ sources | Array of UUIDs | `project_sources` join table | Foreign keys and cascades work |
| 9 | Data schema | JSON schema | `SourceProfile`: each field's role, unit and aggregation | The engine must know which field is a timestamp, a measure or text |
| 10 | Example source | Twitter | RSS and CSV, JSON APIs as stretch | X's API charges $0.005 per post read and has no free read tier ([source](https://apiserpent.com/blog/free-twitter-x-api)) |

**Numbers rule.** Every number shown on the dashboard comes from the stats worker: a statistical run's `AnalysisResult`, or the replayed output of an investigation step. The LLM decides what to analyse and explains what was found; it never computes or restates figures.

**Languages.** The main service is Go: ingestion, API, SSE, the workflow manager, every LLM call and chart building. The stats engine is a Python worker (FastAPI, pandas, scipy, statsmodels) behind one contract: `AnalysisSpec` in, `AnalysisResult` out. Python gives tested Spearman p-values with ties, Benjamini–Hochberg, Welch tests and resampling, and the language boundary falls on a team boundary (workstream B).

## Architecture

One Go binary runs the API, the background loops and every LLM call; a Python worker runs the statistics and serves the investigator's tools; Postgres is the only store; the React app talks to the Go service over REST and one SSE stream.

&#91;embedded content: architecture · agents in Go, statistics in a Python worker, one Postgres database\]

Ingestion and the API sit above the database. Below it, the agents write workflow artifacts and card text, and the worker re-runs those artifacts on every update; only the tinted boxes call an LLM.

**Process model.** `cmd/server` starts the HTTP server and two goroutines: the ingest loop and the run loop, which runs both workflow kinds. Generating a workflow (author or investigator) happens in its own goroutine, so a slow agent never delays runs. An in-process pub/sub in `internal/events` (a mutex-guarded set of subscriber channels) carries `source_updated`, `project_updated`, `workflow_regenerated`, `investigation_step` and `run_failed` between the loops and the SSE handler. The run loop and the investigator call the stats worker over HTTP (`http://stats:8001`). Docker Compose runs Postgres 16, the Go service and the worker. In development, Vite proxies `/api` to the Go service.

**Libraries.**

- Go service (Go 1.22 or later): standard `net/http` with method and path patterns in `ServeMux`, `log/slog`, `github.com/jackc/pgx/v5` with `pgxpool`, `github.com/mmcdole/gofeed`, `github.com/anthropics/anthropic-sdk-go/v2`, `github.com/google/uuid`.
- Stats worker: Python 3.12, `fastapi`, `uvicorn`, `psycopg[binary]` 3, `pydantic` 2, `pandas`, `numpy`, `scipy`, `statsmodels`, `pytest`.
- Frontend: React, Vite, TypeScript, `vega-embed`, TanStack Query.
- Plain SQL through pgx, no ORM. Migrations are SQL files embedded with `go:embed`, applied in order at startup and tracked in a `schema_migrations` table.

**Repo layout.**

```
server/                      # Go module
  go.mod
  cmd/server/main.go         # wires config, db, loops, HTTP
  cmd/seed/main.go           # loads demo sources
  migrations/001_init.sql    # embedded with go:embed
  internal/
    config/                  # env vars, model ids, thresholds, regeneration limits
    db/                      # pgxpool, migration runner, queries
    events/                  # in-process pub/sub
    contracts/               # SourceProfile, AnalysisSpec, AnalysisResult, WorkflowDraft,
                             #   Narration, InvestigationReport, ReviewDecision (frozen after M0)
    llm/                     # Anthropic client wrapper + JSONL call log
    ingest/
      adapter.go             # SourceAdapter interface, RawRecord
      csv.go
      rss.go
      loop.go                # polling scheduler
    profiling/               # pre-pass + LLM labelling
    statsclient/             # HTTP client for the stats worker
    workflows/
      author.go              # statistical: goal + profiles -> WorkflowDraft
      runner.go              # watermark -> worker run or replay -> insights -> review
    investigate/             # investigation: tool-runner loop -> recorded steps + report
    review/                  # reviewer: hard rules, then LLM keep or regenerate
    narration/
      narrator.go
    charts/                  # Vega-Lite builders from worker output
    api/                     # handlers, SSE, demo replay
stats/                       # Python worker
  pyproject.toml
  stats/
    main.py                  # FastAPI: /run, /tools/*, /replay, /healthz
    spec.py                  # Pydantic mirror of the Go contracts
    frames.py                # Postgres rows -> aligned DataFrames
    methods.py               # correlation and anomaly tests, FDR, health block
    engine.py                # run_spec()
    tools.py                 # describe_source, query_series, test_pair
  tests/
contracts/fixtures/          # golden JSON that Go and Python both round-trip
web/
  src/
    api.ts
    pages/Sources.tsx
    pages/NewProject.tsx
    pages/Project.tsx
    components/FindingCard.tsx
    components/WorkflowHistory.tsx
demo/datasets/
docker-compose.yml           # postgres, server, stats
Makefile                     # make dev, make test, make demo
```

## Data model

Nine tables in one Postgres database: sources and their records on the ingest side; projects, workflows (with an investigation's recorded steps), runs, reviews and insights on the analysis side. This is `migrations/001_init.sql`, frozen after M0.

```sql
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
```

- Each run writes a full set of insight rows for its workflow, and the dashboard shows the latest succeeded run of each kind. For statistical findings whose ρ and lag kept their sign, the runner copies the text forward with fresh stats and charts.
- `workflow_steps.output` keeps what the investigator saw when it generated a version, so the reviewer can compare it with each replay.
- Reading a measure out of JSONB: `(payload->>'temp_c')::double precision`. At hackathon volumes (tens of thousands of rows) no further indexing is needed.

## Ingestion

Every source type implements one small adapter; the loop owns scheduling, deduplication and errors, so adapters stay pure.

```go
type RawRecord struct {
    ExternalID string         // stable per item
    ObservedAt time.Time      // UTC
    Payload    map[string]any
}

type SourceAdapter interface {
    Kind() string
    Fetch(ctx context.Context, cfg json.RawMessage, since *time.Time) ([]RawRecord, error)
}
```

**Adapters.**

| Kind | `ExternalID` | `ObservedAt` | Notes |
| --- | --- | --- | --- |
| `csv` | SHA-256 of the row as canonical JSON (sorted keys) | The timestamp column chosen at upload (the profiler proposes it) | Uploaded once through the API; no polling; parsed with the layout the pre-pass detected (RFC 3339, `2006-01-02`, `02/01/2006`), in UTC |
| `rss` | Item `GUID`, else `Link` | `PublishedParsed`, else `UpdatedParsed`; if neither, fetch time and `payload._time_estimated = true` | `gofeed`; payload keeps title, description, link, categories; default poll 300 s |

Stretch: an `http_json` adapter for polled JSON APIs, configured with `url`, `records_path`, `id_field`, `time_field` and header-based auth whose values name environment variables.

**Loop (`internal/ingest/loop.go`).**

1. A `time.Ticker` fires every 10 s. Select sources whose `poll_interval_s` is set and whose `last_polled_at + poll_interval_s` has passed.
2. Fetch each due source in its own goroutine, at most 4 at once, with a context timeout. Insert the batch in one statement (`unnest` arrays or a `pgx.Batch`) with `ON CONFLICT (data_source_id, external_id) DO NOTHING RETURNING observed_at`.
3. Set `last_polled_at`, clear `last_error`. If any rows were inserted, publish `source_updated` with the newest `observed_at`.
4. On an error or a recovered panic, store it in `last_error`, double the effective interval (capped at 30 min) and move on. One failing source never stops the loop.
5. On creating a source, fetch once immediately, then run the profiler.

**Acceptance checks.**

- Polling the same RSS fixture twice inserts zero rows the second time.
- A CSV of 2023 rows gets `observed_at` values in 2023, not today.
- An unreachable URL sets `last_error` while other sources keep polling.
- Adapter tests use recorded fixtures in `tests/fixtures/`, never the live network.

## Source profiling

One LLM call per source turns a deterministic field summary into a `SourceProfile`; the model labels fields, it never sees the full data.

```go
type FieldProfile struct {
    Path        string  `json:"path"` // key in payload, dotted for nesting
    Role        string  `json:"role" jsonschema:"enum=timestamp,enum=measure,enum=category,enum=text,enum=id,enum=ignore"`
    Unit        *string `json:"unit"` // "°C", "GBP", "trips"
    Aggregation *string `json:"aggregation" jsonschema:"enum=mean,enum=sum,enum=last,enum=count"` // measures only
    Description string  `json:"description"`
}

type SourceProfile struct {
    Fields        []FieldProfile `json:"fields"`
    TimestampPath *string        `json:"timestamp_path"` // becomes observed_at (CSV, JSON lists)
    Summary       string         `json:"summary"`        // one sentence: what this source measures
}
```

**Steps (`internal/profiling`).**

1. Pre-pass in Go over up to 1,000 records: per field, the inferred type (number, time with its layout, string), null rate, distinct count, min and max for numbers, and five example values.
2. Call `claude-haiku-5-5` with structured outputs: `client.Beta.Messages.New` with `OutputConfig.Format` set to `anthropic.BetaJSONOutputFormatParam{Schema: &profile}`. The SDK derives the schema from the struct and unmarshals the reply into it; struct schemas are a beta feature in the Go SDK ([source](https://platform.claude.com/docs/en/build-with-claude/structured-outputs)). Send the pre-pass plus 20 sample records.
3. Check the output against the pre-pass: a `measure` that isn't numeric becomes `category`; a `timestamp` that doesn't parse becomes `ignore`; any `path` not in the data is dropped.
4. Save to `data_sources.profile`.

Print each contract's generated schema once in a test and check it: every field required, pointer fields nullable, enums present.

**Order for CSV.** Profile the uploaded file before writing any rows, because `observed_at` comes from `timestamp_path`. RSS and JSON sources are profiled after their first fetch.

**Text fields.** A `text` field (an RSS title or summary) is never correlated directly. The analysis spec turns it into counts per period, either all records or those matching keywords (next section).

**Re-profiling.** When a run's health block reports payload keys missing from a source's profile, the runner re-profiles that source before the reviewer decides on the statistical spec.

**Acceptance check.** A fixture with a date column, two numeric columns and a free-text column is profiled with the right roles, and a deliberately mislabelled output from a stub LLM is corrected by step 3.

## Analysis spec and stats engine

The agent writes an `AnalysisSpec`; `run_spec()` executes it as a pure, deterministic function and reports how many candidate findings it rejected at each step.

**Spec (`internal/contracts/spec.go`).** It is the author's LLM output, so it uses only schema features structured outputs support: lists of small objects instead of tuples or maps. The worker mirrors it in `stats/spec.py`, and both sides round-trip the fixtures in `contracts/fixtures/`.

```go
type Filter struct {
    Path   string `json:"path"`
    Equals string `json:"equals"`
}

type SeriesDef struct {
    ID       string   `json:"id"`        // snake_case, unique within the spec
    Label    string   `json:"label"`     // human name for cards and charts
    SourceID string   `json:"source_id"` // UUID
    Field    *string  `json:"field"`     // measure path; null = count records
    Agg      string   `json:"agg" jsonschema:"enum=mean,enum=sum,enum=last,enum=count"`
    Filters  []Filter `json:"filters"`
    Keywords []string `json:"keywords"`  // text: count records containing any keyword
    Unit     *string  `json:"unit"`
}

type Pair struct {
    A string `json:"a"`
    B string `json:"b"`
}

type AnalysisSpec struct {
    Grain      string      `json:"grain" jsonschema:"enum=hour,enum=day,enum=week"`
    WindowDays int         `json:"window_days"` // 7..730
    Series     []SeriesDef `json:"series"`      // 2..12
    Pairs      []Pair      `json:"pairs"`       // empty = all pairs
    MaxLag     int         `json:"max_lag"`     // 0..14, in grain units
    Tests      []string    `json:"tests"`       // lagged_correlation | anomaly
}
```

Constrained decoding enforces types and enums but not numeric ranges or list lengths ([source](https://platform.claude.com/docs/en/build-with-claude/structured-outputs)), so `AnalysisSpec.Validate()` checks the bounds in the comments above. Engine constants (`MIN_OVERLAP = 20`, `FDR_Q = 0.10`, `MIN_HALF_RHO = 0.2`) live in the worker's config, not in the spec, so the agent can't loosen them.

**Semantic validation**, in the same `Validate()`: series ids unique; every `source_id` belongs to the project; `field` exists in that source's profile with role `measure`; `keywords` only on sources with a `text` field; every pair names defined series; every entry in `tests` is a known test.

**Worker API.** `POST /run` takes `{spec, project_id, data_up_to}` and returns `AnalysisResult`. `POST /tools/describe_source`, `/tools/query_series` and `/tools/test_pair` serve the investigator (see Investigation workflow). `POST /replay` takes a workflow's recorded steps and a new `data_up_to`, re-executes each step and returns a `ReplayResult` with one output per step. The worker reads `data_records` itself through a read-only Postgres role, so records never travel over HTTP. `GET /healthz` serves Compose health checks. The Go client in `internal/statsclient` uses a 60 s timeout (5 s for tool calls) and treats any non-200 response as a failure.

**Engine steps (`run_spec` in the worker).**

1. **Load.** Per series, read the source's records in the window that ends at the run's watermark (not the current time, so historical and replayed data work), apply filters and keywords in pandas, and resample to `grain` with `agg`. Count series fill empty periods with 0; measure series keep gaps as NaN and are never forward-filled.
2. **Align.** Outer-join all series on the period index. Drop any series with fewer than `MIN_OVERLAP` non-null periods and list it in `health.skipped_series`.
3. **Difference.** Take the first difference of every series. This removes shared trends, the main source of spurious correlation.
4. **Lagged correlation.** For each pair (a, b) and each lag k from −`max_lag` to +`max_lag`, compute Spearman ρ between aₜ and bₜ₊ₖ on periods where both exist; skip if n < `MIN_OVERLAP`. Convention: k > 0 means a leads b by k periods. Every (pair, lag) is one test.
5. **False discovery control.** Benjamini–Hochberg across all correlation tests (`statsmodels` `multipletests(method="fdr_bh")`), keeping q ≤ `FDR_Q`. Per pair, keep only the surviving lag with the largest |ρ|.
6. **Stability.** Recompute ρ at the chosen lag on the first and second half of the window separately. Keep the finding only if both halves agree in sign with |ρ| ≥ `MIN_HALF_RHO`.
7. **Anomalies** (undifferenced): robust z = (x − rolling median) / (1.4826 × rolling MAD) over a 28-period trailing window. Flag the last 3 periods where |z| > 3.5.
8. **Health.** Fill the health block the reviewer reads: stale and skipped series, coverage, pairs whose strongest raw lag sits at `max_lag`, payload keys missing from profiles, and how many periods of data exist.

**Result** (Pydantic in the worker, mirrored by `internal/contracts/result.go`).

```python
class Health(BaseModel):
    skipped_series: list[str]              # too few points to analyse
    stale_series: list[str]                # no records in the last 3 periods
    coverage: dict[str, float]             # share of periods with data, per series
    lags_at_boundary: list[str]            # pairs whose strongest raw lag equals max_lag
    unprofiled_keys: dict[str, list[str]]  # source_id -> payload keys missing from its profile
    span_periods: int                      # periods of data available, for grain and window checks

class Finding(BaseModel):
    finding_key: str   # 'corr:{a}:{b}:lag{+k}', 'anom:{s}:{period}'
    kind: Literal["correlation", "anomaly"]
    series: list[str]
    stats: dict[str, float | int | str]  # rho, lag, n, p, q, rho_h1, rho_h2 | z, value, expected
    period_start: datetime
    period_end: datetime

class AnalysisResult(BaseModel):
    data_up_to: datetime
    findings: list[Finding]
    funnel: dict[str, int]   # tests_run, raw_p_lt_05, passed_fdr, passed_stability
    rejected: list[Finding]  # 3 strongest candidates that failed FDR or stability; shown in the demo
    chart_data: dict[str, list[dict]]  # per finding_key: up to 500 points for the Go chart builders
    health: Health
```

The `funnel` drives the dashboard line "tested N, kept M", which is the pitch's evidence that the tool discards noise.

**Known limitation.** Autocorrelation left after differencing still makes p-values optimistic; the half-window stability check is the practical guard. Stretch: a circular-shift permutation test for survivors (200 random shifts of b, each larger than `max_lag`).

**Acceptance checks (stats/`tests/test_engine.py`, synthetic data, no database).**

- Six independent random walks of 200 days, 50 seeds: at most 5 seeds produce any correlation finding.
- b = a shifted forward 2 days plus noise: exactly one finding, lag +2, positive ρ.
- Two series sharing a linear trend with independent noise: no finding.
- A spike of 6 MADs in the last period: one anomaly finding.
- A series whose last 3 periods are empty appears in `health.stale_series`.
- The same inputs give byte-identical `AnalysisResult` JSON.

## Investigation workflow

The investigator explores the data once to answer the project goal; its tool calls and report are saved as the workflow, and every later run replays those calls deterministically instead of calling the agent again.

**Tools (served by the worker, read-only).** Every call carries the project id and the investigation's fixed `data_up_to`; the worker rejects sources outside the project.

| Tool | Input | Returns |
| --- | --- | --- |
| `describe_source` | `source_id` | Fields with roles and units, record count, first and last `observed_at` |
| `query_series` | A `SeriesDef`, `grain`, `window_days` | Up to 200 periods of the resampled series, plus mean, min and max |
| `test_pair` | Two `SeriesDef`s, `grain`, `window_days`, `max_lag` | Spearman ρ, n and p for each lag on the differenced series, the best lag, and the aligned series (up to 200 periods) |

**Generating a version (`internal/investigate`).**

1. Triggered when the project is created, or when the reviewer decides `regenerate` for the investigation workflow.
2. Run `claude-sonnet-5-5` with the Go SDK's tool runner over the three tools, with `MaxIterations` 12 and `InvestigationReport` as the structured output. Input: the goal and the source profiles; for a regeneration, also the previous version's findings and the reviewer's reason, so it doesn't propose retired claims again.
3. Each tool handler calls the worker, records the call as a `workflow_steps` row with its full output, publishes `investigation_step` for the live view, and returns the output to the model truncated to 4 KB.
4. `Validate()` drops any claim whose `seq` or `path` doesn't exist in the recorded steps, because the card reads its numbers from there. A version with no findings left is still saved, with its rationale.
5. In one transaction, save the report and steps as a new `investigation` version and deactivate the previous one. Then trigger a replay run so the findings become insights.

```go
type Claim struct {
    Seq   int     `json:"seq"`   // workflow_steps.seq this number comes from
    Path  string  `json:"path"`  // dotted path into that step's output, e.g. "lags.3.rho"
}

type InvestigationFinding struct {
    Headline    string   `json:"headline"`    // plain words, no digits
    Explanation string   `json:"explanation"` // two or three sentences, no digits
    Claims      []Claim  `json:"claims"`      // every number the card will show
    Caveats     []string `json:"caveats"`
    Chart       string   `json:"chart" jsonschema:"enum=dual_line,enum=lag_scatter,enum=series_line"`
    ChartSeq    int      `json:"chart_seq"`   // step whose output the chart is built from
}

type InvestigationReport struct {
    Rationale string                 `json:"rationale"` // what was explored and why; shown in the UI
    Findings  []InvestigationFinding `json:"findings"`
}
```

**Replay runs.** On each run of the investigation workflow, the worker's `POST /replay` re-executes the recorded steps at the new watermark. The runner re-reads every claim at its `seq` and `path` in the replayed outputs; those values become the card's `stats`, and its chart is rebuilt from step `chart_seq`. No LLM call is made. The reviewer then compares replayed values with the originals.

**Exploratory, not proven.** Investigation findings sit outside the engine's false-discovery family, because an agent trying hypotheses until one looks good is the multiple-comparisons problem the engine exists to prevent. Their cards carry an "exploratory" label, and every number links to the step it came from.

**Acceptance checks.**

- On the demo dataset, generation finishes within 12 tool calls and saves at least one finding.
- A replay at the original `data_up_to` reproduces every recorded output exactly.
- A claim whose `path` doesn't exist in its step output is dropped by `Validate()`.
- A tool call naming a source outside the project is rejected by the worker.

## Workflow manager

Each project has one active statistical workflow and one active investigation workflow; the manager generates a new version only when a project is created or the reviewer asks, and otherwise runs the active versions on every data update.

&#91;embedded content: workflow lifecycle · generate, run, review\]

Runs never call the generating agent; only a `regenerate` decision does, and the reviewer's hard rules can force one without an LLM call.

**Generating.**

| Kind | Generator | Artifact | When |
| --- | --- | --- | --- |
| Statistical | Author (`internal/workflows/author.go`) | `AnalysisSpec`, insight prompt, rationale | Project created; reviewer decides `regenerate` |
| Investigation | Investigator (`internal/investigate`) | Recorded steps and report | Project created; reviewer decides `regenerate` |

**Authoring the statistical workflow.**

```go
type WorkflowDraft struct {
    AnalysisSpec  AnalysisSpec `json:"analysis_spec"`
    InsightPrompt string       `json:"insight_prompt"` // what the user cares about, for the narrator
    Rationale     string       `json:"rationale"`      // why these series and lags; shown in the UI
}
```

1. Input: the goal, and each source's `SourceProfile` with its record count and date range. For a regeneration, also the previous spec and the reviewer's reason.
2. Call `claude-sonnet-5-5` through `internal/llm` with `WorkflowDraft` as the structured output schema.
3. Run `Validate()`. On failure, retry once with the errors appended; if it fails again, store the error in `projects.last_error` and keep the previous version active.
4. In one transaction, insert the new version, deactivate the previous one, and set the triggering review's `next_workflow_id`. Publish `workflow_regenerated`, then trigger a run.

**Running (`internal/workflows/runner.go`).**

1. **Triggers:** `source_updated` events, debounced 30 s per project, plus a sweep every 5 min.
2. **Watermark:** newest `observed_at` across the project's sources. For each active workflow, run if it has no succeeded run yet or the watermark is newer than its last run's `data_up_to`.
3. **Lock:** at most one run per workflow at a time, using a mutex-guarded set (the Go service is a single process).
4. **Execute:** a statistical workflow calls the worker's `POST /run` with its spec; an investigation workflow calls `POST /replay` with its recorded steps. Both pass the watermark.
5. **Statistical insights:** new findings go to the narrator, at most 5 per run, ranked by |ρ| or |z|. An existing finding keeps its text unless ρ or its lag changed sign, in which case it is re-narrated. Findings that disappeared are dropped.
6. **Investigation insights:** text comes from the active version's report; numbers and charts come from the replay.
7. **Store** the insight rows, mark the run `succeeded`, publish `project_updated`.
8. **Review:** hand the run to the reviewer, which may trigger regeneration.
9. **On error:** mark the run `failed` and publish `run_failed`. The previous insights stay on the dashboard.

**Acceptance checks.**

- Creating a project over two seeded CSV sources yields one workflow of each kind and a succeeded run of each, with no manual steps.
- Triggering again with no new data creates no run.
- Appending rows makes no LLM calls beyond narrating new statistical findings and at most one reviewer call per workflow, per the call log.
- A stub LLM returning an invalid draft is retried once; the error shows on the project and the previous version stays active.

## Reviewer

After each succeeded run, the reviewer decides whether the workflow that produced it still fits the data. `keep` changes nothing; `regenerate` asks that workflow's generator for a new version. Every decision is stored in `reviews` with its reason.

**When it runs.** After a succeeded run, at most once per workflow per `REVIEW_MIN_INTERVAL` (60 s). If a hard rule settles the decision, no LLM call is made and the review records `decided_by = 'rule'`.

**Statistical workflow: does the spec still suit the data?** The reviewer sees the spec, the run's health block and whether the project's sources changed. It never sees p-values, findings or the funnel, because regenerating a spec until something passes would be p-hacking.

| Reason category | Signal | Decided by |
| --- | --- | --- |
| `sources_changed` | The project's sources differ from those the spec uses | Rule: regenerate |
| `schema_drift` | `unprofiled_keys` is still non-empty after re-profiling | Reviewer |
| `stale_series` | A series has had no records for 3 periods | Reviewer |
| `coverage` | A series' coverage is under 50%, or `span_periods` now suits a finer grain or a longer window | Reviewer |
| `lag_boundary` | A pair's strongest raw lag sits at `max_lag` | Reviewer |

**Investigation workflow: do the findings still hold?** The reviewer sees the goal, each finding's text and claims, the original step outputs and the replayed ones.

| Reason category | Signal | Decided by |
| --- | --- | --- |
| `claim_contradicted` | A claimed value has changed sign in the replay | Rule: regenerate |
| `effect_faded` | A claimed value has moved more than halfway to zero | Reviewer |
| `explanation_mismatch` | The text no longer describes the replayed numbers | Reviewer |
| `new_scope` | Sources or fields exist that the investigation never looked at | Reviewer |

**Limits.** `keep` is the default: the prompt says to regenerate only when a listed reason clearly applies. A workflow is regenerated at most `MAX_REGENERATIONS` (3) times per day; past that, the decision is stored with `acted_on = false` and shown on the project page.

**Contract.**

```go
type ReviewDecision struct {
    Decision       string `json:"decision" jsonschema:"enum=keep,enum=regenerate"`
    ReasonCategory string `json:"reason_category" jsonschema:"enum=none,enum=schema_drift,enum=stale_series,enum=coverage,enum=lag_boundary,enum=effect_faded,enum=explanation_mismatch,enum=new_scope"`
    Reason         string `json:"reason"` // one line, shown in the workflow history
}
```

`Validate()` rejects `regenerate` with category `none`, and categories that don't belong to the workflow's kind; a rejected decision is stored as `keep`. Model: `claude-sonnet-5-5`, one call per review.

**Acceptance checks.**

- Adding a source to a project regenerates its statistical spec with no LLM review call.
- A replay in which a claimed value changes sign regenerates the investigation with no LLM review call.
- A stub reviewer returning `regenerate` with category `none` is stored as `keep`.
- A fourth regeneration in a day is recorded with `acted_on = false` and not executed.
- The spec reviewer's logged prompt contains no p-values or findings.

## Narration and charts

The LLM writes card text and picks a chart type; deterministic code builds every chart and fills in a template when the LLM call fails.

**Narrator (`internal/narration/narrator.go`).** One `claude-sonnet-5-5` call per run covers the new or sign-changed statistical findings to narrate. Input: the goal, the workflow's `insight_prompt`, and per finding its kind, stats, and each series' label, unit and source summary. No raw records.

```go
type InsightDraft struct {
    FindingKey  string   `json:"finding_key"`
    Headline    string   `json:"headline"`    // one line, plain words, no digits
    Explanation string   `json:"explanation"` // two or three sentences, no digits
    Direction   string   `json:"direction" jsonschema:"enum=positive,enum=negative,enum=up,enum=down"`
    Leader      *string  `json:"leader"`      // series id that moves first; lagged correlations only
    Caveats     []string `json:"caveats"`
    Chart       string   `json:"chart" jsonschema:"enum=dual_line,enum=lag_scatter,enum=anomaly_band"`
}

type Narration struct {
    Insights []InsightDraft `json:"insights"`
}
```

The prompt asks for plain words with no digits and no causal wording. Nothing enforces this, but every number on a card is rendered from `stats`, so wrong text can't change a figure. If the narrator call fails after one retry, use a template such as "{a} and {b} move together, with {leader} moving first" and set `narrated_by = 'template'`.

**Charts (`internal/charts`).** This is the "AI Visualiser" box from the sketch, made deterministic. Each builder turns worker output (a statistical finding's `chart_data`, or an investigation step's output) into Vega-Lite JSON with the data inlined, at most 500 points.

| Chart | Used for | Shows |
| --- | --- | --- |
| `dual_line` | Correlations, from either workflow | Both series as z-scores over time, b shifted by the lag |
| `lag_scatter` | Correlations, from either workflow | Differenced aₜ against bₜ₊ₖ, with a regression line |
| `anomaly_band` | Statistical anomalies | The series, a band of rolling median ± 3.5 scaled MAD, flagged points highlighted |
| `series_line` | Investigation observations | One series from a `query_series` step |

**Card contents.** Headline, explanation, a stats line built from `stats` ("ρ 0.62 at lag +1 day · n 180 · q 0.03 · holds in both halves"), caveats, the chart, and a "template" badge when `narrated_by = 'template'`.

Investigation cards also carry an "exploratory" label, link each number to the step it came from, and show which workflow version produced them.

**Acceptance checks.**

- With the LLM stubbed to fail, every finding still appears, with template text.
- A finding whose ρ changes sign between runs is re-narrated.
- Every chart builder's output compiles with the frontend's `vega-lite` version (a Vitest test over fixture specs).

## API and dashboard

The API is a dozen REST endpoints plus one SSE stream; the dashboard refetches on events instead of receiving data over the stream, so the database stays the only source of truth.

| Method | Path | Does |
| --- | --- | --- |
| POST | `/api/sources` | Create an `rss` source; fetch once, then profile |
| POST | `/api/sources/csv` | Multipart upload (name, file); profile, then ingest |
| GET | `/api/sources` | List: kind, record count, `last_polled_at`, `last_error`, profile summary |
| GET | `/api/sources/{id}` | Detail: full profile and the 20 newest records |
| POST | `/api/projects` | Body: name, goal, `source_ids`; generates both workflows and triggers their runs |
| GET | `/api/projects` | List with each workflow's latest run status and finding counts |
| GET | `/api/projects/{id}` | Project, both active workflows (spec or report, rationale), latest run of each with its funnel |
| GET | `/api/projects/{id}/insights` | Rows from `latest_insights`, both kinds |
| GET | `/api/projects/{id}/workflows` | Every version of both kinds, with its reviews |
| GET | `/api/workflows/{id}/steps` | An investigation version's recorded steps |
| POST | `/api/projects/{id}/workflows/{kind}/regenerate` | Manual regeneration, counted against the daily cap |
| GET | `/api/stream` | SSE: `project_updated`, `source_updated`, `workflow_regenerated`, `investigation_step`, `run_failed` |
| POST | `/api/replay` | Demo replay (below) |

**Replay (`internal/api/replay.go`).** Body: `source_ids`, `periods_per_second`, `start_fraction` (default 0.5). It loads the first part of each demo dataset at once, then inserts the remaining rows in time order across sources at the given speed, publishing `source_updated` as it goes. It writes straight to `data_records`, bypassing adapters.

**Pages (React, Vite, TypeScript).**

- **Sources:** list with a status dot from `last_error`, record count and field roles. Add form with two tabs: CSV upload; RSS URL with poll interval.
- **New project:** name, goal text, source checkboxes. Submitting opens the project page, where the investigator's steps stream in while both workflows generate.
- **Project:** the goal; run status per workflow; the funnel line ("tested 330 · 41 under p 0.05 · 3 passed FDR · 2 held in both halves"); statistical cards, then exploratory cards; a "Workflows" panel with each kind's active version, rationale and spec or steps, and its history of versions and review decisions with reasons; today's regeneration count against the cap; a regenerate button per kind.

**Live updates.** One `EventSource('/api/stream')` in a top-level hook. On `project_updated` or `workflow_regenerated` for the open project, invalidate the TanStack Query keys `['project', id]`, `['insights', id]` and `['workflows', id]`. `investigation_step` events append to the live step view. Show "reconnecting" while the stream is down. Charts render with `vega-embed` inside `FindingCard`, with `actions: false`.

**Acceptance checks.**

- During a replay, the project page updates within 2 s of a run finishing, with no reload.
- A regeneration shows the new version and its review reason without a reload.
- Stopping the backend shows "reconnecting"; restarting it resumes updates.

## Work plan

Four workstreams build in parallel against the contracts fixed in the first two hours; the statistical workflow runs end to end by hour 13, investigations and the reviewer by hour 19, and features freeze at hour 22.

&#91;embedded content: 24-hour build · six phases, statistical end to end at hour 13\]

M0 fixes the contracts so the four workstreams can build in parallel through M1. The statistical workflow works end to end first, so the demo has a fallback if investigations slip.

| Workstream | Language | Owns | Starts with | Needs from others |
| --- | --- | --- | --- | --- |
| A. Data and API | Go | Migrations, `internal/db`, CSV and RSS adapters, ingest loop, profiler, HTTP handlers, SSE, demo replay, seed command, charts | Compose file and migration | Nothing |
| B. Stats worker | Python | `stats/`: spec mirror, frames, methods, health block, `run_spec`, `/tools/*`, `/replay`, tests | Synthetic-data tests, no database | The spec, result and step contracts |
| C. Agents | Go | `internal/llm`, author, investigator, reviewer, narrator | Author on fixture profiles | Contracts; B's endpoints |
| D. Frontend | TypeScript | Everything in `web/`, including workflow history and the live step view | Pages against the API stubs' fixture JSON | API response shapes |

With three people, drop the Sources page (load sources with `cmd/seed`), and A takes the frontend once the M1 data gate passes.

**Milestone gates.**

- [ ] **M0, hours 0–2.** `docker compose up` starts Postgres, the Go service and the stats worker; migrations apply.
- [ ] **M0.** `internal/contracts` defines `SourceProfile`, `AnalysisSpec`, `AnalysisResult`, `WorkflowDraft`, `Narration`, `InvestigationReport`, `ReplayResult` and `ReviewDecision`; Go and the worker both round-trip the fixtures in `contracts/fixtures/`.
- [ ] **M0.** Every endpoint returns fixture JSON, and the frontend renders a fixture project page.
- [ ] **M0.** The demo dataset is chosen and downloaded.
- [ ] **M1, hours 2–8.** CSV and RSS adapters ingest with deduplication; the Sources page lists real sources.
- [ ] **M1.** The engine passes every acceptance test on synthetic data, and the worker serves the three tools and `/replay`.
- [ ] **M1.** The profiler labels the demo dataset's fields correctly.
- [ ] **M1.** The engine, run by hand on the demo dataset, yields at least one stable finding. If not, change the dataset now.
- [ ] **M2, hours 8–13.** Creating a project generates the statistical workflow and shows cards with charts, updating live over SSE.
- [ ] **M3, hours 13–19.** The investigation workflow generates and replays on new data, and the reviewer keeps or regenerates both kinds with reasons shown.
- [ ] **M4, hours 19–22.** Demo replay mode, error states, cached narration and polish are done. Feature freeze at hour 22.
- [ ] **Hours 22–24.** Rehearse the demo twice and record a backup video.

## Rules for the coding agent

1. The migrations, `internal/contracts` and the worker's mirror models are frozen after M0. Propose changes to them and wait for a human.
2. Never execute model output or splice it into code or SQL. An LLM only ever produces JSON that is unmarshalled into a contract struct and checked by `Validate()`.
3. Every LLM call goes through `internal/llm`: structured outputs with the contract struct as the schema, a 60 s context timeout, one retry, and a JSONL log line with purpose, model, latency, tokens and validation result.
4. Model ids live in `internal/config`: profiler `claude-haiku-5-5`; author, investigator, reviewer and narrator `claude-sonnet-5-5`.
5. Agents read data only through the worker's tools. The worker connects with a read-only Postgres role and makes no other network calls.
6. Regeneration limits (`REVIEW_MIN_INTERVAL`, `MAX_REGENERATIONS`) and the reviewer's reason lists live in code and config, not in prompts.
7. The spec reviewer's input never includes p-values, findings or the funnel.
8. Every goroutine loop recovers panics per item, logs with `slog` and carries on.
9. Tests never call the live network or the real LLM. Go tests use recorded fixtures and `httptest`, plus a stub client enabled by `LLM_STUB=1`; worker tests use synthetic data.
10. Secrets (`ANTHROPIC_API_KEY`, `DATABASE_URL`) come from `.env` and are never committed.
11. A milestone is done when `make test` (`go test ./...` and `pytest`) passes and its gate items are ticked.

## Demo plan

The demo shows two agents planning once, the worker re-running their work live, and the reviewer deciding when to regenerate.

1. Add a CSV and an RSS source live.
2. Create a project with a one-sentence goal. Watch the investigator's steps stream in, then open "Workflows" to show the spec, the investigation and their rationales.
3. Start a replay: statistical cards update on every run, and exploratory cards refresh their numbers from replayed steps.
4. Add a source to the project mid-replay: the statistical workflow regenerates, and its history shows why.
5. Point at the funnel line, then at one strong-looking candidate the engine rejected.

**Dataset.** Choose a pair with a known, explainable lagged relationship and at least a year of daily data, for example a city's daily weather and its open bike-hire counts, plus a news RSS feed as the text source. Check each dataset's licence. Decide by hour 2, because the M1 gate depends on it.

**Before presenting.** Pre-generate both workflows, run the full replay once so narrations are cached by `finding_key` and direction, and keep the backup video ready.

## Risks

| Risk | Mitigation |
| --- | --- |
| The demo dataset has no stable finding | Check by hand at M1; swap datasets before hour 8 |
| Investigations slip past hour 19 | The statistical workflow works alone from M2; demo without investigations if needed |
| LLM latency or rate limits on stage | Pre-generated workflows, cached narration, template fallback; replay runs make no LLM calls |
| Regenerating specs until something passes (p-hacking) | The spec reviewer never sees results; reasons from a fixed list; 3 regenerations per day |
| The reviewer regenerates too often, so cards churn | `keep` by default, 60 s minimum interval, daily cap, every decision logged |
| The investigator runs long or loops | 12 iterations, 5 s tool timeout, one generation per project at a time |
| Exploratory findings read as proven | Exploratory label and every number linked to its step; causal wording is discouraged in the prompt only |
| Optimistic p-values from autocorrelation | Differencing and the half-window check; permutation test as stretch |
| Live feeds don't change during the demo slot | Replay mode |
| Card text contradicts its numbers, e.g. says "move together" when ρ is negative | Re-narrate on sign changes; figures always render from the worker; the verifier is the first stretch item to add back |

## Open questions

- Which demo dataset? Decide by hour 2.
- Three people or four, and who takes which workstream?
- Demo hosting: a laptop running Docker, or a cloud VM?

## Sources

- [Free Twitter API (X API) in 2026: What Is Free and What Is Not](https://apiserpent.com/blog/free-twitter-x-api), Serpent API, read 2026-11-07
- [Structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs), Claude Platform docs
- [Tool runner (SDK)](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-runner), Claude Platform docs, for the investigator loop
