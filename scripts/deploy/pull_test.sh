#!/usr/bin/env bash
# End-to-end pull tests use local fixture files and fake systemctl/curl, never a real service.
set -euo pipefail
[[ "$(uname -s)" == Linux ]] || { echo 'run on Linux'; exit 1; }
for tool in jq zstd sha256sum; do command -v "$tool" >/dev/null || { echo "$tool required" >&2; exit 1; }; done
script="$(cd "$(dirname "$0")" && pwd)/pull.sh"
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
export MOCK_DIR="$work" AGENTHOLD_DEPLOY_ROOT="$work/opt" AGENTHOLD_DEPLOY_DATA_DIR="$work/data" AGENTHOLD_DEPLOY_HOST_MOUNT="$work" AGENTHOLD_DEPLOY_TIMEOUT=1
mkdir -p "$work/bin" "$work/fixture" "$work/opt/releases" "$work/data"
export PATH="$work/bin:$PATH"
old=1111111111111111111111111111111111111111
new=2222222222222222222222222222222222222222
export MOCK_NEW="$new"
release="https://github.com/gempir/agenthold/releases/download/sha-$new"
mkdir "$work/opt/releases/$old"
printf '#!/usr/bin/env bash\necho "agenthold old (old)"\n' > "$work/opt/releases/$old/agenthold"
chmod +x "$work/opt/releases/$old/agenthold"
printf '#!/usr/bin/env bash\necho "agenthold sha-%s (deadbeef)"\n' "$new" > "$work/fixture/agenthold"
printf 'small test disk image\n' > "$work/fixture/base"
zstd -q -f "$work/fixture/base" -o "$work/fixture/base.zst"
base_sha=$(sha256sum "$work/fixture/base" | cut -d' ' -f1)
base_zsha=$(sha256sum "$work/fixture/base.zst" | cut -d' ' -f1)
base_size=$(stat -c %s "$work/fixture/base")
base_zsize=$(stat -c %s "$work/fixture/base.zst")
jq -n --arg version "sha-$new" --arg url "$release/base-arm64.ext4.zst" --arg raw "$base_sha" --arg compressed "$base_zsha" --argjson rawsize "$base_size" --argjson compressedsize "$base_zsize" '{format:1,arch:"arm64",version:$version,base:{url:$url,sha256:$raw,size:$rawsize,compressed:{sha256:$compressed,size:$compressedsize}}}' > "$work/fixture/runtime.json"
binary_sha=$(sha256sum "$work/fixture/agenthold" | cut -d' ' -f1)
manifest_sha=$(sha256sum "$work/fixture/runtime.json" | cut -d' ' -f1)
jq -n --arg commit "$new" --arg url "$release" --arg binary "$binary_sha" --arg manifest "$manifest_sha" '{format:1,arch:"arm64",commit:$commit,binary:{url:($url+"/agenthold-linux-arm64"),sha256:$binary},runtime_manifest:{url:($url+"/runtime-linux-arm64.manifest.json"),sha256:$manifest}}' > "$work/fixture/channel.json"
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
cat > "$work/bin/curl" <<'MOCK'
#!/usr/bin/env bash
url=""; out=""
while (($#)); do case "$1" in -o) out="$2"; shift 2;; https://*|http://*) url="$1"; shift;; *) shift;; esac; done
case "$url" in
 */healthz) exit 0;;
 */api/v1/version)
  active="$(basename "$(readlink -f "$AGENTHOLD_DEPLOY_ROOT/current")")"
  if [[ "$active" != "$MOCK_NEW" ]]; then printf '{"version":"old"}\n'; elif [[ "${MOCK_MODE:-}" == healthfail ]]; then printf '{"version":"wrong"}\n'; else printf '{"version":"sha-%s"}\n' "$MOCK_NEW"; fi; exit 0;;
 https://api.github.com/repos/gempir/agenthold/git/ref/heads/main)
  if [[ "${MOCK_MODE:-}" == headmoves ]]; then
   if [[ -e "$MOCK_DIR/head-seen" ]]; then printf '{"object":{"type":"commit","sha":"1111111111111111111111111111111111111111"}}\n'; else touch "$MOCK_DIR/head-seen"; printf '{"object":{"type":"commit","sha":"%s"}}\n' "$MOCK_NEW"; fi
  elif [[ "${MOCK_MODE:-}" == stale ]]; then printf '{"object":{"type":"commit","sha":"1111111111111111111111111111111111111111"}}\n'; else printf '{"object":{"type":"commit","sha":"%s"}}\n' "$MOCK_NEW"; fi; exit 0;;
 */demo-main/channel.json) cp "$MOCK_DIR/fixture/channel.json" "$out";;
 */runtime-linux-arm64.manifest.json) cp "$MOCK_DIR/fixture/runtime.json" "$out";;
 */agenthold-linux-arm64) cp "$MOCK_DIR/fixture/agenthold" "$out"; if [[ "${MOCK_MODE:-}" == corruptbinary ]]; then echo corrupt >> "$out"; fi;;
 */base-arm64.ext4.zst) cp "$MOCK_DIR/fixture/base.zst" "$out"; if [[ "${MOCK_MODE:-}" == corruptbase ]]; then echo corrupt >> "$out"; fi;;
 *) exit 1;;
esac
MOCK
cat > "$work/bin/df" <<'MOCK'
#!/usr/bin/env bash
if [[ "${MOCK_MODE:-}" == nospace ]]; then printf 'Filesystem 1B-blocks Used Available Use%% Mounted on\nmock 1 1 0 100%% /\n'; else exec /usr/bin/df "$@"; fi
MOCK
chmod +x "$work/bin/uname" "$work/bin/systemctl" "$work/bin/curl" "$work/bin/df"
reset() {
 rm -rf "$work/opt/releases/$new" "$work/data/base" "$work/restarts" "$work/head-seen" "$work/restart-started"
 ln -sfn "$work/opt/releases/$old" "$work/opt/current"
}
run() { env MOCK_MODE="$1" bash "$script" >"$work/run.log" 2>&1; }
reset
run success || { cat "$work/run.log"; exit 1; }
[[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$new" ]]
[[ "$(sha256sum "$work/data/base/$base_sha.ext4" | cut -d' ' -f1)" == "$base_sha" ]]
[[ "$(wc -l < "$work/restarts")" == 1 ]]
run success
[[ "$(wc -l < "$work/restarts")" == 1 ]]
for mode in stale headmoves corruptbinary corruptbase nospace; do
 reset
 if run "$mode"; then echo "$mode unexpectedly succeeded" >&2; exit 1; fi
 [[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$old" && ! -e "$work/restarts" ]]
done
for mode in restartfail healthfail; do
 reset
 if run "$mode"; then echo "$mode unexpectedly succeeded" >&2; exit 1; fi
 [[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$old" ]]
 [[ "$(wc -l < "$work/restarts")" == 2 ]]
done

# --- pruning ---
hex() { printf '%064d' "$1" | tr '0' "$2"; }
oldbase=$(hex 1 a); anc1=$(hex 1 b); anc2=$(hex 1 c); pinned=$(hex 1 d)
seed_prune() {
 reset
 rm -rf "$work/data" "$work/outside" "$work/opt/releases/"{3,4,5,6}* ; mkdir -p "$work/data/base" "$work/data/agents/ag1" "$work/outside"
 jq -n --arg b "$oldbase" '{base:{sha256:$b}}' > "$work/opt/releases/$old/runtime.manifest.json"
 for b in "$oldbase" "$anc1" "$anc2" "$pinned"; do printf 'img-%s' "$b" > "$work/data/base/$b.ext4"; done
 printf 'keep' > "$work/data/base/notes.txt"
 jq -n --arg p "$work/data/base/$pinned.ext4" '{baseImagePath:$p}' > "$work/data/agents/ag1/state.json"
 for i in 3 4 5 6; do d="$work/opt/releases/$(printf '%040d' 0 | tr '0' "$i")"; mkdir "$d"; touch -d "2020-01-0$i" "$d"; done
 touch -d '2021-01-01' "$work/opt/releases/$old"
}
rel() { printf '%040d' 0 | tr '0' "$1"; }
seed_prune
run success || { cat "$work/run.log"; exit 1; }
grep -q 'prune: freed' "$work/run.log"
# current + previous bases and the agent-pinned base stay; unreferenced ones go.
[[ -f "$work/data/base/$base_sha.ext4" && -f "$work/data/base/$oldbase.ext4" && -f "$work/data/base/$pinned.ext4" ]]
[[ ! -e "$work/data/base/$anc1.ext4" && ! -e "$work/data/base/$anc2.ext4" && -f "$work/data/base/notes.txt" ]]
grep -q "prune: removed $work/data/base/$anc1.ext4" "$work/run.log"
# Newest three releases stay (new, previous, newest old); the rest go.
[[ -d "$work/opt/releases/$new" && -d "$work/opt/releases/$old" && -d "$work/opt/releases/$(rel 6)" ]]
[[ ! -e "$work/opt/releases/$(rel 3)" && ! -e "$work/opt/releases/$(rel 4)" && ! -e "$work/opt/releases/$(rel 5)" ]]
# An unreadable agent state means nothing can be proven unused: no base is pruned.
seed_prune; printf 'not json' > "$work/data/agents/ag1/state.json"
run success || { cat "$work/run.log"; exit 1; }
[[ -f "$work/data/base/$anc1.ext4" && -f "$work/data/base/$anc2.ext4" ]]
# Rollback and failed switches never prune.
for mode in healthfail restartfail; do
 seed_prune
 if run "$mode"; then echo "$mode unexpectedly succeeded" >&2; exit 1; fi
 [[ -f "$work/data/base/$anc1.ext4" && -f "$work/data/base/$anc2.ext4" && -d "$work/opt/releases/$(rel 3)" ]]
 ! grep -q 'prune:' "$work/run.log"
done
# PRUNE=0 opts out.
seed_prune
env PRUNE=0 MOCK_MODE=success bash "$script" >"$work/run.log" 2>&1 || { cat "$work/run.log"; exit 1; }
[[ -f "$work/data/base/$anc1.ext4" && -d "$work/opt/releases/$(rel 3)" ]]
grep -q 'disabled by PRUNE=0' "$work/run.log"
# Never leaves the cache: symlinked entries and a symlinked cache directory are refused.
seed_prune
printf 'precious' > "$work/outside/target"
ln -s "$work/outside/target" "$work/data/base/$(hex 1 e).ext4"
ln -s "$work/outside" "$work/opt/releases/$(rel 7)"
touch -h -d 2019-01-01 "$work/opt/releases/$(rel 7)"
run success || { cat "$work/run.log"; exit 1; }
[[ "$(cat "$work/outside/target")" == precious && -L "$work/data/base/$(hex 1 e).ext4" && -L "$work/opt/releases/$(rel 7)" ]]
rm -f "$work/opt/releases/$(rel 7)"
seed_prune
mv "$work/data/base" "$work/outside/base"; ln -s "$work/outside/base" "$work/data/base"
run success || { cat "$work/run.log"; exit 1; }
[[ -f "$work/outside/base/$anc1.ext4" ]] && grep -q 'not a plain directory' "$work/run.log"
rm -f "$work/data/base"; mkdir "$work/data/base"
reset
env MOCK_MODE=signal bash "$script" >"$work/run.log" 2>&1 &
pid=$!
for _ in $(seq 1 100); do [[ -f "$work/restart-started" ]] && break; sleep 0.1; done
[[ -f "$work/restart-started" ]] || { kill "$pid" 2>/dev/null || true; echo 'restart did not start' >&2; exit 1; }
kill -TERM "$pid"
if wait "$pid"; then echo 'interrupted deployment unexpectedly succeeded' >&2; exit 1; fi
[[ "$(readlink -f "$work/opt/current")" == "$work/opt/releases/$old" ]]
[[ "$(wc -l < "$work/restarts")" == 2 ]]
echo 'deployment pull tests passed' 
