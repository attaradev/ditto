#!/usr/bin/env bash
# pre-job.sh — ditto pre-job hook for GitHub Actions self-hosted runners.
#
# Install path: /home/runner/hooks/pre-job.sh
# Required env var in runner service unit:
#   ACTIONS_RUNNER_HOOK_JOB_STARTED=/home/runner/hooks/pre-job.sh
#
# Set DITTO_ENABLED=true on a job to provision an isolated database copy.
# The hook writes DATABASE_URL and DITTO_COPY_ID to $GITHUB_ENV and masks
# the connection string from Actions logs.

set -euo pipefail

if [[ "${DITTO_ENABLED:-false}" != "true" ]]; then
  exit 0
fi

# Provision a copy. ditto copy create prints two lines to stdout in pipe mode:
#   line 1: copy ID (ULID)
#   line 2: connection string (DSN)
OUTPUT=$(ditto copy create --format=json 2>/dev/null)

COPY_ID=$(echo "$OUTPUT" | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['ID'])")
CONN=$(echo "$OUTPUT"    | python3 -c "import sys,json; d=json.load(sys.stdin); print(d['ConnectionString'])")

# Mask the connection string BEFORE writing to GITHUB_ENV so it is redacted
# from all subsequent log output in this job.
echo "::add-mask::${CONN}"

# Export to the job environment.
{
  echo "DATABASE_URL=${CONN}"
  echo "DITTO_COPY_ID=${COPY_ID}"
} >> "$GITHUB_ENV"

echo "ditto: copy ${COPY_ID} ready on DATABASE_URL (masked)" >&2
