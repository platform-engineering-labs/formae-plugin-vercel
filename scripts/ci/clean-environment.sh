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

echo "clean-environment.sh: done"
