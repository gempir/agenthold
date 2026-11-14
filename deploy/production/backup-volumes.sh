#!/usr/bin/env bash
# Backs up (or restores) the home volumes of all agents.
#   ./backup-volumes.sh backup  <dir>   writes <dir>/<volume>.tar.gz per agent
#   ./backup-volumes.sh restore <dir>   recreates the volumes from those files
# Stop agents (or the whole stack) first for a consistent copy.
set -euo pipefail
mode=${1:?backup|restore}; dir=${2:?directory}
prefix=${AGENTHOLD_PREFIX:-agenthold}
mkdir -p "$dir"
case "$mode" in
  backup)
    for v in $(docker volume ls -q --filter "label=agenthold.managed=true" --filter "label=agenthold.prefix=$prefix" | grep -- "-home-"); do
      docker run --rm -v "$v":/v:ro -v "$(cd "$dir" && pwd)":/b alpine:3.22 tar czf "/b/$v.tar.gz" -C /v .
      echo "backed up $v"
    done ;;
  restore)
    for f in "$dir"/*-home-*.tar.gz; do
      v=$(basename "$f" .tar.gz)
      agent=${v#"$prefix"-home-}
      docker volume create --label agenthold.managed=true --label agenthold.prefix="$prefix" --label agenthold.agent="$agent" "$v" >/dev/null
      docker run --rm -v "$v":/v -v "$(cd "$dir" && pwd)":/b:ro alpine:3.22 sh -c "cd /v && tar xzf /b/$(basename "$f")"
      echo "restored $v"
    done ;;
  *) echo "usage: $0 backup|restore <dir>" >&2; exit 2 ;;
esac
