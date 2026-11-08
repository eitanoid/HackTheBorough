# Anduin

Just like the great river that spans all of Middle-earth, Anduin seamlessly
integrates heterogeneous data flows with real-time AI synthesis, dynamically
generating statistical analyses to expose unseen correlations and deliver
actionable intelligence.

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

`make dev` keeps running and refreshes on code edits.

By default the following endpoints are exposed:

- Frontend: <http://localhost:8080>
- Backend health check: <http://localhost:8081/healthz>
- Backend API: <http://localhost:8081/api/hello>

The Helm install does not seed sources. Register them through the REST
examples in [Local ingestion backend](#local-ingestion-backend).


## Local ingestion backend

The Go backend includes the polling loop and the `rss`, CSV, and `web`
ingestion adapters. Web sources use Crawl4AI's schema-constrained LLM extraction and can
follow next-page or numbered pagination; records are deduplicated by their
stable external ID before being written to `data_records`.

Register the demo sources:

```sh
cd svc/backend
kpf deployments/hacktheburgh-postgres 5432:5432
export DATABASE_URL='postgres://hacktheburgh:hacktheburgh@127.0.0.1:5432/hacktheburgh?sslmode=disable'
go run ./cmd/seed
curl http://127.0.0.1:8081/api/sources
# Force one immediate poll; normal polling runs every 300 seconds.
curl -X POST http://127.0.0.1:8081/api/sources/<source-uuid>/poll
```

hey
