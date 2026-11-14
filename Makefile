VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X github.com/gempir/agenthold/internal/version.Version=$(VERSION) \
	-X github.com/gempir/agenthold/internal/version.Commit=$(COMMIT)
GO      ?= go
PNPM    ?= pnpm
COMPOSE ?= docker compose -f deploy/docker-compose.dev.yml

export CGO_ENABLED := 0

.PHONY: all build build-go web web-deps test test-go test-web lint lint-go lint-web fmt dev dev-down image clean generate gen-sql gen-go-api gen-api

all: build

## build: build the web app and both binaries into bin/
build: web build-go

build-go:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/agenthold ./cmd/agenthold
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/sandboxd ./cmd/sandboxd

web-deps:
	cd web && $(PNPM) install --frozen-lockfile

web: web-deps
	cd web && $(PNPM) build

## test: run all Go and web tests
test: test-go test-web

test-go:
	CGO_ENABLED=1 $(GO) test -race ./...

test-web: web-deps
	cd web && $(PNPM) test

## lint: run all linters
lint: lint-go lint-web

lint-go:
	golangci-lint run ./...

lint-web: web-deps
	cd web && $(PNPM) lint

fmt:
	golangci-lint fmt ./...
	cd web && $(PNPM) format

## dev: run the server (live reload) and Vite in containers; open http://localhost:8080
dev:
	$(COMPOSE) up --build

dev-down:
	$(COMPOSE) down

## image: build the production server image
image:
	docker build --build-arg VERSION=$(VERSION) --build-arg COMMIT=$(COMMIT) -t agenthold:$(VERSION) .

clean:
	rm -rf bin tmp web/node_modules
	find web/dist -mindepth 1 ! -name .gitkeep -delete

SQLC_VERSION := v1.31.1

## generate: regenerate sqlc queries, Go API types and the TypeScript client
generate: gen-sql gen-go-api gen-api

gen-sql:
	CGO_ENABLED=1 $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate

gen-go-api:
	$(GO) generate ./internal/api/gen/

## gen-api: regenerate web/src/api/schema.d.ts from api/openapi.yaml
gen-api: web-deps
	cd web && $(PNPM) gen:api
