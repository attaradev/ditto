#!/usr/bin/env bash
# post-job.sh — ditto post-job hook for GitHub Actions self-hosted runners.
#
# Install path: /home/runner/hooks/post-job.sh
# Required env var in runner service unit:
#   ACTIONS_RUNNER_HOOK_JOB_COMPLETED=/home/runner/hooks/post-job.sh
#
# Runs after every job regardless of outcome. Destroys the copy provisioned
# by pre-job.sh if DITTO_COPY_ID is set.

set -euo pipefail

if [[ -z "${DITTO_COPY_ID:-}" ]]; then
  exit 0
fi

echo "ditto: destroying copy ${DITTO_COPY_ID}" >&2
ditto copy delete "${DITTO_COPY_ID}" || true
