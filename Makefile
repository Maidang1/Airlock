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

.PHONY: all assets tailnet-assets web-surface cage cage-update-installer prepare-resources prepare-resources-fast dev build-app dmg build-client test test-go clean

all: build-app

web-surface:
	node scripts/build-web-surface.mjs

assets:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(GOARCH) $(GO) build -trimpath \
		-ldflags "-s -w -X github.com/lima-vm/lima/v2/pkg/version.Version=$(LIMA_VERSION)" \
		-o $(ASSETS)/lima-guestagent.Linux-$(GUEST_ARCH) github.com/lima-vm/lima/v2/cmd/lima-guestagent
	gzip -9 -n -f $(ASSETS)/lima-guestagent.Linux-$(GUEST_ARCH)
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
	$(GO_BUILD) -o _output/bin/cage ./cmd/cage
	codesign -f --entitlements third_party/lima/vz.entitlements -s - _output/bin/cage

cage-update-installer:
	CGO_ENABLED=1 $(GO) build -trimpath -ldflags "-s -w" -o _output/bin/CageUpdateInstaller ./cmd/cage-update-installer
	codesign -f -s - _output/bin/CageUpdateInstaller

prepare-resources:
	bash scripts/prepare-resources.sh

prepare-resources-fast:
	bash scripts/prepare-resources.sh --fast

dev: prepare-resources-fast
	CGO_ENABLED=1 $(GO) tool mygo dev

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

clean:
	rm -rf _output resources/darwin-arm64 .mygo
	find $(ASSETS) -mindepth 1 ! -name README -exec rm -rf {} +
	find internal/workbench/web -mindepth 1 ! -name README -exec rm -rf {} +
