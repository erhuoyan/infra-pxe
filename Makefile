.PHONY: help build build-linux pack clean

VERSION ?= $(shell date +%Y%m%d)

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-16s\033[0m %s\n", $$1, $$2}'

build: ## Build for current OS/arch
	go build -ldflags="-s -w" -o bin/infra-pxe .

build-linux: ## Cross-compile for linux amd64 + arm64
	GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/infra-pxe-linux-amd64 .
	GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o bin/infra-pxe-linux-arm64 .

pack: ## Build + package release tarballs
	bash release/pack.sh $(VERSION)

clean: ## Clean build artifacts
	rm -rf bin/ release/dist/ release/bin/
