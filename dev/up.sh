#!/usr/bin/env bash
set -euo pipefail

if ! kind get clusters | grep -qx hacktheburgh; then
  kind create cluster --config dev/kind.yaml --wait 60s
else
  echo "kind cluster 'hacktheburgh' already exists"
fi
