#!/usr/bin/env bash
# Starts the real Claude Code harness in a base-image agent and reports what it
# advertises. Without a login, the prompt must fail with a clear message.
source "$(dirname "$0")/lib.sh"
JAR=$(login alice alice)
WS=$(api "$JAR" "$BASE/api/v1/me" | json 'd["personalWorkspaceId"]')
AG=$(api "$JAR" -X POST "$BASE/api/v1/workspaces/$WS/agents" -d '{"name":"claude","image":"agenthold/base:dev","memoryMb":2048}' | json 'd["id"]')
wait_running "$JAR" "$AG"
echo "agent $AG"
api "$JAR" "$BASE/api/v1/agents/$AG/harness?connect=true" | python3 -m json.tool
SES=$(api "$JAR" -X POST "$BASE/api/v1/agents/$AG/sessions" -d '{}' | json 'd["id"]')
api "$JAR" -X POST "$BASE/api/v1/sessions/$SES/prompt" -d '{"text":"Say hi"}' -o /dev/null -w "prompt %{http_code}\n"
for _ in $(seq 1 60); do
  n=$(api "$JAR" "$BASE/api/v1/sessions/$SES/events" | json 'sum(1 for e in d if e["type"]=="turn_end")')
  [[ $n -ge 1 ]] && break; sleep 1
done
api "$JAR" "$BASE/api/v1/sessions/$SES/events" | json '"\n".join(str(e["seq"])+" "+e["type"]+" "+json.dumps(e["data"])[:400] for e in d)'
echo "$AG" > "${KEEP_AGENT_FILE:-/dev/null}"
