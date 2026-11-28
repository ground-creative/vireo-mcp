# Makefile — the handful of commands a contributor actually runs.
#
#   make build     compile ./vireo-mcp
#   make test      the whole test suite, with the race detector
#   make check     format, vet, test and build — what CI runs
#   make release   cross-platform archives + checksums in ./dist
#   make clean     remove build output
#
# Nothing here needs CGO: the bridge is pure Go, so every target is a
# cross-compile rather than a native build.

BINARY  := vireo-mcp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILT   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

# -s -w strip symbol and DWARF tables; the version metadata is the only thing a
# release stamp needs to add.
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.buildCommit=$(COMMIT) \
	-X main.buildTime=$(BUILT)

.PHONY: build test check fmt vet release clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

# -race only works where cgo is available, which is every developer machine and
# the CI runner; the shipped binary never uses it.
test:
	go test -count=1 -race ./...

fmt:
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi

vet:
	go vet ./...

check: fmt vet test build

release:
	./scripts/release.sh "$(VERSION)" dist

clean:
	rm -f $(BINARY)
	rm -rf dist
