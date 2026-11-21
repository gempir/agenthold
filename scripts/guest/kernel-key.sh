#!/usr/bin/env bash
# Prints the key of the guest kernel: a hash of everything that goes into it (the build script and
# scripts/guest/kernel/) and nothing else. Release binaries embed it and fetch the kernel by it, so
# it must not depend on the host that computes it.
set -euo pipefail
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
{ sha256sum scripts/guest/build-kernel.sh; find scripts/guest/kernel -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum; } | sha256sum | cut -c1-32
