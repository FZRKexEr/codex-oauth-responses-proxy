APP_NAME := oauth-responses-proxy
BIN_DIR := bin
BIN := $(BIN_DIR)/$(APP_NAME)
ROOT_DIR := $(CURDIR)
TOKEN_FILE := $(ROOT_DIR)/.oauth_tokens.json

.PHONY: build run run-debug clean fmt check

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN) .

run:
	OPENAI_OAUTH_TOKEN_FILE=$(TOKEN_FILE) go run .

run-debug:
	DEBUG_REQUEST_BODY=true OPENAI_OAUTH_TOKEN_FILE=$(TOKEN_FILE) go run .

fmt:
	gofmt -w main.go $$(find internal -name '*.go' -type f | sort)

check:
	go build ./...

clean:
	rm -rf $(BIN_DIR)
