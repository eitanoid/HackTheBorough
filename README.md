# Anduin

Just like the great river that spans all of Middle-earth, Anduin seamlessly
integrates heterogeneous data flows with real-time AI synthesis, dynamically
generating statistical analyses to expose unseen correlations and deliver
actionable intelligence.

## Description

Anduin is a general-purpose, autonomous data-flow platform that gathers
information from configured sources, including websites and live data feeds. It
analyzes data across sources to uncover correlations and hidden connections
between events, then turns its findings into clear, human-readable
insights—helping users spot important developments as they emerge.

## Architecture

Frontend React with NextJS, Backend Golang, Protobuf ConnectRPC API, Kubernetes deployments, Crawl4AI web scraper.

![](assets/architecture.png)

## Local Kubernetes development

Requirements:
- Infra: Docker, kubectl, kind, Skaffold, Helm, and yq.
- API: buf, protoc.

### Secrets

Replace the placeholder secrets in `chart/templates/secrets` before deploying. Do not commit real
credentials.

### Deploying

The `frontend` (React) and `backend` (Go) images are built locally by Skaffold.
The Helm chart in `chart/` also deploys PostgreSQL and Crawl4AI to the kind
cluster. No image registry is needed.

```sh
make setup      # create the kind cluster and deploy once
make dev        # create the cluster if needed; deploy and watch for code changes
make teardown   # delete the entire kind cluster and its workloads
make delete     # uninstall the helm release but keep the cluster

```

`make dev` keeps running and rebuilds/redeploys the affected image when its
source changes.

By default the following endpoints are exposed:

- Frontend: <http://localhost:8080>

- Backend health check: <http://localhost:8081/healthz>
- Backend API: <http://localhost:8081/api/hello>
- Postgres Database: <localhost:8082>

The Helm install does not seed sources. Create and manage projects and sources
through Connect RPC. Configured sources are polled automatically at their
selected intervals.

For manual Connect RPC checks, open [`tests/http/anduin.http`](tests/http/anduin.http)
with httpyac while the backend and database are running (`cd tests/http && httpyac send anduin.http`).
The file covers source/project CRUD, CSV upload, web source configuration,
forced statistical reruns, and the currently unconfigured workflow regeneration
and demo replay RPCs.

### Statistical runner settings

The runner sweeps projects every five minutes and debounces `source_updated`
events for 30 seconds by default. Override these intervals in seconds with
`RUN_SWEEP_S` and `RUN_DEBOUNCE_S`. `FDR_Q` controls the Benjamini–Hochberg
false-discovery threshold and defaults to `0.1` (`MIN_N` defaults to `30`).

To run the active statistical workflow again without waiting for new data, call
the Connect RPC `RunWorkflow` with a project ID and
`WORKFLOW_KIND_STATISTICAL`. It queues a run of the existing workflow; it does
not regenerate the analysis script. A linked source deletion removes that
source's records and clears workflow results for affected projects; those
projects remain and can rebuild analysis if they still have data.


## Local ingestion backend

The Go backend includes the polling loop and the `rss`, CSV, and `web`
ingestion adapters. Web sources use Crawl4AI's schema-constrained LLM extraction and can
follow next-page or numbered pagination; records are deduplicated by their
stable external ID before being written to `data_records`.

Register the demo sources:

```sh
cd svc/backend
export DATABASE_URL='postgres://hacktheburgh:hacktheburgh@127.0.0.1:8082/hacktheburgh?sslmode=disable'
go run ./cmd/seed
curl -sS http://127.0.0.1:8081/hacktheburgh.v1.AnduinService/ListSources \
  -H 'Content-Type: application/json' -d '{}'
```

The seed sources are picked up by the normal ingestion loop according to their
configured polling intervals.
