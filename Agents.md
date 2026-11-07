# hello

## Helm chart rules

- In `chart/templates/backend.yaml`, the backend container's env block must stay exactly as below. Never remove or replace it with hardcoded `env:` entries; add plain env vars to `backend.env` in `chart/values.yaml`.

  ```yaml
  {{- if .Values.backend.env }}
  env: {{ toYaml .Values.backend.env | nindent 10 }}
  {{- end }}
  ```

- Env vars that skaffold fills in (the `LLM_*` ones) live under `backend.llm` in `values.yaml`, are set in `skaffold.yaml` `setValueTemplates`, and reach the container through the `<release>-backend-llm` ConfigMap via `envFrom`. Don't set list items like `backend.env[N]` from skaffold: Helm's `--set` replaces the whole list.
