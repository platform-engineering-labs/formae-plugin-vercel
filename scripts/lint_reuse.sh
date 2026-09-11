#!/bin/bash
# © 2026 Platform Engineering Labs Inc.
#
# SPDX-License-Identifier: FSL-1.1-ALv2
#
# Check REUSE license compliance (optional locally, required in CI).

if ! command -v reuse >/dev/null 2>&1; then
    echo "REUSE tool not installed. Install with: pipx install reuse"
    echo "Skipping local check - CI will catch any issues."
    exit 0
fi

echo "Checking REUSE compliance..."
reuse lint
