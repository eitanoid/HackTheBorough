# Live Insights Platform: Implementation Plan

Nov 7, 2026 · @Tymur

## Overview

In 24 hours, 3–4 people build a service where each project has two agent-generated workflows, one statistical and one investigative. Both re-run deterministically whenever data lands, and a reviewer agent decides when either needs regenerating.

**MVP (must ship):**

- Two source types: CSV upload and RSS/Atom feed.
- Projects: a goal in plain language plus a set of sources.
- A statistical workflow per project: an agent-written pandas script that receives the project's data, runs its own tests (lagged correlation, anomalies, custom tests) and reports every one; the backend applies false-discovery control across all reported tests before any becomes a finding.
- An investigation workflow per project: an agent explores the data with unsandboxed Python to answer the goal; its tool calls are recorded and replayed on new data.
- A reviewer agent that, after each run, decides whether the analysis script still suits the data and whether investigation findings still hold, and triggers regeneration when they don't.
- A React dashboard with statistical and exploratory finding cards, Vega-Lite charts, workflow history with review reasons, and live updates over SSE.
- A replay mode that streams historical data in fast, for the demo.

**Stretch (only after milestone M3 passes):** an `http_json` source adapter, a trend-change test and a permutation test in the example script, ad-hoc user questions, investigations started automatically by anomalies, a deterministic verifier for card text.

**Non-goals:** authentication, multi-tenancy, web scraping, X/Twitter, running model-written code anywhere but the Python runner, human-written Python, production deployment.

**For the coding agent:** build the milestones in the order given in the work plan. Each milestone ends with acceptance checks; do not start the next one until they pass. The SQL migrations and the Go structs in `internal/contracts/` are shared by every workstream: propose changes to them, don't make them unilaterally.

**Python is only for model-written code.** The only Python that runs is what an LLM wrote: the author's analysis scripts and the investigator's `run_python` code, executed by a generic Python runner (built separately). Data loading, validation, false-discovery correction, storage and charts are Go. Nobody writes a Python stats library.

## Design decisions

The sketch's shape stays: ingestor, data store, project store, workflow manager, analysis, reporter. Eight changes make it safe to run and statistically honest.

| # | Area | Sketch | Plan | Why |
| --- | --- | --- | --- | --- |
| 1 | Statistical analysis | LLM-written "Statistics Code" | One pandas script written by the author and run by the Python runner; it receives the project's data, runs its tests and reports every one, and the Go backend applies correction across all of them | Scripts can express anything pandas can; one artifact instead of a plan plus a library; the same correction applies on every run; every number comes from executed code |
| 2 | Workflows per project | One Workflows table with statistics code and an AI insights prompt | One statistical and one investigation workflow per project, each a versioned artifact re-run deterministically on new data | Re-runs are cheap and comparable; an LLM is called to regenerate only when the reviewer says so |
| 3 | Regeneration | — | Only for reasons from a fixed list, capped per day; the script reviewer never sees results | Regenerating a script until something passes would be p-hacking |
| 4 | Time | One `CreatedAt`, set at ingestion | `observed_at` (event time) plus `ingested_at` | Correlations must run on when things happened, not when we fetched them |
| 5 | Duplicates | None | Unique `(data_source_id, external_id)`; insert with `ON CONFLICT DO NOTHING` | Polling re-fetches the same items, so counts would inflate on every poll |
| 6 | Results | JSON columns on Project and an Insights table | Insights only, written by runs; a `reviews` table; a `latest_insights` view per workflow kind | One source of truth, and provenance for every claim |
| 7 | Polling | On the source and on the project | On the source only; a project's workflows re-run when its sources' newest `observed_at` passes their last run | Two projects sharing a source can't disagree |
| 8 | Project ↔ sources | Array of UUIDs | `project_sources` join table | Foreign keys and cascades work |
| 9 | Data schema | JSON schema | `SourceProfile`: each field's role, unit and aggregation | The engine must know which field is a timestamp, a measure or text |
| 10 | Example source | Twitter | RSS and CSV, JSON APIs as stretch | X's API charges $0.005 per post read and has no free read tier ([source](https://apiserpent.com/blog/free-twitter-x-api)) |
| 11 | Model-written code | — | Author scripts and the investigator's run\_python, executed unsandboxed in the Python runner's child processes and re-run on new data | Lets the agents use pandas and scipy directly while staying replayable; an accepted risk for the hackathon |

**Numbers rule.** Every number shown on the dashboard comes from executed code: a statistical script's reported results (plus the q-values the backend adds), or the replayed output of an investigation step. The LLM decides what to analyse and explains what was found; it never states figures in its text.

**Languages.** The main service is Go: ingestion, API, SSE, the workflow manager, every LLM call, building each script's input, validating and correcting script results, and chart building. The only Python is model-written code, executed by a generic Python runner (built separately; see Analysis script) with pandas, numpy, scipy and statsmodels installed. The runner has no database access and no secrets: Go passes each script its data.

## Architecture

One Go binary runs the API, the background loops, every LLM call and the statistics bookkeeping; a Python runner executes model-written code and nothing else; Postgres is the only store; the React app talks to the Go service over REST and one SSE stream.

> The two embedded diagrams in this section and in Workflow manager predate the runner change and still show a Python stats worker; redraw them.

&#91;embedded content: architecture · agents in Go, statistics in a Python worker, one Postgres database\]

Ingestion and the API sit above the database. Below it, the agents write workflow artifacts and card text, and the runner re-runs those artifacts on every update; only the tinted boxes call an LLM.

**Process model.** `cmd/server` starts the HTTP server and two goroutines: the ingest loop and the run loop, which runs both workflow kinds. Generating a workflow (author or investigator) happens in its own goroutine, so a slow agent never delays runs. An in-process pub/sub in `internal/events` (a mutex-guarded set of subscriber channels) carries `source_updated`, `project_updated`, `workflow_regenerated`, `investigation_step` and `run_failed` between the loops and the SSE handler. The run loop and the investigator execute model-written code through the `pyrunner.Runner` Go interface; another engineer provides its implementation, which decides how the code is run. The stack runs locally in a kind cluster under Skaffold, deployed by the Helm chart. The Next.js frontend calls the Go service through the Connect-RPC client generated from `proto/`.

**Libraries.**

- Go service: standard `net/http` with method and path patterns in `ServeMux`, `log/slog`, `connectrpc.com/connect/v2` with protobuf stubs generated by `buf`, `github.com/jackc/pgx/v5` with `pgxpool`, `github.com/mmcdole/gofeed`, `github.com/google/uuid`. LLM calls use the provider-neutral client in `internal/llm` (OpenAI-compatible chat completions; a thin `Provider` interface is the only provider-specific seam).
- Python runner: Python 3.12 with `pandas`, `numpy`, `scipy` and `statsmodels` available to model-written code; its server framework is up to its owner. No database driver.
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
    contracts/               # SourceProfile, ScriptResult, AnalysisResult, WorkflowDraft,
                             #   Narration, InvestigationReport, ReplayResult, ReviewDecision (frozen after M0)
      testdata/              # golden JSON the contracts round-trip; fixture script results
    llm/                     # provider-neutral client (OpenAI-compatible), JSONL call log
    ingest/
      adapter.go             # SourceAdapter interface, RawRecord
      csv.go
      rss.go
      loop.go                # polling scheduler
    profiling/               # pre-pass + LLM labelling
    pyrunner/                # Runner interface only; the implementation comes from the runner's engineer
    analysis/
      input.go               # data_records -> the script's INPUT
      analyse.go             # run script -> validate ScriptResult -> correct -> AnalysisResult
      fdr.go                 # Benjamini–Hochberg
    workflows/
      author.go              # statistical: goal + profiles -> WorkflowDraft (a script)
      runner.go              # watermark -> analyse or replay -> insights -> review
    investigate/             # investigation: tool loop -> recorded steps + report; replay
    review/                  # reviewer: hard rules, then LLM keep or regenerate
    narration/
      narrator.go
    charts/                  # Vega-Lite builders from Chart points
    api/                     # handlers, SSE, demo replay
  tests/fixtures/
svc/frontend/                # Next.js app
  app/                       # routes: sources, new project, project
  gen/                       # buf output: TypeScript stubs
runner/                      # generic Python code runner (another engineer builds it); no stats code
demo/datasets/
Makefile                     # cluster, skaffold and generate targets today; add test and demo
```

## Data model

Nine tables in one Postgres database: sources and their records on the ingest side; projects, workflows (with an investigation's recorded steps), runs, reviews and insights on the analysis side. This is the schema after `001_init.sql`, `002_script_workflows.sql` and the proposed `003_workflow_sources.sql` (adds `workflows.source_ids`); the SQL below is the combined result. Applied migrations are never edited: schema changes are new numbered files.

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
  analysis_script text,                  -- the author's pandas script; statistical only
  report         jsonb,                  -- InvestigationReport; investigation only
  insight_prompt text NOT NULL,          -- narrator guidance or investigator brief
  rationale      text NOT NULL,          -- why this plan; shown in the UI
  source_ids     uuid[] NOT NULL DEFAULT '{}', -- project sources when generated (003); for sources_changed and new_scope
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
                                         -- only describe_source and run_python are used now
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
  stats       jsonb NOT NULL,            -- numbers from executed code (+ q); the UI renders figures from here
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

- Each run writes a full set of insight rows for its workflow, and the dashboard shows the latest succeeded run of each kind. For statistical findings whose effect kept its sign, the runner copies the text forward with fresh stats and charts.
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

## Analysis script

The author writes the statistical workflow as one pandas script. Go hands the script the project's data; the script computes whatever statistics fit the goal and reports every test it ran; Go then applies false-discovery correction across all of them, so only tests that survive become findings.

**One run, step by step (`internal/analysis`).**

1. Go builds the script's input (`INPUT`) from `data_records` at the run's watermark.
2. Go sends the script and `INPUT` to the Python runner, which executes it in a child process (60 s, 1 GB).
3. The script sets a `result` dict: the series it built, every test it ran, and any anomalies.
4. Go checks `result` against the `ScriptResult` contract. An invalid result fails the run.
5. Go applies Benjamini–Hochberg across every reported test; survivors become findings.
6. Go stores the `AnalysisResult` on the run.

The script is the only Python in this flow. The same script re-runs on every data update with no LLM call; only a reviewer `regenerate` decision gets a new one.

**Python runner (another engineer builds it).** We define only the Go interface our code calls, in `internal/pyrunner`. We write no implementation of it: no HTTP client and no process spawning. Tests use a fake.

```go
type Request struct {
	Code    string        // Python source
	Input   any           // the code sees it as the global dict INPUT
	Timeout time.Duration
}

type Outcome struct {
	OK       bool
	Result   json.RawMessage // the code's global `result`, JSON-encoded, untruncated
	Stdout   string          // may be truncated (4 KB)
	Error    string          // traceback when the code raised
	TimedOut bool
}

// Run returns an error only when the runner itself fails, never because the code failed.
type Runner interface {
	Run(ctx context.Context, r Request) (Outcome, error)
}
```

- The implementation runs the code in a child process with a 1 GB memory limit, with pandas, numpy, scipy and statsmodels available.
- It encodes `result` with NaN and Infinity rejected, and supports results up to 16 MB.
- It gives the code no database access and none of the backend's secrets.
- How it executes code (a separate service or a local subprocess) is up to its engineer.

**Input (`INPUT`, built by `internal/analysis/input.go`).**

```json
{
  "data_up_to": "2024-06-30T00:00:00Z",
  "sources": {
    "bikes": {
      "id": "4f0c…", "name": "Bike hire", "kind": "csv",
      "summary": "Daily bike hire trips in the city",
      "fields": [{"path": "trips", "role": "measure", "unit": "trips", "aggregation": "sum", "description": "Trips started"}],
      "records": [{"observed_at": "2024-01-01T00:00:00Z", "payload": {"trips": 5123}}]
    },
    "news": {"id": "…", "name": "City news", "kind": "rss", "summary": "…", "fields": [], "records": []}
  }
}
```

- Keys under `sources` are slugs of the source names, unique within the project (`_2` on a collision). The author prompt uses the same keys, so scripts reference sources by name, never by UUID.
- Only the project's sources; only records with `observed_at ≤ data_up_to` and within `ANALYSIS_HISTORY_DAYS` (365) of it; sorted by `observed_at`, then `external_id`. The same data always produces the same input bytes.
- At hackathon volumes (tens of thousands of records) the input is a few MB, so no further batching is needed.

**A script.** This is also the example in the author prompt.

```python
import pandas as pd
from scipy import stats

def frame(key):
    recs = INPUT['sources'][key]['records']
    df = pd.json_normalize([r['payload'] for r in recs])
    df.index = pd.to_datetime([r['observed_at'] for r in recs], utc=True)
    return df

trips = frame('bikes')['trips'].resample('D').sum(min_count=1)   # measure: gaps stay NaN
news = frame('news')
strike = news['title'].str.contains('strike|walkout', case=False, na=False).resample('D').sum()  # count: 0 when none

result = {
    'series': [
        {'id': 'bike_trips', 'label': 'Bike trips', 'source': 'bikes', 'unit': 'trips',
         'grain': 'day', 'periods': len(trips), 'points': int(trips.notna().sum())},
        {'id': 'strike_news', 'label': 'Strike news', 'source': 'news', 'unit': 'items',
         'grain': 'day', 'periods': len(strike), 'points': int(strike.notna().sum())},
    ],
    'tests': [],
    'anomalies': [],
}

# Correlate day-to-day changes, not levels, and report every lag whatever its p-value.
# Lag k compares bike_trips on day t with strike_news on day t+k.
da, db = trips.diff(), strike.diff()
for lag in range(-3, 4):
    pair = pd.concat([da, db.shift(-lag)], axis=1).dropna()
    rho, p = stats.spearmanr(pair.iloc[:, 0], pair.iloc[:, 1])
    result['tests'].append({
        'key': f'corr:bike_trips:strike_news:lag{lag:+d}', 'kind': 'correlation',
        'series': ['bike_trips', 'strike_news'], 'p': float(p), 'effect': float(rho), 'n': len(pair),
        'stats': {'rho': float(rho), 'lag': lag},
        'chart': {'type': 'lag_scatter',
                  'points': [{'x': float(x), 'y': float(y)} for x, y in pair.tail(500).values]},
    })
```

**Result (`ScriptResult`, `internal/contracts/script.go`).** What the script must put in `result`.

```go
type SeriesInfo struct {
    ID      string  `json:"id"`      // snake_case, unique within the result
    Label   string  `json:"label"`   // human name for cards and charts
    Source  string  `json:"source"`  // key in INPUT.sources
    Unit    *string `json:"unit"`
    Grain   string  `json:"grain"`   // hour | day | week
    Periods int     `json:"periods"` // periods in the window the script analysed
    Points  int     `json:"points"`  // periods with a value
}

type Chart struct {
    Type   string           `json:"type"`   // dual_line | lag_scatter | series_line | anomaly_band
    Points []map[string]any `json:"points"` // at most 500; columns fixed per type (Narration and charts)
}

type Test struct {
    Key    string         `json:"key"`    // stable across runs: built from series ids and parameters, never from data values
    Kind   string         `json:"kind"`   // correlation | custom
    Series []string       `json:"series"` // SeriesInfo ids
    P      float64        `json:"p"`
    Effect float64        `json:"effect"` // signed size (ρ, difference in medians, ...); a sign change triggers re-narration
    N      int            `json:"n"`      // observations the test used
    Stats  map[string]any `json:"stats"`  // numbers and short strings shown on the card
    Chart  *Chart         `json:"chart"`
}

type Anomaly struct {
    Key      string    `json:"key"`      // 'anom:{series}:{period}'
    Series   string    `json:"series"`
    Period   time.Time `json:"period"`
    Value    float64   `json:"value"`
    Expected float64   `json:"expected"`
    Score    float64   `json:"score"`    // e.g. robust z
    Chart    *Chart    `json:"chart"`
}

type ScriptResult struct {
    Series    []SeriesInfo `json:"series"`
    Tests     []Test       `json:"tests"`     // every test run, passed or not
    Anomalies []Anomaly    `json:"anomalies"` // not hypothesis tests; not corrected
}
```

**Validation (`ScriptResult.Validate()`).** Any failed check fails the run; on a dry run the message goes back to the author.

- Series ids and test and anomaly keys are unique, at most 120 characters, and use only `[a-z0-9_:+-]`.
- `source` is a key in `INPUT.sources`; `grain` is `hour`, `day` or `week`; `points ≤ periods`.
- Every series a test or anomaly names is in `series`.
- `p` is in [0, 1]; every number is finite; `n ≥ 0`; `kind` is `correlation` or `custom`.
- Charts have a known type, at most 500 points, and the columns that type needs.
- At most `MAX_TESTS` (2,000) tests and 50 anomalies.
- At least one series. A result with no series means the script analysed nothing.

Anything else the script prints, returns or puts in `result` is ignored.

**Correction (`internal/analysis`).**

1. Benjamini–Hochberg across the p-values of all reported tests gives each test a q.
2. A test is a candidate if q ≤ `FDR_Q` (0.1) and n ≥ `MIN_N` (30). Tests under `MIN_N` still count in the family.
3. Of the candidate correlations on the same pair of series (typically one per lag), keep only the one with the largest |effect|.
4. The remaining candidates become findings. Anomalies become findings as reported, at most 3 per series by |score|.
5. `rejected` holds the 3 lowest-q tests that did not become findings, for the demo.

`FDR_Q`, `MIN_N`, `MAX_TESTS` and `ANALYSIS_HISTORY_DAYS` live in Go config; the script can't change them.

**Stored on the run (`AnalysisResult`, `internal/contracts/result.go`).**

```go
type SourceHealth struct {
    Records    int       `json:"records"`      // in the input window
    FirstAt    time.Time `json:"first_at"`
    LastAt     time.Time `json:"last_at"`
    MedianGapS float64   `json:"median_gap_s"` // typical time between records
}

type Health struct {
    Series         []SeriesInfo            `json:"series"`          // as reported by the script
    Sources        map[string]SourceHealth `json:"sources"`         // source_id -> facts computed by Go from INPUT
    UnprofiledKeys map[string][]string     `json:"unprofiled_keys"` // source_id -> payload keys missing from its profile
}

type Finding struct {
    FindingKey string         `json:"finding_key"` // the test or anomaly key
    Kind       string         `json:"kind"`        // correlation | anomaly | custom
    Series     []string       `json:"series"`
    Stats      map[string]any `json:"stats"`       // the script's stats plus p, q, n, effect (anomalies: value, expected, score)
    Chart      *Chart         `json:"chart"`
}

type AnalysisResult struct {
    DataUpTo time.Time      `json:"data_up_to"`
    Findings []Finding      `json:"findings"` // sorted by finding_key
    Funnel   map[string]int `json:"funnel"`   // tests_run, raw_p_lt_05, passed_fdr, kept
    Rejected []Finding      `json:"rejected"`
    Health   Health         `json:"health"`
}
```

The `funnel` drives the dashboard line "tested N, kept M", which is the pitch's evidence that the tool discards noise.

**Reporting every test.** Correction only works if the script reports every test it runs, and nothing can force it to. The design narrows the room for it:

- The prompt requires every test to be appended to `result['tests']` and forbids choosing what to report by p-value or effect size; the example script reports every lag.
- The script is fixed between regenerations, so it can't change what it tests from run to run.
- The script and rationale are visible in the Workflows panel, and `tests_run` is on the dashboard.
- The script reviewer never sees tests or findings, so regeneration can't be used to fish for significance.

**Determinism.** The prompt requires that data comes only from `INPUT`, that any randomness uses a fixed seed, and that the code never reads the clock, files or the network. Go builds `INPUT` deterministically and sorts findings, so the same script on the same data gives byte-identical `AnalysisResult` JSON.

**Known limitation.** Statistical method belongs to the script: differencing before correlating, handling ties, checking that a correlation holds across the window. The prompt and example script show the expected methods; nothing enforces them. Autocorrelation left after differencing still makes p-values optimistic. Stretch: a circular-shift permutation test in the example script.

**Acceptance checks (`internal/analysis`, Go).** A fake runner returns fixture results; no database unless noted.

- 40 tests with p spread evenly over (0, 1], plus one with p = 0.04: no finding.
- One test with p = 0.0001 among 20 null tests: one finding, with q in its stats.
- A test with p = 0.0001 and n = 10 counts in `tests_run` but is not a finding.
- Three surviving correlations on one pair at different lags give one finding: the largest |effect|.
- Each malformed result fails the run with a message naming the problem: a duplicate key, p = 1.5, NaN, a test naming an unreported series, a 501-point chart, an unknown chart type, no series.
- Printed output and extra keys in `result` produce no findings.
- `INPUT` contains only the project's sources and only records up to `data_up_to` (database test).
- With the real runner: the example script on synthetic data where strike news leads trips by 2 days gives one correlation finding at lag +2; run twice, the `AnalysisResult` JSON is byte-identical; a script that raises fails the run with its traceback.

## Investigation workflow

The investigator explores the data once to answer the project goal; its tool calls and report are saved as the workflow, and every later run replays those calls deterministically instead of calling the agent again.

**Tools.** Every call uses the investigation's fixed `data_up_to`, and only the project's sources are visible.

| Tool | Served by | Input | Returns |
| --- | --- | --- | --- |
| `describe_source` | Go, from the database | `source`: a key in `INPUT.sources` | Fields with roles and units, record count, first and last `observed_at` |
| `run_python` | The Python runner | `code`: Python source | The `result` dict the code builds, plus up to 4 KB of stdout, or the traceback if it fails |

**`run_python`.** Go builds the same `INPUT` as for analysis scripts (see Analysis script), at the investigation's `data_up_to`, and sends the code and input to the runner with a 30 s timeout and a 1 GB memory limit. It is not sandboxed: the code can reach the runner container's filesystem and network, which is an accepted hackathon risk (see Risks).

- **Data access.** Only through `INPUT`, which holds only the project's sources. The runner has no database access, so there is no other route to data.
- **Output.** The code must put its numbers in a dict named `result`, and claims cite paths into it, such as `result.rho`.
- **Determinism.** The prompt requires that data comes only from `INPUT`, that any randomness uses a fixed seed, and that the code never reads the clock, files or the network. On replay the same code runs again with `INPUT` at the new watermark.
- **Charts.** `result` may contain chart objects in the `Chart` shape from Analysis script (`dual_line`, `lag_scatter` or `series_line`). A finding points at one with `chart_seq` and `chart_path`.

**Generating a version (`internal/investigate`).**

1. Triggered when the project is created, or when the reviewer decides `regenerate` for the investigation workflow.
2. Run the investigator model (`LLM_MODEL_INVESTIGATOR`, `gpt-4.1`) through `llm.RunTools[InvestigationReport]` over the two tools, with at most `MaxInvestigatorIt` (12) iterations and `InvestigationReport` as the structured output. Input: the goal and the source profiles; for a regeneration, also the previous version's findings and the reviewer's reason, so it doesn't propose retired claims again.
3. Each tool handler answers `describe_source` from the database or sends `run_python` to the runner, records the call as a `workflow_steps` row with its full output, publishes `investigation_step` for the live view, and returns the output to the model truncated to 4 KB.
4. `Validate()` drops any claim whose `seq` or `path` doesn't exist in the recorded steps, because the card reads its numbers from there, and clears a chart whose `chart_path` doesn't hold a valid `Chart`. A version with no findings left is still saved, with its rationale.
5. In one transaction, save the report and steps as a new `investigation` version with the project's current `source_ids`, and deactivate the previous one. Then trigger a replay run so the findings become insights.

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
    ChartSeq    *int     `json:"chart_seq"`   // run_python step whose result holds the chart; null for no chart
    ChartPath   *string  `json:"chart_path"`  // dotted path to a Chart in that result, e.g. "charts.trips"
}

type InvestigationReport struct {
    Rationale string                 `json:"rationale"` // what was explored and why; shown in the UI
    Findings  []InvestigationFinding `json:"findings"`
}
```

**Replay runs (`internal/investigate/replay.go`).** On each run of the investigation workflow, Go re-executes the recorded steps at the new watermark: `describe_source` against the database, and each `run_python` step through the runner with a fresh `INPUT`. The outputs form a `ReplayResult` with one output per step. The workflow runner re-reads every claim at its `seq` and `path` in the replayed outputs; those values become the card's `stats`, and its chart is read from `chart_seq` and `chart_path`. No LLM call is made. The reviewer then compares replayed values with the originals.

**Exploratory, not proven.** Investigation findings sit outside the statistical workflow's false-discovery correction, because an agent trying hypotheses until one looks good is the multiple-comparisons problem the engine exists to prevent. Their cards carry an "exploratory" label, and every number links to the step it came from.

**Acceptance checks.**

- On the demo dataset, generation finishes within 12 tool calls and saves at least one finding.
- A replay at the original `data_up_to` reproduces every recorded output exactly, `run_python` steps included.
- A claim whose `path` doesn't exist in its step output is dropped by `Validate()`.
- `describe_source` with a key outside the project is rejected, and a `run_python` step's `INPUT` holds only the project's sources.
- A `run_python` step that runs past 30 s is killed, and the agent sees the timeout as the tool result.

## Workflow manager

Each project has one active statistical workflow and one active investigation workflow; the manager generates a new version only when a project is created or the reviewer asks, and otherwise runs the active versions on every data update.

&#91;embedded content: workflow lifecycle · generate, run, review\]

Runs never call the generating agent; only a `regenerate` decision does, and the reviewer's hard rules can force one without an LLM call.

**Generating.**

| Kind | Generator | Artifact | When |
| --- | --- | --- | --- |
| Statistical | Author (`internal/workflows/author.go`) | pandas script, insight prompt, rationale | Project created; reviewer decides `regenerate` |
| Investigation | Investigator (`internal/investigate`) | Recorded steps and report | Project created; reviewer decides `regenerate` |

**Authoring the statistical workflow.**

```go
type WorkflowDraft struct {
    Script        string `json:"script"`         // pandas script: reads INPUT, sets result (a ScriptResult)
    InsightPrompt string `json:"insight_prompt"` // what the user cares about, for the narrator
    Rationale     string `json:"rationale"`      // why these series and tests; shown in the UI
}
```

1. **Input:** the goal; each source's `INPUT` key, `SourceProfile`, record count and date range; the `INPUT` format, the `ScriptResult` contract and the example script from Analysis script. For a regeneration, also the previous script and the reviewer's reason. The prompt states the rules: report every test you run; never choose what to report by p-value or effect; correlate changes, not levels; data only from `INPUT`; no clock, files, network or unseeded randomness; no digits in the rationale.
2. **Generate:** call the author model (`LLM_MODEL_AUTHOR`, `gpt-4.1`) through `llm.Generate[WorkflowDraft]`, with `WorkflowDraft.Validate()` as the validator. It rejects an empty script or rationale, and a script that never reads `INPUT`, never sets `result`, or uses `open(`, `os`, `subprocess`, `socket`, `requests`, `datetime.now` or `time.time`. This is a check for mistakes, not a security boundary.
3. **Dry run:** run the script through `internal/analysis` at the current watermark. If it raises, times out or returns an invalid result, generate once more with the script and the error appended. The retry shows the author the error only, never the result's tests or findings. If it fails again, store the error in `projects.last_error` and keep the previous version active.
4. **Save:** in one transaction, deactivate the previous version, insert the new one with the project's current `source_ids`, store the dry run as its first succeeded run, and set the triggering review's `next_workflow_id`. Publish `workflow_regenerated`, then hand the run to the runner's insight steps (5–8 below) so its findings become cards.

At most one generation per project and kind runs at a time; it runs in its own goroutine, so the API returns at once.

**Running (`internal/workflows/runner.go`).**

1. **Triggers:** `source_updated` events, debounced 30 s per project, plus a sweep every 5 min.
2. **Watermark:** newest `observed_at` across the project's sources. For each active workflow, run if it has no succeeded run yet or the watermark is newer than its last run's `data_up_to`.
3. **Lock:** at most one run per workflow at a time, using a mutex-guarded set (the Go service is a single process).
4. **Execute:** a statistical workflow runs its script through `internal/analysis`; an investigation workflow is replayed by `internal/investigate`. Both use the watermark.
5. **Statistical insights:** new findings go to the narrator, at most 5 per run, ranked by |effect| or |score|. An existing finding keeps its text unless its effect changed sign, in which case it is re-narrated. Findings that disappeared are dropped.
6. **Investigation insights:** text comes from the active version's report; numbers and charts come from the replay.
7. **Store** the insight rows, mark the run `succeeded`, publish `project_updated`.
8. **Review:** hand the run to the reviewer, which may trigger regeneration.
9. **On error:** mark the run `failed` and publish `run_failed`. The previous insights stay on the dashboard.

**Acceptance checks.**

- Creating a project over two seeded CSV sources yields one workflow of each kind and a succeeded run of each, with no manual steps.
- Triggering again with no new data creates no run.
- Appending rows makes no LLM calls beyond narrating new statistical findings and at most one reviewer call per workflow, per the call log.
- A stub LLM returning an invalid draft is retried once; the error shows on the project and the previous version stays active.
- A script that raises on current data, or returns an invalid result, is retried once with the error, then reported on the project.
- The author's retry prompt contains the error but no tests, p-values or findings.

## Reviewer

After each run, the reviewer decides whether the workflow that produced it still fits the data. `keep` changes nothing; `regenerate` asks that workflow's generator for a new version. Every decision is stored in `reviews` with its reason.

**When it runs.** After a succeeded run, or a failed statistical run, at most once per workflow per `REVIEW_MIN_INTERVAL` (60 s). If a hard rule settles the decision, no LLM call is made and the review records `decided_by = 'rule'`.

**Statistical workflow: does the script still suit the data?** The reviewer sees the script, the run's health block (the series the script reported, per-source facts and unprofiled keys) and whether the project's sources changed. It never sees tests, p-values, findings or the funnel, because regenerating a script until something passes would be p-hacking.

| Reason category | Signal | Decided by |
| --- | --- | --- |
| `sources_changed` | The project's sources differ from the version's `source_ids` | Rule: regenerate |
| `script_error` | The script raised or returned an invalid result on new data, for example on a field that disappeared | Rule: regenerate |
| `schema_drift` | `unprofiled_keys` is still non-empty after re-profiling | Reviewer |
| `stale_series` | A source the script uses has had no records for 3 of its usual gaps (`median_gap_s`) | Reviewer |
| `coverage` | A reported series has values in under 50% of its periods, or its sources now hold far more history than the script analyses | Reviewer |

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
    ReasonCategory string `json:"reason_category" jsonschema:"enum=none,enum=schema_drift,enum=stale_series,enum=coverage,enum=effect_faded,enum=explanation_mismatch,enum=new_scope"`
    Reason         string `json:"reason"` // one line, shown in the workflow history
}
```

`Validate()` rejects `regenerate` with category `none`, and categories that don't belong to the workflow's kind; a rejected decision is stored as `keep`. Model: `LLM_MODEL_REVIEWER` (`gpt-4.1`), one call per review.

**Acceptance checks.**

- Adding a source to a project regenerates its statistical script with no LLM review call.
- A replay in which a claimed value changes sign regenerates the investigation with no LLM review call.
- A stub reviewer returning `regenerate` with category `none` is stored as `keep`.
- A fourth regeneration in a day is recorded with `acted_on = false` and not executed.
- The script reviewer's logged prompt contains no tests, p-values or findings.

## Narration and charts

The LLM writes card text; chart points come from executed code, deterministic Go lays them out, and a template fills in when the LLM call fails.

**Narrator (`internal/narration/narrator.go`).** One call to the narrator model (`LLM_MODEL_NARRATOR`, `gpt-4.1-mini`) per run covers the new or sign-changed statistical findings to narrate. Input: the goal, the workflow's `insight_prompt`, and per finding its kind, stats, and each series' label, unit and source summary. No raw records.

```go
type InsightDraft struct {
    FindingKey  string   `json:"finding_key"`
    Headline    string   `json:"headline"`    // one line, plain words, no digits
    Explanation string   `json:"explanation"` // two or three sentences, no digits
    Direction   string   `json:"direction" jsonschema:"enum=positive,enum=negative,enum=up,enum=down"`
    Leader      *string  `json:"leader"`      // series id that moves first; lagged correlations only
    Caveats     []string `json:"caveats"`
}

type Narration struct {
    Insights []InsightDraft `json:"insights"`
}
```

The prompt asks for plain words with no digits and no causal wording. Nothing enforces this, but every number on a card is rendered from `stats`, so wrong text can't change a figure. If the narrator call fails after one retry, use a template such as "{a} and {b} move together, with {leader} moving first" and set `narrated_by = 'template'`.

**Charts (`internal/charts`).** This is the "AI Visualiser" box from the sketch, made deterministic. Each builder turns a `Chart` (`{type, points}`) from a statistical finding or an investigation step into Vega-Lite JSON with the data inlined, at most 500 points. Builders lay out the points they're given; the only computing they do is display scaling inside Vega-Lite.

| Chart | Used for | Point columns | Shows |
| --- | --- | --- | --- |
| `dual_line` | Correlations | `t`, `a`, `b` (b already shifted by the lag) | Both series as z-scores over time |
| `lag_scatter` | Correlations | `x`, `y` | One series' changes against the other's, with a regression line |
| `anomaly_band` | Anomalies | `t`, `v`, `lo`, `hi`, `flagged` | The series inside its expected band, flagged points highlighted |
| `series_line` | Custom tests and investigation observations | `t`, `v` | One series over time |

**Card contents.** Headline, explanation, a stats line built from `stats` ("ρ 0.62 at lag +1 day · n 180 · q 0.03"), caveats, the chart, and a "template" badge when `narrated_by = 'template'`.

Investigation cards also carry an "exploratory" label, link each number to the step it came from, and show which workflow version produced them.

**Acceptance checks.**

- With the LLM stubbed to fail, every finding still appears, with template text.
- A finding whose effect changes sign between runs is re-narrated.
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
- **Project:** the goal; run status per workflow; the funnel line ("tested 330 · 41 under p 0.05 · 3 passed correction · 2 kept"); statistical cards, then exploratory cards; a "Workflows" panel with each kind's active version, rationale and script or steps, and its history of versions and review decisions with reasons; today's regeneration count against the cap; a regenerate button per kind.

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
| B. Analysis | Go | `internal/analysis` (`INPUT` builder, `ScriptResult` validation, correction, health), the `pyrunner.Runner` interface (no implementation), investigation replay, the example script and fixture script results | Correction and validation tests against a fake runner, no database | The `ScriptResult` and `AnalysisResult` contracts; the runner's interface |
| C. Agents | Go | `internal/llm`, author, investigator (including the `run_python` prompt rules), reviewer, narrator | Author on fixture profiles | Contracts; B's `internal/analysis` |
| D. Frontend | TypeScript | Everything in `svc/frontend/`, including workflow history and the live step view | Pages against the API stubs' fixture JSON | API response shapes |

The Python runner is not one of these workstreams: another engineer is building it as a generic code runner. B defines only the Go interface for it and works against a fake runner until the implementation is wired in.

With three people, drop the Sources page (load sources with `cmd/seed`), and A takes the frontend once the M1 data gate passes.

**Milestone gates.**

- [ ] **M0, hours 0–2.** Postgres, the Go service and the Python runner run locally under Skaffold, and migrations apply.
- [ ] **M0.** `internal/contracts` defines `SourceProfile`, `ScriptResult`, `AnalysisResult`, `WorkflowDraft`, `Narration`, `InvestigationReport`, `ReplayResult` and `ReviewDecision`; Go round-trips the fixtures in `internal/contracts/testdata/`; the runner's interface is agreed.
- [ ] **M0.** Every endpoint returns fixture JSON, and the frontend renders a fixture project page.
- [ ] **M0.** The demo dataset is chosen and downloaded.
- [ ] **M1, hours 2–8.** CSV and RSS adapters ingest with deduplication; the Sources page lists real sources.
- [ ] **M1.** `internal/analysis` passes its acceptance checks, and the runner executes a script with `INPUT` and returns its `result`.
- [ ] **M1.** The profiler labels the demo dataset's fields correctly.
- [ ] **M1.** The example script, adapted by hand to the demo dataset, yields at least one finding that survives correction. If not, change the dataset now.
- [ ] **M2, hours 8–13.** Creating a project generates the statistical workflow and shows cards with charts, updating live over SSE.
- [ ] **M3, hours 13–19.** The investigation workflow generates and replays on new data, and the reviewer keeps or regenerates both kinds with reasons shown.
- [ ] **M4, hours 19–22.** Demo replay mode, error states, cached narration and polish are done. Feature freeze at hour 22.
- [ ] **Hours 22–24.** Rehearse the demo twice and record a backup video.

## Rules for the coding agent

1. The migrations and `internal/contracts` are frozen after M0. Propose changes to them and wait for a human.
2. Never execute model output or splice it into code or SQL, except model-written Python (author scripts and the investigator's run\_python code), which runs only in the Python runner's child processes (hackathon only). Don't write Python yourself, apart from the example script in the author prompt (prompt material, run only in tests): statistics bookkeeping, validation and correction are Go. An LLM only ever produces JSON that is unmarshalled into a contract struct and checked by `Validate()`.
3. Every LLM call goes through `internal/llm`: JSON-schema structured outputs derived from the contract struct, a 60 s context timeout, one retry, and a JSONL log line with purpose, model, latency, tokens and validation result.
4. Model ids are read by `internal/config` from `LLM_MODEL_*` env vars, which are set in `backend.env` in `chart/values.yaml` (profiler and narrator `gpt-4.1-mini`; author, investigator and reviewer `gpt-4.1`). Never hardcode them elsewhere.
5. Model-written code reads data only through the `INPUT` that Go builds, which holds only the project's sources. The runner has no database access and holds no API keys or other secrets.
6. Regeneration limits (`REVIEW_MIN_INTERVAL`, `MAX_REGENERATIONS`), correction thresholds (`FDR_Q`, `MIN_N`, `MAX_TESTS`) and the reviewer's reason lists live in code and config, not in prompts.
7. The script reviewer's input, and the author's retry prompt, never include tests, p-values, findings or the funnel.
8. Every goroutine loop recovers panics per item, logs with `slog` and carries on.
9. Tests never call the live network or the real LLM. Go tests use recorded fixtures and `httptest`, plus a stub client enabled by `LLM_STUB=1`, and a fake runner returning fixture results. Tests against the real runner are added once its implementation exists.
10. Secrets (the LLM API key, delivered as `OPENAPI_KEY` from the `openai-key` Kubernetes secret, and `DATABASE_URL`) are never committed. The runner receives neither.
11. A milestone is done when `make test` (`go test ./...`, plus the runner's own tests) passes and its gate items are ticked.

## Demo plan

The demo shows two agents planning once, their code re-running live on new data, and the reviewer deciding when to regenerate.

1. Add a CSV and an RSS source live.
2. Create a project with a one-sentence goal. Watch the investigator's steps stream in, then open "Workflows" to show the script, the investigation and their rationales.
3. Start a replay: statistical cards update on every run, and exploratory cards refresh their numbers from replayed steps.
4. Add a source to the project mid-replay: the statistical workflow regenerates, and its history shows why.
5. Point at the funnel line, then at one strong-looking candidate that failed correction.

**Dataset.** Choose a pair with a known, explainable lagged relationship and at least a year of daily data, for example a city's daily weather and its open bike-hire counts, plus a news RSS feed as the text source. Check each dataset's licence. Decide by hour 2, because the M1 gate depends on it.

**Before presenting.** Pre-generate both workflows, run the full replay once so narrations are cached by `finding_key` and direction, and keep the backup video ready.

## Risks

| Risk | Mitigation |
| --- | --- |
| The demo dataset has no stable finding | Check by hand at M1; swap datasets before hour 8 |
| Investigations slip past hour 19 | The statistical workflow works alone from M2; demo without investigations if needed |
| LLM latency or rate limits on stage | Pre-generated workflows, cached narration, template fallback; replay runs make no LLM calls |
| Regenerating scripts until something passes (p-hacking) | The script reviewer and the author's retries never see results; reasons from a fixed list; 3 regenerations per day |
| A script reports only the tests that look good, so correction is bypassed | The prompt requires every test and forbids filtering by p or effect; the example script reports every lag; the script is fixed between regenerations and visible in the Workflows panel; `tests_run` is on the dashboard |
| The reviewer regenerates too often, so cards churn | `keep` by default, 60 s minimum interval, daily cap, every decision logged |
| The investigator runs long or loops | 12 iterations, 30 s `run_python` timeout, one generation per project at a time |
| Exploratory findings read as proven | Exploratory label and every number linked to its step; causal wording is discouraged in the prompt only |
| Optimistic p-values from autocorrelation | The prompt and example script correlate differenced series; not enforced; a permutation test in the example script as stretch |
| Live feeds don't change during the demo slot | Replay mode |
| Card text contradicts its numbers, e.g. says "move together" when ρ is negative | Re-narrate on sign changes; figures always render from `stats`; the verifier is the first stretch item to add back |
| Model-written Python runs unsandboxed on the demo machine; RSS text could carry prompt injection that turns into code | Trusted feeds only; the runner has no secrets and no database access; 60 s / 30 s timeouts and 1 GB memory limit; demo not exposed beyond the laptop or venue network |
| A script runs cleanly but analyses the wrong thing (a wrong aggregation, a filter that leaks future data) | `INPUT` never holds records past the watermark; Go applies correction; the dry run and result validation catch errors; the script and rationale are visible in the Workflows panel |

## Open questions

- Which demo dataset? Decide by hour 2.
- Three people or four, and who takes which workstream?
- The `pyrunner.Runner` interface (Analysis script): agree it with the runner's engineer, especially an untruncated `Result`, NaN rejected, and errors reserved for runner failures.

## Sources

- [Free Twitter API (X API) in 2026: What Is Free and What Is Not](https://apiserpent.com/blog/free-twitter-x-api), Serpent API, read 2026-11-07
