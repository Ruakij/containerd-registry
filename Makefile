VERSION ?= dev
LDFLAGS = -s -w -X main.version=$(VERSION)

.PHONY: build
build:
	CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "$(LDFLAGS)" -o bin/containerd-registry ./cmd/containerd-registry
