GOLANGCI_LINT_VERSION ?= v2.5.0
COMPOSE ?= $(shell command -v docker >/dev/null 2>&1 && echo "docker compose" || echo "podman compose")
CONTAINER ?= $(shell command -v docker >/dev/null 2>&1 && echo docker || echo podman)
IMAGE ?= ghcr.io/michele/quietbox:latest
PLATFORMS ?= linux/amd64,linux/arm64
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
REVISION ?= $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
DOCKER_BUILD_ARGS := --label org.opencontainers.image.version=$(VERSION) --label org.opencontainers.image.revision=$(REVISION)
COMPOSE_FILE := docker-compose.test.yml
ITEST_CONFIG := testdata/config/itest.toml

.PHONY: test lint itest itest-up itest-down seed build docker-build docker-buildx

test:
	go test ./... -race

lint:
	go vet ./...
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

build:
	CGO_ENABLED=0 go build -trimpath -o bin/quietbox ./cmd/quietbox

# Container image for this machine's architecture, tagged quietbox:local.
docker-build:
	$(CONTAINER) build $(DOCKER_BUILD_ARGS) -t quietbox:local .

# Multi-arch image (amd64 + arm64) named $(IMAGE). The build stage cross-compiles,
# so no QEMU is needed. PUSH=1 pushes it; without PUSH, docker only builds (the
# result stays in the buildx cache) and podman keeps a local manifest list.
docker-buildx:
ifeq ($(CONTAINER),podman)
	-podman manifest rm $(IMAGE) 2>/dev/null
	podman build $(DOCKER_BUILD_ARGS) --platform $(PLATFORMS) --manifest $(IMAGE) .
	$(if $(PUSH),podman manifest push --all $(IMAGE) docker://$(IMAGE))
else
	docker buildx build $(DOCKER_BUILD_ARGS) --platform $(PLATFORMS) -t $(IMAGE) $(if $(PUSH),--push) .
endif

itest-up:
	$(COMPOSE) -f $(COMPOSE_FILE) up -d --wait

itest-down:
	$(COMPOSE) -f $(COMPOSE_FILE) down -v

seed:
	go run ./tools/seed --addr 127.0.0.1:1143 --user me@quietbox.test --dir testdata/seed

itest: itest-up seed
	QUIETBOX_IMAP_PASSWORD=test go test -tags integration -race -count=1 ./... ; \
	status=$$?; $(MAKE) itest-down; exit $$status
