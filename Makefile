BINARY := slopcop
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
EXT := $(if $(filter windows,$(GOOS)),.exe,)
LDFLAGS := -X main.version=$(VERSION)

# The npm package is packed from the archives of a published release, so the
# packer takes that release's tag and the directory its archives were
# downloaded into.
NPM_TAG ?= $(if $(RELEASE_TAG),$(RELEASE_TAG),$(shell git describe --tags --exact-match 2>/dev/null))
NPM_ASSETS ?= release-assets
NPM_OUTPUT ?= npm-dist
NPM_SMOKE ?= npm-smoke

# The PyPI wheels are packed from the binaries of a release: the release
# workflow points the packer at GoReleaser's dist/ and at the tag, while the
# default packs what `make cross-check` left in bin/, under a version that
# cannot be mistaken for a release.
PYPI_FLAGS ?= --bin-dir bin
PYPI_VERSION ?= 0.0.0.dev0
PYPI_OUTPUT ?= pypi-dist

.PHONY: all build install test test-short vet fmt fmt-check cross-check release-check npm-test npm-pack npm-smoke npm-publish wheels clean

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

# The launcher and the packer are Node programs, so their tests run on Node.
npm-test:
	node --test "npm/test/*.test.*"

# Packs the tarball from the release archives, verifying every archive against
# the release's checksums.txt before it is staged.
npm-pack:
	node npm/scripts/release.mjs pack --release-tag "$(NPM_TAG)" --assets "$(NPM_ASSETS)" --output "$(NPM_OUTPUT)"

# Installs the packed tarball the way a user would and runs it, so a payload
# that cannot start fails here instead of in someone's terminal.
npm-smoke:
	rm -rf "$(NPM_SMOKE)"
	npm install --prefix "$(NPM_SMOKE)" --ignore-scripts --no-audit --no-fund "./$(NPM_OUTPUT)"/brokkai-slopcop-*.tgz
	@binary="$(NPM_SMOKE)/node_modules/.bin/slopcop"; \
	reported="$$($$binary version)"; \
	case "$$reported" in \
		"slopcop version $(NPM_TAG)"|"slopcop version $(patsubst v%,%,$(NPM_TAG))") echo "$$reported";; \
		*) echo "the packed binary reports '$$reported', expected the $(NPM_TAG) release" >&2; exit 1;; \
	esac
	"$(NPM_SMOKE)/node_modules/.bin/slopcop" --help > /dev/null

# Publishes with the short-lived credentials GitHub OIDC issues to this
# workflow; there is no npm token. See docs/npm-publishing.md.
npm-publish:
	node npm/scripts/release.mjs publish --release-tag "$(NPM_TAG)" --assets "$(NPM_ASSETS)" --output "$(NPM_OUTPUT)"

# Packs one wheel per release target, each carrying the binary for its
# platform, plus a musllinux wheel for each Linux binary. See
# docs/pypi-publishing.md.
wheels:
	python3 packaging/pypi/build_wheels.py --version "$(PYPI_VERSION)" $(PYPI_FLAGS) --out "$(PYPI_OUTPUT)"

clean:
	rm -rf bin dist "$(NPM_OUTPUT)" "$(NPM_SMOKE)" "$(NPM_ASSETS)" "$(PYPI_OUTPUT)"
