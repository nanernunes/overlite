.PHONY: test build run tidy

# Pretty test output via gotestfmt (registered as a go tool dependency).
test:
	@set -euo pipefail; go test -json -v ./... 2>&1 | go tool gotestfmt

build:
	go build -o bin/overlite ./cmd/overlite

run: build
	./bin/overlite

tidy:
	go mod tidy
