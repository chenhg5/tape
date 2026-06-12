# tape - quality gates, cross-platform builds and release workflow.
# `make ci` runs everything CI runs; any failure blocks the merge.
# `make dist` cross-compiles release artifacts for all supported platforms.

GO          ?= go
BIN         := tape
COVER_MIN   := 60
COVDIR      := $(CURDIR)/.cover
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
DIST        := dist/release

# GOOS/GOARCH pairs shipped as release artifacts and npm platform packages
PLATFORMS   := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: build dist fmt vet test smoke e2e cover cover-e2e ci clean \
        npm-dry npm-beta npm-release

build:
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/tape

## cross-compile every platform into dist/release/ with checksums
dist:
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@for platform in $(PLATFORMS); do \
		goos=$${platform%/*}; goarch=$${platform#*/}; \
		bin=tape; [ "$$goos" = "windows" ] && bin=tape.exe; \
		out="$(DIST)/tape-$(VERSION)-$$goos-$$goarch"; \
		echo "building $$out"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch \
			$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o "$$out/$$bin" ./cmd/tape || exit 1; \
		cp README.md LICENSE "$$out/"; \
		if [ "$$goos" = "windows" ] && command -v zip >/dev/null; then \
			(cd $(DIST) && zip -qr "$$(basename $$out).zip" "$$(basename $$out)") || exit 1; \
		else \
			tar -czf "$$out.tar.gz" -C $(DIST) "$$(basename $$out)" || exit 1; \
		fi; \
		rm -rf "$$out"; \
	done
	@cd $(DIST) && sha256sum * > SHA256SUMS
	@echo && ls -lh $(DIST)

## npm publishing (wraps scripts/release-npm.sh; see that file for details)
npm-dry:
	NPM_DRY_RUN=1 ./scripts/release-npm.sh $(VERSION) beta

npm-beta:
	./scripts/release-npm.sh $(VERSION) beta

npm-release:
	./scripts/release-npm.sh $(VERSION) latest

## gate 1: formatting
fmt:
	@out=$$(gofmt -l .); \
	if [ -n "$$out" ]; then \
		echo "gofmt needed on:"; echo "$$out"; exit 1; \
	fi

## gate 2: static analysis
vet:
	$(GO) vet ./...

## gate 3: unit + integration tests with race detector
test:
	$(GO) test -race ./internal/...

## gate 4: unit coverage threshold
cover:
	@$(GO) test -coverprofile=coverage.out ./internal/... > /dev/null
	@total=$$($(GO) tool cover -func=coverage.out | awk '/^total:/ {sub(/%/,"",$$3); print $$3}'); \
	echo "unit coverage: $$total% (minimum $(COVER_MIN)%)"; \
	awk "BEGIN {exit !($$total >= $(COVER_MIN))}" || \
		{ echo "coverage gate failed"; exit 1; }

## gate 5: smoke tests (fast subset of e2e against the real binary)
smoke:
	$(GO) test -short -count=1 ./test/e2e/

## gate 6: full end-to-end suite against the real binary
e2e:
	$(GO) test -count=1 ./test/e2e/

## e2e coverage report (binary built with -cover, counters merged)
cover-e2e:
	@rm -rf $(COVDIR) && mkdir -p $(COVDIR)
	@E2E_COVER=1 GOCOVERDIR=$(COVDIR) $(GO) test -count=1 ./test/e2e/ > /dev/null
	@echo "e2e binary coverage:"
	@$(GO) tool covdata percent -i=$(COVDIR)

ci: fmt vet test cover smoke e2e
	@echo "all gates passed"

clean:
	rm -rf $(BIN) coverage.out $(COVDIR) dist
