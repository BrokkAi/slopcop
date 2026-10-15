BINARY := slopcop
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
EXT := $(if $(filter windows,$(GOOS)),.exe,)
LDFLAGS := -X main.version=$(VERSION)

.PHONY: all build install test test-short vet fmt fmt-check cross-check release-check clean

all: build

build:
	CGO_ENABLED=0 GOOS=$(GOOS) GOARCH=$(GOARCH) go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-$(GOOS)-$(GOARCH)$(EXT) ./cmd/slopcop

install:
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/slopcop

test:
	go test ./...

test-short:
	go test -short ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

fmt-check:
	@files="$$(gofmt -l .)"; if [ -n "$$files" ]; then \
		echo "gofmt needed:"; echo "$$files"; exit 1; \
	fi

# Every release target is a real build input, so a cross-build failure is a
# release failure.
cross-check:
	$(MAKE) build GOOS=linux GOARCH=amd64
	$(MAKE) build GOOS=linux GOARCH=arm64
	$(MAKE) build GOOS=darwin GOARCH=amd64
	$(MAKE) build GOOS=darwin GOARCH=arm64
	$(MAKE) build GOOS=windows GOARCH=amd64
	$(MAKE) build GOOS=windows GOARCH=arm64

# Validates the exact release config CI uses. Requires goreleaser on PATH.
release-check:
	goreleaser check
	goreleaser build --snapshot --clean

clean:
	rm -rf bin dist
