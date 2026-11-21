#!/usr/bin/env bash
# Prints the cache key of the harnesses layer: a hash of everything that goes into it
# (images/harnesses, the Node version in images/base/versions.env, and build-harnesses.sh).
# Bumping a harness package changes this key without invalidating the Debian base image.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
{
  sha256sum scripts/guest/build-harnesses.sh
  grep -E '^(DEBIAN_SNAPSHOT|NODE_VERSION|NODE_SHA256_)=' images/base/versions.env
  find images/harnesses -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum
} | sha256sum | cut -c1-32
