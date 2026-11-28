.PHONY: build install

# Static build: the SQLite driver is pure Go, so the binary built in the dev container also runs on the host.
build:
	CGO_ENABLED=0 go build -o bin/filo ./cmd/filo

# Builds, then copies bin/filo to ~/.local/bin and loads bash completion from ~/.bashrc. Only where Go is
# installed; without it, run scripts/install.sh on the host, which builds in the dev container.
install: build
	FILO_INSTALL_NO_BUILD=1 scripts/install.sh
