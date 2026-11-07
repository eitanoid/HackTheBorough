# Live Insights Platform: Implementation Plan

Nov 7, 2026 · @Tymur

## Overview

In 24 hours, 3–4 people build a service where an agent writes each project's analysis plan once, a deterministic engine re-runs that plan whenever new data lands, and only verified findings reach a live dashboard.

**MVP (must ship):**

- Three source types: CSV upload, RSS/Atom feed, polled JSON HTTP endpoint.
- Projects: a goal in plain language plus a set of sources.
- An agent-authored workflow per project: an analysis spec plus an insight prompt.
- A deterministic stats engine that executes the spec: lagged correlation, anomalies and trend changes, with false-discovery control.
- Narration of findings, checked by a deterministic verifier before anything is shown, and reviewed by an agent as new data arrives.
- A React dashboard with finding cards, Vega-Lite charts and live updates over SSE.
- A replay mode that streams historical data in fast, for the demo.

**Stretch (only after milestone M3 passes):** Postgres source adapter, permutation test for surviving findings, user feedback on findings, an "ask a question" box.

**Non-goals:** authentication, multi-tenancy, web scraping, X/Twitter, running model-written code, production deployment.

**For the coding agent:** build the milestones in the order given in the work plan. Each milestone ends with acceptance checks; do not start the next one until they pass. The SQL migrations, the Go structs in `internal/contracts/` and the stats worker's matching Pydantic models are shared by every workstream: propose changes to them, don't make them unilaterally.

## Design decisions

The sketch's shape stays: ingestor, data store, project store, workflow manager, analysis, reporter. Eight changes make it safe to run and statistically honest.

| # | Area | Sketch | Plan | Why |
| --- | --- | --- | --- | --- |
| 1 | Per-project analysis | LLM-written "Statistics Code" | `AnalysisSpec` JSON, validated, executed by our engine | No model-written code runs on the server; the same corrections apply on every run; the verifier knows exactly what was computed |
| 2 | Time | One `CreatedAt`, set at ingestion | `observed_at` (event time) plus `ingested_at` | Correlations must run on when things happened, not when we fetched them |
| 3 | Duplicates | None | Unique `(data_source_id, external_id)`; insert with `ON CONFLICT DO NOTHING` | Polling re-fetches the same items, so counts would inflate on every poll |
| 4 | Results | JSON columns on Project and an Insights table | Insights table only, plus a `runs` table and a `latest_insights` view; each insight records its run and workflow | One source of truth, and provenance for every claim |
| 5 | Polling | On the source and on the project | On the source only; a project re-runs when its sources' newest `observed_at` passes its last run | Two projects sharing a source can't disagree |
| 6 | Project ↔ sources | Array of UUIDs | `project_sources` join table | Foreign keys and cascades work |
| 7 | Data schema | JSON schema | `SourceProfile`: each field's role, unit and aggregation | The engine must know which field is a timestamp, a measure or text |
| 8 | Example source | Twitter | RSS, JSON APIs, CSV | X's API charges $0.005 per post read and has no free read tier ([source](https://apiserpent.com/blog/free-twitter-x-api)) |

**Numbers rule.** Every number shown on the dashboard comes from the engine's `AnalysisResult`. The LLM decides what to analyse and explains what was found; it never computes or restates figures.

**Languages.** The main service is Go: ingestion, API, SSE, the workflow manager, every LLM call and chart building. The stats engine is a Python worker (FastAPI, pandas, scipy, statsmodels) behind one contract: `AnalysisSpec` in, `AnalysisResult` out. Python gives tested Spearman p-values with ties, Benjamini–Hochberg, Welch tests and resampling, and the language boundary falls on a team boundary (workstream B).

## Architecture

One Go binary runs the API, both background loops and every LLM call; a small Python worker runs the statistics; Postgres is the only store; the React app talks to the Go service over REST and one SSE stream.

&#91;embedded content: architecture · two loops around one Postgres database\]

The ingest loop and the API sit above the database; the run loop's five steps sit below it. Only the three tinted steps call an LLM, and only the stats engine runs outside the Go binary.

**Process model.** `cmd/server` starts the HTTP server and two goroutines: the ingest loop and the run loop. An in-process pub/sub in `internal/events` (a mutex-guarded set of subscriber channels) carries `source_updated`, `project_updated` and `run_failed` between the loops and the SSE handler. The run loop calls the stats worker over HTTP (`POST http://stats:8001/run`). Docker Compose runs Postgres 16, the Go service and the worker. In development, Vite proxies `/api` to the Go service.

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
  migrations/001_init.sql    # embedded with go:embed
  internal/
    config/                  # env vars, model ids, thresholds
    db/                      # pgxpool, migration runner, queries
    events/                  # in-process pub/sub
    contracts/               # SourceProfile, AnalysisSpec, AnalysisResult,
                             #   WorkflowDraft, Narration, Review (frozen after M0)
    llm/                     # Anthropic client wrapper + JSONL call log
    ingest/
      adapter.go             # SourceAdapter interface, RawRecord
      csv.go
      rss.go
      httpjson.go
      loop.go                # polling scheduler
    profiling/               # pre-pass + LLM labelling
    statsclient/             # HTTP client for the stats worker
    workflows/
      author.go              # LLM: goal + profiles -> WorkflowDraft
      runner.go              # watermark -> stats worker -> narrate -> verify -> store
    narration/
      narrator.go
      verifier.go
      reviewer.go
    charts/                  # Vega-Lite builders from chart_data
    api/                     # handlers, SSE, replay
  cmd/seed/main.go           # loads demo sources
stats/                       # Python worker
  pyproject.toml
  stats/
    main.py                  # FastAPI: POST /run, GET /healthz
    spec.py                  # Pydantic mirror of the Go contracts
    frames.py                # Postgres rows -> aligned DataFrames
    methods.py               # correlation, anomaly, trend tests, FDR
    engine.py                # run_spec()
  tests/
contracts/fixtures/          # golden JSON that Go and Python both round-trip
web/
  src/
    api.ts
    pages/Sources.tsx
    pages/NewProject.tsx
    pages/Project.tsx
    components/FindingCard.tsx
demo/datasets/
docker-compose.yml           # postgres, server, stats
Makefile                     # make dev, make test, make demo
```

## Data model

Seven tables in one Postgres database: sources and their records on the ingest side; projects, workflows, runs and insights on the analysis side. This is `migrations/001_init.sql`, frozen after M0.

```sql
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
```

- Each run writes a full set of insight rows. For findings whose text the reviewer keeps, the runner copies the text and narrated\_stats forward, with fresh stats and chart\_spec (see Narration).
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
| `http_json` | Item's `id_field`, or for snapshot APIs the poll time bucket (`ObservedAt` rounded down to the poll interval) | Item's `time_field`, or the bucket for snapshots | Config: `url`, `records_path` (dotted path to the array, empty for a single object), `id_field`, `time_field`; `net/http` client with a 10 s timeout |

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
    Tests      []string    `json:"tests"`       // lagged_correlation | anomaly | trend_change
}
```

Constrained decoding enforces types and enums but not numeric ranges or list lengths ([source](https://platform.claude.com/docs/en/build-with-claude/structured-outputs)), so `AnalysisSpec.Validate()` checks the bounds in the comments above. Engine constants (`MIN_OVERLAP = 20`, `FDR_Q = 0.10`, `MIN_HALF_RHO = 0.2`) live in the worker's config, not in the spec, so the agent can't loosen them.

**Semantic validation**, in the same `Validate()`: series ids unique; every `source_id` belongs to the project; `field` exists in that source's profile with role `measure`; `keywords` only on sources with a `text` field; every pair names defined series; every entry in `tests` is a known test.

**Worker API.** `POST /run` takes `{spec, project_id, data_up_to}` and returns `AnalysisResult`. The worker reads `data_records` itself through a read-only Postgres role, so records never travel over HTTP. `GET /healthz` serves Compose health checks. The Go client in `internal/statsclient` uses a 60 s timeout and treats any non-200 response as a failed run.

**Engine steps (`run_spec` in the worker).**

1. **Load.** Per series, read the source's records in the window that ends at the run's watermark (not the current time, so historical and replayed data work), apply filters and keywords in pandas, and resample to `grain` with `agg`. Count series fill empty periods with 0; measure series keep gaps as NaN and are never forward-filled.
2. **Align.** Outer-join all series on the period index. Drop any series with fewer than `MIN_OVERLAP` non-null periods and list it in `skipped`.
3. **Difference.** Take the first difference of every series. This removes shared trends, the main source of spurious correlation.
4. **Lagged correlation.** For each pair (a, b) and each lag k from −`max_lag` to +`max_lag`, compute Spearman ρ between aₜ and bₜ₊ₖ on periods where both exist; skip if n < `MIN_OVERLAP`. Convention: k > 0 means a leads b by k periods. Every (pair, lag) is one test.
5. **False discovery control.** Benjamini–Hochberg across all correlation tests (`statsmodels` `multipletests(method="fdr_bh")`), keeping q ≤ `FDR_Q`. Per pair, keep only the surviving lag with the largest |ρ|.
6. **Stability.** Recompute ρ at the chosen lag on the first and second half of the window separately. Keep the finding only if both halves agree in sign with |ρ| ≥ `MIN_HALF_RHO`.
7. **Anomalies** (undifferenced): robust z = (x − rolling median) / (1.4826 × rolling MAD) over a 28-period trailing window. Flag the last 3 periods where |z| > 3.5.
8. **Trend change:** Welch t-test of the last 7 periods against the 21 before, with Benjamini–Hochberg within this family.

**Result** (Pydantic in the worker, mirrored by `internal/contracts/result.go`).

```python
class Finding(BaseModel):
    finding_key: str   # 'corr:{a}:{b}:lag{+k}', 'anom:{s}:{period}', 'trend:{s}'
    kind: Literal["correlation", "anomaly", "trend_change"]
    series: list[str]
    stats: dict[str, float | int | str]  # rho, lag, n, p, q, rho_h1, rho_h2 | z, value, expected | ...
    period_start: datetime
    period_end: datetime

class AnalysisResult(BaseModel):
    data_up_to: datetime
    findings: list[Finding]
    funnel: dict[str, int]   # tests_run, raw_p_lt_05, passed_fdr, passed_stability
    skipped: list[str]
    rejected: list[Finding]  # 3 strongest candidates that failed FDR or stability; shown in the demo
    chart_data: dict[str, list[dict]]  # per finding_key: up to 500 points for the Go chart builders
```

The `funnel` drives the dashboard line "tested N, kept M", which is the pitch's evidence that the tool discards noise.

**Known limitation.** Autocorrelation left after differencing still makes p-values optimistic; the half-window stability check is the practical guard. Stretch: a circular-shift permutation test for survivors (200 random shifts of b, each larger than `max_lag`).

**Acceptance checks (stats/`tests/test_engine.py`, synthetic data, no database).**

- Six independent random walks of 200 days, 50 seeds: at most 5 seeds produce any correlation finding.
- b = a shifted forward 2 days plus noise: exactly one finding, lag +2, positive ρ.
- Two series sharing a linear trend with independent noise: no finding.
- A spike of 6 MADs in the last period: one anomaly finding.
- The same inputs give byte-identical `AnalysisResult` JSON.

## Workflow manager

The manager has two jobs: authoring a workflow with one LLM call when a project changes, and re-running it with no LLM call unless a finding materially changes.

**Authoring (`internal/workflows/author.go`).**

```go
type WorkflowDraft struct {
    AnalysisSpec  AnalysisSpec `json:"analysis_spec"`
    InsightPrompt string       `json:"insight_prompt"` // what the user cares about, for the narrator
    Rationale     string       `json:"rationale"`      // why these series and lags; shown in the UI
}
```

1. Triggered by project creation, `POST /api/projects/{id}/workflow/regenerate`, or a change to the project's sources.
2. Input: the goal, each source's `SourceProfile`, and per source its record count and first and last `observed_at`.
3. Call `claude-sonnet-5-5` through `internal/llm` with `WorkflowDraft` as the structured output schema.
4. Run `Validate()`. On failure, retry once with the errors appended to the prompt; if it fails again, store the error in `projects.last_error` and show it in the UI.
5. In one transaction: insert the workflow with `version = max + 1`, deactivate the previous one. Then trigger a run.

**Running (`internal/workflows/runner.go`).**

1. **Triggers:** `source_updated` events, debounced 30 s per project, plus a sweep every 5 min.
2. **Watermark:** newest `observed_at` across the project's sources. Run if there's no succeeded run for the active workflow, or the watermark is newer than the last run's `data_up_to`.
3. **Lock:** at most one run per project, using a mutex-guarded set of running project ids (the Go service is a single process).
4. **Execute:** insert a `running` row and call the worker's `POST /run` with the spec, project id and watermark.
5. **Diff** against the previous succeeded run by `finding_key`: each finding is new, still present, or gone.
6. **Stats:** every finding's `stats` and `chart_spec` come from this run, always, whatever happens to its text.
7. **Text:** new findings go to the narrator (at most 5 per run, ranked by |ρ| or |z|); gone findings are retired; present findings go to the reviewer, which keeps or revises their text (see Narration).
8. **Store** the insight rows, mark the run `succeeded`, publish `project_updated`.
9. **On error:** mark the run `failed` with the message and publish `run_failed`. The previous insights stay on the dashboard.

**Acceptance checks.**

- Creating a project over two seeded CSV sources yields a workflow and a succeeded run with no manual steps.
- Triggering again with no new data creates no run.
- Appending rows to series no finding uses makes zero LLM calls, per the call log.
- Appending rows that shift a finding makes one reviewer call, and the card's stats line updates whether its text is kept or revised.
- Data that flips a finding's sign rewrites its text without a reviewer call.
- A stub LLM returning an invalid draft is retried once, then the error shows on the project.

## Narration, verification and charts

The LLM writes words and picks a chart type; deterministic code checks every claim, builds every chart, and falls back to a template, so unverified text never reaches the dashboard.

**Narrator (`internal/narration/narrator.go`).** One `claude-sonnet-5-5` call per run covers all findings to narrate. Input: the goal, the workflow's `insight_prompt`, and per finding its kind, stats, and each series' label, unit and source summary. No raw records.

```go
type InsightDraft struct {
    FindingKey  string   `json:"finding_key"`
    Headline    string   `json:"headline"`    // one line, plain words, no digits
    Explanation string   `json:"explanation"` // two or three sentences, no digits
    Direction   string   `json:"direction" jsonschema:"enum=positive,enum=negative,enum=up,enum=down"`
    Leader      *string  `json:"leader"`      // series id that moves first; lagged correlations only
    Caveats     []string `json:"caveats"`
    Chart       string   `json:"chart" jsonschema:"enum=dual_line,enum=lag_scatter,enum=anomaly_band,enum=trend_split"`
}

type Narration struct {
    Insights []InsightDraft `json:"insights"`
}
```

**Verifier (`internal/narration/verifier.go`).** A draft is rejected if any of these holds:

- `finding_key` isn't one of the findings sent.
- `direction` disagrees with the stats: positive only if ρ > 0; up only if the later mean is higher (trend) or z > 0 (anomaly).
- `leader` disagrees with the lag: lag > 0 means series a, lag < 0 means series b, lag 0 means none.
- `headline` or `explanation` contains a digit after series labels are stripped out. The card renders all numbers from `stats`.
- Causal wording matches `\b(causes?|caused|causing|drives?|driven|results? in|leads? to|because|due to)\b`, case-insensitive.
- `chart` doesn't suit the finding's kind.

Rejected drafts get one retry with the reasons. If a draft fails again, or the LLM call errors, use a template such as "{a} and {b} move together, with {leader} moving first" and set `narrated_by = 'template'`.

**Reviewer (`internal/narration/reviewer.go`).** When new data arrives, an agent decides whether each existing finding's text still fits. Stats lines, charts and the funnel are not its decision: they come from the engine and refresh on every run.

1. **Hard rules first.** A finding missing from the new result is retired. A new finding goes to the narrator. Existing text that fails the verifier against current stats (sign flipped, leader changed) is rewritten without consulting the reviewer.
2. **Who is reviewed.** Remaining findings whose series received new records since their last review, at most once per finding per `REVIEW_MIN_INTERVAL` (60 s), so a fast replay doesn't call the LLM on every run.
3. **One call per run.** `claude-sonnet-5-5` gets, per finding: the current text, `narrated_stats` (the stats that text was written against), the current stats, and a delta summary (Δρ, Δn, Δq, each half's ρ). Comparing against `narrated_stats` rather than the previous run catches slow drift that no single run shows.
4. **Instructions.** Revise when the strength band changes (|ρ| below 0.3, 0.3 to 0.6, above 0.6), a caveat stops or starts applying, or the explanation no longer describes the stats. Otherwise keep: needless rewrites make cards churn.
5. **Verify and store.** Every revision goes through the verifier. A failed revision keeps the old text if that still verifies, else falls back to the template. `revise` replaces the text and `narrated_stats`; `keep` sets `reviewed_at` and `review_reason`. The card shows when its text was last checked, with the reason on hover.

```go
type ReviewItem struct {
    FindingKey string        `json:"finding_key"`
    Decision   string        `json:"decision" jsonschema:"enum=keep,enum=revise"`
    Reason     string        `json:"reason"`   // one line, shown on the card
    Revision   *InsightDraft `json:"revision"` // required when decision = revise
}

type Review struct {
    Items []ReviewItem `json:"items"`
}
```

**Charts (`internal/charts`).** This is the "AI Visualiser" box from the sketch, made deterministic. Each builder turns a finding's `chart_data` from the worker into Vega-Lite JSON with the data inlined, at most 500 points.

| Chart | Used for | Shows |
| --- | --- | --- |
| `dual_line` | Correlation | Both series as z-scores over time, b shifted by the lag |
| `lag_scatter` | Correlation | Differenced aₜ against bₜ₊ₖ, with a regression line |
| `anomaly_band` | Anomaly | The series, a band of rolling median ± 3.5 scaled MAD, flagged points highlighted |
| `trend_split` | Trend change | The series with the before and after means as two rules |

**Card contents.** Headline, explanation, a stats line built from `stats` ("ρ 0.62 at lag +1 day · n 180 · q 0.03 · holds in both halves"), caveats, the chart, and a "template" badge when `narrated_by = 'template'`.

**Acceptance checks.**

- Unit tests show drafts with a digit, a causal verb, a wrong direction or a wrong leader are each rejected.
- With the LLM stubbed to fail, every finding still appears, with template text.
- Every chart builder's output compiles with the frontend's `vega-lite` version (a Vitest test over fixture specs).

## API and dashboard

The API is a dozen REST endpoints plus one SSE stream; the dashboard refetches on events instead of receiving data over the stream, so the database stays the only source of truth.

| Method | Path | Does |
| --- | --- | --- |
| POST | `/api/sources` | Create an `rss` or `http_json` source; fetch once, then profile |
| POST | `/api/sources/csv` | Multipart upload (name, file); profile, then ingest |
| GET | `/api/sources` | List: kind, record count, `last_polled_at`, `last_error`, profile summary |
| GET | `/api/sources/{id}` | Detail: full profile and the 20 newest records |
| POST | `/api/projects` | Body: name, goal, `source_ids`; authors a workflow and triggers a run |
| GET | `/api/projects` | List with latest run status and finding count |
| GET | `/api/projects/{id}` | Project, active workflow (spec and rationale), latest run with its funnel |
| GET | `/api/projects/{id}/insights` | Rows from `latest_insights` |
| POST | `/api/projects/{id}/workflow/regenerate` | Author a new workflow version |
| GET | `/api/stream` | SSE: `project_updated`, `source_updated`, `run_failed`, ids only |
| POST | `/api/replay` | Demo replay (below) |

**Replay (`internal/api/replay.go`).** Body: `source_ids`, `periods_per_second`, `start_fraction` (default 0.5). It loads the first part of each demo dataset at once, then inserts the remaining rows in time order across sources at the given speed, publishing `source_updated` as it goes. It writes straight to `data_records`, bypassing adapters.

**Pages (React, Vite, TypeScript).**

- **Sources:** list with a status dot from `last_error`, record count and field roles. Add form with one tab per kind: CSV upload; RSS URL; JSON URL with `records_path`, `id_field`, `time_field`; poll interval.
- **New project:** name, goal text, source checkboxes. Submitting opens the project page in an "authoring workflow" state.
- **Project:** the goal; run status ("updated 12 s ago", running, failed); the funnel line ("tested 330 · 41 under p 0.05 · 3 passed FDR · 2 held in both halves"); a grid of finding cards; a collapsible "Analysis plan" with the rationale and spec, so judges can see what the agent decided; a regenerate button.

**Live updates.** One `EventSource('/api/stream')` in a top-level hook. On `project_updated` for the open project, invalidate the TanStack Query keys `['project', id]` and `['insights', id]`. Show "reconnecting" while the stream is down. Charts render with `vega-embed` inside `FindingCard`, with `actions: false`.

**Acceptance checks.**

- During a replay, the project page updates within 2 s of a run finishing, with no reload.
- Stopping the backend shows "reconnecting"; restarting it resumes updates.

## Work plan

Four workstreams build in parallel against the contracts fixed in the first two hours; the first end-to-end run is due at hour 14, feature freeze at hour 22.

&#91;embedded content: 24-hour build · six phases, first end-to-end run at hour 14\]

M0 fixes the contracts so the four workstreams can build in parallel through M1; everything after hour 14 is live updates, polish and rehearsal.

| Workstream | Language | Owns | Starts with | Needs from others |
| --- | --- | --- | --- | --- |
| A. Data and API | Go | Migrations, `internal/db`, adapters, ingest loop, HTTP handlers, SSE, replay, demo seed | Compose file and migration | Nothing |
| B. Stats worker | Python | `stats/`: spec mirror, frames, methods, `run_spec`, `POST /run`, engine tests | Synthetic-data tests, no database | The spec and result contracts |
| C. Agent | Go | `internal/llm`, profiler, author, narrator, reviewer, verifier, charts | Profiler on a fixture CSV | Contracts; B's `AnalysisResult` for narration |
| D. Frontend | TypeScript | Everything in `web/` | Pages against the API stubs' fixture JSON | API response shapes |

With three people, A also takes the profiler and charts, and C's author, narrator, reviewer and verifier go to whoever passes their M1 checks first.

**Milestone gates.**

- [ ] **M0, hours 0–2.** `docker compose up` starts Postgres, the Go service and the stats worker; migrations apply.
- [ ] **M0.** `internal/contracts` defines `SourceProfile`, `AnalysisSpec`, `AnalysisResult`, `WorkflowDraft``, Narration and Review`; Go and the worker both round-trip the fixtures in `contracts/fixtures/`.
- [ ] **M0.** Every endpoint returns fixture JSON, and the frontend renders a fixture project page.
- [ ] **M0.** The demo dataset is chosen and downloaded.
- [ ] **M1, hours 2–8.** CSV and RSS adapters ingest with deduplication; the Sources page lists real sources.
- [ ] **M1.** The engine passes every acceptance test on synthetic data.
- [ ] **M1.** The profiler labels the demo dataset's fields correctly.
- [ ] **M1.** The engine, run by hand on the demo dataset, yields at least one stable finding. If not, change the dataset now.
- [ ] **M2, hours 8–14.** Creating a project in the UI authors a workflow and shows verified cards with charts, with no manual steps.
- [ ] **M3, hours 14–19.** The JSON adapter, SSE, replay and change diffing work; a replay at demo speed updates cards live.
- [ ] **M4, hours 19–22.** Error states, cached narration and demo polish are done. Feature freeze at hour 22.
- [ ] **Hours 22–24.** Rehearse the demo twice and record a backup video.

## Rules for the coding agent

1. The migrations, `internal/contracts` and the worker's mirror models are frozen after M0. Propose changes to them and wait for a human.
2. Never execute model output or splice it into code or SQL. An LLM only ever produces JSON that is unmarshalled into a contract struct and checked by `Validate()`.
3. Every LLM call goes through `internal/llm`: structured outputs with the contract struct as the schema, a 60 s context timeout, one retry, and a JSONL log line with purpose, model, latency, tokens and validation result.
4. Model ids live in `internal/config`: profiler `claude-haiku-5-5`; author, narrator and reviewer `claude-sonnet-5-5`.
5. The stats worker connects with a read-only Postgres role and makes no other network calls.
6. Every goroutine loop recovers panics per item, logs with `slog` and carries on.
7. Tests never call the live network or the real LLM. Go tests use recorded fixtures and `httptest`, plus a stub client enabled by `LLM_STUB=1`; worker tests use synthetic data.
8. Secrets (`ANTHROPIC_API_KEY`, `DATABASE_URL`) come from `.env` and are never committed.
9. A milestone is done when `make test` (`go test ./...` and `pytest`) passes and its gate items are ticked.

## Demo plan

The demo shows the agent planning once, the engine updating live, and the funnel proving that noise is thrown away.

1. Add two or three sources live (CSV, RSS, JSON).
2. Create a project with a one-sentence goal and open the "Analysis plan" to show what the agent chose and why.
3. Start a replay; stats update on every run, while card text changes only when the reviewer judges it stale, with its reason shown.
4. Point at the funnel line, then at one strong-looking candidate the engine rejected.

**Dataset.** Choose a pair with a known, explainable lagged relationship and at least a year of daily data, for example a city's daily weather and its open bike-hire counts, plus a news RSS feed as the text source. Check each dataset's licence. Decide by hour 2, because the M1 gate depends on it.

**Before presenting.** Run the full replay once so narrations are cached by `finding_key` and direction, and keep the backup video ready.

## Risks

| Risk | Mitigation |
| --- | --- |
| The demo dataset has no stable finding | Check by hand at M1; swap datasets before hour 8 |
| LLM latency or rate limits on stage | Narration cache, a pre-run replay, template fallback |
| Optimistic p-values from autocorrelation | Differencing and the half-window check; permutation test as stretch |
| Live feeds don't change during the demo slot | Replay mode |
| Scope creep (scraping, more adapters) | Stretch work only after the M3 gate |
| Reading JSONB is too slow | Volumes are small; move to typed tables only if a run takes over 5 s |
| The reviewer rewrites too often, so cards churn | Prompt biased to keep, a 60 s minimum interval, and the keep/revise ratio logged per run |

## Open questions

- Which demo dataset? Decide by hour 2.
- Three people or four, and who takes which workstream?
- Demo hosting: a laptop running Docker, or a cloud VM?

## Sources

- [Free Twitter API (X API) in 2026: What Is Free and What Is Not](https://apiserpent.com/blog/free-twitter-x-api), Serpent API, read 2026-11-07
- [Structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs), Claude Platform docs
- [Tool runner (SDK)](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-runner), Claude Platform docs, for a later "ask a question" feature
