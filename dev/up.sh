#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
storage_dir="$repo_root/dev/storage"
mkdir -p "$storage_dir/postgres"

if ! kind get clusters | grep -qx hacktheburgh; then
  # dynamically set the storage path for the local mount
  kind_config="$(mktemp)"
  trap 'rm -f "$kind_config"' EXIT
  cp "$repo_root/dev/kind.yaml" "$kind_config"
  DEV_STORAGE_PATH="$storage_dir" yq -i '.nodes[0].extraMounts[0].hostPath = strenv(DEV_STORAGE_PATH)' "$kind_config"
  kind create cluster --config "$kind_config" --wait 60s
else
  echo "kind cluster 'hacktheburgh' already exists"
  echo "If this cluster predates the dev/storage mount, recreate it with 'make teardown' and 'make cluster-up'."
fi
