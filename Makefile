.PHONY: build test test-race vet fmt fmt-check check demo deb
build:
	mkdir -p bin
	go build -o bin/streamchat ./cmd/streamchat
test:
	go test ./...
test-race:
	go test -race ./...
vet:
	go vet ./...
fmt:
	gofmt -w .
fmt-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
check: fmt-check vet test test-race build
demo: build
	./bin/streamchat demo --no-color --timestamps
deb:
	./scripts/build-deb.sh
