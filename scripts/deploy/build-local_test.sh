#!/usr/bin/env bash
# End-to-end tests for build-local.sh using a local fixture git repo and fake systemd-run/curl.
# Exercises default main local builds, docs-only commit skipping, --tag semver releases (both
# downloaded and local), AGENTHOLD_BUILD_FOLLOW for sha-<commit> and v* controllers, and pull.sh
# --check verification of every published store.
set -euo pipefail
[[ "$(uname -s)" == Linux ]] || { echo 'run on Linux' >&2; exit 1; }
[[ $EUID -eq 0 ]] || { echo 'run as root (for example: sudo bash scripts/deploy/build-local_test.sh)' >&2; exit 1; }
for tool in git jq zstd sha256sum flock curl make; do
  command -v "$tool" >/dev/null || { echo "$tool required" >&2; exit 1; }
done

case "$(uname -m)" in
  aarch64) ARCH=arm64 ;;
  x86_64) ARCH=amd64 ;;
  *) echo 'unsupported architecture' >&2; exit 1 ;;
esac

here="$(cd "$(dirname "$0")" && pwd)"
build_script="$here/build-local.sh"
pull_script="$here/pull.sh"
work="$(mktemp -d /tmp/ah-build-test.XXXXXX)"
trap 'rm -rf -- "$work"' EXIT
chmod 755 "$work"

fail() { echo "FAIL: $*" >&2; [[ -f "$work/run.log" ]] && cat "$work/run.log" >&2; exit 1; }

kkey=$(printf '%032d' 0 | tr 0 a)
bkey=$(printf '%032d' 0 | tr 0 b)
hkey=$(printf '%032d' 0 | tr 0 e)

# --- fixture git repo ---------------------------------------------------------------------------
origin="$work/origin.git"
src="$work/src"
git init --bare --quiet --initial-branch=main "$origin"
git clone --quiet "$origin" "$src"
git -C "$src" config user.name test
git -C "$src" config user.email test@example.com

mkdir -p "$src/scripts/deploy" "$src/scripts/guest" "$src/scripts/release" "$src/docs"
cat > "$src/Makefile" <<'EOF'
.PHONY: test-go web test-kvm
test-go:
	@echo "mock test-go ok"
web:
	@mkdir -p web/dist && echo ok > web/dist/index.html
test-kvm:
	@test -x "$$AGENTHOLD_KVM_SANDBOXD" || { echo "missing sandboxd" >&2; exit 1; }
	@echo "mock test-kvm ok ($$AGENTHOLD_KVM_SANDBOXD)"
EOF
printf '#!/usr/bin/env bash\nexit 0\n' > "$src/scripts/deploy/pull_test.sh"
printf '#!/usr/bin/env bash\nexit 0\n' > "$src/scripts/deploy/bootstrap_test.sh"
printf '#!/usr/bin/env bash\necho "%s"\n' "$kkey" > "$src/scripts/guest/kernel-key.sh"
printf '#!/usr/bin/env bash\necho "%s"\n' "$bkey" > "$src/scripts/guest/base-key.sh"
printf '#!/usr/bin/env bash\necho "%s"\n' "$hkey" > "$src/scripts/guest/harnesses-key.sh"
printf '#!/usr/bin/env bash\nmkdir -p build/runtime\nprintf "#!/usr/bin/env bash\\necho fc\\n" > build/runtime/firecracker\nprintf "#!/usr/bin/env bash\\necho j\\n" > build/runtime/jailer\nchmod +x build/runtime/firecracker build/runtime/jailer\n' > "$src/scripts/guest/fetch-runtime.sh"
cat > "$src/scripts/guest/build-kernel.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
mkdir -p build/guest/$ARCH
printf 'fake kernel\n' > build/guest/$ARCH/vmlinux-test
sha=\$(sha256sum build/guest/$ARCH/vmlinux-test | cut -d' ' -f1)
jq -n --arg file vmlinux-test --arg sha "\$sha" '{file:\$file,sha256:\$sha}' > build/guest/$ARCH/kernel.manifest.json
EOF
cat > "$src/scripts/release/build.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
out=""
while ((\$#)); do case "\$1" in --out) out="\$2"; shift 2;; *) shift;; esac; done
mkdir -p "\$out" build/runtime
commit=\$(git rev-parse --short=12 HEAD)
printf '#!/usr/bin/env bash\necho "agenthold %s (%s)"\n' "\$VERSION" "\$commit" > "\$out/agenthold-linux-$ARCH"
chmod +x "\$out/agenthold-linux-$ARCH"
jq -n --arg v "\$VERSION" --arg arch "$ARCH" --arg k "$kkey" --arg b "$bkey" --arg h "$hkey" \
  '{format:2,version:\$v,arch:\$arch,files:{},kernel:{key:\$k},base:{key:\$b},harnesses:{key:\$h}}' > "\$out/runtime-linux-$ARCH.manifest.json"
(cd "\$out" && sha256sum "agenthold-linux-$ARCH" "runtime-linux-$ARCH.manifest.json" > SHA256SUMS)
echo "local-built:\$VERSION" >> "$work/events.log"
EOF
cat > "$src/scripts/release/build-assets.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
g="build/guest/$ARCH"
a="build/assets/$ARCH"
mkdir -p "\$g" "\$a"
printf 'fake base\n' > "\$g/base-$ARCH.ext4"
zstd -q -f --long=27 "\$g/base-$ARCH.ext4" -o "\$g/base-$ARCH.ext4.zst"
jq -n --arg k "$bkey" '{inputs:{baseKey:\$k}}' > "\$g/base-$ARCH.manifest.json"
printf 'fake harnesses\n' > "\$g/harnesses-$ARCH.ext4"
zstd -q -f --long=27 "\$g/harnesses-$ARCH.ext4" -o "\$g/harnesses-$ARCH.ext4.zst"
jq -n --arg k "$hkey" '{inputs:{harnessesKey:\$k}}' > "\$g/harnesses-$ARCH.manifest.json"
zstd -q -f "\$g/vmlinux-test" -o "\$g/vmlinux-test.zst"
write_asset() {
  local kind="\$1" key="\$2" raw="\$3" zst="\$4"
  cp "\$zst" "\$a/\$kind-\$key.zst"
  jq -n --arg kind "\$kind" --arg arch "$ARCH" --arg key "\$key" \
    --arg sha "\$(sha256sum "\$raw" | cut -d' ' -f1)" --argjson size "\$(stat -c %s "\$raw")" \
    --arg csha "\$(sha256sum "\$a/\$kind-\$key.zst" | cut -d' ' -f1)" --argjson csize "\$(stat -c %s "\$a/\$kind-\$key.zst")" \
    '{format:1,kind:\$kind,arch:\$arch,key:\$key,sha256:\$sha,size:\$size,compressed:{sha256:\$csha,size:\$csize}}' > "\$a/\$kind-\$key.json"
}
write_asset kernel "$kkey" "\$g/vmlinux-test" "\$g/vmlinux-test.zst"
write_asset base "$bkey" "\$g/base-$ARCH.ext4" "\$g/base-$ARCH.ext4.zst"
write_asset harnesses "$hkey" "\$g/harnesses-$ARCH.ext4" "\$g/harnesses-$ARCH.ext4.zst"
EOF
chmod +x "$src/scripts/deploy/"*.sh "$src/scripts/guest/"*.sh "$src/scripts/release/"*.sh
printf 'v1\n' > "$src/app.txt"
git -C "$src" add .
git -C "$src" commit --quiet -m "code commit 1"
c1="$(git -C "$src" rev-parse HEAD)"
git -C "$src" tag v0.1.0 "$c1"

# Docs-only commit on top of c1.
printf 'docs change\n' > "$src/docs/deploy.mdx"
printf 'readme change\n' > "$src/README.md"
git -C "$src" add docs/deploy.mdx README.md
git -C "$src" commit --quiet -m "docs only"
c2="$(git -C "$src" rev-parse HEAD)"
git -C "$src" push --quiet origin main v0.1.0

# --- mocks: systemd-run, systemctl, curl --------------------------------------------------------
mkdir -p "$work/bin"
cat > "$work/bin/systemd-run" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
wd=""
envs=()
while (($#)); do
  case "$1" in
    --) shift; break ;;
    -p)
      case "$2" in WorkingDirectory=*) wd="${2#WorkingDirectory=}" ;; esac
      shift 2 ;;
    -E)
      envs+=("$2")
      shift 2 ;;
    *) shift ;;
  esac
done
cd "${wd:-.}"
exec env "${envs[@]}" "$@"
MOCK
cat > "$work/bin/systemctl" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
cat > "$work/bin/curl" <<'MOCK'
#!/usr/bin/env bash
set -euo pipefail
head_only=0; out=""; url=""
args=("$@")
while (($#)); do
  case "$1" in
    --head|-I) head_only=1; shift ;;
    -o) out="$2"; shift 2 ;;
    http://*|https://*|file://*) url="$1"; shift ;;
    *) shift ;;
  esac
done
case "$url" in
  http://controller.test/api/v1/version)
    cat "$MOCK_FOLLOW_FILE"
    exit 0 ;;
  https://github.com/gempir/agenthold/releases/download/*)
    rel="${url#https://github.com/gempir/agenthold/releases/download/}"
    echo "gh-download:$rel" >> "$MOCK_WORK/events.log"
    src="$MOCK_GH_RELEASES/$rel"
    [[ -f "$src" ]] || exit 22
    if ((!head_only)); then cp "$src" "$out"; fi
    exit 0 ;;
  *)
    exec /usr/bin/curl "${args[@]}" ;;
esac
MOCK
chmod +x "$work/bin/systemd-run" "$work/bin/systemctl" "$work/bin/curl"
export PATH="$work/bin:$PATH"
export MOCK_WORK="$work"
export MOCK_GH_RELEASES="$work/gh-releases"
export MOCK_FOLLOW_FILE="$work/follow.json"

# Populate a fake GitHub release for v0.1.0.
make_gh_release() { # make_gh_release <version> <commit>
  local ver="$1" c="$2" d="$work/gh-releases/$1"
  mkdir -p "$d"
  printf '#!/usr/bin/env bash\necho "agenthold %s (%s)"\n' "$ver" "${c:0:12}" > "$d/agenthold-linux-$ARCH"
  chmod +x "$d/agenthold-linux-$ARCH"
  jq -n --arg v "$ver" --arg arch "$ARCH" --arg k "$kkey" --arg b "$bkey" --arg h "$hkey" \
    '{format:2,version:$v,arch:$arch,files:{},kernel:{key:$k},base:{key:$b},harnesses:{key:$h}}' > "$d/runtime-linux-$ARCH.manifest.json"
  (cd "$d" && sha256sum "agenthold-linux-$ARCH" "runtime-linux-$ARCH.manifest.json" > SHA256SUMS)
}
make_gh_release v0.1.0 "$c1"

reset_env() {
  rm -rf "$work/store" "$work/failed" "$work/state" "$work/cache" "$work/logs" "$work/deploy" "$work/events.log"
  mkdir -p "$work/store" "$work/failed" "$work/state" "$work/cache" "$work/logs" "$work/deploy"
  : > "$work/events.log"
}
run_build() {
  local -a env_args=() script_args=()
  local seen_sep=0 arg
  for arg in "$@"; do
    if ((!seen_sep)) && [[ "$arg" == -- ]]; then
      seen_sep=1
    elif ((seen_sep)); then
      script_args+=("$arg")
    else
      env_args+=("$arg")
    fi
  done
  env \
    AGENTHOLD_BUILD_REPO="$origin" \
    AGENTHOLD_BUILD_STORE="$work/store" \
    AGENTHOLD_BUILD_FAILED="$work/failed" \
    AGENTHOLD_BUILD_STATE="$work/state" \
    AGENTHOLD_BUILD_CACHE="$work/cache" \
    AGENTHOLD_BUILD_LOGS="$work/logs" \
    AGENTHOLD_BUILD_USER=root \
    AGENTHOLD_BUILD_LOCK="$work/build.lock" \
    AGENTHOLD_BUILD_MIN_FREE_GB=0 \
    AGENTHOLD_DEPLOY_ROOT="$work/deploy" \
    "${env_args[@]}" bash "$build_script" "${script_args[@]}" >"$work/run.log" 2>&1
}
verify_pull_check() {
  local expected_ver="$1"
  AGENTHOLD_DEPLOY_SOURCE="$work/store" AGENTHOLD_DEPLOY_ROOT="$work/deploy" AGENTHOLD_DEPLOY_DATA_DIR="$work/pdata" \
    AGENTHOLD_DEPLOY_CONFIG="$work/none.conf" AGENTHOLD_DEPLOY_HOST_MOUNT="" \
    bash "$pull_script" --check >"$work/pull.log" 2>&1 || { cat "$work/pull.log" >&2; fail "pull --check for $expected_ver"; }
  grep -q "check passed: $expected_ver " "$work/pull.log" || { cat "$work/pull.log" >&2; fail "pull --check version $expected_ver"; }
}

# 1. Default main build skips docs-only c2, builds c1 locally, publishes sha-<c1>, passes pull --check.
reset_env
run_build || fail 'default main build'
grep -q "newest non-docs commit is $c1" "$work/run.log" || fail 'did not skip docs-only commit'
grep -qx "local-built:sha-$c1" "$work/events.log" || fail 'default main build did not build locally'
[[ "$(jq -er .version "$work/store/channel.json")" == "sha-$c1" ]] || fail 'channel version for main'
[[ "$(jq -er .version "$work/state/last-build.json")" == "sha-$c1" ]] || fail 'last-build version'
verify_pull_check "sha-$c1"
# Re-running skips already-built release.
run_build || fail 'idempotent main build'
grep -q "sha-$c1 is already built" "$work/run.log" || fail 'did not skip already built sha'

# 2. New code commit c3 on main triggers a fresh local build.
printf 'v2\n' > "$src/app.txt"
git -C "$src" add app.txt
git -C "$src" commit --quiet -m "code commit 3"
c3="$(git -C "$src" rev-parse HEAD)"
git -C "$src" tag v0.2.0-rc.1 "$c3"
git -C "$src" push --quiet origin main v0.2.0-rc.1
run_build || fail 'c3 main build'
[[ "$(jq -er .version "$work/store/channel.json")" == "sha-$c3" ]] || fail 'channel version for c3'
verify_pull_check "sha-$c3"

# 3. --tag v0.1.0 defaults to GitHub Releases download (not local build) and publishes v0.1.0.
reset_env
run_build -- --tag v0.1.0 || fail '--tag v0.1.0 build'
grep -q "gh-download:v0.1.0/agenthold-linux-$ARCH" "$work/events.log" || fail '--tag did not download from GitHub Releases'
! grep -q "local-built:" "$work/events.log" || fail '--tag unexpectedly built locally'
[[ "$(jq -er .version "$work/store/channel.json")" == "v0.1.0" && "$(jq -er .commit "$work/store/channel.json")" == "$c1" ]] || fail 'channel for v0.1.0'
verify_pull_check "v0.1.0"
run_build -- --tag v0.1.0 || fail 'idempotent --tag v0.1.0'
grep -q 'v0.1.0 is already built' "$work/run.log" || fail 'did not skip already built v0.1.0'

# 4. --tag v0.2.0-rc.1 with AGENTHOLD_BUILD_RELEASES=local builds the semver prerelease locally.
run_build AGENTHOLD_BUILD_RELEASES=local -- --tag v0.2.0-rc.1 || fail '--tag v0.2.0-rc.1 local build'
grep -qx "local-built:v0.2.0-rc.1" "$work/events.log" || fail '--tag local did not build locally'
[[ "$(jq -er .version "$work/store/channel.json")" == "v0.2.0-rc.1" ]] || fail 'channel for v0.2.0-rc.1'
verify_pull_check "v0.2.0-rc.1"
run_build || fail 'main timer after release'
[[ "$(jq -er .version "$work/store/channel.json")" == "v0.2.0-rc.1" ]] || fail 'main timer replaced the tag on the same commit'

# 5. AGENTHOLD_BUILD_FOLLOW following a sha-<commit> controller defaults to local build.
reset_env
jq -n --arg v "sha-$c1" --arg c "${c1:0:12}" '{version:$v,commit:$c}' > "$MOCK_FOLLOW_FILE"
run_build AGENTHOLD_BUILD_FOLLOW=http://controller.test || fail 'follow sha build'
grep -qx "local-built:sha-$c1" "$work/events.log" || fail 'follow sha did not default to local build'
[[ "$(jq -er .version "$work/store/channel.json")" == "sha-$c1" ]] || fail 'follow sha channel'

# 6. AGENTHOLD_BUILD_FOLLOW following a v0.1.0 controller defaults to GitHub Releases download.
reset_env
jq -n --arg v "v0.1.0" --arg c "${c1:0:12}" '{version:$v,commit:$c}' > "$MOCK_FOLLOW_FILE"
run_build AGENTHOLD_BUILD_FOLLOW=http://controller.test || fail 'follow semver build'
grep -q "gh-download:v0.1.0/agenthold-linux-$ARCH" "$work/events.log" || fail 'follow semver did not download from GitHub Releases'
[[ "$(jq -er .version "$work/store/channel.json")" == "v0.1.0" ]] || fail 'follow semver channel'
verify_pull_check "v0.1.0"

# 7. AGENTHOLD_BUILD_FOLLOW when a tagged GitHub release binary is not yet published exits 0 cleanly.
reset_env
jq -n --arg v "v0.2.0-rc.1" --arg c "${c3:0:12}" '{version:$v,commit:$c}' > "$MOCK_FOLLOW_FILE"
run_build AGENTHOLD_BUILD_FOLLOW=http://controller.test || fail 'follow unpublished semver must exit 0'
grep -q "v0.2.0-rc.1 has no release for $ARCH yet; nothing to build" "$work/run.log" || fail 'follow unpublished message'
[[ ! -e "$work/store/channel.json" ]] || fail 'unpublished semver wrote channel'

# 8. Invalid tags and conflicting options are rejected.
for bad in "sha-$c1" "v01.0.0" "v0.1" "v0.1.0-" "v0.1.0..1"; do
  if run_build -- --tag "$bad"; then fail "invalid --tag $bad accepted"; fi
done
if run_build -- --commit "$c1" --tag v0.1.0; then fail '--commit + --tag accepted'; fi

echo 'build-local tests passed'
