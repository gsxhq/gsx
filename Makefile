# gsx developer tasks. Use tabs for recipe indentation.
.PHONY: test cover cover-html examples ci

test:
	go test ./... -count=1

# Mirrors .github/workflows/ci.yml (minus the VitePress docs build, which clones
# the external site repo). Run before every commit to main / merge.
ci:
	go build ./...
	go vet ./...
	go test ./... -count=1
	cd playground/server && go build ./... && go test ./... -count=1
	$(MAKE) examples
	git diff --exit-code -- docs/guide/examples.md docs/examples.json playground/server/examples.json
	test -z "$$(gofmt -l $$(git ls-files '*.go' | grep -v /testdata/))"
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
