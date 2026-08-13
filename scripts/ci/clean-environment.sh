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

echo "  access groups..."
ag_ids=$(api GET "/v1/access-groups" \
  | jq -r --arg p "${TEST_PREFIX}" '
      (if type == "array" then . else (.accessGroups // []) end)
      | .[]? | select(.name? // "" | startswith($p)) | .accessGroupId' || true)
for id in ${ag_ids}; do
  echo "    DELETE access-group ${id}"
  api DELETE "/v1/access-groups/${id}" >/dev/null || true
done

echo "  drains..."
dr_ids=$(api GET "/v1/drains" \
  | jq -r --arg p "${TEST_PREFIX}" '
      (if type == "array" then . else (.drains // []) end)
      | .[]? | select(.name? // "" | startswith($p)) | .id' || true)
for id in ${dr_ids}; do
  echo "    DELETE drain ${id}"
  api DELETE "/v1/drains/${id}" >/dev/null || true
done

# Auth tokens are account-wide and NOT team-scoped, so they are queried
# without the team parameter.
echo "  auth tokens..."
tok_ids=$(curl --silent --show-error \
    --header "Authorization: Bearer ${TOKEN}" --header "Accept: application/json" \
    "${API_BASE}/v6/user/tokens" \
  | jq -r --arg p "${TEST_PREFIX}" '
      (if type == "array" then . else (.tokens // []) end)
      | .[]? | select(.name? // "" | startswith($p)) | .id' || true)
for id in ${tok_ids}; do
  echo "    DELETE token ${id}"
  curl --silent --show-error --request DELETE \
    --header "Authorization: Bearer ${TOKEN}" \
    "${API_BASE}/v3/user/tokens/${id}" >/dev/null || true
done

echo "clean-environment.sh: done"
