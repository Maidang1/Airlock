GO ?= GOTOOLCHAIN=auto go
GOOS ?= $(shell $(GO) env GOOS)
GOARCH ?= $(shell $(GO) env GOARCH)
VERSION ?= $(shell tr -d '\r\n' < VERSION)
LIMA_VERSION := $(shell cat internal/limaembed/lima-version)

ifeq ($(GOARCH),amd64)
GUEST_ARCH := x86_64
else ifeq ($(GOARCH),arm64)
GUEST_ARCH := aarch64
else
GUEST_ARCH := $(GOARCH)
endif

ASSETS := internal/limaembed/assets
LDFLAGS := -s -w \
	-X github.com/sylvinus/agent-vm/internal/version.Version=$(VERSION) \
	-X github.com/lima-vm/lima/v2/pkg/version.Version=$(LIMA_VERSION)
TAGS := yq_noxml,yq_nolua,yq_noini,yq_nohcl,yq_notoml,yq_noprops,yq_nocsv,yq_nouri,yq_nobase64,yq_nosh,yq_noshell
CGO_ENABLED ?= 1
GO_BUILD := CGO_ENABLED=$(CGO_ENABLED) $(GO) build -trimpath -tags $(TAGS) -ldflags "$(LDFLAGS)"

.PHONY: dev-microvm all assets tailnet-assets web-surface cage cage-bin cage-update-installer prepare-resources prepare-resources-fast dev build-app dmg build-client test test-go airlock-init airlock-microvm microvm-image test-microvm clean

all: build-app

web-surface:
	node scripts/build-web-surface.mjs

# Lima runs only the macOS guest: its guest agent and Lima's templates.
assets:
	mkdir -p $(ASSETS)/Darwin
	CGO_ENABLED=0 GOOS=darwin GOARCH=$(GOARCH) $(GO) build -trimpath -tags $(TAGS) \
		-ldflags "-s -w -X github.com/lima-vm/lima/v2/pkg/version.Version=$(LIMA_VERSION)" \
		-o $(ASSETS)/Darwin/lima-guestagent.Darwin-$(GUEST_ARCH) github.com/lima-vm/lima/v2/cmd/lima-guestagent
	gzip -9 -n -f $(ASSETS)/Darwin/lima-guestagent.Darwin-$(GUEST_ARCH)
	rm -rf $(ASSETS)/templates && cp -R third_party/lima/templates $(ASSETS)/templates

tailnet-assets:
	bash scripts/build-tailnet.sh linux amd64 "$(CURDIR)/_output/tailnet/cage-tailnet-linux-amd64"
	bash scripts/build-tailnet.sh linux arm64 "$(CURDIR)/_output/tailnet/cage-tailnet-linux-arm64"
	gzip -n -c _output/tailnet/cage-tailnet-linux-amd64 > internal/tailnetassets/assets/cage-tailnet-linux-amd64.gz
	gzip -n -c _output/tailnet/cage-tailnet-linux-arm64 > internal/tailnetassets/assets/cage-tailnet-linux-arm64.gz

cage: web-surface assets tailnet-assets
	$(MAKE) cage-bin

# Helper binary only; assumes embedded assets are already built (see scripts/dev.sh).
cage-bin:
	$(GO_BUILD) -o _output/bin/cage ./cmd/cage
	codesign -f --entitlements third_party/lima/vz.entitlements -s - _output/bin/cage

cage-update-installer:
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags "-s -w" -o _output/bin/CageUpdateInstaller ./cmd/cage-update-installer
	codesign -f -s - _output/bin/CageUpdateInstaller

prepare-resources:
	bash scripts/prepare-resources.sh

prepare-resources-fast:
	bash scripts/prepare-resources.sh --fast

# Desktop development on the local MicroVM image (make microvm-image first).
dev:
	bash scripts/dev.sh

dev-microvm: dev

build-app:
	bash scripts/build-macos.sh

dmg:
	bash scripts/build-macos.sh --dmg

build-client:
	bash scripts/build-client.sh

test: test-go

test-go: web-surface assets tailnet-assets
	$(GO) test -tags $(TAGS) . ./cmd/... ./internal/...
	cd tools/tailnet && GOFLAGS= $(GO) test ./...

airlock-init:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags "-s -w" -o _output/bin/airlock-init ./cmd/airlock-init

airlock-microvm:
	$(GO_BUILD) -o _output/bin/airlock-microvm ./cmd/airlock-microvm
	codesign -f --entitlements third_party/lima/vz.entitlements -s - _output/bin/airlock-microvm

microvm-image: airlock-init
	bash scripts/build-microvm-image.sh

test-microvm:
	$(GO) test -tags $(TAGS) ./internal/microvm/... ./cmd/airlock-init/... ./cmd/airlock-microvm/...

clean:
	rm -rf _output resources/darwin-arm64 .mygo
	find $(ASSETS) -mindepth 1 ! -name README -exec rm -rf {} +
	find internal/workbench/web -mindepth 1 ! -name README -exec rm -rf {} +
