.PHONY: help build build-linux pack iso clean

VERSION ?= $(shell date +%Y%m%d)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build for current OS/arch
	go build -ldflags="-s -w" -o bin/infra-pxe .

build-linux: ## Cross-compile for linux amd64 + arm64
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/infra-pxe-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/infra-pxe-linux-arm64 .

pack: ## Build + package release tarballs
	bash release/pack.sh $(VERSION)

iso: build-linux ## Build Alpine appliance ISO (one command, auto-builds builder)
	@if ! docker image inspect infra-pxe-mkimage-builder:3.24 >/dev/null 2>&1; then \
		echo "── Builder image not found, building (one-time, ~5 min)..."; \
		bash alpine/mkimage/build-builder.sh; \
	fi
	bash alpine/mkimage/build-iso.sh x86_64
	bash alpine/mkimage/build-iso.sh aarch64

iso-x86: build-linux ## Build x86_64 ISO
	bash alpine/mkimage/build-iso.sh x86_64

iso-arm: build-linux ## Build ARM64 ISO
	bash alpine/mkimage/build-iso.sh aarch64

clean: ## Clean build artifacts
	rm -rf bin/ release/dist/ release/bin/
