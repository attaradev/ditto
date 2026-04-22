#!/usr/bin/env bash
# scripts/govulncheck.sh — govulncheck wrapper with accepted-vulnerability filtering.
#
# Accepted vulnerabilities (last reviewed: 2026-04-22):
#
#   GO-2026-4887  Moby AuthZ plugin bypass with oversized request bodies.
#                 Fixed only in github.com/moby/moby/v2 — no patched version of
#                 github.com/docker/docker exists on the Go module proxy.
#                 Ditto uses only the Docker client SDK; it never runs a daemon,
#                 implements authorization plugins, or calls the affected symbols.
#                 govulncheck reachability is init()-chain only (not direct).
#                 Track: https://pkg.go.dev/vuln/GO-2026-4887
#
#   GO-2026-4883  Moby off-by-one in plugin privilege validation. Same root cause
#                 as GO-2026-4887. Fixed only in moby/moby/v2. Ditto never calls
#                 plugin privilege validation functions.
#                 Track: https://pkg.go.dev/vuln/GO-2026-4883

set -euo pipefail

ACCEPTED_VULNS=(
  "GO-2026-4887"
  "GO-2026-4883"
)

if ! command -v govulncheck &>/dev/null; then
  echo "ERROR: govulncheck not found. Install: go install golang.org/x/vuln/cmd/govulncheck@latest" >&2
  exit 1
fi

if ! command -v jq &>/dev/null; then
  echo "ERROR: jq not found. Install with your package manager (e.g. brew install jq)." >&2
  exit 1
fi

accepted_pattern=$(printf "%s|" "${ACCEPTED_VULNS[@]}")
accepted_pattern="${accepted_pattern%|}"

tmpfile=$(mktemp)
trap 'rm -f "$tmpfile"' EXIT

echo "Running: govulncheck -json $*"
# govulncheck exits non-zero when vulns are found; capture output regardless.
govulncheck -json "$@" > "$tmpfile" || true

# Extract unique OSV IDs from all finding objects.
# govulncheck emits a stream of JSON objects (pretty-printed, multi-line).
# jq handles streaming over them natively with --slurp.
unaccepted=0

while IFS= read -r osv_id; do
  [ -z "$osv_id" ] && continue
  if echo "$osv_id" | grep -qE "^(${accepted_pattern})$"; then
    echo "ACCEPTED (not a failure): $osv_id — see comment in scripts/govulncheck.sh"
  else
    echo "UNACCEPTED VULNERABILITY: $osv_id"
    jq --arg id "$osv_id" -rs '.[] | select(.finding.osv == $id) | .finding' "$tmpfile" | head -20
    unaccepted=$((unaccepted + 1))
  fi
done < <(jq -rs '[.[].finding.osv | select(. != null)] | unique[]' "$tmpfile")

if [ "$unaccepted" -gt 0 ]; then
  echo "ERROR: $unaccepted unaccepted vulnerability/vulnerabilities found." >&2
  exit 1
fi

echo "govulncheck: no unaccepted vulnerabilities found."
