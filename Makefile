IMAGE     ?= docker.io/danotchuuy/game-relay
VERSION   ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PLATFORMS ?= linux/amd64,linux/arm64

.PHONY: build test run docker-build docker-push

build:
	go build -o bin/relay ./cmd/relay

test:
	go test -race ./...

run:
	go run ./cmd/relay

# Builds the image for the local platform only and loads it into Docker.
docker-build:
	docker build -t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

# Builds a multi-arch image and pushes $(VERSION) and latest. Run `docker login` first.
# Usage: make docker-push VERSION=1.2.0 (defaults to `git describe`).
docker-push:
	docker buildx build --platform $(PLATFORMS) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):latest --push .
