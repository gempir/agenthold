#!/usr/bin/env bash
# End-to-end pull tests use a fixture release store and fake systemctl/curl, never a real service.
set -euo pipefail
[[ "$(uname -s)" == Linux ]] || { echo 'run on Linux'; exit 1; }
for tool in jq zstd sha256sum flock; do command -v "$tool" >/dev/null || { echo "$tool required" >&2; exit 1; }; done
script="$(cd "$(dirname "$0")" && pwd)/pull.sh"
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
export MOCK_DIR="$work" AGENTHOLD_DEPLOY_ROOT="$work/opt" AGENTHOLD_DEPLOY_DATA_DIR="$work/data" AGENTHOLD_DEPLOY_HOST_MOUNT="$work" AGENTHOLD_DEPLOY_TIMEOUT=1
export AGENTHOLD_DEPLOY_SOURCE="$work/src" AGENTHOLD_DEPLOY_CONFIG="$work/none.conf" TMPDIR="$work/tmp"
mkdir -p "$work/bin" "$work/fixture" "$work/opt/releases" "$work/data" "$work/tmp"
export PATH="$work/bin:$PATH"
old=1111111111111111111111111111111111111111
new=2222222222222222222222222222222222222222
export MOCK_NEW="$new"
v="sha-$new"
fail() { echo "FAIL: $*" >&2; [[ -f "$work/run.log" ]] && cat "$work/run.log" >&2; exit 1; }

# --- fixture store: what build-local.sh publishes ---
mkdir -p "$work/fixture/$v" "$work/opt/releases/$old"
printf '#!/usr/bin/env bash\necho "agenthold old (old)"\n' > "$work/opt/releases/$old/agenthold"
chmod +x "$work/opt/releases/$old/agenthold"
f="$work/fixture/$v"
printf '#!/usr/bin/env bash\necho "agenthold sha-%s (deadbeef)"\n' "$new" > "$f/agenthold-linux-arm64"
chmod +x "$f/agenthold-linux-arm64"
printf 'small test disk image\n' > "$work/fixture/base"
zstd -q -f "$work/fixture/base" -o "$f/base-arm64.ext4.zst"
base_sha=$(sha256sum "$work/fixture/base" | cut -d' ' -f1)
jq -n '{file:"base-arm64.ext4"}' > "$f/base-arm64.manifest.json"
jq -n --arg version "$v" --arg raw "$base_sha" --arg compressed "$(sha256sum "$f/base-arm64.ext4.zst" | cut -d' ' -f1)" \
  --argjson rawsize "$(stat -c %s "$work/fixture/base")" --argjson compressedsize "$(stat -c %s "$f/base-arm64.ext4.zst")" \
  '{format:1,arch:"arm64",version:$version,base:{url:"",sha256:$raw,size:$rawsize,compressed:{sha256:$compressed,size:$compressedsize}}}' > "$f/runtime-linux-arm64.manifest.json"
channel() { # channel <store dir>: (re)write SHA256SUMS and channel.json like build-local.sh
  local s="$1"
  (cd "$s/$v" && sha256sum agenthold-linux-arm64 base-arm64.ext4.zst base-arm64.manifest.json runtime-linux-arm64.manifest.json > SHA256SUMS)
  jq -n --arg commit "$new" --arg v "$v" --arg b "$(sha256sum "$s/$v/agenthold-linux-arm64" | cut -d' ' -f1)" \
    --arg m "$(sha256sum "$s/$v/runtime-linux-arm64.manifest.json" | cut -d' ' -f1)" --arg s "$(sha256sum "$s/$v/SHA256SUMS" | cut -d' ' -f1)" \
    '{format:2,arch:"arm64",commit:$commit,version:$v,binary:{path:($v+"/agenthold-linux-arm64"),sha256:$b},
      runtime_manifest:{path:($v+"/runtime-linux-arm64.manifest.json"),sha256:$m},sums:{path:($v+"/SHA256SUMS"),sha256:$s}}' > "$s/channel.json"
}
channel "$work/fixture"

cat > "$work/bin/uname" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == -m ]]; then echo aarch64; else exec /usr/bin/uname "$@"; fi
MOCK
cat > "$work/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == is-active ]]; then exit 0; fi
if [[ "$1" == restart ]]; then
 echo "$(basename "$(readlink -f "$AGENTHOLD_DEPLOY_ROOT/current")")" >> "$MOCK_DIR/restarts"
 if [[ "${MOCK_MODE:-}" == signal && "$(basename "$(readlink -f "$AGENTHOLD_DEPLOY_ROOT/current")")" == "$MOCK_NEW" ]]; then touch "$MOCK_DIR/restart-started"; sleep 2; fi
 if [[ "${MOCK_MODE:-}" == restartfail && "$(basename "$(readlink -f "$AGENTHOLD_DEPLOY_ROOT/current")")" == "$MOCK_NEW" ]]; then exit 1; fi
 exit 0
fi
exit 1
MOCK
# Health endpoints and an https /dl/ root (https://dl.example/dl/) backed by the fixture store.
cat > "$work/bin/curl" <<'MOCK'
#!/usr/bin/env bash
url=""; out=""
while (($#)); do case "$1" in -o) out="$2"; shift 2;; https://*|http://*) url="$1"; shift;; *) shift;; esac; done
case "$url" in
 */healthz) exit 0;;
 */api/v1/version)
  active="$(basename "$(readlink -f "$AGENTHOLD_DEPLOY_ROOT/current")")"
  if [[ "$active" != "$MOCK_NEW" ]]; then printf '{"version":"old"}\n'; elif [[ "${MOCK_MODE:-}" == healthfail ]]; then printf '{"version":"wrong"}\n'; else printf '{"version":"sha-%s"}\n' "$MOCK_NEW"; fi; exit 0;;
 https://dl.example/dl/*)
  rel="${url#https://dl.example/dl/}"
  [[ -f "$MOCK_DIR/src/$rel" ]] || exit 22
  echo "$rel" >> "$MOCK_DIR/fetched"
  cp "$MOCK_DIR/src/$rel" "$out";;
 *) exit 1;;
esac
MOCK
cat > "$work/bin/df" <<'MOCK'
#!/usr/bin/env bash
if [[ "${MOCK_MODE:-}" == nospace ]]; then printf 'Filesystem 1B-blocks Used Available Use%% Mounted on\nmock 1 1 0 100%% /\n'; else exec /usr/bin/df "$@"; fi
MOCK
chmod +x "$work/bin/uname" "$work/bin/systemctl" "$work/bin/curl" "$work/bin/df"

reset() {
  rm -rf "$work/opt/releases/$new" "$work/data/base" "$work/data/dl" "$work/restarts" "$work/restart-started" "$work/src" "$work/fetched"
  cp -a "$work/fixture" "$work/src"
  ln -sfn "$work/opt/releases/$old" "$work/opt/current"
}
run() { env MOCK_MODE="$1" bash "$script" "${@:2}" >"$work/run.log" 2>&1; }
deployed() {
  [[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$new" ]] || fail 'not switched'
  [[ "$(sha256sum "$work/data/base/$base_sha.ext4" | cut -d' ' -f1)" == "$base_sha" ]] || fail 'base not unpacked'
  cmp -s "$work/data/base/$base_sha.ext4.zst" "$work/fixture/$v/base-arm64.ext4.zst" || fail 'compressed base not kept for /dl/'
  cmp -s "$work/data/dl/channel.json" "$work/fixture/channel.json" || fail 'channel not published under /dl/'
  cmp -s "$work/data/dl/$v/SHA256SUMS" "$work/fixture/$v/SHA256SUMS" || fail 'SHA256SUMS not published under /dl/'
  cmp -s "$work/data/dl/$v/base-arm64.manifest.json" "$work/fixture/$v/base-arm64.manifest.json" || fail 'base manifest not published'
  [[ "$(stat -c %a "$work/data/base/$base_sha.ext4.zst")" == 644 ]] || fail 'compressed base mode'
}
unchanged() {
  [[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$old" ]] || fail "$1: switched"
  [[ ! -e "$work/data/dl/channel.json" ]] || fail "$1: published"
}

# A local store deploys, keeps the compressed base (hard-linked) and publishes the release under /dl/.
reset
run success || fail 'local deploy'
deployed
[[ "$(wc -l < "$work/restarts")" == 1 ]] || fail 'restart count'
[[ "$(stat -c %i "$work/data/base/$base_sha.ext4.zst")" == "$(stat -c %i "$work/src/$v/base-arm64.ext4.zst")" ]] || fail 'local base not hard-linked'
run success || fail 'second run'
grep -q "already running $new" "$work/run.log" || fail 'already running'
[[ "$(wc -l < "$work/restarts")" == 1 ]] || fail 'second run restarted'
# Running, but the /dl/ publication is gone: it is repaired without a restart.
rm -rf "$work/data/dl" "$work/data/base/$base_sha.ext4.zst"
run success || fail 'republish'
deployed
[[ "$(wc -l < "$work/restarts")" == 1 ]] || fail 'republish restarted'

# The configuration file selects the source when the environment does not.
reset
printf '# comment\nsource = %s\n' "$work/src" > "$work/pull.conf"
env -u AGENTHOLD_DEPLOY_SOURCE AGENTHOLD_DEPLOY_CONFIG="$work/pull.conf" MOCK_MODE=success bash "$script" >"$work/run.log" 2>&1 || fail 'config file source'
deployed

# An https /dl/ root works the same way (and the bytes are downloaded, not linked).
reset
AGENTHOLD_DEPLOY_SOURCE=https://dl.example/dl/ run success || fail 'https deploy'
deployed
grep -qx "$v/base-arm64.ext4.zst" "$work/fetched" || fail 'https base not fetched'
for bad in http://dl.example/dl/ https://dl.example/dl relative/path; do
  reset
  if AGENTHOLD_DEPLOY_SOURCE="$bad" run success; then fail "source $bad accepted"; fi
  unchanged "source $bad"
done

# Anything that does not verify leaves the running release alone.
mutate() { # mutate <mode>: break the store copy in one way
  local s="$work/src"
  case "$1" in
    corruptbinary) echo corrupt >> "$s/$v/agenthold-linux-arm64";;
    corruptbase) echo corrupt >> "$s/$v/base-arm64.ext4.zst";;
    resignedbase) echo corrupt >> "$s/$v/base-arm64.ext4.zst"; channel "$s";;
    wrongversion) printf '#!/usr/bin/env bash\necho "agenthold sha-%s (x)"\n' "$old" > "$s/$v/agenthold-linux-arm64"; channel "$s";;
    badsums) echo "$(printf '%064d' 0)  extra" >> "$s/$v/SHA256SUMS";;
    oldformat) jq '.format = 1' "$s/channel.json" > "$s/c" && mv "$s/c" "$s/channel.json";;
    wrongpath) jq --arg p "sha-$old/agenthold-linux-arm64" '.binary.path = $p' "$s/channel.json" > "$s/c" && mv "$s/c" "$s/channel.json";;
    traversal) jq '.version = "../../etc"' "$s/channel.json" > "$s/c" && mv "$s/c" "$s/channel.json";;
    symlink) mv "$s/$v/agenthold-linux-arm64" "$work/elsewhere"; ln -s "$work/elsewhere" "$s/$v/agenthold-linux-arm64";;
    nochannel) rm "$s/channel.json";;
    nospace) ;;
  esac
}
for mode in corruptbinary corruptbase resignedbase wrongversion badsums oldformat wrongpath traversal symlink nochannel nospace; do
  reset
  mutate "$mode"
  if run "$mode"; then fail "$mode unexpectedly succeeded"; fi
  unchanged "$mode"
  [[ ! -e "$work/restarts" ]] || fail "$mode restarted"
  [[ ! -e "$work/data/base/$base_sha.ext4" ]] || [[ "$mode" == nospace ]] || [[ "$(sha256sum "$work/data/base/$base_sha.ext4" | cut -d' ' -f1)" == "$base_sha" ]] || fail "$mode left a bad base"
  rm -f "$work/elsewhere"
done
for mode in restartfail healthfail; do
  reset
  if run "$mode"; then fail "$mode unexpectedly succeeded"; fi
  [[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$old" ]] || fail "$mode: no rollback"
  [[ "$(wc -l < "$work/restarts")" == 2 ]] || fail "$mode: restart count"
  [[ ! -e "$work/data/dl/channel.json" ]] || fail "$mode published"
done

# --check verifies everything (including unpacking the base) and touches nothing.
reset
run success --check || fail 'check'
grep -q "check passed: $v" "$work/run.log" || fail 'check output'
unchanged check
[[ ! -e "$work/data/base" && ! -e "$work/restarts" && -z "$(ls -A "$work/tmp")" ]] || fail 'check left files behind'
reset
mutate corruptbase
if run success --check; then fail 'check accepted a corrupt base'; fi
[[ -z "$(ls -A "$work/tmp")" ]] || fail 'failed check left files behind'

# --- pruning ---
hex() { printf '%064d' "$1" | tr '0' "$2"; }
rel() { printf '%040d' 0 | tr '0' "$1"; }
oldbase=$(hex 1 a); anc1=$(hex 1 b); anc2=$(hex 1 c); pinned=$(hex 1 d)
seed_prune() {
  reset
  rm -rf "$work/data" "$work/outside" "$work/opt/releases/"{3,4,5,6}*; mkdir -p "$work/data/base" "$work/data/agents/ag1" "$work/outside" "$work/data/dl"
  jq -n --arg b "$oldbase" '{base:{sha256:$b}}' > "$work/opt/releases/$old/runtime.manifest.json"
  for b in "$oldbase" "$anc1" "$anc2" "$pinned"; do printf 'img-%s' "$b" > "$work/data/base/$b.ext4"; printf 'zst-%s' "$b" > "$work/data/base/$b.ext4.zst"; done
  printf 'keep' > "$work/data/base/notes.txt"
  jq -n --arg p "$work/data/base/$pinned.ext4" '{baseImagePath:$p}' > "$work/data/agents/ag1/state.json"
  for i in 3 4 5 6; do d="$work/opt/releases/$(rel "$i")"; mkdir "$d"; touch -d "2020-01-0$i" "$d"; done
  touch -d '2021-01-01' "$work/opt/releases/$old"
  for i in 3 4 5 6; do d="$work/src/sha-$(rel "$i")"; mkdir "$d"; touch -d "2020-01-0$i" "$d"; done
  mkdir "$work/src/sha-$old"; touch -d '2019-01-01' "$work/src/sha-$old"
  mkdir "$work/data/dl/sha-$(rel 5)" "$work/data/dl/operator-notes"
}
seed_prune
run success || fail 'prune deploy'
grep -q 'prune: freed' "$work/run.log" || fail 'prune summary'
# current + previous bases and the agent-pinned base stay (with their compressed files); others go.
for b in "$base_sha" "$oldbase" "$pinned"; do [[ -f "$work/data/base/$b.ext4" ]] || fail "base $b pruned"; done
for b in "$oldbase" "$pinned"; do [[ -f "$work/data/base/$b.ext4.zst" ]] || fail "compressed base $b pruned"; done
for b in "$anc1" "$anc2"; do [[ ! -e "$work/data/base/$b.ext4" && ! -e "$work/data/base/$b.ext4.zst" ]] || fail "base $b kept"; done
[[ -f "$work/data/base/notes.txt" ]] || fail 'foreign file pruned'
grep -q "prune: removed $work/data/base/$anc1.ext4.zst" "$work/run.log" || fail 'compressed prune not logged'
# Newest three releases stay (new, previous, newest old); the rest go.
[[ -d "$work/opt/releases/$new" && -d "$work/opt/releases/$old" && -d "$work/opt/releases/$(rel 6)" ]] || fail 'kept releases'
[[ ! -e "$work/opt/releases/$(rel 3)" && ! -e "$work/opt/releases/$(rel 4)" && ! -e "$work/opt/releases/$(rel 5)" ]] || fail 'old releases'
# The store keeps the newest two versions plus the deployed and previous ones.
[[ -d "$work/src/$v" && -d "$work/src/sha-$old" && -d "$work/src/sha-$(rel 6)" ]] || fail 'kept store versions'
[[ ! -e "$work/src/sha-$(rel 3)" && ! -e "$work/src/sha-$(rel 4)" && ! -e "$work/src/sha-$(rel 5)" ]] || fail 'old store versions'
# /dl/ serves the deployed version only; operator files that are not versions stay.
[[ -d "$work/data/dl/$v" && ! -e "$work/data/dl/sha-$(rel 5)" && -d "$work/data/dl/operator-notes" ]] || fail 'dl prune'
# Releases N and N-1 on one base (the normal case since the base holds no sandboxd): that base and
# its compressed file stay, shared with the store by hard link; a base nothing names any more goes.
seed_prune
jq -n --arg b "$base_sha" '{base:{sha256:$b}}' > "$work/opt/releases/$old/runtime.manifest.json"
ln -f "$work/src/$v/base-arm64.ext4.zst" "$work/src/sha-$old/base-arm64.ext4.zst"
run success || fail 'shared base deploy'
[[ -f "$work/data/base/$base_sha.ext4" && -f "$work/data/base/$base_sha.ext4.zst" ]] || fail 'shared base pruned'
! grep -q "prune: removed $work/data/base/$base_sha" "$work/run.log" || fail 'shared base removal logged'
[[ "$(stat -c %i "$work/data/base/$base_sha.ext4.zst")" == "$(stat -c %i "$work/src/$v/base-arm64.ext4.zst")" ]] || fail 'compressed base not shared with the store'
[[ ! -e "$work/data/base/$oldbase.ext4" && ! -e "$work/data/base/$oldbase.ext4.zst" && -f "$work/data/base/$pinned.ext4" ]] || fail 'unnamed base kept or pinned base pruned'
[[ -f "$work/src/sha-$old/base-arm64.ext4.zst" ]] || fail 'previous store version pruned'
# An https source is never pruned.
seed_prune
AGENTHOLD_DEPLOY_SOURCE=https://dl.example/dl/ run success || fail 'https prune deploy'
[[ -d "$work/src/sha-$(rel 3)" ]] || fail 'https source pruned'
# An unreadable agent state means nothing can be proven unused: no base is pruned.
seed_prune; printf 'not json' > "$work/data/agents/ag1/state.json"
run success || fail 'unreadable state deploy'
[[ -f "$work/data/base/$anc1.ext4" && -f "$work/data/base/$anc2.ext4.zst" ]] || fail 'unreadable state pruned'
# Rollback and failed switches never prune.
for mode in healthfail restartfail; do
  seed_prune
  if run "$mode"; then fail "$mode unexpectedly succeeded"; fi
  [[ -f "$work/data/base/$anc1.ext4" && -f "$work/data/base/$anc2.ext4" && -d "$work/opt/releases/$(rel 3)" && -d "$work/src/sha-$(rel 3)" ]] || fail "$mode pruned"
  ! grep -q 'prune:' "$work/run.log" || fail "$mode logged a prune"
done
# PRUNE=0 opts out.
seed_prune
env PRUNE=0 MOCK_MODE=success bash "$script" >"$work/run.log" 2>&1 || fail 'PRUNE=0 deploy'
[[ -f "$work/data/base/$anc1.ext4" && -d "$work/opt/releases/$(rel 3)" ]] || fail 'PRUNE=0 pruned'
grep -q 'disabled by PRUNE=0' "$work/run.log" || fail 'PRUNE=0 message'
# Never leaves the cache: symlinked entries and a symlinked cache directory are refused.
seed_prune
printf 'precious' > "$work/outside/target"
ln -s "$work/outside/target" "$work/data/base/$(hex 1 e).ext4"
ln -s "$work/outside/target" "$work/data/base/$(hex 1 e).ext4.zst"
ln -s "$work/outside" "$work/opt/releases/$(rel 7)"
touch -h -d 2019-01-01 "$work/opt/releases/$(rel 7)"
ln -s "$work/outside" "$work/src/sha-$(rel 7)"
touch -h -d 2018-01-01 "$work/src/sha-$(rel 7)"
run success || fail 'symlink prune deploy'
[[ "$(cat "$work/outside/target")" == precious && -L "$work/data/base/$(hex 1 e).ext4" && -L "$work/opt/releases/$(rel 7)" && -L "$work/src/sha-$(rel 7)" ]] || fail 'followed a symlink'
rm -f "$work/opt/releases/$(rel 7)"
seed_prune
mv "$work/data/base" "$work/outside/base"; ln -s "$work/outside/base" "$work/data/base"
run success || fail 'symlinked cache deploy'
[[ -f "$work/outside/base/$anc1.ext4" ]] && grep -q 'not a plain directory' "$work/run.log" || fail 'symlinked cache pruned'
rm -f "$work/data/base"; mkdir "$work/data/base"

# An interrupted switch rolls back.
reset
env MOCK_MODE=signal bash "$script" >"$work/run.log" 2>&1 &
pid=$!
for _ in $(seq 1 100); do [[ -f "$work/restart-started" ]] && break; sleep 0.1; done
[[ -f "$work/restart-started" ]] || { kill "$pid" 2>/dev/null || true; fail 'restart did not start'; }
kill -TERM "$pid"
if wait "$pid"; then fail 'interrupted deployment unexpectedly succeeded'; fi
[[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$old" ]] || fail 'interrupt rollback'
[[ "$(wc -l < "$work/restarts")" == 2 ]] || fail 'interrupt restart count'
echo 'deployment pull tests passed'
