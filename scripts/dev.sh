#!/usr/bin/env bash
# Dev container helper for Filo. Needs rootless podman. Works from anywhere inside the repo clone.
#
#   scripts/dev.sh build          build the image (also done automatically on first use)
#   scripts/dev.sh shell          interactive shell in the container
#   scripts/dev.sh run CMD...     run one command, e.g.  scripts/dev.sh run go test ./...
#   scripts/dev.sh clean          remove the image and the Go cache volumes
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
  podman build \
    --build-arg "UID=$(id -u)" --build-arg "GID=$(id -g)" \
    -f "$repo/.devcontainer/Containerfile" \
    -t "$image" \
    "$repo/.devcontainer"
}

run() {
  podman image inspect "$image" >/dev/null 2>&1 || build

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
  sed -n '2,10p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
}

case "${1:-}" in
  build) build ;;
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
  *) usage; exit 2 ;;
  claude) shift; run claude "$@" ;;
esac
