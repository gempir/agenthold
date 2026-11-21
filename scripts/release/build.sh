#!/usr/bin/env bash
# Native Linux release build: VERSION=v0.1.0 scripts/release/build.sh [--out DIR]
# Builds a bundled binary and the matching compressed base, without leaving generated source assets.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${VERSION:?set VERSION to a stable release tag (for example v0.1.0)}"
[[ "$VERSION" =~ ^[A-Za-z0-9._-]+$ && "$VERSION" != . && "$VERSION" != .. && ${#VERSION} -le 128 ]] || {
  echo 'VERSION must be a safe release tag, at most 128 letters, digits, dots, dashes or underscores' >&2; exit 2;
}
if [[ "$(uname -s)" == Darwin ]]; then
  make -C "$REPO_ROOT" dev-vm >/dev/null
  exec limactl shell --workdir "$REPO_ROOT" agenthold -- env "VERSION=$VERSION" ${BASE_URL+"BASE_URL=$BASE_URL"} bash "$REPO_ROOT/scripts/release/build.sh" "$@"
fi
[[ "$(uname -s)" == Linux ]] || { echo 'release builds require Linux' >&2; exit 1; }
case "$(uname -m)" in
  aarch64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
  *) echo 'unsupported architecture' >&2; exit 1 ;;
esac
OUT="$REPO_ROOT/build/release/$ARCH"
while (($#)); do
  case "$1" in
    --out) OUT="$2"; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
[[ -f "$REPO_ROOT/web/dist/index.html" ]] || { echo 'Build the frontend first: make web' >&2; exit 1; }
ART="$REPO_ROOT/build/guest/$ARCH"
RUNTIME="$REPO_ROOT/build/runtime"
STAGE="$REPO_ROOT/internal/runtimeassets/staged"
WORK="$(mktemp -d "$OUT/.release.XXXXXX")"
HAD_STAGE=0
NEW_STAGE=0
cleanup() {
  local status=$?
  set +e
  if ((NEW_STAGE)); then
  python3 - "$STAGE" <<'PY'
import os, shutil, sys
p = sys.argv[1]
if os.path.islink(p):
    os.unlink(p)
elif os.path.exists(p):
    shutil.rmtree(p)
PY
  fi
  if ((HAD_STAGE)); then mv "$WORK/original-stage" "$STAGE"; fi
  python3 - "$WORK" <<'PY'
import shutil, sys
shutil.rmtree(sys.argv[1])
PY
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ -e "$STAGE" || -L "$STAGE" ]]; then
  mv "$STAGE" "$WORK/original-stage"
  HAD_STAGE=1
fi
mkdir -p "$STAGE" "$ART"
NEW_STAGE=1

bash "$REPO_ROOT/scripts/guest/fetch-runtime.sh" --out "$RUNTIME"
if [[ ! -f "$ART/kernel.manifest.json" ]]; then
  bash "$REPO_ROOT/scripts/guest/build-kernel.sh"
fi
KERNEL_FILE="$(jq -er --arg arch "$ARCH" 'select(.arch == $arch and .release == "6.1.188-agenthold") | .file' "$ART/kernel.manifest.json")"
[[ "$KERNEL_FILE" == vmlinux-6.1.188-agenthold ]] || { echo 'unexpected kernel manifest filename' >&2; exit 1; }
KERNEL="$ART/$KERNEL_FILE"
echo "$(jq -er .sha256 "$ART/kernel.manifest.json")  $KERNEL" | sha256sum --check --status
COMMIT="$(git -C "$REPO_ROOT" rev-parse --short=12 HEAD)"
LDFLAGS="-s -w -X github.com/gempir/agenthold/internal/version.Version=$VERSION -X github.com/gempir/agenthold/internal/version.Commit=$COMMIT"
BASE_MANIFEST="$ART/base-$ARCH.manifest.json"
BASE="$ART/base-$ARCH.ext4"
# The base holds no sandboxd (the binary delivers itself to every guest at boot): it is rebuilt only
# when its own inputs change, so a Go-only release keeps the previous base byte for byte.
BASE_KEY="$(bash "$REPO_ROOT/scripts/guest/base-key.sh")"
HAVE="$(jq -r '.inputs.baseKey // empty' "$BASE_MANIFEST" 2>/dev/null || true)"
if [[ ! -f "$BASE" || "$HAVE" != "$BASE_KEY" ]]; then
  bash "$REPO_ROOT/scripts/guest/build-rootfs.sh"
fi
[[ "$(jq -er '.inputs.baseKey' "$BASE_MANIFEST")" == "$BASE_KEY" ]] || { echo 'base image was not built from these inputs' >&2; exit 1; }
echo "$(jq -er .sha256 "$BASE_MANIFEST")  $BASE" | sha256sum --check --status
if [[ ! -f "$BASE.zst" || "$(jq -r '.zstd.sha256 // empty' "$BASE_MANIFEST")" == '' ]]; then
  zstd -q -f -T0 -6 --long=27 -o "$BASE.zst" "$BASE"
  jq --arg sha "$(sha256sum "$BASE.zst" | cut -d' ' -f1)" --argjson size "$(stat -c %s "$BASE.zst")" \
    '.zstd = {file: (.file + ".zst"), size: $size, sha256: $sha}' "$BASE_MANIFEST" > "$WORK/base.manifest.json"
  mv "$WORK/base.manifest.json" "$BASE_MANIFEST"
fi
echo "$(jq -er .zstd.sha256 "$BASE_MANIFEST")  $BASE.zst" | sha256sum --check --status
[[ "$(stat -c %s "$BASE.zst")" -le 2000000000 ]] || { echo 'compressed base exceeds the 2 GB download budget' >&2; exit 1; }

cp "$RUNTIME/firecracker" "$RUNTIME/jailer" "$STAGE/"
cp "$KERNEL" "$STAGE/kernel"
# Default empty: the base comes from a controller's /dl/ (or sandbox.firecracker.base_url); set BASE_URL to pin a mirror.
BASE_URL="${BASE_URL-}"
jq -n --arg version "$VERSION" --arg arch "$ARCH" --arg url "$BASE_URL" --slurpfile base "$BASE_MANIFEST" \
  --arg fc "$(sha256sum "$STAGE/firecracker" | cut -d' ' -f1)" --argjson fcsize "$(stat -c %s "$STAGE/firecracker")" \
  --arg jailer "$(sha256sum "$STAGE/jailer" | cut -d' ' -f1)" --argjson jailersize "$(stat -c %s "$STAGE/jailer")" \
  --arg kernel "$(sha256sum "$STAGE/kernel" | cut -d' ' -f1)" --argjson kernelsize "$(stat -c %s "$STAGE/kernel")" \
  '{format: 1, version: $version, arch: $arch,
    files: {firecracker: {sha256: $fc, size: $fcsize}, jailer: {sha256: $jailer, size: $jailersize},
      kernel: {sha256: $kernel, size: $kernelsize}},
    base: {url: $url, sha256: $base[0].sha256, size: $base[0].size,
      compressed: {sha256: $base[0].zstd.sha256, size: $base[0].zstd.size}}}' > "$STAGE/manifest.json"
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -trimpath -tags bundled -ldflags "$LDFLAGS" -o "$OUT/agenthold-linux-$ARCH" ./cmd/agenthold)
cp "$STAGE/manifest.json" "$OUT/runtime-linux-$ARCH.manifest.json"
cp "$BASE_MANIFEST" "$OUT/base-$ARCH.manifest.json"
# A hard link where possible: the compressed base is large and never modified in place.
ln -f "$BASE.zst" "$OUT/base-$ARCH.ext4.zst" 2>/dev/null || cp "$BASE.zst" "$OUT/base-$ARCH.ext4.zst"
(cd "$OUT" && sha256sum "agenthold-linux-$ARCH" "base-$ARCH.ext4.zst" "base-$ARCH.manifest.json" "runtime-linux-$ARCH.manifest.json" > SHA256SUMS)
printf 'Release artifacts: %s\n' "$OUT"
ls -lh "$OUT/agenthold-linux-$ARCH" "$OUT/base-$ARCH.ext4.zst"
