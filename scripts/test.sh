#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

echo "Running repository verification and test suite..."
timeout -k 5 30s "$REPO_ROOT/scripts/check-repo-tidiness.sh"

if [[ -f "$REPO_ROOT/go.mod" ]]; then
  timeout -k 5 60s go test -v -race ./...
else
  echo "Unit test suite: No Go test files found yet. Basic verification passed."
fi
