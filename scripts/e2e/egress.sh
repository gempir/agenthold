#!/usr/bin/env bash
# M4 acceptance against the dev stack: make dev, then scripts/e2e/egress.sh
source "$(dirname "$0")/lib.sh"
docker image inspect agenthold/base:dev >/dev/null 2>&1 || docker build -q -t agenthold/base:dev images/base >/dev/null

JAR=$(login alice alice)
AG=$(new_agent "$JAR" agenthold/base:dev)
WS=$(api "$JAR" "$BASE/api/v1/me" | json 'd["personalWorkspaceId"]')
echo "agent $AG"
policy() { api "$JAR" -X PUT "$BASE/api/v1/agents/$AG/network-policy" -d "$1" >/dev/null; sleep 6; }

echo "Reaches the internet only through the proxy"
[[ $(in_sandbox "$AG" 'curl -sS -o /dev/null -w "%{http_code}" https://example.com') == 200 ]] && pass "HTTPS with HTTPS_PROXY" || fail "HTTPS with HTTPS_PROXY"
[[ $(in_sandbox "$AG" 'env -u HTTPS_PROXY -u https_proxy curl -sS -o /dev/null -w "%{http_code}" https://example.com') == 200 ]] && pass "HTTPS without proxy settings (transparent)" || fail "transparent HTTPS"
out=$(in_sandbox "$AG" 'exec 3<>/dev/tcp/example.com/80; printf "HEAD / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n" >&3; head -1 <&3')
[[ $out == HTTP/1.1* ]] && pass "raw socket that ignores HTTPS_PROXY is intercepted" || fail "raw socket: $out"
for target in 169.254.169.254 10.0.0.1 172.17.0.1 keycloak:8081; do
  code=$(in_sandbox "$AG" "curl -sS -m 5 -o /dev/null -w '%{http_code}' http://$target/")
  [[ $code == 403 ]] && pass "blocked $target" || fail "$target returned $code"
done
SRV=$(in_sandbox "$AG" "awk '/nameserver/{print \$2}' /etc/resolv.conf")
code=$(in_sandbox "$AG" "curl -sS -m 5 -o /dev/null -w '%{http_code}' http://$SRV:8080/healthz")
[[ $code == *000 ]] && pass "server API not reachable from the sandbox" || fail "server API returned $code"

echo "git over SSH"
policy '{"mode":"allowlist","allow":["example.com","postman-echo.com"]}'
out=$(in_sandbox "$AG" 'timeout 15 ssh -T -o BatchMode=yes -o StrictHostKeyChecking=accept-new git@github.com')
[[ $out != *"Permission denied (publickey)"* ]] && pass "denied when not allowed" || fail "ssh connected while denied"
policy '{"mode":"allowlist","allow":["example.com","postman-echo.com","github.com:22"]}'
out=$(in_sandbox "$AG" 'timeout 15 ssh -T -o BatchMode=yes -o StrictHostKeyChecking=accept-new git@github.com')
[[ $out == *"Permission denied (publickey)"* ]] && pass "works when allowed (reaches GitHub's SSH server)" || fail "ssh: $out"
sleep 2 # connections are logged when they close
log=$(api "$JAR" "$BASE/api/v1/agents/$AG/egress-log?limit=200")
echo "$log" | json 'any(e["host"]=="github.com" and not e["allowed"] for e in d)' | grep -q True && pass "denied attempt is in the request log" || fail "no denied github.com entry"
echo "$log" | json 'any(e["host"]=="github.com" and e["port"]==22 and e["allowed"] for e in d)' | grep -q True && pass "allowed SSH is in the request log" || fail "no allowed ssh entry"

echo "Secrets"
VALUE="e2e-real-$RANDOM$RANDOM"
SEC=$(api "$JAR" -X POST "$BASE/api/v1/workspaces/$WS/secrets" -d "{\"name\":\"e2e-$RANDOM\",\"value\":\"$VALUE\",\"rule\":{\"hosts\":[\"postman-echo.com\"],\"header\":\"X-E2E\",\"template\":\"{{.Value}}\"}}" | json 'd["id"]')
api "$JAR" -X PUT "$BASE/api/v1/agents/$AG/secrets/$SEC" -d '{"envVar":"E2E_TOKEN"}' >/dev/null
api "$JAR" -X POST "$BASE/api/v1/agents/$AG/rebuild" >/dev/null
sleep 2; wait_running "$JAR" "$AG"
ph=$(in_sandbox "$AG" 'printf %s "$E2E_TOKEN"')
[[ $ph == agenthold-placeholder-* ]] && pass "sandbox sees a placeholder" || fail "env value: $ph"
seen=$(in_sandbox "$AG" 'curl -sS -H "Authorization: Bearer $E2E_TOKEN" https://postman-echo.com/headers')
[[ $seen == *"\"x-e2e\":\"$VALUE\""* && $seen == *"Bearer $VALUE"* ]] && pass "placeholder replaced on the wire (header set and placeholder substituted)" || fail "upstream saw: $seen"
hits=$(docker exec "agenthold-dev-$AG" bash -c "grep -rls '$VALUE' /etc /home /opt /run /tmp /proc/[0-9]*/environ 2>/dev/null | wc -l")
[[ $hits -eq 0 ]] && pass "value not found in the sandbox's files or process environments" || fail "value found in $hits places"

api "$JAR" -X DELETE "$BASE/api/v1/agents/$AG" >/dev/null
api "$JAR" -X DELETE "$BASE/api/v1/secrets/$SEC" >/dev/null
exit $FAILED
