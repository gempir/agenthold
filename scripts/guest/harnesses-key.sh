#!/usr/bin/env bash
# Prints the cache key of the harnesses layer: a hash of everything that goes into it
# (images/harnesses, the Bun version in images/base/versions.env, and the build scripts).
# Bumping a harness package changes this key without invalidating the Debian base image.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
{
  sha256sum scripts/guest/build-harnesses.sh scripts/guest/install-bun.sh
  grep -E '^(DEBIAN_SNAPSHOT|BUN_VERSION|BUN_SHA256_ARM64|BUN_SHA256_X64)=' images/base/versions.env
  find images/harnesses -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum
} | sha256sum | cut -c1-32
