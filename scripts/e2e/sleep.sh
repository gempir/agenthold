#!/usr/bin/env bash
# M6 acceptance against the dev stack (idle_check_interval: 10s in deploy/dev):
# CPU use keeps an agent awake, an idle agent sleeps, a prompt wakes it and the
# conversation continues with its history.
source "$(dirname "$0")/lib.sh"
D=$(dirname "$0")
JAR=$(login alice alice)
COOKIE=$(awk '/agenthold_session/{print $7}' "$JAR")
WSURL=${BASE/http/ws}
WS=$(api "$JAR" "$BASE/api/v1/me" | json 'd["personalWorkspaceId"]')
AG=$(api "$JAR" -X POST "$BASE/api/v1/workspaces/$WS/agents" -d '{"name":"e2e-sleep","image":"'"$IMG"'","harness":"fake","memoryMb":512,"idleMinutes":1}' | json 'd["id"]')
wait_running "$JAR" "$AG"
echo "agent $AG (idle timeout 1 minute)"
state() { api "$JAR" "$BASE/api/v1/agents/$AG" | json 'd["state"]'; }

SES=$(api "$JAR" -X POST "$BASE/api/v1/agents/$AG/sessions" -d '{}' | json 'd["id"]')
api "$JAR" -X POST "$BASE/api/v1/sessions/$SES/prompt" -d '{"text":"remember the word banana"}' >/dev/null
node "$D/ws.mjs" "$WSURL/api/v1/sessions/$SES/stream?after=0" "$COOKIE" events 0 turn_end >/dev/null
api "$JAR" "$BASE/api/v1/agents/$AG" | json '"sleepsAt" in d' | grep -q True && pass "agent reports when it will sleep" || fail "no sleepsAt"

echo "Busy sandbox stays awake"
docker exec -d "agenthold-dev-$AG" sh -c 'timeout 100 sh -c "while :; do :; done"'
sleep 85
[[ $(state) == running ]] && pass "still running after 85s of CPU use (timeout 60s)" || fail "slept while busy: $(state)"
docker exec "agenthold-dev-$AG" pkill -f 'while :' >/dev/null 2>&1 || true

echo "Idle sandbox sleeps"
for _ in $(seq 1 120); do [[ $(state) == sleeping ]] && break; sleep 2; done
[[ $(state) == sleeping ]] && pass "asleep after being idle" || fail "state $(state)"
[[ $(docker inspect -f '{{.State.Running}}' "agenthold-dev-$AG") == false ]] && pass "container stopped" || fail "container still running"

echo "Prompt wakes it; history continues"
api "$JAR" -X POST "$BASE/api/v1/sessions/$SES/prompt" -d '{"text":"what was the word"}' >/dev/null
node "$D/ws.mjs" "$WSURL/api/v1/sessions/$SES/stream?after=0" "$COOKIE" events 0 none 120000 "echo: what was the word" > "$WORK/out" && pass "agent answered after waking" || fail "no answer"
grep -q 'Starting the sandbox' "$WORK/out" && pass "the log shows the wake-up" || fail "no wake notice"
grep -q 'fresh context' "$WORK/out" && fail "context was reset" || pass "conversation restored (session/load), no context reset"
[[ $(in_sandbox "$AG" 'cat /home/agent/.fakeagent/sessions/*.json' | grep -c banana) -ge 1 ]] && pass "history still on disk" || fail "history lost"
[[ $(state) == running ]] && pass "running again" || fail "state $(state)"

api "$JAR" -X DELETE "$BASE/api/v1/agents/$AG" >/dev/null
exit $FAILED
