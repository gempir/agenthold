#!/usr/bin/env bash
# Bootstrap tests use local fixture files and temporary directories, never a real service.
set -euo pipefail
[[ "$(uname -s)" == Linux ]] || { echo 'run on Linux'; exit 1; }
for tool in jq zstd sha256sum curl flock; do command -v "$tool" >/dev/null || { echo "$tool required" >&2; exit 1; }; done
script="$(cd "$(dirname "$0")" && pwd)/bootstrap.sh"
work="$(mktemp -d)"
trap 'rm -rf -- "$work"' EXIT
mkdir -p "$work/bin" "$work/release"
export PATH="$work/bin:$PATH"
cat > "$work/bin/uname" <<'MOCK'
#!/usr/bin/env bash
if [[ "$1" == -m ]]; then echo aarch64; else exec /usr/bin/uname "$@"; fi
MOCK
chmod +x "$work/bin/uname"
commit=3333333333333333333333333333333333333333
printf '#!/usr/bin/env bash\necho "agenthold sha-%s (333333333333)"\n' "$commit" > "$work/release/agenthold-linux-arm64"
chmod +x "$work/release/agenthold-linux-arm64"
head -c 3000000 /dev/zero | tr '\0' 'x' > "$work/base"
zstd -q -f --long=27 "$work/base" -o "$work/release/base-arm64.ext4.zst"
base_sha=$(sha256sum "$work/base" | cut -d' ' -f1)
jq -n --arg version "sha-$commit" --arg raw "$base_sha" --arg compressed "$(sha256sum "$work/release/base-arm64.ext4.zst" | cut -d' ' -f1)" \
  --argjson rawsize "$(stat -c %s "$work/base")" --argjson compressedsize "$(stat -c %s "$work/release/base-arm64.ext4.zst")" \
  '{format:1,arch:"arm64",version:$version,base:{url:"https://example.invalid/base",sha256:$raw,size:$rawsize,compressed:{sha256:$compressed,size:$compressedsize}}}' > "$work/release/runtime-linux-arm64.manifest.json"
(cd "$work/release" && sha256sum agenthold-linux-arm64 base-arm64.ext4.zst runtime-linux-arm64.manifest.json > SHA256SUMS-arm64)
export AGENTHOLD_DEPLOY_ROOT="$work/opt" AGENTHOLD_DEPLOY_DATA_DIR="$work/data" AGENTHOLD_DEPLOY_LIB_DIR="$work/lib" AGENTHOLD_DEPLOY_UNIT_DIR="$work/units"

fail() { echo "FAIL: $*" >&2; exit 1; }
bash "$script" "$work/release" >/dev/null || fail 'bootstrap'
[[ "$(readlink "$work/opt/current")" == "$work/opt/releases/$commit" ]] || fail 'current link'
[[ -x "$work/opt/releases/$commit/agenthold" && -f "$work/opt/releases/$commit/runtime.manifest.json" ]] || fail 'release files'
cmp "$work/base" "$work/data/base/$base_sha.ext4" || fail 'base cache'
[[ "$(stat -c %a "$work/data/base")" == 700 && "$(stat -c %a "$work/data/base/$base_sha.ext4")" == 644 ]] || fail 'permissions'
[[ -x "$work/lib/pull.sh" ]] || fail 'pull.sh'
for unit in agenthold.service agenthold-pull.service agenthold-pull.timer agenthold-cloudflared.service; do [[ -f "$work/units/$unit" ]] || fail "$unit"; done
if bash "$script" "$work/release" >/dev/null 2>&1; then fail 'second bootstrap must refuse'; fi

rm -rf "$work/opt" "$work/data"
echo corrupt >> "$work/release/base-arm64.ext4.zst"
if bash "$script" "$work/release" >/dev/null 2>&1; then fail 'corrupt release must be refused'; fi
[[ ! -e "$work/opt/current" ]] || fail 'corrupt release left a deployment'
echo 'bootstrap tests passed'
