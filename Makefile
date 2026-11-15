VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w \
	-X github.com/gempir/agenthold/internal/version.Version=$(VERSION) \
	-X github.com/gempir/agenthold/internal/version.Commit=$(COMMIT)
GO ?= go
PNPM ?= pnpm

export CGO_ENABLED := 0

.PHONY: all build build-go web web-deps test test-go test-web test-kvm test-guest lint lint-go lint-web fmt dev dev-vm dev-vm-shell dev-vm-delete guest-kernel guest-base runtime clean generate gen-sql gen-go-api gen-api

all: build
build: web build-go

build-go:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/agenthold ./cmd/agenthold
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/sandboxd ./cmd/sandboxd

web-deps:
	cd web && $(PNPM) install --frozen-lockfile
web: web-deps
	cd web && $(PNPM) build

test: test-go test-web
test-go:
	CGO_ENABLED=1 $(GO) test -race ./...
test-web: web-deps
	cd web && $(PNPM) test

# The tagged tests require a Linux host with /dev/kvm and root networking privileges.
test-kvm:
	@if [ "$$(uname -s)" = Darwin ]; then \
		$(MAKE) dev-vm && limactl shell --workdir "$(CURDIR)" agenthold -- make test-kvm; \
	else \
		test -c /dev/kvm || { echo '/dev/kvm is required' >&2; exit 1; }; \
		case "$$(uname -m)" in x86_64) arch=amd64;; aarch64) arch=arm64;; *) echo 'unsupported architecture' >&2; exit 1;; esac; \
		base="$${AGENTHOLD_KVM_BASE:-$(CURDIR)/build/guest/$$arch/base-$$arch.ext4}"; \
		kernel="$${AGENTHOLD_KVM_KERNEL:-$(CURDIR)/build/guest/$$arch/vmlinux-6.1.188-agenthold}"; \
		fc="$${AGENTHOLD_KVM_FIRECRACKER:-$(CURDIR)/build/runtime/firecracker}"; \
		jailer="$${AGENTHOLD_KVM_JAILER:-$(CURDIR)/build/runtime/jailer}"; \
		for path in "$$base" "$$kernel" "$$fc" "$$jailer"; do test -f "$$path" || { echo "missing KVM fixture: $$path" >&2; exit 1; }; done; \
		work="$$(mktemp -d /tmp/ahkvm-XXXXXX)"; trap 'rm -rf -- "$$work"' EXIT; chmod 755 "$$work"; \
		cp --sparse=always "$$base" "$$work/base.ext4" && cp "$$kernel" "$$work/kernel" && cp "$$fc" "$$work/firecracker" && cp "$$jailer" "$$work/jailer" || exit 1; \
		chmod 644 "$$work/base.ext4" "$$work/kernel"; chmod 755 "$$work/firecracker" "$$work/jailer"; \
		runner=; if [ "$$(id -u)" -ne 0 ]; then runner=sudo; fi; \
		$$runner env "PATH=$$PATH" CGO_ENABLED=1 GOFLAGS=-buildvcs=false AGENTHOLD_REQUIRE_KVM=1 AGENTHOLD_KVM_BASE="$$work/base.ext4" AGENTHOLD_KVM_KERNEL="$$work/kernel" AGENTHOLD_KVM_FIRECRACKER="$$work/firecracker" AGENTHOLD_KVM_JAILER="$$work/jailer" $(GO) test -v -tags kvm -count=1 ./... >"$$work/test.log" 2>&1 || { cat "$$work/test.log"; exit 1; }; \
		cat "$$work/test.log"; \
		for test in TestJailedLifecycle TestGuestFirewallIsolation; do grep -q "^--- PASS: $$test " "$$work/test.log" || { echo "KVM test did not pass: $$test" >&2; exit 1; }; done; \
	fi

lint: lint-go lint-web
lint-go:
	golangci-lint run ./...
lint-web: web-deps
	cd web && $(PNPM) lint
fmt:
	golangci-lint fmt ./...
	cd web && $(PNPM) format

# These scripts run natively on Linux and re-execute in the Lima VM on macOS.
guest-kernel:
	./scripts/guest/build-kernel.sh
guest-base:
	./scripts/guest/build-rootfs.sh
test-guest:
	./scripts/guest/boot-test.sh
runtime:
	./scripts/guest/fetch-runtime.sh

# On macOS, build the embedded web UI locally, then run the native Linux server
# as root inside the VM. Guest artifacts can be built once with the targets above.
dev: web dev-vm
	limactl shell --workdir "$(CURDIR)" agenthold -- bash -c 'test -x build/runtime/firecracker && test -x build/runtime/jailer && test -f build/guest/arm64/vmlinux-6.1.188-agenthold && test -f build/guest/arm64/base-arm64.ext4 || { echo "Run make runtime guest-kernel guest-base first" >&2; exit 1; }; go install github.com/air-verse/air@v1.67.4 && exec sudo env "PATH=$$PATH" AGENTHOLD_CONFIG=deploy/dev/agenthold.yaml "$$(go env GOPATH)/bin/air" -c .air.toml'

dev-vm:
	@command -v limactl >/dev/null || { echo 'limactl not found: brew install lima' >&2; exit 1; }
	@limactl list -q 2>/dev/null | grep -qx agenthold || \
		limactl create --tty=false --name=agenthold --mount-only '$(CURDIR):w' deploy/dev/lima.yaml
	@limactl list --format '{{.Status}}' agenthold 2>/dev/null | grep -qx Running || limactl start agenthold
dev-vm-shell: dev-vm
	limactl shell --workdir '$(CURDIR)' agenthold
dev-vm-delete:
	limactl delete --force agenthold

clean:
	rm -rf bin tmp web/node_modules
	find web/dist -mindepth 1 ! -name .gitkeep -delete

SQLC_VERSION := v1.31.1
generate: gen-sql gen-go-api gen-api
gen-sql:
	CGO_ENABLED=1 $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION) generate
gen-go-api:
	$(GO) generate ./internal/api/gen/
gen-api: web-deps
	cd web && $(PNPM) gen:api
