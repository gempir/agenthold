#!/usr/bin/env bash
# Prints the cache key of the base image: a hash of everything that goes into it (images/base and
# the rootfs build scripts) and nothing else. sandboxd is not part of the base (the host delivers it
# at boot), so a Go-only change keeps the key, and the base, unchanged.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
{ sha256sum scripts/guest/build-rootfs.sh scripts/guest/install-bun.sh; find images/base -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum; } | sha256sum | cut -c1-32
