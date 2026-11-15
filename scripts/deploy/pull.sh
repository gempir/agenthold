#!/usr/bin/env bash
# Pull an immutable, tested main build and switch the already-installed demo service.
# The timer is deliberately installed disabled until the first bundled build is validated.
set -euo pipefail
umask 077

ROOT="${AGENTHOLD_DEPLOY_ROOT:-/opt/agenthold}"
CHANNEL="${AGENTHOLD_DEPLOY_CHANNEL:-https://github.com/gempir/agenthold/releases/download/demo-main/channel.json}"
HEALTH="${AGENTHOLD_DEPLOY_HEALTH:-http://127.0.0.1:8080}"
DATA_DIR="${AGENTHOLD_DEPLOY_DATA_DIR:-/var/lib/agenthold}"
HOST_MOUNT="${AGENTHOLD_DEPLOY_HOST_MOUNT:-/Users/gempir/dev/agenthold}"
TIMEOUT="${AGENTHOLD_DEPLOY_TIMEOUT:-1800}"
SERVICE="${AGENTHOLD_DEPLOY_SERVICE:-agenthold.service}"
REPOSITORY="https://github.com/gempir/agenthold/releases/download"
[[ "$ROOT" == /* && "$DATA_DIR" == /* && "$HOST_MOUNT" == /* && "$CHANNEL" == "$REPOSITORY/demo-main/channel.json" ]] || { echo 'invalid deployment root or channel URL' >&2; exit 1; }
[[ "$HEALTH" == 'http://127.0.0.1:'* && "$TIMEOUT" =~ ^[0-9]+$ && "$TIMEOUT" -ge 1 && "$TIMEOUT" -le 3600 ]] || { echo 'invalid health settings' >&2; exit 1; }
[[ "$(uname -m)" == aarch64 ]] || { echo 'the demo channel requires arm64 Linux' >&2; exit 1; }
[[ -d "$ROOT/releases" && -L "$ROOT/current" ]] || { echo 'deployment has not been bootstrapped; refusing to start a service' >&2; exit 1; }
exec 9>"$ROOT/.pull.lock"
flock -n 9 || { echo 'another pull is running'; exit 0; }

current="$(readlink -f "$ROOT/current")"
[[ "$current" == "$ROOT/releases/"* && -x "$current/agenthold" ]] || { echo 'invalid current deployment' >&2; exit 1; }
systemctl is-active --quiet "$SERVICE" && curl -fsS --max-time 3 "$HEALTH/healthz" >/dev/null || { echo 'baseline service is not healthy; refusing deployment' >&2; exit 1; }
old_version="$(curl -fsS --max-time 3 "$HEALTH/api/v1/version" | jq -er '.version | select(type == "string" and length > 0)')" || { echo 'baseline version is unavailable' >&2; exit 1; }
work="$(mktemp -d "$ROOT/.pull.XXXXXXXX")"
compressed_tmp=""
base_tmp=""
switched=0
cleanup() {
 local status=$?
 trap - EXIT INT TERM
 if ((switched)); then
  echo 'deployment interrupted or failed; rolling back' >&2
  if ln -s "$current" "$work/rollback" && mv -Tf "$work/rollback" "$ROOT/current"; then
   if systemctl restart "$SERVICE"; then
    if healthy "$old_version" 180; then echo 'rollback restored service' >&2
    else echo 'rollback did not become healthy' >&2; fi
   else echo 'rollback restart failed' >&2; fi
  else echo 'rollback could not restore the previous release link' >&2; fi
 fi
 [[ -z "$compressed_tmp" ]] || rm -f -- "$compressed_tmp"
 [[ -z "$base_tmp" ]] || rm -f -- "$base_tmp"
 rm -rf -- "$work"
 exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fetch() { curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time "${4:-300}" --max-filesize "$2" "$1" -o "$3"; }
verify() { printf '%s  %s\n' "$1" "$2" | sha256sum --check --status; }
fetch "$CHANNEL" 65536 "$work/channel.json"
[[ "$(wc -c < "$work/channel.json")" -le 65536 ]] || { echo 'channel is too large' >&2; exit 1; }
commit="$(jq -er 'select(.format == 1 and .arch == "arm64") | .commit | select(type == "string" and test("^[0-9a-f]{40}$"))' "$work/channel.json")"
head_is_current() {
 local head
 head="$(curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 15 --max-time 30 --max-filesize 65536 'https://api.github.com/repos/gempir/agenthold/git/ref/heads/main' | jq -er 'select(.object.type == "commit") | .object.sha | select(type == "string" and test("^[0-9a-f]{40}$"))')" || return 1
 [[ "$head" == "$commit" ]]
}
head_is_current || { echo 'channel does not match the current main head' >&2; exit 1; }
release="$REPOSITORY/sha-$commit"
binary_url="$(jq -er '.binary.url | select(type == "string")' "$work/channel.json")"
binary_sha="$(jq -er '.binary.sha256 | select(type == "string" and test("^[0-9a-f]{64}$"))' "$work/channel.json")"
manifest_url="$(jq -er '.runtime_manifest.url | select(type == "string")' "$work/channel.json")"
manifest_sha="$(jq -er '.runtime_manifest.sha256 | select(type == "string" and test("^[0-9a-f]{64}$"))' "$work/channel.json")"
[[ "$binary_url" == "$release/agenthold-linux-arm64" && "$manifest_url" == "$release/runtime-linux-arm64.manifest.json" ]] || { echo 'channel does not point to matching immutable artifacts' >&2; exit 1; }
[[ "$current" != "$ROOT/releases/$commit" ]] || { echo "already running $commit"; exit 0; }

fetch "$manifest_url" 65536 "$work/runtime.json"
verify "$manifest_sha" "$work/runtime.json" || { echo 'runtime manifest checksum mismatch' >&2; exit 1; }
jq -e --arg version "sha-$commit" --arg base "$release/base-arm64.ext4.zst" '
 .format == 1 and .arch == "arm64" and .version == $version and .base.url == $base and
 (.base.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
 (.base.compressed.sha256 | type == "string" and test("^[0-9a-f]{64}$")) and
 (.base.size | type == "number" and . == floor and . > 0 and . <= 34359738368) and
 (.base.compressed.size | type == "number" and . == floor and . > 0 and . <= 2000000000)
' "$work/runtime.json" >/dev/null || { echo 'runtime manifest does not match the release' >&2; exit 1; }
fetch "$binary_url" 500000000 "$work/agenthold"
verify "$binary_sha" "$work/agenthold" || { echo 'binary checksum mismatch' >&2; exit 1; }
chmod 0755 "$work/agenthold"
[[ "$("$work/agenthold" --version)" == "agenthold sha-$commit ("* ]] || { echo 'binary version does not match the release' >&2; exit 1; }
# Fetch the compressed base while the old service is healthy. Decode it once
# into the same content-addressed location the bundled binary checks at boot.
base_sha="$(jq -er '.base.sha256' "$work/runtime.json")"
base_size="$(jq -er '.base.size' "$work/runtime.json")"
compressed_sha="$(jq -er '.base.compressed.sha256' "$work/runtime.json")"
compressed_size="$(jq -er '.base.compressed.size' "$work/runtime.json")"
install -d -m 0700 "$DATA_DIR/base"
base="$DATA_DIR/base/$base_sha.ext4"
if [[ -f "$base" && ! -L "$base" && "$(stat -c %s "$base")" == "$base_size" ]] && verify "$base_sha" "$base"; then
 echo 'verified base is already cached'
else
 [[ -d "$HOST_MOUNT" ]] || { echo 'host storage mount is unavailable' >&2; exit 1; }
 available="$(df -PB1 "$DATA_DIR/base" | awk 'END {print $4}')"
 host_available="$(df -PB1 "$HOST_MOUNT" | awk 'END {print $4}')"
 required=$((base_size + compressed_size + 536870912))
 [[ "$available" =~ ^[0-9]+$ && "$host_available" =~ ^[0-9]+$ ]] && (( available >= required && host_available >= required )) || { echo 'insufficient guest or host disk space for base image and rollback' >&2; exit 1; }
 compressed_tmp="$(mktemp "$DATA_DIR/base/.download.XXXXXXXX")"
 base_tmp="$(mktemp "$DATA_DIR/base/.unpack.XXXXXXXX")"
 fetch "$release/base-arm64.ext4.zst" "$compressed_size" "$compressed_tmp" 1800
 [[ "$(stat -c %s "$compressed_tmp")" == "$compressed_size" ]] && verify "$compressed_sha" "$compressed_tmp" || { echo 'compressed base verification failed' >&2; exit 1; }
 (ulimit -f $(((base_size + 1023) / 1024)); zstd -d -q -f --long=27 --sparse "$compressed_tmp" -o "$base_tmp")
 [[ "$(stat -c %s "$base_tmp")" == "$base_size" ]] && verify "$base_sha" "$base_tmp" || { echo 'base image verification failed' >&2; exit 1; }
 chmod 0644 "$base_tmp"
 mv -f "$base_tmp" "$base"
 base_tmp=""
 rm -f "$compressed_tmp"
 compressed_tmp=""
fi

release_dir="$ROOT/releases/$commit"
if [[ -e "$release_dir" ]]; then
 [[ -d "$release_dir" && ! -L "$release_dir" && -f "$release_dir/agenthold" ]] && verify "$binary_sha" "$release_dir/agenthold" || { echo 'existing release differs from channel' >&2; exit 1; }
else
 mkdir "$work/release"
 mv "$work/agenthold" "$work/release/agenthold"
 mv "$work/runtime.json" "$work/release/runtime.manifest.json"
 mv "$work/release" "$release_dir"
fi

healthy() {
 local version="$1" deadline=$((SECONDS + $2)) reply
 while ((SECONDS < deadline)); do
  if reply="$(curl -fsS --max-time 3 "$HEALTH/api/v1/version" 2>/dev/null)" &&
     jq -e --arg expected "$version" '.version == $expected' >/dev/null 2>&1 <<<"$reply" &&
     curl -fsS --max-time 3 "$HEALTH/healthz" >/dev/null 2>&1; then return 0; fi
  sleep 5
 done
 return 1
}

head_is_current || { echo 'main changed during preparation; leaving the current release running' >&2; exit 1; }
ln -s "$release_dir" "$work/next"
switched=1
mv -Tf "$work/next" "$ROOT/current"
if systemctl restart "$SERVICE" && healthy "sha-$commit" "$TIMEOUT"; then
 switched=0
 echo "deployed $commit"
 exit 0
fi

echo 'new release failed health checks' >&2
exit 1
