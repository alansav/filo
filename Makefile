.PHONY: build install

# Static build: the SQLite driver is pure Go, so the binary built in the dev container also runs on the host.
build:
	CGO_ENABLED=0 go build -o bin/filo ./cmd/filo

# Builds, then copies bin/filo to ~/.local/bin. Only where Go is installed; with the dev container, run
# `scripts/dev.sh run make build` and then scripts/install.sh on the host.
install: build
	scripts/install.sh
