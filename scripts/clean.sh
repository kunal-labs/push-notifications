#!/usr/bin/env bash
set -euo pipefail

echo "Cleaning temporary build artifacts and test caches..."
rm -rf bin/ tmp/ coverage.out
if command -v go >/dev/null 2>&1; then
  go clean -cache -testcache 2>/dev/null || true
fi
echo "Clean completed."
