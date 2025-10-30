APP_NAME=linkpeek
PKG=./cmd/linkpeek
BIN_DIR=./bin
GOFILES=$(shell find . -name '*.go' -not -path './vendor/*')

.PHONY: all build run clean test tidy docker-build docker-up docker-down

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

docker-up:
	@docker compose up -d --build

docker-down:
	@docker compose down -v
