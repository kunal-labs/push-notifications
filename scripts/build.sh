#!/usr/bin/env bash
set -euo pipefail

echo "Building push-notifications delivery platform components..."
if [[ -f "go.mod" ]]; then
  timeout -k 5 60s go build ./...
else
  echo "No Go modules found yet. Build step completed."
fi
