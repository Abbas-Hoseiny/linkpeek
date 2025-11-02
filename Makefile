APP_NAME=linkpeek
PKG=./cmd/linkpeek
BIN_DIR=./bin
GOFILES=$(shell find . -name '*.go' -not -path './vendor/*')

# Docker image settings for multi-arch release builds
IMAGE ?= hoseiny/linkpeek
PLATFORMS ?= linux/amd64,linux/arm64
BUILDER ?= linkpeek-builder
TAG ?= $(shell git describe --tags --always --dirty 2>/dev/null)
ifeq ($(strip $(TAG)),)
TAG := latest
endif

.PHONY: all build run clean test tidy docker-build docker-up docker-down docker-builder docker-release

all: build

build:
	@echo "==> Building $(APP_NAME)"
	@mkdir -p $(BIN_DIR)
	@CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BIN_DIR)/$(APP_NAME) $(PKG)

run: build
	@DATA_DIR=./data $(BIN_DIR)/$(APP_NAME)

clean:
	@rm -rf $(BIN_DIR)

test:
	@go test ./...

tidy:
	@go mod tidy

docker-build:
	@docker build -t linkpeek:local .

docker-builder:
	@docker buildx inspect $(BUILDER) >/dev/null 2>&1 || docker buildx create --name $(BUILDER) --use
	@docker buildx use $(BUILDER)
	@docker buildx inspect --bootstrap $(BUILDER) >/dev/null

docker-release: docker-builder
	@echo "==> Building multi-arch image $(IMAGE):$(TAG)"
	@docker buildx build \
		--platform $(PLATFORMS) \
		-t $(IMAGE):$(TAG) \
		-t $(IMAGE):latest \
		--push .

docker-up:
	@docker compose up -d --build

docker-down:
	@docker compose down -v
