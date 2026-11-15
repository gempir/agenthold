#!/usr/bin/env bash
# M8 acceptance: the same flows with OpenCode (a real model, via OpenCode's
# free tier, through the egress proxy): chat that edits a file, reload from the
# cursor, sleep and wake with history.
source "$(dirname "$0")/lib.sh"
D=$(dirname "$0")
JAR=$(login alice alice)
COOKIE=$(awk '/agenthold_session/{print $7}' "$JAR")
WSURL=${BASE/http/ws}
AG=$(api "$JAR" -X POST "$BASE/api/v1/agents" -d '{"name":"e2e-opencode","image":"agenthold/base:dev","harness":"opencode","memoryMb":2048}' | json 'd["id"]')
wait_running "$JAR" "$AG"
echo "agent $AG"
api "$JAR" "$BASE/api/v1/agents/$AG/harness?connect=true" | json 'd["agentName"]+" "+d["agentVersion"]+" loadSession="+str(d["loadSession"])+" auth="+",".join(m["id"]+":"+m["type"] for m in d["authMethods"])'
SES=$(api "$JAR" -X POST "$BASE/api/v1/agents/$AG/sessions" -d '{}' | json 'd["id"]')

api "$JAR" -X POST "$BASE/api/v1/sessions/$SES/prompt" -d '{"text":"Create a file named hello.txt in the current directory containing exactly the text: agenthold works. Then reply with the single word DONE."}' >/dev/null
node "$D/ws.mjs" "$WSURL/api/v1/sessions/$SES/stream?after=0" "$COOKIE" events 0 turn_end 300000 > "$WORK/t1" || true
grep -c '"tool_call"' "$WORK/t1" | xargs -I{} echo "  tool calls: {}"
grep -o '"stopReason":"[a-z_]*"' "$WORK/t1" | tail -1
[[ $(in_sandbox "$AG" 'cat ~/hello.txt' | tr -d '\n') == "agenthold works" ]] && pass "OpenCode edited a file in the sandbox" || fail "hello.txt: $(in_sandbox "$AG" 'cat ~/hello.txt')"
cat "$WORK/t1" | json_lines_text | tail -c 300; echo

echo "Reload from the cursor"
LAST=$(tail -1 "$WORK/t1" | json 'd["seq"]')
node "$D/ws.mjs" "$WSURL/api/v1/sessions/$SES/stream?after=0" "$COOKIE" events 0 none 5000 "\"seq\":$LAST," > "$WORK/t2" || true
[[ $(wc -l < "$WORK/t2") -eq $LAST ]] && pass "full log replays from seq 0 ($LAST events)" || fail "replayed $(wc -l < "$WORK/t2") of $LAST"

echo "Sleep and wake"
api "$JAR" -X POST "$BASE/api/v1/agents/$AG/stop" >/dev/null
for _ in $(seq 1 30); do [[ $(api "$JAR" "$BASE/api/v1/agents/$AG" | json 'd["state"]') == sleeping ]] && break; sleep 1; done
api "$JAR" -X POST "$BASE/api/v1/sessions/$SES/prompt" -d '{"text":"What is the exact content of the file you created? Answer with only the content."}' >/dev/null
node "$D/ws.mjs" "$WSURL/api/v1/sessions/$SES/stream?after=$LAST" "$COOKIE" events "$LAST" turn_end 300000 > "$WORK/t3" || true
grep -q 'fresh context' "$WORK/t3" && fail "context was reset" || pass "conversation restored after waking (no reset notice)"
ans=$(cat "$WORK/t3" | json_lines_text)
echo "  answer: ${ans:0:200}"
[[ $ans == *"agenthold works"* ]] && pass "agent remembers after sleep" || fail "unexpected answer"

[[ -n ${KEEP:-} ]] || api "$JAR" -X DELETE "$BASE/api/v1/agents/$AG" >/dev/null
exit $FAILED
