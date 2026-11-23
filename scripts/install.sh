#!/bin/sh
set -eu
src="$(dirname "$0")/../bin/filo"
dest="${HOME}/.local/bin"
mkdir -p "$dest"
install -m 0755 "$src" "$dest/filo.new"
mv "$dest/filo.new" "$dest/filo"
