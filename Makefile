VERSION ?= v1.20.0
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
COMMIT := $(shell git rev-parse HEAD 2>/dev/null || echo unknown)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.buildTime=$(BUILD_TIME) -X main.commit=$(COMMIT)
BUILD_FLAGS := -mod=readonly -trimpath -buildvcs=true
BUILD_TAGS := with_quic with_utls with_wireguard with_acme with_clash_api

.PHONY: build clean test docker install build-linux build-linux-arm64 build-all

# Build for current platform
build:
	go build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -tags "$(BUILD_TAGS)" -o yz-agent ./cmd/yz-agent

# Build for Linux amd64
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -tags "$(BUILD_TAGS)" -o yz-agent-linux-amd64 ./cmd/yz-agent
	cp yz-agent-linux-amd64 xboard-node-linux-amd64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o xbctl-linux-amd64 ./cmd/xbctl

# Build for Linux arm64
build-linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -tags "$(BUILD_TAGS)" -o yz-agent-linux-arm64 ./cmd/yz-agent
	cp yz-agent-linux-arm64 xboard-node-linux-arm64
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(BUILD_FLAGS) -ldflags "$(LDFLAGS)" -o xbctl-linux-arm64 ./cmd/xbctl

# Build all platforms
build-all: build-linux build-linux-arm64

# Run tests
test:
	bash tests/upgrade_version_test.sh
	bash tests/install_service_manager_test.sh
	bash tests/install_kernel_defaults_test.sh
	bash tests/install_paths_test.sh
	bash tests/install_name_migration_test.sh
	go test -mod=readonly -v -race -count=1 -tags "$(BUILD_TAGS)" ./...
	go test -mod=readonly -v -race -count=1 github.com/anytls/sing-anytls/session
	go test -mod=readonly -v -race -count=1 github.com/sagernet/sing-shadowsocks/shadowaead_2022
	go test -mod=readonly -v -race -count=1 -tags "$(BUILD_TAGS)" github.com/xtls/xray-core/common/singbridge github.com/xtls/xray-core/transport/internet/hysteria github.com/sagernet/sing-box/transport/v2raygrpclite github.com/sagernet/sing-box/third_party/sing-shadowsocks/shadowaead_2022

# Clean build artifacts
clean:
	rm -f yz-agent yz-agent-linux-* xboard-node xbctl xboard-node-linux-* xbctl-linux-*

# Build Docker image
docker:
	docker build --build-arg NODE_VERSION=$(VERSION) --build-arg SOURCE_COMMIT=$(COMMIT) -t yz-agent:$(VERSION) -t yz-agent:latest .

# Install to system (single node, legacy compat)
install: build
	sudo cp yz-agent /usr/local/bin/
	sudo mkdir -p /etc/yz-agent
	@if [ ! -f /etc/yz-agent/config.yml ]; then \
		sudo cp config.yml.example /etc/yz-agent/config.yml; \
		echo "Config copied to /etc/yz-agent/config.yml - please edit it"; \
	fi
