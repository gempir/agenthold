#!/usr/bin/env bash
# Install the first bundled release on a demo host so that pull.sh can take over later.
#
#   sudo scripts/deploy/bootstrap.sh RELEASE_DIR
#
# RELEASE_DIR holds the files of one sha-<commit> build: scripts/release/build.sh's output
# (build/release/arm64), a version directory of a release store (/srv/agenthold/releases/sha-<commit>)
# or the files downloaded from a /dl/ root: agenthold-linux-arm64, runtime-linux-arm64.manifest.json,
# base-arm64.ext4.zst and SHA256SUMS (or SHA256SUMS-arm64).
# It creates the layout pull.sh expects, keeps the verified compressed base and unpacks it into the
# content-addressed cache (<data_dir>/base/<sha256>.ext4 and .ext4.zst, which /dl/ serves), and
# installs pull.sh, build-local.sh and the systemd units. It does not write /etc/agenthold, start or
# enable anything.
set -euo pipefail
umask 022

ROOT="${AGENTHOLD_DEPLOY_ROOT:-/opt/agenthold}"
DATA_DIR="${AGENTHOLD_DEPLOY_DATA_DIR:-/var/lib/agenthold}"
LIB_DIR="${AGENTHOLD_DEPLOY_LIB_DIR:-/usr/local/lib/agenthold}"
UNIT_DIR="${AGENTHOLD_DEPLOY_UNIT_DIR:-/etc/systemd/system}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UNITS="agenthold.service agenthold-pull.service agenthold-pull.timer agenthold-cloudflared.service agenthold-build.service agenthold-build.timer agenthold-build.slice"
[[ $# -eq 1 && -d "$1" ]] || { echo 'usage: bootstrap.sh RELEASE_DIR' >&2; exit 2; }
SRC="$(cd "$1" && pwd)"
[[ "$ROOT" == /* && "$DATA_DIR" == /* && "$LIB_DIR" == /* && "$UNIT_DIR" == /* ]] || { echo 'paths must be absolute' >&2; exit 2; }
[[ "$(uname -m)" == aarch64 ]] || { echo 'the demo channel requires arm64 Linux' >&2; exit 1; }
for tool in jq zstd sha256sum curl flock; do command -v "$tool" >/dev/null || { echo "$tool is required (pull.sh needs it too)" >&2; exit 1; }; done

sums="$SRC/SHA256SUMS"
[[ -f "$sums" ]] || sums="$SRC/SHA256SUMS-arm64"
[[ -f "$sums" ]] || { echo 'SHA256SUMS is missing' >&2; exit 1; }
(cd "$SRC" && sha256sum --check --quiet "$sums") || { echo 'release checksum verification failed' >&2; exit 1; }
for file in agenthold-linux-arm64 runtime-linux-arm64.manifest.json base-arm64.ext4.zst; do
  grep -q "  $file\$" "$sums" || { echo "$file is not covered by SHA256SUMS" >&2; exit 1; }
done

manifest="$SRC/runtime-linux-arm64.manifest.json"
commit="$(jq -er 'select(.format == 1 and .arch == "arm64") | .version | select(type == "string" and test("^sha-[0-9a-f]{40}$")) | ltrimstr("sha-")' "$manifest")" ||
  { echo 'only sha-<full commit> builds can be installed (VERSION=sha-<commit> scripts/release/build.sh)' >&2; exit 1; }
base_sha="$(jq -er '.base.sha256 | select(type == "string" and test("^[0-9a-f]{64}$"))' "$manifest")"
compressed_size="$(jq -er '.base.compressed.size | select(type == "number" and . > 0 and . <= 2000000000)' "$manifest")"
base_size="$(jq -er '.base.size | select(type == "number" and . > 0 and . <= 34359738368)' "$manifest")"
compressed_sha="$(jq -er '.base.compressed.sha256 | select(type == "string" and test("^[0-9a-f]{64}$"))' "$manifest")"
[[ "$(wc -c < "$SRC/base-arm64.ext4.zst")" -gt 0 ]] && echo "$compressed_sha  $SRC/base-arm64.ext4.zst" | sha256sum --check --status ||
  { echo 'compressed base does not match the runtime manifest' >&2; exit 1; }
[[ "$("$SRC/agenthold-linux-arm64" --version)" == "agenthold sha-$commit ("* ]] || { echo 'binary version does not match the manifest' >&2; exit 1; }

release_dir="$ROOT/releases/$commit"
[[ ! -e "$release_dir" ]] || { echo "$release_dir already exists; refusing to overwrite a release" >&2; exit 1; }
if [[ -e "$ROOT/current" || -L "$ROOT/current" ]]; then echo "$ROOT/current already exists; use pull.sh to update" >&2; exit 1; fi

install -d -m 0755 "$ROOT" "$ROOT/releases"
install -d -m 0700 "$DATA_DIR" "$DATA_DIR/base"
base="$DATA_DIR/base/$base_sha.ext4"
available="$(df -PB1 "$DATA_DIR/base" | awk 'END {print $4}')"
(( available >= base_size + compressed_size + 536870912 )) || { echo 'not enough free space for the base image' >&2; exit 1; }
zst="$DATA_DIR/base/$base_sha.ext4.zst"
if [[ ! -f "$zst" ]]; then
  tmpz="$(mktemp "$DATA_DIR/base/.download.XXXXXXXX")"
  trap 'rm -f -- "$tmpz"' EXIT
  cp -- "$SRC/base-arm64.ext4.zst" "$tmpz"
  echo "$compressed_sha  $tmpz" | sha256sum --check --status || { echo 'compressed base copy verification failed' >&2; exit 1; }
  if [[ $EUID -eq 0 ]]; then chown root:root "$tmpz"; fi
  chmod 0644 "$tmpz"
  mv -f "$tmpz" "$zst"
  trap - EXIT
fi
if [[ ! -f "$base" ]]; then
  tmp="$(mktemp "$DATA_DIR/base/.unpack.XXXXXXXX")"
  trap 'rm -f -- "$tmp"' EXIT
  (ulimit -f $(((base_size + 1023) / 1024)); zstd -d -q -f --long=27 --sparse "$SRC/base-arm64.ext4.zst" -o "$tmp")
  [[ "$(stat -c %s "$tmp")" == "$base_size" ]] && echo "$base_sha  $tmp" | sha256sum --check --status || { echo 'base image verification failed' >&2; exit 1; }
  # zstd copies the owner of its input file; the cache belongs to the service user.
  if [[ $EUID -eq 0 ]]; then chown root:root "$tmp"; fi
  chmod 0644 "$tmp"
  mv -f "$tmp" "$base"
  trap - EXIT
fi

staging="$(mktemp -d "$ROOT/.bootstrap.XXXXXXXX")"
trap 'rm -rf -- "$staging"' EXIT
install -m 0755 "$SRC/agenthold-linux-arm64" "$staging/agenthold"
install -m 0644 "$manifest" "$staging/runtime.manifest.json"
chmod 0755 "$staging"
mv "$staging" "$release_dir"
trap - EXIT
ln -s "$release_dir" "$ROOT/current.new"
mv -Tf "$ROOT/current.new" "$ROOT/current"

install -d -m 0755 "$LIB_DIR" "$UNIT_DIR"
install -m 0755 "$HERE/pull.sh" "$LIB_DIR/pull.sh"
install -m 0755 "$HERE/build-local.sh" "$LIB_DIR/build-local.sh"
for unit in $UNITS; do
  install -m 0644 "$HERE/$unit" "$UNIT_DIR/$unit"
done
echo "installed sha-$commit under $ROOT (base $base_sha cached in $DATA_DIR/base)"
echo 'Next: write /etc/agenthold/agenthold.yaml, then: systemctl daemon-reload && systemctl enable --now agenthold'
echo 'Releases: build-local.sh publishes into /srv/agenthold/releases, pull.sh deploys from the source in /etc/agenthold/pull.conf'
