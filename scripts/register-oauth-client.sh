#!/bin/bash
# Register agentserver as an OAuth client in Hydra.
# Run after Hydra is up: docker compose exec hydra sh -c "$(cat scripts/register-oauth-client.sh)"
#
# Or run from host with Hydra admin accessible:
#   HYDRA_ADMIN_URL=http://localhost:4445 AGENTSERVER_REDIRECT_URI=https://your-agentserver/api/auth/modelserver/callback ./scripts/register-oauth-client.sh

set -euo pipefail

HYDRA_ADMIN_URL="${HYDRA_ADMIN_URL:-http://127.0.0.1:4445}"
CLIENT_ID="${CLIENT_ID:-agentserver}"
CLIENT_SECRET="${CLIENT_SECRET:-agentserver-oauth-secret-change-me}"
AGENTSERVER_REDIRECT_URI="${AGENTSERVER_REDIRECT_URI:-https://localhost:8080/api/auth/modelserver/callback}"

echo "Registering OAuth client '${CLIENT_ID}' in Hydra..."

# Delete existing client if present (idempotent)
curl -s -o /dev/null -w "" -X DELETE \
  "${HYDRA_ADMIN_URL}/admin/clients/${CLIENT_ID}" 2>/dev/null || true

# Create client
curl -s -X POST "${HYDRA_ADMIN_URL}/admin/clients" \
  -H "Content-Type: application/json" \
  -d "{
    \"client_id\": \"${CLIENT_ID}\",
    \"client_secret\": \"${CLIENT_SECRET}\",
    \"redirect_uris\": [\"${AGENTSERVER_REDIRECT_URI}\"],
    \"grant_types\": [\"authorization_code\", \"refresh_token\"],
    \"response_types\": [\"code\"],
    \"scope\": \"project:llm offline_access\",
    \"token_endpoint_auth_method\": \"client_secret_post\"
  }" | python3 -m json.tool 2>/dev/null || cat

echo ""
echo "Done. Client '${CLIENT_ID}' registered."
echo "  Redirect URI: ${AGENTSERVER_REDIRECT_URI}"
echo "  Secret:       ${CLIENT_SECRET}"
