#!/usr/bin/env bash
set -euo pipefail

if kind get clusters | grep -qx hacktheborough; then
  kind delete cluster --name hacktheborough
else
  echo "kind cluster 'hacktheborough' does not exist"
fi
