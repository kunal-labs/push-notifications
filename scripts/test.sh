#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

echo "Running repository verification and test suite..."
timeout -k 5 30s "$REPO_ROOT/scripts/tidy-check.sh"

# G1: Zero Mermaid or D2 fenced diagrams in docs, README, or code/UI
echo "Running G1 diagram fence check..."
G1_TARGETS=("README.md" "docs")
for dir in cmd internal; do
  if [[ -d "$dir" ]]; then
    G1_TARGETS+=("$dir")
  fi
done
if grep -rnE '```[[:space:]]*(mermaid|d2)\b' "${G1_TARGETS[@]}" >/dev/null 2>&1; then
  echo "ERROR: Prohibited Mermaid or D2 diagram fence detected (G1 violation)." >&2
  grep -rnE '```[[:space:]]*(mermaid|d2)\b' "${G1_TARGETS[@]}" >&2 || true
  exit 1
fi

# TI-3 / N1: Zero internal Charter/ADR/Issue ID codes in user-facing surfaces
echo "Running TI-3 zero-internal-ID hygiene check..."
TI3_ID_REGEX='\b(ADR(-[0-9]+)?|AD-[0-9]+|[TDNGX][1-9]|FR-[0-9]+|NFR-[0-9]+|INV-[0-9]+)\b'
TI3_ISSUE_REGEX='(^|[^a-zA-Z0-9_&#])#[1-9][0-9]?([^a-fA-F0-9]|$)'

check_ti3_stream() {
  local label="$1"
  local content
  content="$(cat)"
  if printf '%s\n' "$content" | grep -nE "$TI3_ID_REGEX" >/dev/null 2>&1; then
    echo "ERROR: Internal ID code detected in ${label} (TI-3 violation):" >&2
    printf '%s\n' "$content" | grep -nE "$TI3_ID_REGEX" >&2 || true
    exit 1
  fi
  if printf '%s\n' "$content" | grep -nE "$TI3_ISSUE_REGEX" >/dev/null 2>&1; then
    echo "ERROR: Issue reference detected in ${label} (TI-3 violation):" >&2
    printf '%s\n' "$content" | grep -nE "$TI3_ISSUE_REGEX" >&2 || true
    exit 1
  fi
}

check_ti3_stream "README.md" < "$REPO_ROOT/README.md"
if [[ -f "$REPO_ROOT/internal/ui/static/index.html" ]]; then
  check_ti3_stream "internal/ui/static/index.html" < "$REPO_ROOT/internal/ui/static/index.html"
fi

if [[ -f "$REPO_ROOT/go.mod" && -f "$REPO_ROOT/cmd/sideplane-demo/main.go" ]]; then
  CLI_OUTPUT="$(timeout -k 5 30s go run ./cmd/sideplane-demo --mode=cli)"
  printf '%s\n' "$CLI_OUTPUT" | check_ti3_stream "CLI --mode=cli output"
fi

# G4: Optional runtime-injected forbidden customer/domain pattern scan
if [[ -n "${G4_FORBIDDEN_REGEX:-}" ]]; then
  echo "Running G4 runtime pattern scan..."
  if grep -rnE --exclude-dir=.git --exclude-dir=bin "$G4_FORBIDDEN_REGEX" "$REPO_ROOT" >/dev/null 2>&1; then
    echo "ERROR: Forbidden pattern matched G4_FORBIDDEN_REGEX." >&2
    exit 1
  fi
fi

if [[ -f "$REPO_ROOT/go.mod" ]]; then
  echo "Running Go race test suite..."
  timeout -k 5 60s go test -race -v ./...
else
  echo "Unit test suite: No go.mod found yet."
fi
