#!/usr/bin/env bash
# M9 acceptance with OpenCode (real model): an attached skill and an attached
# MCP server are usable in the next session.
source "$(dirname "$0")/lib.sh"
D=$(dirname "$0")
HARNESS=${HARNESS:-opencode}
JAR=$(login alice alice)
COOKIE=$(awk '/agenthold_session/{print $7}' "$JAR")
WSURL=${BASE/http/ws}
AG=$(api "$JAR" -X POST "$BASE/api/v1/agents" -d "{\"name\":\"e2e-tools\",\"image\":\"agenthold/base:dev\",\"harness\":\"$HARNESS\",\"memoryMb\":2048}" | json 'd["id"]')
wait_running "$JAR" "$AG"
echo "agent $AG ($HARNESS)"
N=$RANDOM
SK=$(api "$JAR" -X POST "$BASE/api/v1/skills" -d "{\"name\":\"secret-phrase-$N\",\"description\":\"Use this skill whenever someone asks for the team's secret phrase.\",\"content\":\"# Secret phrase\\n\\nThe team's secret phrase is: purple-elephant-$N. Answer with exactly that phrase.\"}" | json 'd["id"]')
MC=$(api "$JAR" -X POST "$BASE/api/v1/mcp-servers" -d "{\"name\":\"everything$N\",\"transport\":\"stdio\",\"command\":\"npx\",\"args\":[\"-y\",\"@modelcontextprotocol/server-everything@2.3.0\"]}" | json 'd["id"]')
api "$JAR" -X PUT "$BASE/api/v1/agents/$AG/skills/$SK" -o /dev/null -w "attach skill %{http_code}\n"
api "$JAR" -X PUT "$BASE/api/v1/agents/$AG/mcp-servers/$MC" -o /dev/null -w "attach mcp %{http_code}\n"

ask() { # ask PROMPT -> prints the answer, keeps events in $WORK/last
  local ses
  ses=$(api "$JAR" -X POST "$BASE/api/v1/agents/$AG/sessions" -d '{}' | json 'd["id"]')
  api "$JAR" -X POST "$BASE/api/v1/sessions/$ses/prompt" -d "$(python3 -c 'import json,sys;print(json.dumps({"text":sys.argv[1]}))' "$1")" >/dev/null
  node "$D/ws.mjs" "$WSURL/api/v1/sessions/$ses/stream?after=0" "$COOKIE" events 0 turn_end 300000 > "$WORK/last" || true
  json_lines_text < "$WORK/last"
}

ans=$(ask "What is the team's secret phrase? Use your skills.")
echo "  answer: ${ans:0:200}"
[[ $ans == *"purple-elephant-$N"* ]] && pass "skill used in a new session" || fail "skill not used"
[[ -f /dev/null ]] && in_sandbox "$AG" "ls ~/.config/opencode/skills ~/.claude/skills 2>/dev/null" | tr '\n' ' '; echo

ans=$(ask "Call the echo tool of the everything$N MCP server with the message mcp-ok-$N and reply with exactly what it returned.")
echo "  answer: ${ans:0:200}"
grep -q '"tool_call"' "$WORK/last" && pass "a tool call happened" || fail "no tool call"
[[ $ans == *"mcp-ok-$N"* ]] && pass "MCP server tool used in a new session" || fail "MCP tool not used"

api "$JAR" -X DELETE "$BASE/api/v1/agents/$AG" >/dev/null
api "$JAR" -X DELETE "$BASE/api/v1/skills/$SK" >/dev/null
api "$JAR" -X DELETE "$BASE/api/v1/mcp-servers/$MC" >/dev/null
exit $FAILED
