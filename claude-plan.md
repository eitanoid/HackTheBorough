# Live Insights Platform: Implementation Plan

Nov 7, 2026 · @Tymur

## Overview

In 24 hours, 3–4 people build a service where each project has two agent-generated workflows, one statistical and one investigative. Both re-run deterministically whenever data lands, and a reviewer agent decides when either needs regenerating.

**MVP (must ship):**

- Two source types: CSV upload and RSS/Atom feed.
- Projects: a goal in plain language plus a set of sources.
- A statistical workflow per project: an agent-written Python script, run by the worker against a tested stats library that applies false-discovery control to every test the script registers (lagged correlation, anomalies, custom tests).
- An investigation workflow per project: an agent explores the data through worker tools, including unsandboxed Python, to answer the goal; its tool calls are recorded and replayed on new data.
- A reviewer agent that, after each run, decides whether the analysis script still suits the data and whether investigation findings still hold, and triggers regeneration when they don't.
- A React dashboard with statistical and exploratory finding cards, Vega-Lite charts, workflow history with review reasons, and live updates over SSE.
- A replay mode that streams historical data in fast, for the demo.

**Stretch (only after milestone M3 passes):** an `http_json` source adapter, the `trend_change` test, ad-hoc user questions, investigations started automatically by anomalies, a permutation test for surviving findings, a deterministic verifier for card text.

**Non-goals:** authentication, multi-tenancy, web scraping, X/Twitter, running model-written code outside the worker's script runner and run\_python tool, production deployment.

**For the coding agent:** build the milestones in the order given in the work plan. Each milestone ends with acceptance checks; do not start the next one until they pass. The SQL migrations, the Go structs in `internal/contracts/` and the stats worker's matching Pydantic models are shared by every workstream: propose changes to them, don't make them unilaterally.

## Design decisions

The sketch's shape stays: ingestor, data store, project store, workflow manager, analysis, reporter. Eight changes make it safe to run and statistically honest.

| # | Area | Sketch | Plan | Why |
| --- | --- | --- | --- | --- |
| 1 | Statistical analysis | LLM-written "Statistics Code" | A Python script written by the author and run by the worker; every test goes through a library ledger, and the harness applies correction across all of them | Scripts can express anything pandas can, but can't skip correction; the same corrections apply on every run; every number comes from the worker |
| 2 | Workflows per project | One Workflows table with statistics code and an AI insights prompt | One statistical and one investigation workflow per project, each a versioned artifact re-run deterministically on new data | Re-runs are cheap and comparable; an LLM is called to regenerate only when the reviewer says so |
| 3 | Regeneration | — | Only for reasons from a fixed list, capped per day; the script reviewer never sees results | Regenerating a script until something passes would be p-hacking |
| 4 | Time | One `CreatedAt`, set at ingestion | `observed_at` (event time) plus `ingested_at` | Correlations must run on when things happened, not when we fetched them |
| 5 | Duplicates | None | Unique `(data_source_id, external_id)`; insert with `ON CONFLICT DO NOTHING` | Polling re-fetches the same items, so counts would inflate on every poll |
| 6 | Results | JSON columns on Project and an Insights table | Insights only, written by runs; a `reviews` table; a `latest_insights` view per workflow kind | One source of truth, and provenance for every claim |
| 7 | Polling | On the source and on the project | On the source only; a project's workflows re-run when its sources' newest `observed_at` passes their last run | Two projects sharing a source can't disagree |
| 8 | Project ↔ sources | Array of UUIDs | `project_sources` join table | Foreign keys and cascades work |
| 9 | Data schema | JSON schema | `SourceProfile`: each field's role, unit and aggregation | The engine must know which field is a timestamp, a measure or text |
| 10 | Example source | Twitter | RSS and CSV, JSON APIs as stretch | X's API charges $0.005 per post read and has no free read tier ([source](https://apiserpent.com/blog/free-twitter-x-api)) |
| 11 | Model-written code | — | Author scripts and the investigator's run\_python, executed unsandboxed in worker child processes and re-run on new data | Lets the agent explore beyond fixed tools while staying replayable; an accepted risk for the hackathon |

**Numbers rule.** Every number shown on the dashboard comes from the stats worker: a statistical run's `AnalysisResult`, or the replayed output of an investigation step. The LLM decides what to analyse and explains what was found; it never computes or restates figures.

**Languages.** The main service is Go: ingestion, API, SSE, the workflow manager, every LLM call and chart building. The stats engine is a Python worker (FastAPI, pandas, scipy, statsmodels) that runs the author's scripts against its stats library: a script in, `AnalysisResult` out. Python gives tested Spearman p-values with ties, Benjamini–Hochberg, Welch tests and resampling, and the language boundary falls on a team boundary (workstream B).

## Architecture

One Go binary runs the API, the background loops and every LLM call; a Python worker runs the statistics and serves the investigator's tools; Postgres is the only store; the React app talks to the Go service over REST and one SSE stream.

&#91;embedded content: architecture · agents in Go, statistics in a Python worker, one Postgres database\]

Ingestion and the API sit above the database. Below it, the agents write workflow artifacts and card text, and the worker re-runs those artifacts on every update; only the tinted boxes call an LLM.

**Process model.** `cmd/server` starts the HTTP server and two goroutines: the ingest loop and the run loop, which runs both workflow kinds. Generating a workflow (author or investigator) happens in its own goroutine, so a slow agent never delays runs. An in-process pub/sub in `internal/events` (a mutex-guarded set of subscriber channels) carries `source_updated`, `project_updated`, `workflow_regenerated`, `investigation_step` and `run_failed` between the loops and the SSE handler. The run loop and the investigator call the stats worker over HTTP (`http://stats:8001`). The stack runs locally in a kind cluster under Skaffold, deployed by the Helm chart. The Next.js frontend calls the Go service through the Connect-RPC client generated from `proto/`.

**Libraries.**

- Go service: standard `net/http` with method and path patterns in `ServeMux`, `log/slog`, `connectrpc.com/connect/v2` with protobuf stubs generated by `buf`, `github.com/jackc/pgx/v5` with `pgxpool`, `github.com/mmcdole/gofeed`, `github.com/google/uuid`. LLM calls use the provider-neutral client in `internal/llm` (OpenAI-compatible chat completions; a thin `Provider` interface is the only provider-specific seam).
- Stats worker: Python 3.12, `fastapi`, `uvicorn`, `psycopg[binary]` 3, `pydantic` 2, `pandas`, `numpy`, `scipy`, `statsmodels`, `pytest`.
- Frontend: Next.js (App Router), React, TypeScript, pnpm, `vega-embed`, TanStack Query. Types come from the `buf`-generated TypeScript stubs.
- Plain SQL through pgx, no ORM. Migrations are SQL files in `internal/db/migrations/`, embedded with `go:embed`, applied in order at startup and tracked in a `schema_migrations` table.

**Repo layout.**

```
proto/hacktheburgh/v1/service.proto  # AnduinService: the API contract (buf generates Go and TS)
buf.yaml, buf.gen.yaml
skaffold.yaml, chart/                # Helm chart: frontend, backend, postgres (stats to add)
dev/                                 # kind cluster up/down scripts
svc/backend/                         # Go module `hacktheburgh/backend`
  cmd/server/main.go         # wires config, db, loops, HTTP
  cmd/seed/main.go           # loads demo sources
  gen/                       # buf output: protobuf and Connect stubs
  internal/
    config/                  # env vars, model ids, thresholds, regeneration limits
    db/                      # pgxpool, migration runner
      migrations/*.sql         # numbered, embedded with go:embed
    events/                  # in-process pub/sub
    contracts/               # SourceProfile, SeriesDef, AnalysisResult, WorkflowDraft,
                             #   Narration, InvestigationReport, ReviewDecision (frozen after M0)
    llm/                     # provider-neutral client (OpenAI-compatible), JSONL call log
    ingest/
      adapter.go             # SourceAdapter interface, RawRecord
      csv.go
      rss.go
      loop.go                # polling scheduler
    profiling/               # pre-pass + LLM labelling
    statsclient/             # HTTP client for the stats worker
    workflows/
      author.go              # statistical: goal + profiles -> WorkflowDraft (a script)
      runner.go              # watermark -> worker run or replay -> insights -> review
    investigate/             # investigation: tool loop -> recorded steps + report
    review/                  # reviewer: hard rules, then LLM keep or regenerate
    narration/
      narrator.go
    charts/                  # Vega-Lite builders from worker output
    api/                     # handlers, SSE, demo replay
  tests/fixtures/
svc/frontend/                # Next.js app
  app/                       # routes: sources, new project, project
  gen/                       # buf output: TypeScript stubs
stats/                       # Python worker (not yet created)
  pyproject.toml
  stats/
    main.py                  # FastAPI: /run, /tools/*, /replay, /healthz
    contracts.py             # Pydantic mirror of the Go contracts
    lib.py                   # load_series, derive, test_pairs, anomalies, register_test
    harness.py               # runs scripts, owns the ledger, applies FDR and stability
    health.py                # health block from loaded series
    tools.py                 # describe_source, query_series, test_pair, run_python
  tests/
contracts/fixtures/          # golden JSON that Go and Python both round-trip
demo/datasets/
Makefile                     # cluster, skaffold and generate targets today; add test and demo
```

## Data model

Nine tables in one Postgres database: sources and their records on the ingest side; projects, workflows (with an investigation's recorded steps), runs, reviews and insights on the analysis side. This is the schema after `001_init.sql` and `002_script_workflows.sql`; the SQL below is the combined result. Applied migrations are never edited: schema changes are new numbered files.

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
  analysis_script text,                  -- Python using the stats library; statistical only
  report         jsonb,                  -- InvestigationReport; investigation only
  insight_prompt text NOT NULL,          -- narrator guidance or investigator brief
  rationale      text NOT NULL,          -- why this plan; shown in the UI
  is_active      boolean NOT NULL DEFAULT true,
  created_at     timestamptz NOT NULL DEFAULT now(),
  UNIQUE (project_id, kind, version),
  CHECK ((kind = 'statistical') = (analysis_script IS NOT NULL)),
  CHECK ((kind = 'investigation') = (report IS NOT NULL))
);
CREATE UNIQUE INDEX one_active_workflow ON workflows (project_id, kind) WHERE is_active;

CREATE TABLE workflow_steps (            -- an investigation's recorded tool calls
  workflow_id uuid NOT NULL REFERENCES workflows(id) ON DELETE CASCADE,
  seq         integer NOT NULL,
  tool        text NOT NULL CHECK (tool IN ('describe_source', 'query_series', 'test_pair', 'run_python')),
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
  kind        text NOT NULL CHECK (kind IN ('correlation', 'anomaly', 'custom', 'observation')),
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
2. Call the profiler model (`LLM_MODEL_PROFILER`, `gpt-4.1-mini`) through `llm.Generate[SourceProfile]`, which derives a JSON schema from the struct with `llm.SchemaFor` and unmarshals the reply into it. Send the pre-pass plus 20 sample records.
3. Check the output against the pre-pass: a `measure` that isn't numeric becomes `category`; a `timestamp` that doesn't parse becomes `ignore`; any `path` not in the data is dropped.
4. Save to `data_sources.profile`.

Print each contract's generated schema once in a test and check it: every field required, pointer fields nullable, enums present.

**Order for CSV.** Profile the uploaded file before writing any rows, because `observed_at` comes from `timestamp_path`. RSS and JSON sources are profiled after their first fetch.

**Text fields.** A `text` field (an RSS title or summary) is never correlated directly. The analysis script turns it into counts per period, either all records or those matching keywords (next section).

**Re-profiling.** When a run's health block reports payload keys missing from a source's profile, the runner re-profiles that source before the reviewer decides on the statistical script.

**Acceptance check.** A fixture with a date column, two numeric columns and a free-text column is profiled with the right roles, and a deliberately mislabelled output from a stub LLM is corrected by step 3.

## Analysis script and stats library

The author writes the statistical workflow as a Python script. The worker runs it against a tested stats library, and the harness, not the script, decides which results become findings, so false-discovery correction can't be skipped.

**Series definitions (`internal/contracts/series.go`).** Shared by scripts and the investigator's tools, and mirrored in `stats/contracts.py`.

```go
type Filter struct {
    Path   string `json:"path"`
    Equals string `json:"equals"`
}

type SeriesDef struct {
    ID       string   `json:"id"`        // snake_case, unique within a script
    Label    string   `json:"label"`     // human name for cards and charts
    SourceID string   `json:"source_id"` // UUID
    Field    *string  `json:"field"`     // measure path; null = count records
    Agg      string   `json:"agg"`       // mean | sum | last | count
    Filters  []Filter `json:"filters"`
    Keywords []string `json:"keywords"`  // text: count records containing any keyword
    Unit     *string  `json:"unit"`
}
```

**A script.** The prelude provides `lib`, the project's sources as `SOURCES` (name to id), and `numpy`, `pandas` and `scipy`. The script loads series, derives any it needs and registers tests. Its return value and printed output are ignored; the harness reads the ledger.

```python
trips = lib.load_series({'id': 'bike_trips', 'label': 'Bike trips', 'source_id': SOURCES['bikes'],
                         'field': 'trips', 'agg': 'sum'}, grain='day', window_days=180)
strike = lib.load_series({'id': 'strike_news', 'label': 'Strike news', 'source_id': SOURCES['news'],
                          'field': None, 'agg': 'count', 'keywords': ['strike', 'walkout']},
                         grain='day', window_days=180)

# Something the old spec format couldn't express: weekdays only
weekday_trips = lib.derive('weekday_trips', trips[trips.index.dayofweek < 5],
                           label='Weekday bike trips')

lib.test_pairs([weekday_trips, strike], max_lag=3)
lib.anomalies([trips, strike])

# A custom test, corrected together with everything else
on_strike = strike > 0
p = scipy.stats.mannwhitneyu(trips[on_strike], trips[~on_strike]).pvalue
lib.register_test('trips_on_strike_days', p, series=[trips, strike],
                  stats={'median_on': float(trips[on_strike].median()),
                         'median_off': float(trips[~on_strike].median())})
```

**Library (`stats/lib.py`).**

| Function | Does | Guarantees |
| --- | --- | --- |
| `load_series(def, grain, window_days)` | Reads the source's records in the window ending at the run's watermark, applies filters and keywords, resamples | Counts fill empty periods with 0; measures keep gaps and are never forward-filled; sources outside the project are rejected; every loaded series is recorded for the health block |
| `derive(id, series, label)` | Registers a series the script computed itself | It can be tested and charted like a loaded series |
| `test_pairs(series, max_lag, pairs=None)` | Spearman ρ for every pair at every lag from −`max_lag` to +`max_lag`, on differenced series | Every (pair, lag) enters the ledger; survivors get the half-window check |
| `anomalies(series)` | Robust z over a 28-period trailing window, for the last 3 periods | Flags only \|z\| > 3.5; not part of the false-discovery family |
| `register_test(key, p, series, stats)` | Adds a test the library doesn't provide | Enters the same Benjamini–Hochberg family as the correlations; marked as not stability-checked |

**Harness (`stats/harness.py`), for each `POST /run` with `{script, project_id, data_up_to}`.**

1. Run the script in a child Python process with the prelude, a 60 s timeout and a 1 GB memory limit. It is not sandboxed (see Risks).
2. If the script raises or times out, the run fails and the traceback is returned.
3. Apply Benjamini–Hochberg across every correlation and custom test in the ledger, keeping q ≤ `FDR_Q`. Per pair, keep the surviving lag with the largest |ρ|, then drop correlations that fail the half-window check (both halves the same sign, |ρ| ≥ `MIN_HALF_RHO`).
4. Build findings, the funnel, rejected candidates, chart data and the health block from the ledger and the loaded series. Nothing the script prints or returns becomes a finding.

`MIN_OVERLAP`, `FDR_Q` and `MIN_HALF_RHO` live in the worker's config, and no library function takes them as arguments, so a script can't loosen them.

**Determinism.** The same rules as `run_python`: data only through `load_series`, fixed seeds for any randomness, and no reading the current time. The same script on the same data must give byte-identical results.

**Worker API.** `POST /run` takes `{script, project_id, data_up_to}` and returns `AnalysisResult`. `POST /tools/describe_source`, `/tools/query_series`, `/tools/test_pair` and `/tools/run_python` serve the investigator (see Investigation workflow). `POST /replay` takes a workflow's recorded steps and a new `data_up_to`, re-executes each step and returns a `ReplayResult` with one output per step. The worker reads `data_records` itself through a read-only Postgres role, so records never travel over HTTP. `GET /healthz` serves health checks. The Go client in `internal/statsclient` uses a 90 s timeout for runs and 35 s for tool calls, and treats any non-200 response as a failure.

**Result** (Pydantic in the worker, mirrored by `internal/contracts/result.go`).

```python
class Health(BaseModel):
    loaded_series: dict[str, str]          # series id -> source_id, from load_series calls
    skipped_series: list[str]              # too few points to analyse
    stale_series: list[str]                # no records in the last 3 periods
    coverage: dict[str, float]             # share of periods with data, per series
    lags_at_boundary: list[str]            # pairs whose strongest raw lag equals max_lag
    unprofiled_keys: dict[str, list[str]]  # source_id -> payload keys missing from its profile
    span_periods: int                      # periods of data available, for grain and window checks

class Finding(BaseModel):
    finding_key: str   # 'corr:{a}:{b}:lag{+k}', 'anom:{s}:{period}', 'custom:{key}'
    kind: Literal['correlation', 'anomaly', 'custom']
    series: list[str]
    stats: dict[str, float | int | str]  # rho, lag, n, p, q, rho_h1, rho_h2 | z, value, expected | ...
    stability_checked: bool              # false for custom tests
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

**Known limitation.** Autocorrelation left after differencing still makes p-values optimistic. The half-window check is the practical guard, and custom tests don't get it, so their cards say "not stability-checked". Stretch: a circular-shift permutation test for survivors.

**Acceptance checks (`stats/tests/`, synthetic data, no database).**

- A script testing six independent random walks of 200 days, over 50 seeds: at most 5 seeds produce any correlation finding.
- b = a shifted forward 2 days plus noise: exactly one finding, lag +2, positive ρ.
- Two series sharing a linear trend with independent noise: no finding.
- A spike of 6 MADs in the last period: one anomaly finding.
- A script that computes its own correlation and prints or returns it produces no finding; only ledger entries count.
- A custom test with p = 0.04, registered alongside 40 null tests, does not survive correction.
- A series whose last 3 periods are empty appears in `health.stale_series`.
- The same script and data give byte-identical `AnalysisResult` JSON.

## Investigation workflow

The investigator explores the data once to answer the project goal; its tool calls and report are saved as the workflow, and every later run replays those calls deterministically instead of calling the agent again.

**Tools (served by the worker).** Every call carries the project id and the investigation's fixed `data_up_to`; the worker rejects sources outside the project.

| Tool | Input | Returns |
| --- | --- | --- |
| `describe_source` | `source_id` | Fields with roles and units, record count, first and last `observed_at` |
| `query_series` | A `SeriesDef`, `grain`, `window_days` | Up to 200 periods of the resampled series, plus mean, min and max |
| `test_pair` | Two `SeriesDef`s, `grain`, `window_days`, `max_lag` | Spearman ρ, n and p for each lag on the differenced series, the best lag, and the aligned series (up to 200 periods) |
| `run_python` | `code`: Python source | The `result` dict the code builds, plus up to 4 KB of stdout, or the traceback if it fails |

**`run_python`.** The worker writes the code to a temporary file and runs it in a child Python process with the worker's packages (pandas, numpy, scipy, statsmodels), a 30 s timeout and a 1 GB memory limit set with `resource.setrlimit`. It is not sandboxed: the code can reach the worker container's filesystem and network, which is an accepted hackathon risk (see Risks).

- **Data access.** A prelude defines `load_series(series_def, grain, window_days)`, which returns a pandas Series built with the worker's resampling code at the step's watermark and rejects sources outside the project.
- **Output.** The code must put its numbers in a dict named `result`, and claims cite paths into it, such as `result.rho`.
- **Determinism.** The prompt requires that data comes only from `load_series`, that any randomness uses a fixed seed, and that the code never reads the current time. On replay the same code runs again with the new watermark.
- **Charts.** Cards can't be charted from a `run_python` step; `Validate()` rejects a `chart_seq` that points at one.

**Generating a version (`internal/investigate`).**

1. Triggered when the project is created, or when the reviewer decides `regenerate` for the investigation workflow.
2. Run the investigator model (`LLM_MODEL_INVESTIGATOR`, `gpt-4.1`) through `llm.RunTools[InvestigationReport]` over the four tools, with at most `MaxInvestigatorIt` (12) iterations and `InvestigationReport` as the structured output. Input: the goal and the source profiles; for a regeneration, also the previous version's findings and the reviewer's reason, so it doesn't propose retired claims again.
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
- A replay at the original `data_up_to` reproduces every recorded output exactly, `run_python` steps included.
- A claim whose `path` doesn't exist in its step output is dropped by `Validate()`.
- A tool call naming a source outside the project is rejected by the worker, including through `load_series`.
- A `run_python` step that runs past 30 s is killed, and the agent sees the timeout as the tool result.

## Workflow manager

Each project has one active statistical workflow and one active investigation workflow; the manager generates a new version only when a project is created or the reviewer asks, and otherwise runs the active versions on every data update.

&#91;embedded content: workflow lifecycle · generate, run, review\]

Runs never call the generating agent; only a `regenerate` decision does, and the reviewer's hard rules can force one without an LLM call.

**Generating.**

| Kind | Generator | Artifact | When |
| --- | --- | --- | --- |
| Statistical | Author (`internal/workflows/author.go`) | Python script, insight prompt, rationale | Project created; reviewer decides `regenerate` |
| Investigation | Investigator (`internal/investigate`) | Recorded steps and report | Project created; reviewer decides `regenerate` |

**Authoring the statistical workflow.**

```go
type WorkflowDraft struct {
    Script        string `json:"script"`         // Python using the stats library
    InsightPrompt string `json:"insight_prompt"` // what the user cares about, for the narrator
    Rationale     string `json:"rationale"`      // why these series and tests; shown in the UI
}
```

1. Input: the goal; each source's `SourceProfile` with its record count and date range; the library's API reference and one example script. For a regeneration, also the previous script and the reviewer's reason.
2. Call the author model (`LLM_MODEL_AUTHOR`, `gpt-4.1`) through `llm.Generate[WorkflowDraft]`, with `WorkflowDraft.Validate()` as the validator.
3. Dry-run the script through the worker's `/run` at the current watermark. If it raises or loads no series, retry once with the traceback appended; if it fails again, store the error in `projects.last_error` and keep the previous version active.
4. In one transaction, insert the new version, deactivate the previous one, and set the triggering review's `next_workflow_id`. Publish `workflow_regenerated`; the dry run becomes the version's first run.

**Running (`internal/workflows/runner.go`).**

1. **Triggers:** `source_updated` events, debounced 30 s per project, plus a sweep every 5 min.
2. **Watermark:** newest `observed_at` across the project's sources. For each active workflow, run if it has no succeeded run yet or the watermark is newer than its last run's `data_up_to`.
3. **Lock:** at most one run per workflow at a time, using a mutex-guarded set (the Go service is a single process).
4. **Execute:** a statistical workflow calls the worker's `POST /run` with its script; an investigation workflow calls `POST /replay` with its recorded steps. Both pass the watermark.
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
- A script that raises on current data is retried once with its traceback, then reported on the project.

## Reviewer

After each run, the reviewer decides whether the workflow that produced it still fits the data. `keep` changes nothing; `regenerate` asks that workflow's generator for a new version. Every decision is stored in `reviews` with its reason.

**When it runs.** After a succeeded run, or a failed statistical run, at most once per workflow per `REVIEW_MIN_INTERVAL` (60 s). If a hard rule settles the decision, no LLM call is made and the review records `decided_by = 'rule'`.

**Statistical workflow: does the script still suit the data?** The reviewer sees the script, the run's health block and whether the project's sources changed. It never sees p-values, findings or the funnel, because regenerating a script until something passes would be p-hacking.

| Reason category | Signal | Decided by |
| --- | --- | --- |
| `sources_changed` | The project's sources differ from those the script loads | Rule: regenerate |
| `schema_drift` | `unprofiled_keys` is still non-empty after re-profiling | Reviewer |
| `stale_series` | A series has had no records for 3 periods | Reviewer |
| `coverage` | A series' coverage is under 50%, or `span_periods` now suits a finer grain or a longer window | Reviewer |
| `lag_boundary` | A pair's strongest raw lag sits at `max_lag` | Reviewer |
| `script_error` | The script raised on new data, for example on a field that disappeared | Rule: regenerate |

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

`Validate()` rejects `regenerate` with category `none`, and categories that don't belong to the workflow's kind; a rejected decision is stored as `keep`. Model: `LLM_MODEL_REVIEWER` (`gpt-4.1`), one call per review.

**Acceptance checks.**

- Adding a source to a project regenerates its statistical script with no LLM review call.
- A replay in which a claimed value changes sign regenerates the investigation with no LLM review call.
- A stub reviewer returning `regenerate` with category `none` is stored as `keep`.
- A fourth regeneration in a day is recorded with `acted_on = false` and not executed.
- The script reviewer's logged prompt contains no p-values or findings.

## Narration and charts

The LLM writes card text and picks a chart type; deterministic code builds every chart and fills in a template when the LLM call fails.

**Narrator (`internal/narration/narrator.go`).** One call to the narrator model (`LLM_MODEL_NARRATOR`, `gpt-4.1-mini`) per run covers the new or sign-changed statistical findings to narrate. Input: the goal, the workflow's `insight_prompt`, and per finding its kind, stats, and each series' label, unit and source summary. No raw records.

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
| `series_line` | Custom tests and investigation observations | One series from a `query_series` step |

**Card contents.** Headline, explanation, a stats line built from `stats` ("ρ 0.62 at lag +1 day · n 180 · q 0.03 · holds in both halves"), caveats, the chart, and a "template" badge when `narrated_by = 'template'`.

Investigation cards also carry an "exploratory" label, link each number to the step it came from, and show which workflow version produced them.

**Acceptance checks.**

- With the LLM stubbed to fail, every finding still appears, with template text.
- A finding whose ρ changes sign between runs is re-narrated.
- Every chart builder's output compiles with the frontend's `vega-lite` version (a test over fixture specs).

## API and dashboard

The API is a dozen RPCs plus one SSE stream. The RPCs are defined in `proto/hacktheburgh/v1/service.proto` (`AnduinService`) and served with Connect; the table below gives each RPC's HTTP route as registered in `internal/api`. The SSE stream and `/healthz` are plain HTTP. the dashboard refetches on events instead of receiving data over the stream, so the database stays the only source of truth.

| Method | Path | Does |
| --- | --- | --- |
| POST | `/api/sources` | Create an `rss` source; fetch once, then profile |
| POST | `/api/sources/csv` | Multipart upload (name, file); profile, then ingest |
| GET | `/api/sources` | List: kind, record count, `last_polled_at`, `last_error`, profile summary |
| GET | `/api/sources/{id}` | Detail: full profile and the 20 newest records |
| POST | `/api/projects` | Body: name, goal, `source_ids`; generates both workflows and triggers their runs |
| GET | `/api/projects` | List with each workflow's latest run status and finding counts |
| GET | `/api/projects/{id}` | Project, both active workflows (script or report, rationale), latest run of each with its funnel |
| GET | `/api/projects/{id}/insights` | Rows from `latest_insights`, both kinds |
| GET | `/api/projects/{id}/workflows` | Every version of both kinds, with its reviews |
| GET | `/api/workflows/{id}/steps` | An investigation version's recorded steps |
| POST | `/api/projects/{id}/workflows/{kind}/regenerate` | Manual regeneration, counted against the daily cap |
| GET | `/api/stream` | SSE: `project_updated`, `source_updated`, `workflow_regenerated`, `investigation_step`, `run_failed` |
| POST | `/api/replay` | Demo replay (below) |

**Replay (`internal/api/replay.go`).** Body: `source_ids`, `periods_per_second`, `start_fraction` (default 0.5). It loads the first part of each demo dataset at once, then inserts the remaining rows in time order across sources at the given speed, publishing `source_updated` as it goes. It writes straight to `data_records`, bypassing adapters.

**Pages (Next.js, React, TypeScript).**

- **Sources:** list with a status dot from `last_error`, record count and field roles. Add form with two tabs: CSV upload; RSS URL with poll interval.
- **New project:** name, goal text, source checkboxes. Submitting opens the project page, where the investigator's steps stream in while both workflows generate.
- **Project:** the goal; run status per workflow; the funnel line ("tested 330 · 41 under p 0.05 · 3 passed FDR · 2 held in both halves"); statistical cards, then exploratory cards; a "Workflows" panel with each kind's active version, rationale and script or steps, and its history of versions and review decisions with reasons; today's regeneration count against the cap; a regenerate button per kind.

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
| A. Data and API | Go | Migrations, `internal/db`, CSV and RSS adapters, ingest loop, profiler, HTTP handlers, SSE, demo replay, seed command, charts | Migration, with the stack running locally | Nothing |
| B. Stats worker | Python | `stats/`: contracts mirror, stats library, script harness and ledger, health block, `/tools/*` including the `run_python` executor, `/replay`, tests | Synthetic-data tests, no database | The series, result and step contracts |
| C. Agents | Go | `internal/llm`, author, investigator (including the `run_python` prompt rules), reviewer, narrator | Author on fixture profiles | Contracts; B's endpoints |
| D. Frontend | TypeScript | Everything in `svc/frontend/`, including workflow history and the live step view | Pages against the API stubs' fixture JSON | API response shapes |

With three people, drop the Sources page (load sources with `cmd/seed`), and A takes the frontend once the M1 data gate passes.

**Milestone gates.**

- [ ] **M0, hours 0–2.** Postgres, the Go service and the stats worker run locally under Skaffold, and migrations apply.
- [ ] **M0.** `internal/contracts` defines `SourceProfile`, `SeriesDef`, `AnalysisResult`, `WorkflowDraft`, `Narration`, `InvestigationReport`, `ReplayResult` and `ReviewDecision`; Go and the worker both round-trip the fixtures in `contracts/fixtures/`.
- [ ] **M0.** Every endpoint returns fixture JSON, and the frontend renders a fixture project page.
- [ ] **M0.** The demo dataset is chosen and downloaded.
- [ ] **M1, hours 2–8.** CSV and RSS adapters ingest with deduplication; the Sources page lists real sources.
- [ ] **M1.** The library and harness pass every acceptance test on synthetic data, and the worker serves the four tools and `/replay`.
- [ ] **M1.** The profiler labels the demo dataset's fields correctly.
- [ ] **M1.** A hand-written script on the demo dataset yields at least one stable finding. If not, change the dataset now.
- [ ] **M2, hours 8–13.** Creating a project generates the statistical workflow and shows cards with charts, updating live over SSE.
- [ ] **M3, hours 13–19.** The investigation workflow generates and replays on new data, and the reviewer keeps or regenerates both kinds with reasons shown.
- [ ] **M4, hours 19–22.** Demo replay mode, error states, cached narration and polish are done. Feature freeze at hour 22.
- [ ] **Hours 22–24.** Rehearse the demo twice and record a backup video.

## Rules for the coding agent

1. The migrations, `internal/contracts` and the worker's mirror models are frozen after M0. Propose changes to them and wait for a human.
2. Never execute model output or splice it into code or SQL, except model-written Python (author scripts and the investigator's run\_python code), which runs in worker child processes (hackathon only). An LLM only ever produces JSON that is unmarshalled into a contract struct and checked by `Validate()`.
3. Every LLM call goes through `internal/llm`: JSON-schema structured outputs derived from the contract struct, a 60 s context timeout, one retry, and a JSONL log line with purpose, model, latency, tokens and validation result.
4. Model ids are read by `internal/config` from `LLM_MODEL_*` env vars, which are set in `backend.env` in `chart/values.yaml` (profiler and narrator `gpt-4.1-mini`; author, investigator and reviewer `gpt-4.1`). Never hardcode them elsewhere.
5. Agents read data only through the worker's tools. The worker connects with a read-only Postgres role and holds no API keys or other secrets; model-written code reads data only through load\_series.
6. Regeneration limits (`REVIEW_MIN_INTERVAL`, `MAX_REGENERATIONS`) and the reviewer's reason lists live in code and config, not in prompts.
7. The script reviewer's input never includes p-values, findings or the funnel.
8. Every goroutine loop recovers panics per item, logs with `slog` and carries on.
9. Tests never call the live network or the real LLM. Go tests use recorded fixtures and `httptest`, plus a stub client enabled by `LLM_STUB=1`; worker tests use synthetic data.
10. Secrets (the LLM API key, delivered as `OPENAPI_KEY` from the `openai-key` Kubernetes secret, and `DATABASE_URL`) are never committed. The worker never receives the API key; it needs only the read-only database URL.
11. A milestone is done when `make test` (`go test ./...` and `pytest`) passes and its gate items are ticked.

## Demo plan

The demo shows two agents planning once, the worker re-running their work live, and the reviewer deciding when to regenerate.

1. Add a CSV and an RSS source live.
2. Create a project with a one-sentence goal. Watch the investigator's steps stream in, then open "Workflows" to show the script, the investigation and their rationales.
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
| Regenerating scripts until something passes (p-hacking) | The script reviewer never sees results; reasons from a fixed list; 3 regenerations per day |
| The reviewer regenerates too often, so cards churn | `keep` by default, 60 s minimum interval, daily cap, every decision logged |
| The investigator runs long or loops | 12 iterations, 5 s tool timeout, one generation per project at a time |
| Exploratory findings read as proven | Exploratory label and every number linked to its step; causal wording is discouraged in the prompt only |
| Optimistic p-values from autocorrelation | Differencing and the half-window check; permutation test as stretch |
| Live feeds don't change during the demo slot | Replay mode |
| Card text contradicts its numbers, e.g. says "move together" when ρ is negative | Re-narrate on sign changes; figures always render from the worker; the verifier is the first stretch item to add back |
| Model-written Python runs unsandboxed on the demo machine; RSS text could carry prompt injection that turns into code | Trusted feeds only; no secrets in the worker; read-only database role; 30 s timeout and 1 GB memory limit; demo not exposed beyond the laptop or venue network |
| A script runs cleanly but analyses the wrong thing (a wrong aggregation, a filter that leaks future data) | The library does loading, resampling and correction; the dry run catches errors; the script and rationale are visible in the Workflows panel |

## Open questions

- Which demo dataset? Decide by hour 2.
- Three people or four, and who takes which workstream?

## Sources

- [Free Twitter API (X API) in 2026: What Is Free and What Is Not](https://apiserpent.com/blog/free-twitter-x-api), Serpent API, read 2026-11-07
