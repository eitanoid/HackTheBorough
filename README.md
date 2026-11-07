# Anduin

Just like the great river that spans all of Middle-earth, Anduin seamlessly
integrates heterogeneous data flows with real-time AI synthesis, dynamically
generating statistical analyses to expose unseen correlations and deliver
actionable intelligence.

Inspired by Palantir

## Local Kubernetes development

Requirements:
- Infra: Docker, kubectl, kind, Skaffold, Helm, and Mike Farah yq v4.
- API: buf, protoc.

### Secrets

Replace the placeholder values in `chart/templates/secrets/` with your actual
credentials. Do not commit real credentials. 

### Deploying

The `frontend` (React) and `backend` (Go) images are built locally by
Skaffold and deployed with the Helm chart in `chart/` to the `hacktheburgh`
kind cluster. No image registry is needed.

```sh
make setup      # create the kind cluster and deploy both services
make dev        # create the cluster if needed; deploy and watch for code changes
make teardown   # delete the entire kind cluster and its workloads
```

`make dev` keeps running: edit frontend files under `frontend/src/` for Vite
HMR, or Go files under `backend/` for Air to rebuild and restart the API.

- Frontend: <http://localhost:8080>
- Backend health check: <http://localhost:8081/healthz>
- Backend API: <http://localhost:8081/api/hello>

Use `make delete` to uninstall the Helm release but keep the cluster.

## Local ingestion backend

The Go backend includes the polling loop and the `rss` and `web` ingestion
adapters. Web sources use Crawl4AI's schema-constrained LLM extraction and can
follow next-page or numbered pagination; records are deduplicated by their
stable external ID before being written to `data_records`.

Requirements: Docker, Go, a running Crawl4AI API, and an LLM provider supported
by Crawl4AI.

```sh
# One-time local Postgres container
docker run -d --name anduin-postgres \
  -e POSTGRES_USER=hacktheburgh -e POSTGRES_PASSWORD=hacktheburgh \
  -e POSTGRES_DB=hacktheburgh -p 127.0.0.1:5432:5432 postgres:16

cd svc/backend
export DATABASE_URL='postgres://hacktheburgh:hacktheburgh@127.0.0.1:5432/hacktheburgh?sslmode=disable'
export CRAWL4AI_API='http://127.0.0.1:11235'
export CRAWL4AI_API_TOKEN='<crawl4ai-token>'
# Optional provider override; choose a model with enough available credits.
export CRAWL4AI_PROVIDER='openrouter/microsoft/phi-4'

go run ./cmd/server
```

In a second shell, register the three demo polling sources (BleepingComputer
and Yahoo Finance through LLM extraction, and the Trump’s Truth RSS feed):

```sh
cd svc/backend
export DATABASE_URL='postgres://hacktheburgh:hacktheburgh@127.0.0.1:5432/hacktheburgh?sslmode=disable'
go run ./cmd/seed
curl http://127.0.0.1:8080/api/sources
# Force one immediate poll; normal polling runs every 300 seconds.
curl -X POST http://127.0.0.1:8080/api/sources/<source-uuid>/poll
```

BleepingComputer's direct page is protected by a Cloudflare challenge, so its
seed uses Jina Reader as the page transport. Extraction still runs through
Crawl4AI's LLM job endpoint and the returned article links point to
BleepingComputer.
