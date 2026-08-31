.PHONY: test build run fmt vet check clean tidy

# `make test` needs pipefail, which POSIX sh lacks — /bin/sh is dash on Ubuntu,
# where the recipe would die on "Illegal option -o pipefail".
SHELL := /bin/bash

# The release workflow exports GOOS to cross-compile; locally it falls back to
# whatever this machine is.
GOOS ?= $(shell go env GOOS)
VERSION ?= dev

# Windows will not execute a binary without the extension, and `go build -o`
# writes exactly the name it is handed — it adds no suffix of its own.
BIN := bin/overlite$(if $(filter windows,$(GOOS)),.exe)

# Pretty test output via gotestfmt (registered as a go tool dependency).
test:
	@set -euo pipefail; go test -json -v ./... 2>&1 | go tool gotestfmt

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BIN) ./cmd/overlite

run: build
	./$(BIN)

fmt:
	gofmt -w .

vet:
	go vet ./...

# What ci.yml runs, so a failure there can be reproduced with one command.
check: vet test
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

clean:
	rm -rf bin

tidy:
	go mod tidy
