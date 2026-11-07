#!/usr/bin/env bash
set -euo pipefail

if kind get clusters | grep -qx hacktheburgh; then
  kind delete cluster --name hacktheburgh
else
  echo "kind cluster 'hacktheburgh' does not exist"
fi
