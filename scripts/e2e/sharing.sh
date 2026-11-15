#!/usr/bin/env bash
# N1 acceptance against the dev stack: the local admin (auth.local) signs in,
# creates an agent and shares it with a second (OIDC) user, who can chat with
# it but not configure, share or delete it. A third user cannot see it.
source "$(dirname "$0")/lib.sh"
ADMIN=$(login_local "${E2E_ADMIN_USER:-admin}" "${E2E_ADMIN_PASSWORD:-admin}")
[[ $(status "$ADMIN" "$BASE/api/v1/me") == 200 ]] && pass "local admin signed in" || { fail "local admin login"; exit 1; }
[[ $(curl -s -o /dev/null -w '%{http_code}' -H content-type:application/json -X POST "$BASE/api/v1/auth/local" -d '{"username":"admin","password":"wrong"}') == 401 ]] && pass "wrong password refused" || fail "wrong password accepted"
BOB=$(login bob bob)
ALICE=$(login alice alice)
BOB_EMAIL=$(api "$BOB" "$BASE/api/v1/me" | json 'd["user"]["email"]')

AG=$(api "$ADMIN" -X POST "$BASE/api/v1/agents" -d '{"name":"e2e-shared-'$RANDOM'","image":"'"$IMG"'","harness":"fake","memoryMb":512}' | json 'd["id"]')
wait_running "$ADMIN" "$AG"
echo "agent $AG"
[[ $(status "$BOB" "$BASE/api/v1/agents/$AG") == 404 ]] && pass "not visible before sharing" || fail "visible before sharing"

[[ $(status "$ADMIN" -X POST "$BASE/api/v1/agents/$AG/shares" -d "{\"email\":\"$BOB_EMAIL\"}") == 200 ]] && pass "shared with $BOB_EMAIL" || fail "share"
role=$(api "$BOB" "$BASE/api/v1/agents" | json "[a['role'] for a in d if a['id']=='$AG']")
[[ $role == "['shared']" ]] && pass "bob lists it as shared" || fail "bob's list: $role"

SES=$(api "$BOB" -X POST "$BASE/api/v1/agents/$AG/sessions" -d '{}' | json 'd["id"]')
api "$BOB" -X POST "$BASE/api/v1/sessions/$SES/prompt" -d '{"text":"hello from bob"}' >/dev/null
for _ in $(seq 1 30); do out=$(api "$BOB" "$BASE/api/v1/sessions/$SES/events" | json_events_text); [[ $out == *"echo: hello from bob"* ]] && break; sleep 1; done
[[ $out == *"echo: hello from bob"* ]] && pass "shared user chats with the agent" || fail "chat: $out"

[[ $(status "$BOB" -X PATCH "$BASE/api/v1/agents/$AG" -d '{"idleMinutes":30}') == 403 ]] && pass "shared user cannot configure" || fail "shared user configured"
[[ $(status "$BOB" -X POST "$BASE/api/v1/agents/$AG/shares" -d '{"email":"alice@example.com"}') == 403 ]] && pass "shared user cannot share" || fail "shared user shared"
[[ $(status "$BOB" -X DELETE "$BASE/api/v1/agents/$AG") == 403 ]] && pass "shared user cannot delete" || fail "shared user deleted"
[[ $(status "$ALICE" "$BASE/api/v1/agents/$AG") == 404 ]] && pass "a third user cannot see it" || fail "visible to a stranger"

BOB_ID=$(api "$BOB" "$BASE/api/v1/me" | json 'd["user"]["id"]')
[[ $(status "$ADMIN" -X DELETE "$BASE/api/v1/agents/$AG/shares/$BOB_ID") == 204 ]] && pass "unshared" || fail "unshare"
[[ $(status "$BOB" "$BASE/api/v1/agents/$AG") == 404 ]] && pass "gone for bob after unsharing" || fail "still visible after unsharing"

[[ -n ${KEEP:-} ]] || api "$ADMIN" -X DELETE "$BASE/api/v1/agents/$AG" >/dev/null
exit $FAILED
