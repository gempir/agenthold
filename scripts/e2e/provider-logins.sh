#!/usr/bin/env bash
# Account provider logins against the dev stack, with the scripted harness in
# a real sandbox: the login reaches the harness as a placeholder only, the
# proxy injects the value on the wire, and disconnecting takes it away.
source "$(dirname "$0")/lib.sh"
D=$(dirname "$0")
JAR=$(login alice alice)
COOKIE=$(awk '/agenthold_session/{print $7}' "$JAR")
WSURL=${BASE/http/ws}
WS=$(api "$JAR" "$BASE/api/v1/me" | json 'd["personalWorkspaceId"]')
VALUE="fake-e2e-$RANDOM$RANDOM$RANDOM"

echo "Connect a login on the account"
code=$(api "$JAR" -o "$WORK/connect" -w '%{http_code}' -X PUT "$BASE/api/v1/me/provider-logins/fake" -d "{\"kind\":\"token\",\"credential\":\"$VALUE\"}")
[[ $code == 200 ]] && ! grep -q "$VALUE" "$WORK/connect" && pass "connected, the value is not echoed" || fail "connect: $code $(cat "$WORK/connect")"

AG=$(api "$JAR" -X POST "$BASE/api/v1/workspaces/$WS/agents" -d '{"name":"e2e-logins","image":"'"$IMG"'","harness":"fake","memoryMb":512}' | json 'd["id"]')
wait_running "$JAR" "$AG"
echo "agent $AG"
[[ $(api "$JAR" "$BASE/api/v1/agents/$AG" | json 'd["login"]["source"]+" "+str(d["login"]["connected"])') == "account True" ]] && pass "new agent uses its creator's login" || fail "agent login: $(api "$JAR" "$BASE/api/v1/agents/$AG" | json 'd["login"]')"

whoami() {
  local ses
  ses=$(api "$JAR" -X POST "$BASE/api/v1/agents/$AG/sessions" -d '{}' | json 'd["id"]')
  api "$JAR" -X POST "$BASE/api/v1/sessions/$ses/prompt" -d '{"text":"whoami"}' >/dev/null
  node "$D/ws.mjs" "$WSURL/api/v1/sessions/$ses/stream?after=0" "$COOKIE" events 0 turn_end | json_lines_text
}
echo "The harness gets a placeholder, the proxy the value"
said=$(whoami)
PH=${said#token=}; PH=${PH%% *}
[[ $PH == fake-agenthold-placeholder-* ]] && pass "harness sees a placeholder ($PH)" || fail "harness saw: $said"
hits=$(docker exec "${E2E_PREFIX:-agenthold-dev}-$AG" bash -c "grep -rls '$VALUE' /etc /home /opt /run /tmp /proc/[0-9]*/environ 2>/dev/null | wc -l")
[[ $hits -eq 0 ]] && pass "value not found in the sandbox's files or process environments" || fail "value found in $hits places"
seen=$(in_sandbox "$AG" "curl -sS -H 'Authorization: Bearer $PH' -H 'X-Other: $PH' https://postman-echo.com/headers")
[[ $seen == *"Bearer $VALUE"* ]] && pass "placeholder replaced on the wire" || fail "upstream saw: $seen"
[[ $seen == *"\"x-other\":\"$PH\""* ]] && pass "only in the harness's header" || fail "x-other: $seen"

echo "Disconnect"
api "$JAR" -X DELETE "$BASE/api/v1/me/provider-logins/fake" >/dev/null
sleep 6 # the proxy's cache
seen=$(in_sandbox "$AG" "curl -sS -H 'Authorization: Bearer $PH' https://postman-echo.com/headers")
[[ $seen == *"Bearer $PH"* ]] && pass "the proxy no longer injects it" || fail "after disconnect upstream saw: $seen"
[[ $(whoami) == "token= account=" ]] && pass "the harness restarted without it" || fail "harness still has a login"

api "$JAR" -X DELETE "$BASE/api/v1/agents/$AG" >/dev/null
exit $FAILED
