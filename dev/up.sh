#!/usr/bin/env bash
set -euo pipefail

if ! kind get clusters | grep -qx hacktheborough; then
  kind create cluster --config dev/kind.yaml --wait 60s
else
  echo "kind cluster 'hacktheborough' already exists"
fi
