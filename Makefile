VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	CGO_ENABLED=0 go build -ldflags "-X main.version=$(VERSION)" -o bin/filo ./cmd/filo
