#!/usr/bin/env bash
# Deploys pinned agenthold images on the production host. Run by the CI deploy
# workflow over SSH as the forced command of a dedicated key (docs/deploy.md);
# SSH_ORIGINAL_COMMAND carries the arguments:
#
#   deploy <server-image-ref> <base-image-ref> <git-sha>   pull, switch, verify, roll back on failure
#   check  <server-image-ref> <base-image-ref> <git-sha>   validate the arguments only, change nothing
#   status                                                 show what is deployed
#
# Image refs must be digest references into ghcr.io/<owner>/agenthold and
# agenthold-base (owner: $DEPLOY_OWNER, default gempir). An optional first line
# on stdin is a ghcr.io token, used only for this run's pulls.
#
# State lives next to the compose file: .deploy.env (the image refs in use, read
# on top of .env), .deploy.env.prev (the one before) and .deploy.log (history).
# .env itself is never modified. Run compose by hand with:
#   docker compose --env-file .env --env-file .deploy.env <command>
set -euo pipefail

main() {
  local owner=${DEPLOY_OWNER:-gempir}
  local dir=${DEPLOY_DIR:-$HOME/agenthold/deploy/production}
  local service=agenthold

  local -a args
  if [[ -n ${SSH_ORIGINAL_COMMAND:-} ]]; then
    read -r -a args <<<"$SSH_ORIGINAL_COMMAND"
  else
    args=("$@")
  fi
  local cmd=${args[0]:-}

  cd "$dir"
  local envf=.deploy.env prev=.deploy.env.prev

  dc() {
    local -a e=(--env-file .env)
    [[ -f $envf ]] && e+=(--env-file "$envf")
    docker compose "${e[@]}" "$@"
  }

  case "$cmd" in
    status)
      echo "deployed:"
      cat "$envf" 2>/dev/null || echo "  (nothing deployed by CI yet; images come from .env)"
      echo "history:"
      tail -n 5 .deploy.log 2>/dev/null || true
      dc ps "$service"
      return 0
      ;;
    deploy | check) ;;
    *)
      echo "usage: deploy|check <server-image> <base-image> <git-sha> | status" >&2
      return 2
      ;;
  esac

  local server=${args[1]:-} base=${args[2]:-} sha=${args[3]:-}
  [[ $server =~ ^ghcr\.io/${owner}/agenthold@sha256:[0-9a-f]{64}$ ]] || { echo "bad server image ref" >&2; return 2; }
  [[ $base =~ ^ghcr\.io/${owner}/agenthold-base@sha256:[0-9a-f]{64}$ ]] || { echo "bad base image ref" >&2; return 2; }
  [[ $sha =~ ^[0-9a-f]{40}$ ]] || { echo "bad git sha" >&2; return 2; }
  if [[ $cmd == check ]]; then
    echo "ok: would deploy $sha ($server, $base)"
    dc config --images >/dev/null
    docker version --format 'docker {{.Server.Version}}' >/dev/null
    return 0
  fi

  # One deploy at a time on the host, whatever the callers do.
  exec 9>.deploy.lock
  flock -w 600 9 || { echo "another deploy is running" >&2; return 1; }

  # Registry login in a throwaway config dir; nothing is stored on the host.
  local tmp token=
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  export DOCKER_CONFIG=$tmp
  if [[ ! -t 0 ]]; then IFS= read -r -t 10 token || true; fi
  if [[ -n $token ]]; then
    printf '%s' "$token" | docker login ghcr.io -u "$owner" --password-stdin >/dev/null
  fi
  token=

  # Bring the compose file and this script up to date. Local edits to other
  # files (agenthold.yaml) stay; if the merge cannot be fast-forward, carry on
  # with what is checked out.
  if git -C "$dir" fetch -q origin main 2>/dev/null && git -C "$dir" merge -q --ff-only "$sha" 2>/dev/null; then
    install -m 0755 "$dir/deploy.sh" "$HOME/agenthold-deploy.sh.new" && mv -f "$HOME/agenthold-deploy.sh.new" "$HOME/agenthold-deploy.sh"
  else
    echo "warning: checkout not updated to $sha (not a fast-forward?); using the existing compose file" >&2
  fi

  echo "pulling images"
  docker pull -q "$server"
  docker pull -q "$base"

  [[ -f $envf ]] && cp -f "$envf" "$prev" || rm -f "$prev"
  write_env() {
    printf 'AGENTHOLD_IMAGE=%s\nAGENTHOLD_BASE_IMAGE=%s\nAGENTHOLD_DEPLOYED_SHA=%s\n' "$1" "$2" "$3" >"$envf.new"
    mv -f "$envf.new" "$envf"
  }
  write_env "$server" "$base" "$sha"

  echo "starting $service"
  if ! verify "$server" "$service"; then
    echo "deploy of $sha failed; rolling back" >&2
    dc logs --tail 50 "$service" >&2 || true
    if [[ -f $prev ]]; then mv -f "$prev" "$envf"; else rm -f "$envf"; fi
    dc up -d --no-deps --no-build --wait --wait-timeout 240 "$service" >&2 || echo "rollback did not become healthy either" >&2
    echo "$(date -u +%FT%TZ) FAILED $sha (rolled back)" >>.deploy.log
    return 1
  fi
  echo "$(date -u +%FT%TZ) deployed $sha $server $base" >>.deploy.log
  echo "deployed $sha"

  prune "$owner" "$server" "$base"
}

# verify starts the service with the new refs and checks that the running
# container uses exactly the pulled image and answers its health check.
verify() {
  local want service=$2
  want=$(docker image inspect --format '{{.Id}}' "$1")
  dc up -d --no-deps --no-build --wait --wait-timeout 240 "$service" || return 1
  local cid
  cid=$(dc ps -q "$service")
  [[ -n $cid ]] || return 1
  [[ $(docker inspect --format '{{.Image}}' "$cid") == "$want" ]] || { echo "running image differs from $1" >&2; return 1; }
  dc exec -T "$service" /usr/local/bin/agenthold healthcheck
}

# prune removes ghcr agenthold images other than the current and previous
# deploy. Images in use by any container are refused by docker and stay.
prune() {
  local owner=$1 keep keep_ids="" id
  for keep in "$2" "$3"; do keep_ids+=" $(docker image inspect --format '{{.Id}}' "$keep")"; done
  if [[ -f .deploy.env.prev ]]; then
    for keep in $(grep -E '^AGENTHOLD_(BASE_)?IMAGE=' .deploy.env.prev | cut -d= -f2-); do
      id=$(docker image inspect --format '{{.Id}}' "$keep" 2>/dev/null || true)
      keep_ids+=" $id"
    done
  fi
  for id in $(docker image ls --no-trunc --format '{{.ID}} {{.Repository}}' |
    awk -v a="ghcr.io/$owner/agenthold" -v b="ghcr.io/$owner/agenthold-base" '$2==a||$2==b{print $1}' | sort -u); do
    case " $keep_ids " in *" $id "*) continue ;; esac
    docker rmi "$id" >/dev/null 2>&1 || true
  done
}

main "$@"
exit $?
