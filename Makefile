# tape - quality gates and dev workflow.
# `make ci` runs everything CI runs; any failure blocks the merge.

GO          ?= go
BIN         := tape
COVER_MIN   := 60
COVDIR      := $(CURDIR)/.cover

.PHONY: build fmt vet test smoke e2e cover cover-e2e ci clean

build:
	$(GO) build -o $(BIN) ./cmd/tape

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
	rm -rf $(BIN) coverage.out $(COVDIR)
