# hello

## Helm chart rules

- In `chart/templates/backend.yaml`, the backend container's env block must stay exactly as below. Never remove or replace it with hardcoded `env:` entries.

  ```yaml
  {{- if .Values.backend.env }}
  env: {{ toYaml .Values.backend.env | nindent 10 }}
  {{- end }}
  ```

- Add backend env vars (including the `LLM_*` ones) as entries in `backend.env` in `chart/values.yaml`. Don't set them from skaffold, and don't add ConfigMaps or `envFrom` for them.
