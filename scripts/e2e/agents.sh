#!/usr/bin/env bash
# M11 acceptance against the dev stack: one agent delegates a task to another
# and receives the result (scripted harness and a real OpenCode model); a loop
# is stopped by the depth limit.
source "$(dirname "$0")/lib.sh"
D=$(dirname "$0")
JAR=$(login alice alice)
COOKIE=$(awk '/agenthold_session/{print $7}' "$JAR")
WSURL=${BASE/http/ws}
WS=$(api "$JAR" "$BASE/api/v1/me" | json 'd["personalWorkspaceId"]')
mk() { api "$JAR" -X POST "$BASE/api/v1/workspaces/$WS/agents" -d "{\"name\":\"$1\",\"image\":\"$IMG\",\"harness\":\"$2\",\"memoryMb\":${3:-512}}" | json 'd["id"]'; }
N=$RANDOM
PLANNER=$(mk "planner-$N" fake); WORKER=$(mk "worker-$N" fake); BRAIN=""
[[ -n ${SKIP_REAL_MODEL:-} ]] || BRAIN=$(mk "brain-$N" opencode 2048)
for a in $PLANNER $WORKER $BRAIN; do wait_running "$JAR" "$a"; done
echo "agents planner=$PLANNER worker=$WORKER brain=$BRAIN"

ask_session() { # ask_session AGENT PROMPT -> answer text
  local ses
  ses=$(api "$JAR" -X POST "$BASE/api/v1/agents/$1/sessions" -d '{}' | json 'd["id"]')
  api "$JAR" -X POST "$BASE/api/v1/sessions/$ses/prompt" -d "$(python3 -c 'import json,sys;print(json.dumps({"text":sys.argv[1]}))' "$2")" >/dev/null
  node "$D/ws.mjs" "$WSURL/api/v1/sessions/$ses/stream?after=0" "$COOKIE" events 0 turn_end 300000 > "$WORK/last" || true
  json_lines_text < "$WORK/last"
}

echo "Delegation (scripted harness, real sandboxes)"
in_sandbox "$PLANNER" 'ls -la /home/agent/.agenthold/' | grep -q platform.sock && pass "platform socket in the sandbox" || fail "no platform socket"
ans=$(ask_session "$PLANNER" "ask worker-$N check the build")
echo "  planner: ${ans:0:160}"
[[ $ans == *"echo: check the build"* ]] && pass "planner asked worker and got its answer back" || fail "delegation"
api "$JAR" "$BASE/api/v1/agents/$WORKER/sessions" | json '"\n".join(s["title"]+" origin="+(s.get("origin") or {}).get("agentName","") for s in d)' | grep -q "origin=planner-$N" && pass "worker's session is linked to the planner" || fail "no linked session"

echo "Loop"
ans=$(ask_session "$PLANNER" "ask worker-$N ask planner-$N ask worker-$N ask planner-$N ask worker-$N hello")
[[ $ans == *"too many agents in a row"* ]] && pass "loop stopped by the depth limit" || fail "loop: ${ans:0:300}"

if [[ -z ${SKIP_REAL_MODEL:-} ]]; then
echo "Delegation by a real model (OpenCode)"
ans=$(ask_session "$BRAIN" "Use the ask tool of the agenthold MCP server to ask the agent named worker-$N this question: what-is-the-codeword. Then reply with exactly the answer it gave you.")
echo "  brain: ${ans:0:200}"
grep -q '"title":"agenthold_ask"' "$WORK/last" && pass "the model called the agenthold ask tool" || fail "no agenthold ask call"
grep -q 'echo: what-is-the-codeword' "$WORK/last" && pass "the tool returned the worker's answer to the model" || fail "tool result missing"
[[ $ans == *"what-is-the-codeword"* ]] && pass "the model relayed it" || fail "model answer: ${ans:0:200}"
fi

for a in $PLANNER $WORKER $BRAIN; do api "$JAR" -X DELETE "$BASE/api/v1/agents/$a" >/dev/null; done
exit $FAILED
