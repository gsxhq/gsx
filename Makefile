# gsx developer tasks. Use tabs for recipe indentation.
.PHONY: test check cover cover-html examples ci ci-gomod ci-playground ci-examples ci-format

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
# The three remaining lanes are independent, so `make -j3` runs them in parallel
# — the long pole is `ci-gomod` (the gen/ e2e suite), under which the ~7s
# playground build+test and the ~1s format check overlap for free.
ci:
	$(MAKE) ci-examples
	$(MAKE) -j3 ci-gomod ci-playground ci-format

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
ci-examples:
	$(MAKE) examples
	@if ! git diff --exit-code -- docs/guide/examples.md docs/examples.json playground/server/examples.json; then \
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
	go test -coverpkg=./... -coverprofile=cover.out ./... -count=1
	go tool cover -func=cover.out | tail -1

cover-html: cover
	go tool cover -html=cover.out

examples:
	go run ./cmd/gsx-examples
