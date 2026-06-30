# gsx developer tasks. Use tabs for recipe indentation.
.PHONY: test check cover cover-html examples ci ci-gomod ci-playground ci-examples ci-format ci-tailwind-example ci-tailwind-example-drift

# COUNT is the go-test cache control. -count=1 disables the test cache so every
# run re-executes — the authoritative behaviour `ci` uses to mirror GitHub CI.
# `make check` overrides it to empty, letting the cache skip unchanged packages.
COUNT ?= -count=1

test:
	go test ./... -count=1

# Mirrors .github/workflows/ci.yml (minus the VitePress docs build, which clones
# the external site repo). Run before merging to main; this is the authoritative,
# uncached run (-count=1). For the inner dev loop use `make check` instead.
#
# Examples are regenerated FIRST, serially: the playground module embeds
# examples.json (`//go:embed` in playground/server/presets.go), so its build
# must not race the regeneration. The drift check reads the just-written files.
# The four remaining lanes are independent, so `make -j4` runs them in parallel
# — the long pole is `ci-gomod` (the gen/ e2e suite), under which the ~7s
# playground build+test, the tailwind example, and the ~1s format check overlap for free.
ci:
	$(MAKE) ci-examples
	$(MAKE) ci-tailwind-example-drift
	$(MAKE) -j4 ci-gomod ci-playground ci-tailwind-example ci-format

# Fast inner-loop check: the SAME checks as `ci`, but lets the Go test cache
# serve unchanged packages (drops -count=1), so a repeat run after editing one
# package only re-tests that package and its dependents. The cache is content-
# keyed over each test binary's import closure, so your edits always re-run the
# tests they affect — there is no stale-pass risk for code you are changing.
# GitHub CI's -count=1 run (and `make ci`) remain the authoritative gate.
check:
	$(MAKE) ci COUNT=

# Root module: build, vet, test. The long pole (~50s of in-process e2e tests
# in gen/, which spawn the Go toolchain per case).
ci-gomod:
	go build ./...
	go vet ./...
	go test ./... $(COUNT)

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

# examples/tailwind-merge is a separate Go module wiring tailwind-merge-go via class_merger.
ci-tailwind-example:
	cd examples/tailwind-merge && go build ./... && go test ./... $(COUNT)

# Regenerate the tailwind-merge example's generated output and fail if it drifts.
ci-tailwind-example-drift:
	go run ./cmd/gsx -C examples/tailwind-merge generate ./views
	@if ! git diff --exit-code -- examples/tailwind-merge/views/card.x.go; then \
		echo "examples/tailwind-merge/views/card.x.go is stale — run 'go run ./cmd/gsx -C examples/tailwind-merge generate ./views' and commit the result"; \
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
	go test -coverpkg=./... -coverprofile=cover.out ./... -count=1
	go tool cover -func=cover.out | tail -1

cover-html: cover
	go tool cover -html=cover.out

examples:
	go run ./cmd/gsx-examples
