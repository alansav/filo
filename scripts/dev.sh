#!/usr/bin/env bash
# Dev container helper for Filo. Needs rootless podman. Works from anywhere inside the repo clone.
#
#   scripts/dev.sh build               build the image, with Claude Code (also done automatically on first use)
#   scripts/dev.sh build --no-claude   build the image without Claude Code
#   scripts/dev.sh shell               interactive shell in the container
#   scripts/dev.sh run CMD...          run one command, e.g.  scripts/dev.sh run go test ./...
#   scripts/dev.sh clean               remove the image and the Go cache volumes
#   scripts/dev.sh claude              Claude Code (needs an image built with it, the default)
#
# Only the repo is mounted (at /workspace). The container has its own home directory, so a scan of
# "home" inside it can only ever see the container's files, never yours.
#
# Optional: FILO_DEV_PODMAN_ARGS adds extra flags to `podman run`, for example
#   FILO_DEV_PODMAN_ARGS="--sysctl net.ipv6.conf.all.disable_ipv6=1"
# if IPv6 problems stall module downloads.
set -euo pipefail

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
image="${FILO_DEV_IMAGE:-filo-dev}"

build() {
  local claude=1
  case "${1:-}" in
    "") ;;
    --no-claude) claude=0 ;;
    *) echo "usage: $0 build [--no-claude]" >&2; exit 2 ;;
  esac
  podman build \
    --build-arg "UID=$(id -u)" --build-arg "GID=$(id -g)" \
    --build-arg "INSTALL_CLAUDE=$claude" \
    -f "$repo/.devcontainer/Containerfile" \
    -t "$image" \
    "$repo/.devcontainer"
}

# The container cannot see your ~/.gitconfig, so commits made inside it use the repo's own git config.
# If user.name or user.email is missing there, ask for it (suggesting your global value) and store it in
# the repo's .git/config. Values already set are never changed. Without a terminal, only warn.
git_identity() {
  git -C "$repo" rev-parse --git-dir >/dev/null 2>&1 || return 0
  local key value suggested
  for key in user.name user.email; do
    git -C "$repo" config --local "$key" >/dev/null && continue
    if ! [ -t 0 ]; then
      echo "dev.sh: $key is not set in this repo's git config; commits made in the container will fail" >&2
      continue
    fi
    suggested="$(git config --global "$key" || true)"
    read -r -p "git $key for commits made in the container${suggested:+ [$suggested]}: " value
    value="${value:-$suggested}"
    if [ -n "$value" ]; then
      git -C "$repo" config --local "$key" "$value"
    else
      echo "dev.sh: $key left unset" >&2
    fi
  done
}

run() {
  podman image inspect "$image" >/dev/null 2>&1 || build
  git_identity

  local tty=()
  if [ -t 0 ] && [ -t 1 ]; then tty=(-it); fi

  local extra=()
  read -r -a extra <<<"${FILO_DEV_PODMAN_ARGS:-}"

  podman run --rm "${tty[@]}" "${extra[@]}" \
    --userns=keep-id --user "$(id -u):$(id -g)" \
    -v "$repo:/workspace:Z" \
    -v filo-gomod:/home/dev/go/pkg/mod \
    -v filo-gocache:/home/dev/.cache/go-build \
    -v filo-claude:/home/dev/.claude \
    -e CLAUDE_CONFIG_DIR=/home/dev/.claude \
    -e "TERM=${TERM:-xterm-256color}" \
    -w /workspace \
    "$image" "$@"
}

usage() {
  sed -n '2,11p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

case "${1:-}" in
  build) shift; build "$@" ;;
  shell) run bash ;;
  run)
    shift
    [ $# -gt 0 ] || { echo "usage: $0 run CMD..." >&2; exit 2; }
    run "$@"
    ;;
  clean)
    podman rmi -f "$image" || true
    podman volume rm -f filo-gomod filo-gocache
    ;;
  claude)
    shift
    podman image inspect "$image" >/dev/null 2>&1 || build
    if ! podman run --rm "$image" sh -c 'command -v claude' >/dev/null 2>&1; then
      echo "dev.sh: Claude Code is not installed in the image $image (it was built with --no-claude, or before" \
        "Claude Code became the default); rebuild it with: $0 build" >&2
      exit 1
    fi
    run claude "$@"
    ;;
  *) usage; exit 2 ;;
esac
