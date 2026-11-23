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
kkey=$(printf '%032d' 0 | tr 0 a)
bkey=$(printf '%032d' 0 | tr 0 b)
hkey=$(printf '%032d' 0 | tr 0 e)
A="$work/fixture/assets/arm64"
mkdir -p "$work/fixture/$v" "$work/opt/releases/$old" "$A"
printf '#!/usr/bin/env bash\necho "agenthold old (old)"\n' > "$work/opt/releases/$old/agenthold"
chmod +x "$work/opt/releases/$old/agenthold"
f="$work/fixture/$v"
printf '#!/usr/bin/env bash\necho "agenthold sha-%s (deadbeef)"\n' "$new" > "$f/agenthold-linux-arm64"
chmod +x "$f/agenthold-linux-arm64"
jq -n --arg version "$v" --arg k "$kkey" --arg b "$bkey" --arg h "$hkey" '{format:2,arch:"arm64",version:$version,files:{},kernel:{key:$k},base:{key:$b},harnesses:{key:$h}}' > "$f/runtime-linux-arm64.manifest.json"
printf 'small test disk image\n' > "$work/fixture/base"
printf 'small test kernel\n' > "$work/fixture/kernel"
printf 'small test harnesses layer\n' > "$work/fixture/harnesses"
base_sha=$(sha256sum "$work/fixture/base" | cut -d' ' -f1)
kernel_sha=$(sha256sum "$work/fixture/kernel" | cut -d' ' -f1)
harnesses_sha=$(sha256sum "$work/fixture/harnesses" | cut -d' ' -f1)
asset() { # asset <store dir> <kind> <key> <raw file>: (re)write <kind>-<key>.json for the .zst there
  local a="$1/assets/arm64"
  jq -n --arg kind "$2" --arg key "$3" --arg sha "$(sha256sum "$4" | cut -d' ' -f1)" --argjson size "$(stat -c %s "$4")" \
    --arg csha "$(sha256sum "$a/$2-$3.zst" | cut -d' ' -f1)" --argjson csize "$(stat -c %s "$a/$2-$3.zst")" \
    '{format:1,kind:$kind,arch:"arm64",key:$key,sha256:$sha,size:$size,compressed:{sha256:$csha,size:$csize}}' > "$a/$2-$3.json"
}
zstd -q -f "$work/fixture/base" -o "$A/base-$bkey.zst"
zstd -q -f "$work/fixture/kernel" -o "$A/kernel-$kkey.zst"
zstd -q -f "$work/fixture/harnesses" -o "$A/harnesses-$hkey.zst"
channel() { # channel <store dir>: (re)write asset manifests, SHA256SUMS and channel.json like build-local.sh
  local s="$1"
  asset "$s" base "$bkey" "$work/fixture/base"
  asset "$s" kernel "$kkey" "$work/fixture/kernel"
  asset "$s" harnesses "$hkey" "$work/fixture/harnesses"
  (cd "$s/$v" && sha256sum agenthold-linux-arm64 runtime-linux-arm64.manifest.json > SHA256SUMS)
  jq -n --arg commit "$new" --arg v "$v" --arg b "$(sha256sum "$s/$v/agenthold-linux-arm64" | cut -d' ' -f1)" \
    --arg m "$(sha256sum "$s/$v/runtime-linux-arm64.manifest.json" | cut -d' ' -f1)" --arg s "$(sha256sum "$s/$v/SHA256SUMS" | cut -d' ' -f1)" \
    --arg kk "$kkey" --arg ks "$(sha256sum "$s/assets/arm64/kernel-$kkey.json" | cut -d' ' -f1)" \
    --arg bk "$bkey" --arg bs "$(sha256sum "$s/assets/arm64/base-$bkey.json" | cut -d' ' -f1)" \
    --arg hk "$hkey" --arg hs "$(sha256sum "$s/assets/arm64/harnesses-$hkey.json" | cut -d' ' -f1)" \
    '{format:3,arch:"arm64",commit:$commit,version:$v,binary:{path:($v+"/agenthold-linux-arm64"),sha256:$b},
      runtime_manifest:{path:($v+"/runtime-linux-arm64.manifest.json"),sha256:$m},sums:{path:($v+"/SHA256SUMS"),sha256:$s},
      kernel:{key:$kk,manifest:{path:("assets/arm64/kernel-"+$kk+".json"),sha256:$ks}},
      base:{key:$bk,manifest:{path:("assets/arm64/base-"+$bk+".json"),sha256:$bs}},
      harnesses:{key:$hk,manifest:{path:("assets/arm64/harnesses-"+$hk+".json"),sha256:$hs}}}' > "$s/channel.json"
}
channel "$work/fixture"

cat > "$work/bin/uname" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == -m ]]; then echo aarch64; else exec /usr/bin/uname "$@"; fi
MOCK
cat > "$work/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == is-active ]]; then exit 0; fi
if [[ "$1" == show ]]; then echo "invocation-$(basename "$(readlink -f "$AGENTHOLD_DEPLOY_ROOT/current")")"; exit 0; fi
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
  if [[ "$active" != "$MOCK_NEW" ]]; then
    ver="$("$AGENTHOLD_DEPLOY_ROOT/current/agenthold" --version | awk '{print $2}')"
    printf '{"version":"%s"}\n' "$ver"
  elif [[ "${MOCK_MODE:-}" == healthfail ]]; then
    printf '{"version":"wrong"}\n'
  else
    printf '{"version":"%s"}\n' "${MOCK_NEW_VERSION:-sha-$MOCK_NEW}"
  fi
  exit 0;;
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
# The node service's journal (node mode): the start line names the running binary's version.
cat > "$work/bin/journalctl" <<'MOCK'
#!/usr/bin/env bash
active="$(basename "$(readlink -f "$AGENTHOLD_DEPLOY_ROOT/current")")"
[[ " $* " == *" _SYSTEMD_INVOCATION_ID=invocation-$active "* ]] || exit 1
ver="$("$AGENTHOLD_DEPLOY_ROOT/current/agenthold" --version | awk '{print $2}')"
[[ "$active" == "$MOCK_NEW" ]] && ver="${MOCK_NEW_VERSION:-sha-$MOCK_NEW}"
[[ "$active" == "$MOCK_NEW" && "${MOCK_MODE:-}" == healthfail ]] && ver=wrong
printf '{"level":"INFO","msg":"edge node starting","version":"%s","driver":"firecracker"}\n' "$ver"
[[ "${MOCK_MODE:-}" == unreachable && "$active" == "$MOCK_NEW" ]] || printf '{"level":"INFO","msg":"connected to controller"}\n'
MOCK
chmod +x "$work/bin/uname" "$work/bin/systemctl" "$work/bin/curl" "$work/bin/df" "$work/bin/journalctl"

reset() {
  rm -rf "$work/opt/releases/$new" "$work/data/base" "$work/data/harnesses" "$work/data/kernel" "$work/data/assets" "$work/data/dl" "$work/restarts" "$work/restart-started" "$work/src" "$work/fetched"
  mkdir -p "$work/opt/releases/$old"
  printf '#!/usr/bin/env bash\necho "agenthold old (old)"\n' > "$work/opt/releases/$old/agenthold"
  chmod +x "$work/opt/releases/$old/agenthold"
  cp -a "$work/fixture" "$work/src"
  ln -sfn "$work/opt/releases/$old" "$work/opt/current"
}
run() { env MOCK_MODE="$1" bash "$script" "${@:2}" >"$work/run.log" 2>&1; }
deployed() {
  [[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$new" ]] || fail 'not switched'
  [[ "$(sha256sum "$work/data/base/$base_sha.ext4" | cut -d' ' -f1)" == "$base_sha" ]] || fail 'base not unpacked'
  cmp -s "$work/data/base/$base_sha.ext4.zst" "$A/base-$bkey.zst" || fail 'compressed base not kept for /dl/'
  [[ "$(sha256sum "$work/data/harnesses/$harnesses_sha.ext4" | cut -d' ' -f1)" == "$harnesses_sha" ]] || fail 'harnesses not unpacked'
  cmp -s "$work/data/harnesses/$harnesses_sha.ext4.zst" "$A/harnesses-$hkey.zst" || fail 'compressed harnesses not kept for /dl/'
  cmp -s "$work/data/kernel/$kernel_sha" "$work/fixture/kernel" || fail 'kernel not unpacked'
  cmp -s "$work/data/kernel/$kernel_sha.zst" "$A/kernel-$kkey.zst" || fail 'compressed kernel not kept for /dl/'
  cmp -s "$work/data/assets/arm64/base-$bkey.json" "$A/base-$bkey.json" || fail 'base asset manifest not placed'
  cmp -s "$work/data/assets/arm64/harnesses-$hkey.json" "$A/harnesses-$hkey.json" || fail 'harnesses asset manifest not placed'
  cmp -s "$work/data/assets/arm64/kernel-$kkey.json" "$A/kernel-$kkey.json" || fail 'kernel asset manifest not placed'
  [[ -f "$work/opt/releases/$new/kernel.json" && -f "$work/opt/releases/$new/base.json" && -f "$work/opt/releases/$new/harnesses.json" ]] || fail 'release does not record its assets'
  cmp -s "$work/data/dl/channel.json" "$work/fixture/channel.json" || fail 'channel not published under /dl/'
  cmp -s "$work/data/dl/$v/SHA256SUMS" "$work/fixture/$v/SHA256SUMS" || fail 'SHA256SUMS not published under /dl/'
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
[[ "$(stat -c %i "$work/data/base/$base_sha.ext4.zst")" == "$(stat -c %i "$work/src/assets/arm64/base-$bkey.zst")" ]] || fail 'local base not hard-linked'
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
grep -qx "assets/arm64/base-$bkey.zst" "$work/fetched" || fail 'https base not fetched'
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
    corruptbase) echo corrupt >> "$s/assets/arm64/base-$bkey.zst";;
    corruptharnesses) echo corrupt >> "$s/assets/arm64/harnesses-$hkey.zst";;
    corruptkernel) echo corrupt >> "$s/assets/arm64/kernel-$kkey.zst";;
    resignedbase) zstd -q -f "$work/fixture/kernel" -o "$s/assets/arm64/base-$bkey.zst"; channel "$s"
      # The manifest still names the base's uncompressed sha256: the re-signed file unpacks to other bytes.
      jq --arg sha "$base_sha" --argjson size "$(stat -c %s "$work/fixture/base")" '.sha256 = $sha | .size = $size' "$s/assets/arm64/base-$bkey.json" > "$s/m" &&
        mv "$s/m" "$s/assets/arm64/base-$bkey.json"
      jq --arg s "$(sha256sum "$s/assets/arm64/base-$bkey.json" | cut -d' ' -f1)" '.base.manifest.sha256 = $s' "$s/channel.json" > "$s/c" && mv "$s/c" "$s/channel.json";;
    badasset) echo ' ' >> "$s/assets/arm64/base-$bkey.json";;
    otherkey) jq --arg k "$(printf '%032d' 0 | tr 0 c)" '.base.key = $k' "$s/$v/runtime-linux-arm64.manifest.json" > "$s/m" && mv "$s/m" "$s/$v/runtime-linux-arm64.manifest.json"; channel "$s";;
    wrongversion) printf '#!/usr/bin/env bash\necho "agenthold sha-%s (x)"\n' "$old" > "$s/$v/agenthold-linux-arm64"; channel "$s";;
    badsums) echo "$(printf '%064d' 0)  extra" >> "$s/$v/SHA256SUMS";;
    oldformat) jq '.format = 2' "$s/channel.json" > "$s/c" && mv "$s/c" "$s/channel.json";;
    assetpath) jq '.base.manifest.path = "assets/arm64/../../../etc/x.json"' "$s/channel.json" > "$s/c" && mv "$s/c" "$s/channel.json";;
    wrongpath) jq --arg p "sha-$old/agenthold-linux-arm64" '.binary.path = $p' "$s/channel.json" > "$s/c" && mv "$s/c" "$s/channel.json";;
    traversal) jq '.version = "../../etc"' "$s/channel.json" > "$s/c" && mv "$s/c" "$s/channel.json";;
    symlink) mv "$s/$v/agenthold-linux-arm64" "$work/elsewhere"; ln -s "$work/elsewhere" "$s/$v/agenthold-linux-arm64";;
    nochannel) rm "$s/channel.json";;
    nospace) ;;
  esac
}
for mode in corruptbinary corruptbase corruptharnesses corruptkernel resignedbase badasset otherkey wrongversion badsums oldformat wrongpath assetpath traversal symlink nochannel nospace; do
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

# Node mode: no HTTP; health is the node service's journal (its version, then a controller connection).
reset
AGENTHOLD_DEPLOY_MODE=node AGENTHOLD_DEPLOY_HEALTH=http://127.0.0.1:1 run success || fail 'node deploy'
deployed
[[ "$(wc -l < "$work/restarts")" == 1 ]] || fail 'node restart count'
for mode in healthfail unreachable; do
  reset
  if AGENTHOLD_DEPLOY_MODE=node run "$mode"; then fail "node $mode unexpectedly succeeded"; fi
  [[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$old" ]] || fail "node $mode: no rollback"
  [[ "$(wc -l < "$work/restarts")" == 2 ]] || fail "node $mode: restart count"
done
reset
if AGENTHOLD_DEPLOY_MODE=bogus run success; then fail 'unknown mode accepted'; fi
unchanged 'unknown mode'

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
  mkdir -p "$work/data/kernel" && printf 'old kernel' > "$work/data/kernel/$(hex 1 e)" && printf 'z' > "$work/data/kernel/$(hex 1 e).zst"
  printf 'x' > "$work/src/assets/arm64/base-$(printf '%032d' 0 | tr 0 f).zst"
}
seed_prune
run success || fail 'prune deploy'
grep -q 'prune: freed' "$work/run.log" || fail 'prune summary'
# Kernels of no kept release and store assets no kept version names go; the current ones stay.
[[ ! -e "$work/data/kernel/$(hex 1 e)" && ! -e "$work/data/kernel/$(hex 1 e).zst" && -f "$work/data/kernel/$kernel_sha" ]] || fail 'kernel prune'
[[ ! -e "$work/src/assets/arm64/base-$(printf '%032d' 0 | tr 0 f).zst" && -f "$work/src/assets/arm64/base-$bkey.zst" && -f "$work/src/assets/arm64/kernel-$kkey.json" ]] || fail 'store asset prune'
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
run success || fail 'shared base deploy'
[[ -f "$work/data/base/$base_sha.ext4" && -f "$work/data/base/$base_sha.ext4.zst" ]] || fail 'shared base pruned'
! grep -q "prune: removed $work/data/base/$base_sha" "$work/run.log" || fail 'shared base removal logged'
[[ "$(stat -c %i "$work/data/base/$base_sha.ext4.zst")" == "$(stat -c %i "$work/src/assets/arm64/base-$bkey.zst")" ]] || fail 'compressed base not shared with the store'
[[ ! -e "$work/data/base/$oldbase.ext4" && ! -e "$work/data/base/$oldbase.ext4.zst" && -f "$work/data/base/$pinned.ext4" ]] || fail 'unnamed base kept or pinned base pruned'
[[ -d "$work/src/sha-$old" && -f "$work/src/assets/arm64/base-$bkey.zst" ]] || fail 'previous store version or the current assets pruned'
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

# --- semver releases (v0.1.0, prereleases, same-commit sha -> semver transition, mixed pruning) ---
make_semver_store() { # make_semver_store <store dir> <version> <commit>
  local s="$1" ver="$2" c="$3"
  rm -rf "$s"
  mkdir -p "$s/$ver" "$s/assets/arm64"
  cp "$A/base-$bkey.zst" "$A/kernel-$kkey.zst" "$A/harnesses-$hkey.zst" "$s/assets/arm64/"
  asset "$s" base "$bkey" "$work/fixture/base"
  asset "$s" kernel "$kkey" "$work/fixture/kernel"
  asset "$s" harnesses "$hkey" "$work/fixture/harnesses"
  printf '#!/usr/bin/env bash\necho "agenthold %s (%s)"\n' "$ver" "${c:0:12}" > "$s/$ver/agenthold-linux-arm64"
  chmod +x "$s/$ver/agenthold-linux-arm64"
  jq -n --arg version "$ver" --arg k "$kkey" --arg b "$bkey" --arg h "$hkey" '{format:2,arch:"arm64",version:$version,files:{},kernel:{key:$k},base:{key:$b},harnesses:{key:$h}}' > "$s/$ver/runtime-linux-arm64.manifest.json"
  (cd "$s/$ver" && sha256sum agenthold-linux-arm64 runtime-linux-arm64.manifest.json > SHA256SUMS)
  jq -n --arg commit "$c" --arg v "$ver" --arg b "$(sha256sum "$s/$ver/agenthold-linux-arm64" | cut -d' ' -f1)" \
    --arg m "$(sha256sum "$s/$ver/runtime-linux-arm64.manifest.json" | cut -d' ' -f1)" --arg sum "$(sha256sum "$s/$ver/SHA256SUMS" | cut -d' ' -f1)" \
    --arg kk "$kkey" --arg ks "$(sha256sum "$s/assets/arm64/kernel-$kkey.json" | cut -d' ' -f1)" \
    --arg bk "$bkey" --arg bs "$(sha256sum "$s/assets/arm64/base-$bkey.json" | cut -d' ' -f1)" \
    --arg hk "$hkey" --arg hs "$(sha256sum "$s/assets/arm64/harnesses-$hkey.json" | cut -d' ' -f1)" \
    '{format:3,arch:"arm64",commit:$commit,version:$v,binary:{path:($v+"/agenthold-linux-arm64"),sha256:$b},
      runtime_manifest:{path:($v+"/runtime-linux-arm64.manifest.json"),sha256:$m},sums:{path:($v+"/SHA256SUMS"),sha256:$sum},
      kernel:{key:$kk,manifest:{path:("assets/arm64/kernel-"+$kk+".json"),sha256:$ks}},
      base:{key:$bk,manifest:{path:("assets/arm64/base-"+$bk+".json"),sha256:$bs}},
      harnesses:{key:$hk,manifest:{path:("assets/arm64/harnesses-"+$hk+".json"),sha256:$hs}}}' > "$s/channel.json"
}

# Deploy v0.1.0 from a local store and verify idempotency, --check, and HTTPS /dl/ deploy.
reset
make_semver_store "$work/src" v0.1.0 "$new"
MOCK_NEW=v0.1.0 MOCK_NEW_VERSION=v0.1.0 run success --check || fail 'semver check'
grep -q 'check passed: v0.1.0' "$work/run.log" || fail 'semver check output'
MOCK_NEW=v0.1.0 MOCK_NEW_VERSION=v0.1.0 run success || fail 'semver local deploy'
[[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/v0.1.0" ]] || fail 'semver current link'
cmp -s "$work/data/dl/v0.1.0/SHA256SUMS" "$work/src/v0.1.0/SHA256SUMS" || fail 'semver dl SHA256SUMS'
MOCK_NEW=v0.1.0 MOCK_NEW_VERSION=v0.1.0 run success || fail 'semver second run'
grep -q 'already running v0.1.0' "$work/run.log" || fail 'semver already running'
[[ "$(wc -l < "$work/restarts")" == 1 ]] || fail 'semver second run restarted'

# Deploy a semver prerelease (v0.1.0-rc.1) over HTTPS.
reset
rm -rf "$work/opt/releases/v0.1.0-rc.1"
make_semver_store "$work/src" v0.1.0-rc.1 "$new"
AGENTHOLD_DEPLOY_SOURCE=https://dl.example/dl/ MOCK_NEW=v0.1.0-rc.1 MOCK_NEW_VERSION=v0.1.0-rc.1 run success || fail 'semver prerelease https deploy'
[[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/v0.1.0-rc.1" ]] || fail 'semver prerelease current link'

# Transition from sha-<commit> to v0.1.0 on the EXACT same commit, and roll back cleanly on failure.
reset
rm -rf "$work/opt/releases/v0.1.0"
run success || fail 'sha pre-transition deploy'
[[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$new" ]] || fail 'sha current before transition'
make_semver_store "$work/src" v0.1.0 "$new"
if MOCK_NEW=v0.1.0 MOCK_NEW_VERSION=v0.1.0 run healthfail; then fail 'same-commit semver healthfail succeeded'; fi
[[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$new" ]] || fail 'same-commit semver rollback failed'
MOCK_NEW=v0.1.0 MOCK_NEW_VERSION=v0.1.0 run success || fail 'same-commit sha -> v0.1.0 deploy'
[[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/v0.1.0" ]] || fail 'same-commit semver switch'
[[ -d "$work/data/dl/v0.1.0" && ! -e "$work/data/dl/$v" ]] || fail 'same-commit semver dl prune'

# Invalid semver / mismatched sha versions in channel.json are refused.
for badver in v01.0.0 v0.1 v0.1.0- v0.1.0..1 "sha-$old"; do
  reset
  make_semver_store "$work/src" v0.1.0 "$new"
  jq --arg v "$badver" '.version = $v | .binary.path = ($v + "/agenthold-linux-arm64") | .runtime_manifest.path = ($v + "/runtime-linux-arm64.manifest.json") | .sums.path = ($v + "/SHA256SUMS")' \
    "$work/src/channel.json" > "$work/src/c" && mv "$work/src/c" "$work/src/channel.json"
  if MOCK_NEW="$badver" MOCK_NEW_VERSION="$badver" run success; then fail "invalid version $badver accepted"; fi
  unchanged "invalid version $badver"
done

# Mixed semver + sha pruning: keeps newest releases/store versions, preserves previous semver version
# and store assets referenced by semver versions in $SOURCE.
reset
rm -rf "$work/opt/releases/"v* "$work/opt/releases/"{3,4,5,6}*
make_semver_store "$work/src" v0.1.0 "$old"
MOCK_NEW=v0.1.0 MOCK_NEW_VERSION=v0.1.0 run success || fail 'seed v0.1.0'
make_semver_store "$work/src" v0.2.0 "$new"
# Add older semver + sha dirs in releases, store, and /dl/, plus an unreferenced store asset.
mkdir -p "$work/opt/releases/v0.0.1" "$work/opt/releases/v0.0.2" "$work/opt/releases/$(rel 3)"
touch -d '2020-01-01' "$work/opt/releases/v0.0.1"
touch -d '2020-01-02' "$work/opt/releases/v0.0.2"
touch -d '2020-01-03' "$work/opt/releases/$(rel 3)"
touch -d '2021-01-01' "$work/opt/releases/v0.1.0"
mkdir -p "$work/src/v0.0.1" "$work/src/v0.0.2" "$work/src/v0.1.0"
touch -d '2020-01-01' "$work/src/v0.0.1"
touch -d '2020-01-02' "$work/src/v0.0.2"
touch -d '2019-01-01' "$work/src/v0.1.0"
mkdir -p "$work/data/dl/v0.0.1"
printf 'stale' > "$work/src/assets/arm64/base-$(printf '%032d' 0 | tr 0 f).zst"
MOCK_NEW=v0.2.0 MOCK_NEW_VERSION=v0.2.0 run success || fail 'mixed prune deploy v0.2.0'
[[ -d "$work/opt/releases/v0.2.0" && -d "$work/opt/releases/v0.1.0" && -d "$work/opt/releases/$old" ]] || fail 'mixed kept releases'
[[ ! -e "$work/opt/releases/v0.0.1" && ! -e "$work/opt/releases/v0.0.2" ]] || fail 'mixed old semver releases not pruned'
[[ -d "$work/src/v0.2.0" && -d "$work/src/v0.1.0" && ! -e "$work/src/v0.0.1" ]] || fail 'mixed store versions'
[[ -d "$work/data/dl/v0.2.0" && ! -e "$work/data/dl/v0.1.0" && ! -e "$work/data/dl/v0.0.1" ]] || fail 'mixed dl prune'
[[ -f "$work/src/assets/arm64/base-$bkey.zst" && -f "$work/src/assets/arm64/kernel-$kkey.json" && ! -e "$work/src/assets/arm64/base-$(printf '%032d' 0 | tr 0 f).zst" ]] || fail 'mixed store assets prune'

echo 'deployment pull tests passed'
