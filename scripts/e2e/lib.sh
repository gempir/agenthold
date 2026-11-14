#!/usr/bin/env bash
# Helpers for end-to-end checks against the dev compose stack (make dev).
set -euo pipefail
BASE=${BASE:-http://localhost:8080}
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
pass() { printf '  \033[32mok\033[0m   %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; FAILED=1; }
FAILED=0

# login USER PASS: signs in through the dev Keycloak and stores the cookie jar.
login() {
  local jar="$WORK/$1.jar" page action
  page=$(curl -s -c "$jar" -b "$jar" -L "$BASE/api/v1/auth/login/keycloak")
  action=$(echo "$page" | grep -o 'action="[^"]*"' | head -1 | sed 's/action="//;s/"$//;s/&amp;/\&/g')
  curl -s -c "$jar" -b "$jar" -o /dev/null -L --data-urlencode "username=$1" --data-urlencode "password=$2" "$action"
  echo "$jar"
}
api() { local jar=$1; shift; curl -s -b "$jar" -H content-type:application/json "$@"; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

# new_agent JAR IMAGE: creates an agent in the personal workspace and waits until it runs.
new_agent() {
  local ws id st
  ws=$(api "$1" "$BASE/api/v1/me" | json 'd["personalWorkspaceId"]')
  id=$(api "$1" -X POST "$BASE/api/v1/workspaces/$ws/agents" -d "{\"name\":\"e2e-$RANDOM\",\"image\":\"$2\",\"memoryMb\":512}" | json 'd["id"]')
  wait_running "$1" "$id"
  echo "$id"
}
wait_running() {
  local st
  for _ in $(seq 1 120); do
    st=$(api "$1" "$BASE/api/v1/agents/$2" | json 'd["state"]+" "+str(d["needsRebuild"])')
    [[ $st == "running False" ]] && return 0
    [[ $st == error* ]] && { echo "agent $2 failed" >&2; return 1; }
    sleep 1
  done
  return 1
}
# in_sandbox AGENT CMD: runs CMD as the agent user inside the sandbox.
in_sandbox() { docker exec -u agent "agenthold-dev-$1" bash -c "$2" 2>&1 || true; }
