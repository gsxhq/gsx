# gsx developer tasks. Use tabs for recipe indentation.
.PHONY: test check lint cover cover-html examples ci ci-gomod ci-playground ci-examples ci-format reload-probe test-go127rc1

# COUNT is the go-test cache control. -count=1 disables the test cache so every
# run re-executes — the authoritative behaviour `ci` uses to mirror GitHub CI.
# `make check` overrides it to empty, letting the cache skip unchanged packages.
COUNT ?= -count=1

# PARALLEL caps the per-package t.Parallel concurrency. The default (GOMAXPROCS,
# 32 on this machine) is actively counterproductive here: `gen` and
# internal/codegen spend most of their time inside packages.Load, and each
# `go list` internally saturates every core. Running 32 of them at once thrashes
# — measured on `go test ./... -count=1`:
#
#   -parallel 32 (default) : 292s wall, 1443s sys
#   -parallel 8            : 259s wall,  808s sys
#   -parallel 4            : 232s wall,  636s sys
#
# A test that takes 1.46s alone takes 52.4s under the default fan-out (36x).
PARALLEL ?= -parallel 4

test:
	go test ./... -count=1 $(PARALLEL)

# Mirrors .github/workflows/ci.yml (minus the VitePress docs build, which clones
# the external site repo). Run before merging to main; this is the authoritative,
# uncached run (-count=1). For the inner dev loop use `make check` instead.
#
# Examples are regenerated FIRST, serially: the playground module embeds
# examples.json (`//go:embed` in playground/server/presets.go), so its build
# must not race the regeneration. The drift check reads the just-written files.
# The three remaining lanes are independent, so `make -j3` runs them in parallel
# — the long pole is `ci-gomod` (the gen/ e2e suite), under which the ~7s
# playground build+test and the ~1s format check overlap for free.
ci:
	$(MAKE) ci-examples
	$(MAKE) -j3 ci-gomod ci-playground ci-format

# Fast inner-loop check: the SAME checks as `ci` PLUS `lint` (which `ci` omits —
# it's a separate GitHub job), so the golangci-lint failures that only surface in
# CI are caught here first. Lets the Go test cache serve unchanged packages (drops
# -count=1), so a repeat run after editing one package only re-tests that package
# and its dependents. The cache is content-keyed over each test binary's import
# closure, so your edits always re-run the tests they affect — no stale-pass risk
# for code you are changing. GitHub CI's -count=1 run (and `make ci`) remain the
# authoritative gate.
check:
	$(MAKE) ci COUNT=
	$(MAKE) lint

lint:
	golangci-lint run ./...
	cd playground/server && golangci-lint run ./...

# Manual, repeatable local test of `gsx dev`'s browser-reload behavior (NOT in
# `ci` — it spawns a live dev loop). Asserts that introducing a .gsx/main.go
# error posts the overlay and fixing it posts a reload. Pass FRESH=--fresh to
# re-scaffold the throwaway app. See dev/reload-probe/README.md.
reload-probe:
	bash dev/reload-probe/run.sh $(FRESH)

# Root module: build, vet, test. The long pole by a wide margin — `gen` (~264s)
# and internal/codegen (~260s) run concurrently and ARE the suite; every other
# package finishes in under a second. Both are dominated by packages.Load: the
# 85-package / 205k-line gsx runtime closure is re-parsed and re-type-checked
# once per test Module (803 loads across the two packages). See CLAUDE.md
# "Test performance" before adding tests that open a codegen.Module.
ci-gomod:
	go build ./...
	go vet ./...
	go test ./... $(COUNT) $(PARALLEL)

# playground/server is a separate Go module.
ci-playground:
	cd playground/server && go build ./... && go test ./... $(COUNT)

# Regenerate the example artifacts and fail if they drift from what's committed
# (the generator is the source of truth). Run before the parallel lanes in `ci`:
# the playground module embeds examples.json, so its build must not race the regen.
# Note: docs/guide/examples.md is intentionally omitted from the drift check —
# the flat gallery page is retired; all examples are routed into the Syntax pages.
ci-examples:
	$(MAKE) examples
	@if ! git diff --exit-code -- docs/examples.json playground/server/examples.json docs/guide/syntax/_generated; then \
		echo "examples artifacts are stale — run 'make examples' and commit the result"; \
		exit 1; \
	fi

# gofmt + gsx fmt must stay clean (see the format gate note in ci.yml).
ci-format:
	@files=$$(gofmt -l $$(git ls-files '*.go' | grep -v /testdata/)); \
	if [ -n "$$files" ]; then echo "these Go files need gofmt:"; echo "$$files"; exit 1; fi
	go run ./cmd/gsx fmt -l .

# Honest cross-package coverage: -coverpkg attributes the corpus's in-process
# codegen execution (run via internal/corpus) to internal/codegen, which a plain
# per-package -cover does not. Prints the total at the end.
cover:
	go test -coverpkg=./... -coverprofile=cover.out ./... -count=1 $(PARALLEL)
	go tool cover -func=cover.out | tail -1

cover-html: cover
	go tool cover -html=cover.out

examples:
	go run ./cmd/gsx-examples

# Runs the go1.27-gated generic-methods tests under the Go 1.27 RC1 toolchain, with
# skip promoted to FAILURE (GSX_REQUIRE_GENERIC_METHODS=1) so the lane can
# never green-light while silently testing nothing. Requires go1.27rc1:
#   go install golang.org/dl/go1.27rc1@latest && go1.27rc1 download
test-go127rc1:
	@command -v go1.27rc1 >/dev/null 2>&1 || { \
		echo "go1.27rc1 not found — install with:"; \
		echo "  go install golang.org/dl/go1.27rc1@latest && go1.27rc1 download"; \
		exit 1; }
	GOTOOLCHAIN=local GSX_REQUIRE_GENERIC_METHODS=1 go1.27rc1 test ./internal/codegen -run 'Go127|GenericMethod' -count=1 -v
