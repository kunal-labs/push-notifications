#!/usr/bin/env bash
set -euo pipefail

echo "Running repository tidiness check..."

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

# 1. Verify Makefile recipes are strictly single-line @./scripts/<target>.sh
if [[ ! -f "Makefile" ]]; then
  echo "ERROR: Makefile not found at repository root" >&2
  exit 1
fi

# Check for banned inline logic in Makefile
if grep -E '(\bif\b|\bfor\b|&&|;|\|)' Makefile | grep -v '^\s*#' | grep -v '^\.PHONY' >/dev/null; then
  echo "ERROR: Inline logic detected in Makefile. Makefile must only route to scripts/*.sh" >&2
  exit 1
fi

# 2. Extract targets and verify their scripts
TARGETS=$(grep -E '^[a-zA-Z0-9_-]+:' Makefile | cut -d: -f1)

for TARGET in $TARGETS; do
  SCRIPT="scripts/${TARGET}.sh"
  if [[ ! -f "$SCRIPT" ]]; then
    echo "ERROR: Target '$TARGET' does not have corresponding script '$SCRIPT'" >&2
    exit 1
  fi
  if [[ ! -x "$SCRIPT" ]]; then
    echo "ERROR: Script '$SCRIPT' is not executable (missing chmod +x)" >&2
    exit 1
  fi
  FIRST_LINE=$(head -n 1 "$SCRIPT")
  if [[ "$FIRST_LINE" != "#!/usr/bin/env bash" ]]; then
    echo "ERROR: Script '$SCRIPT' must start with '#!/usr/bin/env bash'" >&2
    exit 1
  fi
  if ! grep -q "set -euo pipefail" "$SCRIPT"; then
    echo "ERROR: Script '$SCRIPT' must include 'set -euo pipefail'" >&2
    exit 1
  fi
done

# 3. Verify README.md documents all make targets
if [[ ! -f "README.md" ]]; then
  echo "ERROR: README.md not found" >&2
  exit 1
fi

for TARGET in $TARGETS; do
  if ! grep -q "make ${TARGET}" README.md; then
    echo "ERROR: Target 'make ${TARGET}' is not documented in README.md" >&2
    exit 1
  fi
done

echo "Repository tidiness check passed successfully."
