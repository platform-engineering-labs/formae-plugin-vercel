#!/bin/bash
# © 2026 Platform Engineering Labs Inc.
# SPDX-License-Identifier: FSL-1.1-ALv2
#
# Clean Environment Hook for the Vercel plugin.
#
# Called before AND after conformance tests. Deletes every Vercel project whose
# name starts with the test prefix; a project's environment variables go with
# it, so they need no separate pass.
#
# Required env:
#   VERCEL_TOKEN (or VERCEL_API_TOKEN)  Vercel access token
#
# Optional:
#   VERCEL_TEAM_ID   Team to clean; omit to clean the token's personal account
#   TEST_PREFIX      Project name prefix (default: "formae-sdk-test")
#   VERCEL_API_BASE  Default: https://api.vercel.com
#
# Idempotent: missing resources are not an error.

set -euo pipefail

TEST_PREFIX="${TEST_PREFIX:-formae-sdk-test}"
API_BASE="${VERCEL_API_BASE:-https://api.vercel.com}"
TOKEN="${VERCEL_TOKEN:-${VERCEL_API_TOKEN:-}}"

if [[ -z "${TOKEN}" ]]; then
  echo "clean-environment.sh: VERCEL_TOKEN/VERCEL_API_TOKEN unset — skipping cleanup"
  exit 0
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "clean-environment.sh: jq not found — skipping cleanup" >&2
  exit 0
fi

# Team scope is a query parameter on every endpoint.
TEAM_QS=""
if [[ -n "${VERCEL_TEAM_ID:-}" ]]; then
  TEAM_QS="teamId=${VERCEL_TEAM_ID}"
fi

# api <method> <path-with-leading-slash> [extra curl args...]
api() {
  local method="$1" path="$2"
  shift 2
  local url="${API_BASE}${path}"
  if [[ -n "${TEAM_QS}" ]]; then
    if [[ "${url}" == *\?* ]]; then url="${url}&${TEAM_QS}"; else url="${url}?${TEAM_QS}"; fi
  fi
  curl --silent --show-error \
    --request "${method}" \
    --header "Authorization: Bearer ${TOKEN}" \
    --header "Accept: application/json" \
    "$@" "${url}"
}

echo "clean-environment.sh: cleaning Vercel projects with prefix '${TEST_PREFIX}'"

# GET /v10/projects answers either a bare array or {projects: [...]}; `..|.id?`
# would over-match nested objects, so select the right container explicitly.
project_ids=$(api GET "/v10/projects?limit=100" \
  | jq -r --arg p "${TEST_PREFIX}" '
      (if type == "array" then . else .projects // [] end)
      | .[]? | select(.name? // "" | startswith($p)) | .id' || true)

if [[ -z "${project_ids}" ]]; then
  echo "  no matching projects"
else
  for id in ${project_ids}; do
    echo "  DELETE project ${id}"
    api DELETE "/v9/projects/${id}" >/dev/null || true
  done
fi

# --- Account-level resources -------------------------------------------------
# Projects take their children with them, but Global Configs and webhooks are
# account-scoped and would otherwise leak between runs.

echo "  global configs..."
gc_ids=$(api GET "/v1/global-config" \
  | jq -r --arg p "${TEST_PREFIX}" '
      (if type == "array" then . else (.configs // .globalConfigs // []) end)
      | .[]? | select(.slug? // "" | startswith($p)) | .id' || true)
for id in ${gc_ids}; do
  echo "    DELETE global-config ${id}"
  api DELETE "/v1/global-config/${id}" >/dev/null || true
done

echo "  webhooks..."
wh_ids=$(api GET "/v1/webhooks" \
  | jq -r '
      (if type == "array" then . else (.webhooks // []) end)
      | .[]? | select(.url? // "" | contains("formae-sdk-test")) | .id' || true)
for id in ${wh_ids}; do
  echo "    DELETE webhook ${id}"
  api DELETE "/v1/webhooks/${id}" >/dev/null || true
done

# DNS records under the test domain.
#
# Scoped hard, on purpose. Only records whose *name* starts with the test
# prefix are ever considered: the domain itself is never touched, and neither
# are its real records — the CAA set and the apex/wildcard ALIAS entries carry
# no name and cannot match. A failed DNS fixture otherwise leaves records
# behind in a domain somebody actually uses.
TEST_DOMAIN="${VERCEL_TEST_DOMAIN:-oberlayer.com}"
if [ -n "${TEST_DOMAIN}" ]; then
  echo "  dns records under ${TEST_DOMAIN}..."
  ids=$(api GET "/v5/domains/${TEST_DOMAIN}/records?limit=100" \
    | jq -r '.records[]? | select(.name != null and (.name | startswith("sdk-"))) | .id' || true)
  for id in ${ids}; do
    echo "    DELETE dns-record ${id}"
    api DELETE "/v2/domains/${TEST_DOMAIN}/records/${id}" >/dev/null || true
  done
fi

# Certificates are deliberately not reaped: they cannot be.
#
#   DELETE /v8/certs/{id}
#   400 SSL Certificates provided by the system cannot be deleted.
#
# Vercel issues a certificate itself when a domain is attached to a project,
# and refuses to delete anything it issued. Test runs therefore leave
# certificates for sdk-*.<test domain> behind. They are harmless — the names
# they cover stop resolving as soon as the fixture's project is destroyed, and
# they expire on their own — but they do accumulate, so expect a growing list
# under Settings -> Certificates on an account used for conformance runs.

echo "clean-environment.sh: done"
