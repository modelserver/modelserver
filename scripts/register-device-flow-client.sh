#!/bin/bash
# Register a dedicated device flow OAuth client in Hydra.
#
# Usage:
#   HYDRA_ADMIN_URL=http://localhost:4445 \
#   MODELSERVER_BASE_URL=https://codeapi.example.com \
#   DEVICE_FLOW_CLIENT_ID=device-flow-client \
#   DEVICE_FLOW_CLIENT_SECRET=change-me \
#   ./scripts/register-device-flow-client.sh

set -euo pipefail

HYDRA_ADMIN_URL="${HYDRA_ADMIN_URL:-http://127.0.0.1:4445}"
DEVICE_FLOW_CLIENT_ID="${DEVICE_FLOW_CLIENT_ID:-device-flow-client}"
DEVICE_FLOW_CLIENT_SECRET="${DEVICE_FLOW_CLIENT_SECRET:-device-flow-secret-change-me}"
MODELSERVER_BASE_URL="${MODELSERVER_BASE_URL:-https://localhost:8081}"

REDIRECT_URI="${MODELSERVER_BASE_URL}/oauth/device/callback"

echo "Registering device flow OAuth client '${DEVICE_FLOW_CLIENT_ID}' in Hydra..."

# Delete existing client if present (idempotent)
curl -s -o /dev/null -w "" -X DELETE \
  "${HYDRA_ADMIN_URL}/admin/clients/${DEVICE_FLOW_CLIENT_ID}" 2>/dev/null || true

# Create client
curl -s -X POST "${HYDRA_ADMIN_URL}/admin/clients" \
  -H "Content-Type: application/json" \
  -d "{
    \"client_id\": \"${DEVICE_FLOW_CLIENT_ID}\",
    \"client_name\": \"Device Flow\",
    \"client_secret\": \"${DEVICE_FLOW_CLIENT_SECRET}\",
    \"redirect_uris\": [\"${REDIRECT_URI}\"],
    \"grant_types\": [\"authorization_code\", \"refresh_token\"],
    \"response_types\": [\"code\"],
    \"scope\": \"project:inference offline_access\",
    \"token_endpoint_auth_method\": \"client_secret_post\"
  }" | python3 -m json.tool 2>/dev/null || cat

echo ""
echo "Done. Device flow client '${DEVICE_FLOW_CLIENT_ID}' registered."
echo "  Redirect URI: ${REDIRECT_URI}"
echo "  Secret:       ${DEVICE_FLOW_CLIENT_SECRET}"
