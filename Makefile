GOLANGCI_LINT_VERSION ?= v2.5.0
COMPOSE ?= $(shell command -v docker >/dev/null 2>&1 && echo "docker compose" || echo "podman compose")
COMPOSE_FILE := docker-compose.test.yml
ITEST_CONFIG := testdata/config/itest.toml

.PHONY: test lint itest itest-up itest-down seed build

test:
	go test ./... -race

lint:
	go vet ./...
	go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run

build:
	CGO_ENABLED=0 go build -trimpath -o bin/quietbox ./cmd/quietbox

itest-up:
	$(COMPOSE) -f $(COMPOSE_FILE) up -d --wait

itest-down:
	$(COMPOSE) -f $(COMPOSE_FILE) down -v

seed:
	go run ./tools/seed --addr 127.0.0.1:1143 --user me@quietbox.test --dir testdata/seed

itest: itest-up seed
	QUIETBOX_IMAP_PASSWORD=test go test -tags integration -race -count=1 ./... ; \
	status=$$?; $(MAKE) itest-down; exit $$status
