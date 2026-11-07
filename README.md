# TTE-HackTheBurgh

Requirements:
- Infra: Docker, kubectl, kind, Skaffold, and Helm.
- API: buf, protoc.

## Local Kubernetes development

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
