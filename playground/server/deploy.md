# Deploying the gsx playground to Fly.io

The render service runs at `https://gsx-playground.fly.dev` as the Fly app
`gsx-playground` (org `personal`, region `lhr`). Config: `fly.toml` in this
directory. Build context is the **repo root** — the Dockerfile does
`COPY . /gsx` so visitor code compiles against the live gsx module.

## Security (read before changing the sandbox)

The service compiles and runs **visitor-supplied Go code** with the real Go
toolchain. Defence in depth:

1. **Source-level import allowlist** (`checkGeneratedImports`, `allowedImports`).
   Every submitted file is parsed and any import outside the curated,
   capability-free set is rejected before a build starts. `net`, `os`, `os/exec`,
   `syscall`, `unsafe` and cgo are blocked, which removes the network, exec and
   filesystem vectors. Only a single `.gsx` input is accepted, so no assembly and
   no `//go:linkname`.
2. **Firecracker microVM.** Each Fly Machine is a hardware-isolated VM, not a
   shared kernel. There is no cloud metadata endpoint holding credentials.
3. **Bounded builds.** `GOPROXY=off`, `CGO_ENABLED=0`, a 25s hard timeout per
   build and a two-workspace pool.

Residual risk: outbound network from the VM is open, and the org's private
network (`*.internal`, currently only the static `gsxui-site`) is reachable.
Both are closed by the allowlist rather than at the network layer. Keep the
allowlist the single boundary; do not add packages that expose I/O.

The `-prewarm` build step and `go test` run the same pipeline **on the host
with no sandbox** — they must only ever see trusted inputs.

## One-time setup (done)

```bash
flyctl apps create gsx-playground --org personal
flyctl tokens create deploy -a gsx-playground -n "github-actions gsxhq/gsx" -x 87600h
# → stored as the FLY_API_TOKEN Actions secret in gsxhq/gsx
```

The token is scoped to this one app. Rotate by re-running both lines.

## Deploy

CI (`.github/workflows/deploy-playground-server.yml`) deploys every push to
`main` that can reach the compiler (see its `paths-ignore`), then smoke-tests
`/render`. To deploy by hand, from the **repo root**:

```bash
flyctl deploy -c playground/server/fly.toml --local-only --ha=false .
```

`--local-only` builds with your Docker and pushes to `registry.fly.io`; it needs
`flyctl auth login`. `--ha=false` keeps one machine (the default adds a spare).

## Shape and cost

- `shared-cpu-1x`, 1GB, one machine, pool of two workspaces (`Dockerfile` CMD).
  One build takes ~0.8s; concurrent builds serialise on the shared vCPU.
- `auto_stop_machines = "suspend"`: idle machines are snapshotted, so the warm
  pool survives and the first request after idle resumes in well under a
  second. A cold boot (stop fallback, or after a deploy) is ~3s.
- Billing is per running second plus ~$0.15/GB-month for the stopped rootfs.
  At the playground's traffic this is cents per month, inside the Legacy Hobby
  plan's $6/month machine allowance. A machine that never suspends would cost
  ~$6/month; check `fly dashboard` → Billing if traffic grows.

## Frontend wiring

The site build reads the API base URL from the `GSX_PLAYGROUND_API` Actions
**variable** in `gsxhq/gsxhq.github.io` (`VITE_GSX_PLAYGROUND_API` at build
time). It is set to `https://gsx-playground.fly.dev`. Changing it requires a
site redeploy (`gh workflow run deploy.yml -R gsxhq/gsxhq.github.io`).

## Operations

```bash
flyctl logs -a gsx-playground              # stream logs
flyctl status -a gsx-playground            # machine state (started/suspended/stopped)
flyctl machine list -a gsx-playground
flyctl scale show -a gsx-playground
```

Each deploy pushes a new image to `registry.fly.io/gsx-playground`; only the
one attached to the machine counts towards rootfs storage.
