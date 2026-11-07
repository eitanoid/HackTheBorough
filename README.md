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

The chart uses the `openai-key` Secret (`API_KEY`) for OpenAI and the
`hacktheburgh-crawl4ai` Secret (`CRAWL4AI_API_TOKEN`) for Crawl4AI bearer
authentication. Replace the placeholders in
`chart/templates/secrets/openai-key.yaml` and `chart/values.yaml` before
deploying. Do not commit real credentials.

### Deploying

The `frontend` (React) and `backend` (Go) images are built locally by
Skaffold. The Helm chart in `chart/` also deploys PostgreSQL and Crawl4AI
(`unclecode/crawl4ai:0.9.5`) to the `hacktheburgh` kind cluster. No image
registry is needed.

Crawl4AI needs at least 4 GB available to its container. On a smaller Docker
VM web ingestion will be unreliable; RSS and CSV polling still work. Skaffold
does not build Crawl4AI; the kind node pulls the pinned upstream image.

```sh
make setup      # create the kind cluster and deploy once
make dev        # create the cluster if needed; deploy and watch for code changes
make teardown   # delete the entire kind cluster and its workloads
```

`make dev` keeps running: edit frontend files under `frontend/src/` for Vite
HMR, or Go files under `backend/` for Air to rebuild and restart the API.

- Frontend: <http://localhost:8080>
- Backend health check: <http://localhost:8081/healthz>
- Backend API: <http://localhost:8081/api/hello>

The Helm install does not seed sources. Register them through the REST
examples in [Local ingestion backend](#local-ingestion-backend).

Use `make delete` to uninstall the Helm release but keep the cluster.

## Local ingestion backend

The Go backend includes the polling loop and the `rss`, CSV, and `web`
ingestion adapters. Web sources use Crawl4AI's schema-constrained LLM extraction and can
follow next-page or numbered pagination; records are deduplicated by their
stable external ID before being written to `data_records`.

Requirements: Docker, Go, a running Crawl4AI API, and an OpenAI key available
to Crawl4AI.

```sh
# One-time local Postgres container
docker run -d --name anduin-postgres \
  -e POSTGRES_USER=hacktheburgh -e POSTGRES_PASSWORD=hacktheburgh \
  -e POSTGRES_DB=hacktheburgh -p 127.0.0.1:5432:5432 postgres:16

cd svc/backend
export DATABASE_URL='postgres://hacktheburgh:hacktheburgh@127.0.0.1:5432/hacktheburgh?sslmode=disable'
export CRAWL4AI_API='http://127.0.0.1:11235'
export CRAWL4AI_API_TOKEN='<crawl4ai-token>'
# Crawl4AI performs the LLM call. Start it with OPENAI_API_KEY set; the
# backend only sends the provider name.
export CRAWL4AI_PROVIDER='openai/gpt-4o-mini'

go run ./cmd/server
```

In a second shell, optionally register the demo polling sources (Yahoo Finance
through LLM extraction, the Trump’s Truth RSS feed, and the OWID crude-oil
CSV). The Helm chart does not run this seed:

```sh
cd svc/backend
export DATABASE_URL='postgres://hacktheburgh:hacktheburgh@127.0.0.1:5432/hacktheburgh?sslmode=disable'
go run ./cmd/seed
curl http://127.0.0.1:8080/api/sources
# Force one immediate poll; normal polling runs every 300 seconds.
curl -X POST http://127.0.0.1:8080/api/sources/<source-uuid>/poll
```
