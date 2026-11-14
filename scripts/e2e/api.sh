#!/usr/bin/env bash
# M10 acceptance against the dev stack: a script with a token creates an agent
# and prompts it; a schedule and a webhook each wake a sleeping agent.
source "$(dirname "$0")/lib.sh"
JAR=$(login alice alice)
TOKEN=$(api "$JAR" -X POST "$BASE/api/v1/me/tokens" -d '{"name":"e2e","expiresInDays":1}' | json 'd["token"]')
echo "token ${TOKEN:0:10}…"
# From here on, only the token is used.
T() { curl -s -H "Authorization: Bearer $TOKEN" -H content-type:application/json "$@"; }

WS=$(T "$BASE/api/v1/me" | json 'd["personalWorkspaceId"]')
AG=$(T -X POST "$BASE/api/v1/workspaces/$WS/agents" -d '{"name":"e2e-api","image":"agenthold/base:dev","harness":"fake","memoryMb":512}' | json 'd["id"]')
for _ in $(seq 1 90); do [[ $(T "$BASE/api/v1/agents/$AG" | json 'd["state"]') == running ]] && break; sleep 1; done
SES=$(T -X POST "$BASE/api/v1/agents/$AG/sessions" -d '{}' | json 'd["id"]')
T -X POST "$BASE/api/v1/sessions/$SES/prompt" -d '{"text":"hello from a script"}' >/dev/null
for _ in $(seq 1 30); do out=$(T "$BASE/api/v1/sessions/$SES/events" | json_events_text); [[ $out == *"echo: hello from a script"* ]] && break; sleep 1; done
[[ $out == *"echo: hello from a script"* ]] && pass "token: created an agent and prompted it" || fail "token flow: $out"
[[ $(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer ahp_invalid" "$BASE/api/v1/me") == 401 ]] && pass "invalid token refused" || fail "invalid token accepted"

sleep_agent() {
  T -X POST "$BASE/api/v1/agents/$AG/stop" >/dev/null
  for _ in $(seq 1 30); do [[ $(T "$BASE/api/v1/agents/$AG" | json 'd["state"]') == sleeping ]] && return; sleep 1; done
}
await_text() { # await_text SESSION TEXT SECONDS
  for _ in $(seq 1 "$3"); do T "$BASE/api/v1/sessions/$1/events" | json_events_text | grep -q "$2" && return 0; sleep 1; done
  return 1
}

echo "Schedule"
sleep_agent
SC=$(T -X POST "$BASE/api/v1/agents/$AG/schedules" -d '{"name":"every-minute","cron":"* * * * *","prompt":"scheduled hello"}')
SCS=$(echo "$SC" | json 'd["sessionId"]'); SCID=$(echo "$SC" | json 'd["id"]')
echo "  next run $(echo "$SC" | json 'd["nextRunAt"]'), agent $(T "$BASE/api/v1/agents/$AG" | json 'd["state"]')"
await_text "$SCS" "echo: scheduled hello" 120 && pass "schedule ran and woke the sleeping agent" || fail "schedule did not run"
[[ $(T "$BASE/api/v1/agents/$AG" | json 'd["state"]') == running ]] && pass "agent running after the schedule" || fail "agent not running"
T -X DELETE "$BASE/api/v1/schedules/$SCID" >/dev/null

echo "Webhook"
sleep_agent
WH=$(T -X POST "$BASE/api/v1/agents/$AG/webhooks" -d '{"name":"ci","promptTemplate":"CI says:\n{{payload}}","signingSecret":"whsec"}')
URL=$(echo "$WH" | json 'd["url"]'); WHS=$(echo "$WH" | json 'd["sessionId"]')
BODY='{"status":"failed","job":"build"}'
SIG="sha256=$(printf %s "$BODY" | openssl dgst -sha256 -hmac whsec -hex | awk '{print $NF}')"
[[ $(curl -s -o /dev/null -w '%{http_code}' -X POST -H content-type:application/json -d "$BODY" "$URL") == 401 ]] && pass "unsigned delivery refused" || fail "unsigned accepted"
[[ $(curl -s -o /dev/null -w '%{http_code}' -X POST -H content-type:application/json -H "X-Hub-Signature-256: $SIG" -d "$BODY" "$URL") == 202 ]] && pass "signed delivery accepted" || fail "signed refused"
await_text "$WHS" '"status":"failed"' 60 && pass "webhook woke the sleeping agent and prompted it with the payload" || fail "webhook turn missing"
[[ $(T "$BASE/api/v1/agents/$AG" | json 'd["state"]') == running ]] && pass "agent running after the webhook" || fail "agent not running"

[[ $(curl -s "$BASE/api/v1/openapi.yaml") == "openapi: 3"* ]] && pass "API reference published (/api/v1/openapi.yaml, /api/docs)" || fail "no openapi"
T -X DELETE "$BASE/api/v1/agents/$AG" >/dev/null
exit $FAILED
