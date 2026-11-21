#!/usr/bin/env bash
# Release binary: VERSION=sha-<commit> scripts/release/build.sh [--out DIR]
#
# Builds the static agenthold binary for this host's architecture (it is also the guests'
# sandboxd). It embeds Firecracker, the jailer and a runtime manifest that names the guest kernel
# and base image by key (scripts/guest/kernel-key.sh, base-key.sh). It does not build or embed the
# kernel or the base: those are built natively per architecture (scripts/release/build-assets.sh)
# and served by an asset source. Output in build/release/<arch>/:
#   agenthold-linux-<arch>  runtime-linux-<arch>.manifest.json  SHA256SUMS
# GitHub Actions runs this on an amd64 and an arm64 runner for every main commit (release.yml).
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
VERSION="${VERSION:?set VERSION to a release tag (for example sha-<commit> or v0.1.0)}"
[[ "$VERSION" =~ ^[A-Za-z0-9._-]+$ && "$VERSION" != . && "$VERSION" != .. && ${#VERSION} -le 128 ]] || {
  echo 'VERSION must be a safe release tag, at most 128 letters, digits, dots, dashes or underscores' >&2; exit 2;
}
if [[ "$(uname -s)" == Darwin ]]; then
  make -C "$REPO_ROOT" dev-vm >/dev/null
  exec limactl shell --workdir "$REPO_ROOT" agenthold -- env "VERSION=$VERSION" bash "$REPO_ROOT/scripts/release/build.sh" "$@"
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
RUNTIME="$REPO_ROOT/build/runtime"
STAGE="$REPO_ROOT/internal/runtimeassets/staged"
WORK="$(mktemp -d "$OUT/.release.XXXXXX")"
HAD_STAGE=0
NEW_STAGE=0
cleanup() {
  local status=$?
  set +e
  if ((NEW_STAGE)); then rm -rf -- "$STAGE"; fi
  if ((HAD_STAGE)); then mv "$WORK/original-stage" "$STAGE"; fi
  rm -rf -- "$WORK"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ -e "$STAGE" || -L "$STAGE" ]]; then
  mv "$STAGE" "$WORK/original-stage"
  HAD_STAGE=1
fi
mkdir -p "$STAGE"
NEW_STAGE=1

bash "$REPO_ROOT/scripts/guest/fetch-runtime.sh" --out "$RUNTIME"
cp "$RUNTIME/firecracker" "$RUNTIME/jailer" "$STAGE/"
KERNEL_KEY="$(bash "$REPO_ROOT/scripts/guest/kernel-key.sh")"
BASE_KEY="$(bash "$REPO_ROOT/scripts/guest/base-key.sh")"
jq -n --arg version "$VERSION" --arg arch "$ARCH" --arg kernel "$KERNEL_KEY" --arg base "$BASE_KEY" \
  --arg fc "$(sha256sum "$STAGE/firecracker" | cut -d' ' -f1)" --argjson fcsize "$(stat -c %s "$STAGE/firecracker")" \
  --arg jailer "$(sha256sum "$STAGE/jailer" | cut -d' ' -f1)" --argjson jailersize "$(stat -c %s "$STAGE/jailer")" \
  '{format: 2, version: $version, arch: $arch,
    files: {firecracker: {sha256: $fc, size: $fcsize}, jailer: {sha256: $jailer, size: $jailersize}},
    kernel: {key: $kernel}, base: {key: $base}}' > "$STAGE/manifest.json"
COMMIT="$(git -C "$REPO_ROOT" rev-parse --short=12 HEAD)"
LDFLAGS="-s -w -X github.com/gempir/agenthold/internal/version.Version=$VERSION -X github.com/gempir/agenthold/internal/version.Commit=$COMMIT"
(cd "$REPO_ROOT" && CGO_ENABLED=0 go build -trimpath -tags bundled -ldflags "$LDFLAGS" -o "$OUT/agenthold-linux-$ARCH" ./cmd/agenthold)
cp "$STAGE/manifest.json" "$OUT/runtime-linux-$ARCH.manifest.json"
(cd "$OUT" && sha256sum "agenthold-linux-$ARCH" "runtime-linux-$ARCH.manifest.json" > SHA256SUMS)
printf 'Release binary: %s (kernel %s, base %s)\n' "$OUT/agenthold-linux-$ARCH" "$KERNEL_KEY" "$BASE_KEY"
ls -lh "$OUT/agenthold-linux-$ARCH"
