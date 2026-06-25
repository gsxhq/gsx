# gsx developer tasks. Use tabs for recipe indentation.
.PHONY: test cover cover-html examples

test:
	go test ./... -count=1

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
