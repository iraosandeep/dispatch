BINARY  := dispatch
PKG     := ./cmd/dispatch
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: all build install test race cover vet fmt fmt-check lint cross checksums snapshot clean

all: fmt-check vet test build

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) $(PKG)

install:
	CGO_ENABLED=0 go install -trimpath -ldflags "$(LDFLAGS)" $(PKG)

test:
	go test ./...

race:
	go test -race ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vet:
	go vet ./...

fmt:
	gofmt -w .

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

lint: fmt-check vet

# Cross-compile release binaries into dist/ without GoReleaser.
cross: clean
	@mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; ext=""; [ $$os = windows ] && ext=".exe"; \
		out=dist/$(BINARY)_$(VERSION)_$${os}_$${arch}$$ext; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$out $(PKG) || exit 1; \
	done
	@$(MAKE) --no-print-directory checksums

checksums:
	@cd dist && shasum -a 256 $(BINARY)_* > checksums.txt && cat checksums.txt

# Local dry run of the GitHub release pipeline.
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin dist coverage.out
