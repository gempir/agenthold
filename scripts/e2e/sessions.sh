#!/usr/bin/env bash
# M5 acceptance against the dev stack with the scripted harness in a real sandbox:
# chat that edits files, reload mid-turn from the cursor, login that survives sleep.
source "$(dirname "$0")/lib.sh"
D=$(dirname "$0")
JAR=$(login alice alice)
COOKIE=$(awk '/agenthold_session/{print $7}' "$JAR")
WSURL=${BASE/http/ws}
WS=$(api "$JAR" "$BASE/api/v1/me" | json 'd["personalWorkspaceId"]')
AG=$(api "$JAR" -X POST "$BASE/api/v1/workspaces/$WS/agents" -d '{"name":"e2e-chat","image":"'"$IMG"'","harness":"fake","memoryMb":512}' | json 'd["id"]')
wait_running "$JAR" "$AG"
echo "agent $AG"
SES=$(api "$JAR" -X POST "$BASE/api/v1/agents/$AG/sessions" -d '{}' | json 'd["id"]')

echo "Chat that edits files in the sandbox"
api "$JAR" -X POST "$BASE/api/v1/sessions/$SES/prompt" -d '{"text":"write project/hello.py print(\"hello\")"}' >/dev/null
node "$D/ws.mjs" "$WSURL/api/v1/sessions/$SES/stream?after=0" "$COOKIE" events 0 turn_end > "$WORK/t1"
grep -q '"type":"diff"' "$WORK/t1" && pass "tool call with a diff streamed" || fail "no diff"
[[ $(in_sandbox "$AG" 'cat /home/agent/project/hello.py') == 'print("hello")' ]] && pass "file written in /home/agent" || fail "file: $(in_sandbox "$AG" 'cat /home/agent/project/hello.py')"
[[ $(docker exec "${E2E_PREFIX:-agenthold-dev}-$AG" stat -c %U /home/agent/project/hello.py) == ${E2E_USER:-agent} ]] && pass "harness runs as the agent user" || fail "owner"
LAST=$(tail -1 "$WORK/t1" | json 'd["seq"]')

echo "Reload mid-turn"
api "$JAR" -X POST "$BASE/api/v1/sessions/$SES/prompt" -d '{"text":"slow 12"}' >/dev/null
node "$D/ws.mjs" "$WSURL/api/v1/sessions/$SES/stream?after=$LAST" "$COOKIE" events "$LAST" none 60000 "chunk 3 " > "$WORK/t2"
CUR=$(tail -1 "$WORK/t2" | json 'd["seq"]')
sleep 1 # the page is "reloading" while the turn goes on
node "$D/ws.mjs" "$WSURL/api/v1/sessions/$SES/stream?after=$CUR" "$COOKIE" events "$CUR" turn_end > "$WORK/t3"
all=$(cat "$WORK/t2" "$WORK/t3" | json_lines_text)
cat "$WORK/t2" "$WORK/t3" | python3 -c '
import json,sys
seqs=[json.loads(l)["seq"] for l in sys.stdin if l.strip()]
ok = seqs == list(range(seqs[0], seqs[0]+len(seqs)))
print("contiguous" if ok else "GAP", seqs[0], seqs[-1])' > "$WORK/seq"
grep -q contiguous "$WORK/seq" && pass "stream continues from the stored cursor without gaps or duplicates ($(cat "$WORK/seq"))" || fail "$(cat "$WORK/seq")"
cat "$WORK/t2" "$WORK/t3" | grep -c '"agent_message_chunk"' | grep -qE '^1[2-9]|^[2-9][0-9]' && pass "all 12 chunks plus answer received across the reconnect" || fail "chunks: $(cat "$WORK/t2" "$WORK/t3" | grep -c agent_message_chunk)"

echo "Subscription login survives sleep"
node "$D/ws.mjs" "$WSURL/api/v1/agents/$AG/terminal?auth=fake-login&cols=100&rows=20" "$COOKIE" term FAKE-1234 $'\n' > "$WORK/login"
grep -q "Logged in." "$WORK/login" && pass "login ran in the agent terminal (device code shown)" || fail "login: $(cat "$WORK/login")"
[[ $(api "$JAR" "$BASE/api/v1/agents/$AG" | json 'd["credentialsInSandbox"]') == True ]] && pass "agent marked: credentials inside sandbox" || fail "flag"
api "$JAR" -X POST "$BASE/api/v1/agents/$AG/stop" >/dev/null
for _ in $(seq 1 30); do [[ $(api "$JAR" "$BASE/api/v1/agents/$AG" | json 'd["state"]') == sleeping ]] && break; sleep 1; done
api "$JAR" -X POST "$BASE/api/v1/sessions/$SES/prompt" -d '{"text":"still there?"}' >/dev/null
node "$D/ws.mjs" "$WSURL/api/v1/sessions/$SES/stream?after=0" "$COOKIE" events 0 none 90000 "echo: still there?" > "$WORK/t4" && pass "prompt woke the agent and the conversation continued" || fail "no answer after wake"
[[ $(in_sandbox "$AG" 'cat /home/agent/.fakeagent/credentials') == fake-subscription-token ]] && pass "login still on the agent's disk after sleep" || fail "credentials gone"

api "$JAR" -X DELETE "$BASE/api/v1/agents/$AG" >/dev/null
exit $FAILED
