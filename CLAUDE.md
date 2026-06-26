# gsx

Two Go modules: root (`.`) and `playground/server`.

## Before every commit to main / merge

Run `make ci` and make sure it's green. It mirrors `.github/workflows/ci.yml`:

- `go build ./...`, `go vet ./...`, `go test ./...` (root + `playground/server`)
- examples drift: `make examples` must leave `docs/guide/examples.md`,
  `docs/examples.json`, `playground/server/examples.json` unchanged — commit them if not
- format gates (required): `gofmt -l` clean, `gsx fmt -l .` clean

The CI `docs` job (VitePress build) isn't in `make ci` — it clones the external
`gsxhq/gsxhq.github.io` repo. It only needs checking when editing `docs/guide/**`.

Pin Go to the version in `ci.yml` (`GO_VERSION`) — a different minor re-introduces gofmt drift.
