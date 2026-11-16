#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "Running push-notifications sideplane-demo CLI benchmark..."
timeout -k 5 60s go run ./cmd/sideplane-demo --mode=cli "$@"
