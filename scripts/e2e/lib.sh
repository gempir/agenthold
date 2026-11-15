#!/usr/bin/env bash
# Helpers for checks against a running native development server.
set -euo pipefail
BASE=${BASE:-http://localhost:8080}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
pass() { printf '  ok   %s\n' "$*"; }
fail() { printf '  FAIL %s\n' "$*" >&2; FAILED=1; }
FAILED=0
login_local() {
  local jar="$WORK/local-$1.jar"
  curl -fsS -c "$jar" -o /dev/null -H content-type:application/json -X POST "$BASE/api/v1/auth/local" \
    -d "$(python3 -c 'import json,sys;print(json.dumps({"username":sys.argv[1],"password":sys.argv[2]}))' "$1" "$2")"
  echo "$jar"
}
api() { local jar=$1; shift; curl -fsS -b "$jar" -H content-type:application/json "$@"; }
status() { local jar=$1; shift; curl -s -o /dev/null -w '%{http_code}' -b "$jar" -H content-type:application/json "$@"; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }
