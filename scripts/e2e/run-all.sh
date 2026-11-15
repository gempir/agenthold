#!/usr/bin/env bash
# Runs every end-to-end check against the dev stack (make dev; make base-image).
#   SKIP_REAL_MODEL=1  skip checks that use a real model (OpenCode free tier)
#   SKIP_BASE=1        skip checks that need the base image (egress, OpenCode)
# With arguments, runs only those checks: run-all.sh sleep api
set -uo pipefail
cd "$(dirname "$0")"
checks=(sessions sleep api agents)
[[ -n ${SKIP_BASE:-} ]] || checks=(egress "${checks[@]}")
[[ -n ${SKIP_BASE:-} || -n ${SKIP_REAL_MODEL:-} ]] || checks+=(opencode tools)
if (($#)); then checks=("$@"); fi
failed=()
for c in "${checks[@]}"; do
  echo "=== $c"
  ./"$c".sh || failed+=("$c")
done
echo
if ((${#failed[@]})); then echo "FAILED: ${failed[*]}"; exit 1; fi
echo "all end-to-end checks passed: ${checks[*]}"
