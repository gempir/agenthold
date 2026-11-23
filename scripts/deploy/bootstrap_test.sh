#!/usr/bin/env bash
# bootstrap.sh installs a fixture store's release (through pull.sh --install) and the units, once.
set -euo pipefail
[[ "$(uname -s)" == Linux ]] || { echo 'run on Linux'; exit 1; }
script="$(cd "$(dirname "$0")" && pwd)/bootstrap.sh"
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
mkdir -p "$work/bin"
cat > "$work/bin/uname" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == -m ]]; then echo aarch64; else exec /usr/bin/uname "$@"; fi
MOCK
chmod +x "$work/bin/uname"
export PATH="$work/bin:$PATH"
commit=3333333333333333333333333333333333333333
v="sha-$commit"
kkey=$(printf '%032d' 0 | tr 0 a); bkey=$(printf '%032d' 0 | tr 0 b)
store="$work/store"; A="$store/assets/arm64"
mkdir -p "$store/$v" "$A"
printf '#!/usr/bin/env bash\necho "agenthold sha-%s (333333333333)"\n' "$commit" > "$store/$v/agenthold-linux-arm64"
chmod +x "$store/$v/agenthold-linux-arm64"
jq -n --arg v "$v" --arg k "$kkey" --arg b "$bkey" '{format:2,arch:"arm64",version:$v,files:{},kernel:{key:$k},base:{key:$b}}' > "$store/$v/runtime-linux-arm64.manifest.json"
printf 'base image\n' > "$work/base"; printf 'kernel\n' > "$work/kernel"
for k in base kernel; do
  key=$kkey; [[ $k == base ]] && key=$bkey
  zstd -q -f --long=27 "$work/$k" -o "$A/$k-$key.zst"
  jq -n --arg kind "$k" --arg key "$key" --arg sha "$(sha256sum "$work/$k" | cut -d' ' -f1)" --argjson size "$(stat -c %s "$work/$k")" \
    --arg csha "$(sha256sum "$A/$k-$key.zst" | cut -d' ' -f1)" --argjson csize "$(stat -c %s "$A/$k-$key.zst")" \
    '{format:1,kind:$kind,arch:"arm64",key:$key,sha256:$sha,size:$size,compressed:{sha256:$csha,size:$csize}}' > "$A/$k-$key.json"
done
(cd "$store/$v" && sha256sum agenthold-linux-arm64 runtime-linux-arm64.manifest.json > SHA256SUMS)
s() { sha256sum "$1" | cut -d' ' -f1; }
jq -n --arg c "$commit" --arg v "$v" --arg b "$(s "$store/$v/agenthold-linux-arm64")" --arg m "$(s "$store/$v/runtime-linux-arm64.manifest.json")" \
  --arg s "$(s "$store/$v/SHA256SUMS")" --arg kk "$kkey" --arg ks "$(s "$A/kernel-$kkey.json")" --arg bk "$bkey" --arg bs "$(s "$A/base-$bkey.json")" \
  '{format:3,arch:"arm64",commit:$c,version:$v,binary:{path:($v+"/agenthold-linux-arm64"),sha256:$b},
    runtime_manifest:{path:($v+"/runtime-linux-arm64.manifest.json"),sha256:$m},sums:{path:($v+"/SHA256SUMS"),sha256:$s},
    kernel:{key:$kk,manifest:{path:("assets/arm64/kernel-"+$kk+".json"),sha256:$ks}},
    base:{key:$bk,manifest:{path:("assets/arm64/base-"+$bk+".json"),sha256:$bs}}}' > "$store/channel.json"
export AGENTHOLD_DEPLOY_ROOT="$work/opt" AGENTHOLD_DEPLOY_DATA_DIR="$work/data" AGENTHOLD_DEPLOY_LIB_DIR="$work/lib" AGENTHOLD_DEPLOY_UNIT_DIR="$work/units"
export AGENTHOLD_DEPLOY_CONFIG="$work/none.conf" TMPDIR="$work"

fail() { echo "FAIL: $*" >&2; exit 1; }
bash "$script" "$store" >"$work/log" 2>&1 || { cat "$work/log"; fail 'bootstrap'; }
[[ "$(readlink "$work/opt/current")" == "$work/opt/releases/$commit" ]] || fail 'current link'
[[ -x "$work/opt/releases/$commit/agenthold" && -f "$work/opt/releases/$commit/runtime.manifest.json" && -f "$work/opt/releases/$commit/base.json" ]] || fail 'release files'
base_sha=$(s "$work/base"); kernel_sha=$(s "$work/kernel")
cmp "$work/base" "$work/data/base/$base_sha.ext4" || fail 'base cache'
cmp "$work/kernel" "$work/data/kernel/$kernel_sha" || fail 'kernel cache'
cmp "$A/base-$bkey.zst" "$work/data/base/$base_sha.ext4.zst" || fail 'compressed base kept for /dl/'
cmp "$A/kernel-$kkey.json" "$work/data/assets/arm64/kernel-$kkey.json" || fail 'kernel asset manifest'
[[ "$(stat -c %a "$work/data/base/$base_sha.ext4.zst")" == 644 && "$(stat -c %a "$work/data/base/$base_sha.ext4")" == 644 ]] || fail 'permissions'
[[ -x "$work/lib/pull.sh" && -x "$work/lib/build-local.sh" ]] || fail 'scripts'
for unit in agenthold.service agenthold-node.service agenthold-pull.service agenthold-pull.timer agenthold-cloudflared.service agenthold-build.service agenthold-build.timer agenthold-build.slice; do [[ -f "$work/units/$unit" ]] || fail "$unit"; done
cmp -s "$store/channel.json" "$work/data/dl/channel.json" || fail 'channel not published'
if bash "$script" "$store" >/dev/null 2>&1; then fail 'second bootstrap must refuse'; fi

rm -rf "$work/opt" "$work/data"
echo corrupt >> "$A/base-$bkey.zst"
if bash "$script" "$store" >/dev/null 2>&1; then fail 'corrupt release must be refused'; fi
[[ ! -e "$work/opt/current" ]] || fail 'corrupt release left a deployment'
[[ -z "$(ls -A "$work/data/base" 2>/dev/null)" ]] || fail 'corrupt release left base files'

# Semver release bootstrap (v0.1.0): installs under /opt/agenthold/releases/v0.1.0.
sv="v0.1.0"
sstore="$work/store-semver"; sA="$sstore/assets/arm64"
mkdir -p "$sstore/$sv" "$sA"
printf '#!/usr/bin/env bash\necho "agenthold %s (333333333333)"\n' "$sv" > "$sstore/$sv/agenthold-linux-arm64"
chmod +x "$sstore/$sv/agenthold-linux-arm64"
jq -n --arg v "$sv" --arg k "$kkey" --arg b "$bkey" '{format:2,arch:"arm64",version:$v,files:{},kernel:{key:$k},base:{key:$b}}' > "$sstore/$sv/runtime-linux-arm64.manifest.json"
for k in base kernel; do
  key=$kkey; [[ $k == base ]] && key=$bkey
  zstd -q -f --long=27 "$work/$k" -o "$sA/$k-$key.zst"
  jq -n --arg kind "$k" --arg key "$key" --arg sha "$(s "$work/$k")" --argjson size "$(stat -c %s "$work/$k")" \
    --arg csha "$(s "$sA/$k-$key.zst")" --argjson csize "$(stat -c %s "$sA/$k-$key.zst")" \
    '{format:1,kind:$kind,arch:"arm64",key:$key,sha256:$sha,size:$size,compressed:{sha256:$csha,size:$csize}}' > "$sA/$k-$key.json"
done
(cd "$sstore/$sv" && sha256sum agenthold-linux-arm64 runtime-linux-arm64.manifest.json > SHA256SUMS)
jq -n --arg c "$commit" --arg v "$sv" --arg b "$(s "$sstore/$sv/agenthold-linux-arm64")" --arg m "$(s "$sstore/$sv/runtime-linux-arm64.manifest.json")" \
  --arg sum "$(s "$sstore/$sv/SHA256SUMS")" --arg kk "$kkey" --arg ks "$(s "$sA/kernel-$kkey.json")" --arg bk "$bkey" --arg bs "$(s "$sA/base-$bkey.json")" \
  '{format:3,arch:"arm64",commit:$c,version:$v,binary:{path:($v+"/agenthold-linux-arm64"),sha256:$b},
    runtime_manifest:{path:($v+"/runtime-linux-arm64.manifest.json"),sha256:$m},sums:{path:($v+"/SHA256SUMS"),sha256:$sum},
    kernel:{key:$kk,manifest:{path:("assets/arm64/kernel-"+$kk+".json"),sha256:$ks}},
    base:{key:$bk,manifest:{path:("assets/arm64/base-"+$bk+".json"),sha256:$bs}}}' > "$sstore/channel.json"
rm -rf "$work/opt" "$work/data"
bash "$script" "$sstore" >"$work/log" 2>&1 || { cat "$work/log"; fail 'semver bootstrap'; }
[[ "$(readlink "$work/opt/current")" == "$work/opt/releases/$sv" ]] || fail 'semver current link'
[[ -x "$work/opt/releases/$sv/agenthold" ]] || fail 'semver binary'
cmp -s "$sstore/channel.json" "$work/data/dl/channel.json" || fail 'semver channel not published'
cmp -s "$sstore/$sv/SHA256SUMS" "$work/data/dl/$sv/SHA256SUMS" || fail 'semver SHA256SUMS not published'
echo 'bootstrap tests passed'
