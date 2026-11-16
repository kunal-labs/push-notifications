#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

PROJECT_ID="${PROJECT_ID:-kunal-scratch}"
REGION="${REGION:-us-central1}"
SERVICE_NAME="${SERVICE_NAME:-push-sideplane-demo}"
SANDBOX_FCM_PROJECT_ID="${SANDBOX_FCM_PROJECT_ID:-kunal-scratch}"

validate_sandbox_project() {
  local pid="$1"
  local lower
  lower="$(printf '%s' "$pid" | tr '[:upper:]' '[:lower:]')"
  if [[ "$lower" == *prod* || "$lower" == *live* ]]; then
    echo "ERROR: Refusing deployment — project '$pid' contains forbidden prod/live substring." >&2
    exit 1
  fi
  case "$pid" in
    kunal-scratch) ;;
    *)
      echo "ERROR: Refusing deployment — project '$pid' is not in sandbox allowlist [kunal-scratch]." >&2
      exit 1
      ;;
  esac
}

validate_sandbox_project "$PROJECT_ID"
validate_sandbox_project "$SANDBOX_FCM_PROJECT_ID"

echo "Deploying ${SERVICE_NAME} to Cloud Run (project=${PROJECT_ID}, region=${REGION})..."

AUTH_MODE="public"
if timeout -k 5 300s gcloud run deploy "$SERVICE_NAME" \
  --source=. \
  --project="$PROJECT_ID" \
  --region="$REGION" \
  --port=8080 \
  --allow-unauthenticated \
  --set-env-vars="SANDBOX_FCM_PROJECT_ID=${SANDBOX_FCM_PROJECT_ID}"; then
  echo "Deployed with public unauthenticated access."
else
  echo "Public --allow-unauthenticated deployment failed (likely org policy IAM restriction); falling back to --no-allow-unauthenticated..."
  AUTH_MODE="iam"
  timeout -k 5 300s gcloud run deploy "$SERVICE_NAME" \
    --source=. \
    --project="$PROJECT_ID" \
    --region="$REGION" \
    --port=8080 \
    --no-allow-unauthenticated \
    --set-env-vars="SANDBOX_FCM_PROJECT_ID=${SANDBOX_FCM_PROJECT_ID}"
fi

SERVICE_URL="$(timeout -k 5 30s gcloud run services describe "$SERVICE_NAME" \
  --project="$PROJECT_ID" \
  --region="$REGION" \
  --format='value(status.url)')"

echo "Service URL: ${SERVICE_URL} (auth_mode=${AUTH_MODE})"

CURL_AUTH_ARGS=()
if [[ "$AUTH_MODE" == "iam" ]]; then
  ID_TOKEN="$(timeout -k 5 30s gcloud auth print-identity-token)"
  CURL_AUTH_ARGS=(-H "Authorization: Bearer ${ID_TOKEN}")
fi

echo "Verifying ${SERVICE_URL}/healthz..."
timeout -k 5 30s curl -fsS "${CURL_AUTH_ARGS[@]}" "${SERVICE_URL}/healthz"
echo ""

echo "Verifying ${SERVICE_URL}/api/simulate..."
timeout -k 5 30s curl -fsS "${CURL_AUTH_ARGS[@]}" "${SERVICE_URL}/api/simulate" >/dev/null
echo "Simulation endpoint responded 200 OK."

if [[ "$AUTH_MODE" == "iam" ]]; then
  echo "For local interactive UI access behind IAM auth, run:"
  echo "  gcloud run services proxy ${SERVICE_NAME} --project=${PROJECT_ID} --region=${REGION} --port=8080"
fi
