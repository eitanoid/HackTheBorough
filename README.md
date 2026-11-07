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
