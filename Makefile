.PHONY: build gui test test-race vet fmt fmt-check check demo deb
VERSION ?= $(shell ./scripts/version.sh)
VERSION_LDFLAGS = -X main.version=$(VERSION)

build:
	mkdir -p bin
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/streamchat ./cmd/streamchat

gui:
	mkdir -p bin build/desktop
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/streamchat-core ./cmd/streamchat
	go build -ldflags "$(VERSION_LDFLAGS)" -o bin/streamchat-gui-runtime ./cmd/streamchat-gui
	cmake -S desktop -B build/desktop -DCMAKE_BUILD_TYPE=Release
	cmake --build build/desktop --parallel 2
	cp build/desktop/streamchat-gui bin/streamchat-gui
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
