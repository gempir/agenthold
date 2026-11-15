#!/usr/bin/env bash
# Helpers for end-to-end checks against the dev compose stack (make dev).
set -euo pipefail
BASE=${BASE:-http://localhost:8080}
# Image for agents in the end-to-end checks (CI uses a plain Debian image).
IMG=${E2E_IMAGE:-agenthold/base:dev}
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
# login_local USER PASS: signs in as the local admin (auth.local) and stores the cookie jar.
login_local() {
  local jar="$WORK/local-$1.jar"
  curl -s -c "$jar" -o /dev/null -H content-type:application/json -X POST "$BASE/api/v1/auth/local" \
    -d "$(python3 -c 'import json,sys;print(json.dumps({"username":sys.argv[1],"password":sys.argv[2]}))' "$1" "$2")"
  echo "$jar"
}
api() { local jar=$1; shift; curl -s -b "$jar" -H content-type:application/json "$@"; }
# status JAR CURL-ARGS...: prints only the HTTP status code.
status() { local jar=$1; shift; curl -s -o /dev/null -w '%{http_code}' -b "$jar" -H content-type:application/json "$@"; }
json() { python3 -c "import json,sys; d=json.load(sys.stdin); print($1)"; }

# new_agent JAR IMAGE: creates an agent owned by the signed-in user and waits until it runs.
new_agent() {
  local id
  id=$(api "$1" -X POST "$BASE/api/v1/agents" -d "{\"name\":\"e2e-$RANDOM\",\"image\":\"$2\",\"memoryMb\":512}" | json 'd["id"]')
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
in_sandbox() { docker exec -u "${E2E_USER:-agent}" "${E2E_PREFIX:-agenthold-dev}-$1" bash -c "$2" 2>&1 || true; }
json_lines_text() { python3 -c '
import json,sys
for l in sys.stdin:
  if l.strip():
    d=json.loads(l)
    if d["type"]=="update" and d["data"].get("sessionUpdate")=="agent_message_chunk": sys.stdout.write(d["data"]["content"]["text"])'; }
# json_events_text: agent message text from a JSON array of session events.
json_events_text() { python3 -c '
import json,sys
for d in json.load(sys.stdin):
  if d["type"]=="update" and d["data"].get("sessionUpdate")=="agent_message_chunk": sys.stdout.write(d["data"]["content"]["text"])'; }
