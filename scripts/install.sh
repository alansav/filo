#!/bin/sh
# Builds Filo, installs it to ~/.local/bin and loads its bash completion from ~/.bashrc. Run on the host; safe to run
# again: the binary is replaced, and the .bashrc line is added only if it is not there already.
#
# Builds with the local Go if there is one (make build), otherwise in the dev container (scripts/dev.sh run make
# build). FILO_INSTALL_NO_BUILD=1 skips the build and installs the bin/filo already there (make install uses it).
set -eu
repo="$(cd "$(dirname "$0")/.." && pwd)"

if [ "${FILO_INSTALL_NO_BUILD:-}" != 1 ]; then
  if command -v go >/dev/null 2>&1; then
    make -C "$repo" build
  else
    "$repo/scripts/dev.sh" run make build
  fi
fi

dest="${HOME}/.local/bin"
mkdir -p "$dest"
install -m 0755 "$repo/bin/filo" "$dest/filo.new"
mv "$dest/filo.new" "$dest/filo"
echo "installed $dest/filo"

# Bash completion: one line in ~/.bashrc, which does nothing in a shell where filo is not on the PATH.
line='command -v filo >/dev/null 2>&1 && source <(filo completion bash)'
bashrc="${HOME}/.bashrc"
if [ -f "$bashrc" ] && grep -qxF "$line" "$bashrc"; then
  echo "bash completion already loaded from $bashrc"
else
  printf '\n# filo bash completion\n%s\n' "$line" >>"$bashrc"
  echo "bash completion added to $bashrc (open a new shell, or: source $bashrc)"
fi

case ":${PATH}:" in
  *":$dest:"*) ;;
  *) echo "note: $dest is not on your PATH; add it so filo and its completion work" ;;
esac
