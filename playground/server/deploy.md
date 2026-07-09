# Deploying the gsx playground to Cloud Run (free tier)

## Security (READ BEFORE public deploy)

The render service compiles and runs **visitor-supplied Go code** using the real
Go toolchain. gVisor sandboxes kernel syscalls inside Cloud Run, but **gVisor
does not block outbound network connections**. A visitor can therefore use
`net`, `net/http`, `os/exec`, etc. to make arbitrary outbound requests from
inside the container, including reaching the **GCP metadata endpoint at
`169.254.169.254`** to exfiltrate instance identity tokens.

### Free-tier mitigation package (recommended)

Two mitigations are already implemented or easy to apply and together make a
public free-tier deploy acceptable for a docs playground:

**a. Source-level import allowlist (implemented).** Before the visitor's
component is built or run, the service parses the generated `.x.go` file with
`go/parser` and rejects any import not in a curated capability-free set. The
allowlist covers exactly the packages that gsx-emitted code uses
(`context`, `io`, `strconv`, `fmt`, `gsx/std`, etc.). Blocked packages include
`net`, `os`, `os/exec`, `syscall`, `unsafe`, and cgo — this removes the
network, exec, and file-system vectors without requiring a VPC connector.
`//go:linkname` and cgo are also closed because `unsafe` is denied and
only a single `.gsx` input file is accepted (no assembly).

**b. Dedicated zero-permission service account.** Create a Cloud Run SA with
**no IAM roles**. A metadata-endpoint read from inside the container then
returns a useless token — the credential exfiltration target is neutralised.

```bash
# Maintainer inputs — set these before running
PROJECT=<your-gcp-project-id>
REGION=us-central1
SERVICE=gsx-playground
SA=gsx-playground-sandbox

# Create the service account with no permissions granted.
gcloud iam service-accounts create "$SA" --project "$PROJECT" \
  --display-name "gsx playground (no permissions)"

# Deploy under the zero-permission SA.
gcloud run deploy "$SERVICE" --project "$PROJECT" --region "$REGION" \
  --image "$REGION-docker.pkg.dev/$PROJECT/gsx/$SERVICE" \
  --service-account "${SA}@${PROJECT}.iam.gserviceaccount.com" \
  --allow-unauthenticated \
  --memory 1Gi --cpu 1 --concurrency 4 --timeout 30 \
  --min-instances 0 --max-instances 3 \
  --set-env-vars ALLOWED_ORIGIN=https://gsxhq.github.io
```

With (a) the import allowlist, (b) the zero-permission SA, and (c) Cloud Run's
gVisor sandbox plus the request limits above, a public free-tier deploy is
acceptable. The known trade-off: there is no hard namespace-level network-off
(unlike Go's Playground, which runs on gVisor hosts it fully controls), so
outbound network is blocked by policy rather than by the kernel. This is
documented here as the residual risk.

### Hard no-network alternative

For a guarantee at the network layer rather than the policy layer, attach a
[Serverless VPC Access connector](https://cloud.google.com/vpc/docs/configure-serverless-vpc-access)
and deploy with `--vpc-egress=all-traffic` routed through a VPC that has no
default internet route and no path to `169.254.169.254`. This fully closes the
SSRF surface. Alternatively, use GKE Sandbox (gVisor on a cluster you control).
**Note: a VPC connector incurs cost beyond the always-free tier.**

### Local / CI pipeline note

The `-prewarm` build step and any `go test` runs execute the **same compilation
pipeline on the host**, with **no gVisor sandbox**. These paths must only ever
process **trusted inputs** (your own code, CI fixtures). Never pipe untrusted
user input through a host-side `go build`/`go test` invocation.

## Prerequisites

- `gcloud` CLI authenticated (`gcloud auth login`)
- A GCP project with billing enabled
- Cloud Run and Cloud Build APIs enabled:
  ```bash
  gcloud services enable run.googleapis.com cloudbuild.googleapis.com \
    --project "$PROJECT"
  ```

## Why `cloudbuild.yaml`?

The playground's Dockerfile lives at `playground/server/Dockerfile` but must be
built from the **repo root** as the build context (the Go module is at the root,
and the Dockerfile copies the whole tree with `COPY . /gsx`).

`gcloud builds submit --tag` always looks for `Dockerfile` in the source root
and has no `-f` flag equivalent, so a `cloudbuild.yaml` at the repo root is the
cleanest solution.  It lives at `cloudbuild.yaml` and passes
`-f playground/server/Dockerfile` explicitly.

## Deploy steps

Run these from the **gsx repo root**:

```bash
PROJECT=<your-gcp-project-id>
REGION=us-central1           # free-tier eligible
SERVICE=gsx-playground
REPO=gsx                     # Artifact Registry repo (see "Image retention" below)
IMAGE="$REGION-docker.pkg.dev/$PROJECT/$REPO/$SERVICE"

# 1. Build the container image with Cloud Build.
gcloud builds submit --config cloudbuild.yaml \
  --substitutions "_IMAGE=$IMAGE" \
  --project "$PROJECT" \
  .

# 2. Deploy to Cloud Run (scale-to-zero keeps it on the free tier).
gcloud run deploy "$SERVICE" \
  --project "$PROJECT" \
  --region "$REGION" \
  --image "$IMAGE" \
  --allow-unauthenticated \
  --memory 1Gi \
  --cpu 1 \
  --concurrency 4 \
  --timeout 30 \
  --min-instances 0 \
  --max-instances 3 \
  --set-env-vars ALLOWED_ORIGIN=https://gsxhq.github.io
```

`--min-instances 0` enables scale-to-zero (free tier).  Cold starts after idle
periods take a few seconds; warm requests are sub-second.

## After deploy: wire the frontend

The deploy command prints the service URL, e.g.:
```
Service URL: https://gsx-playground-<hash>-uc.a.run.app
```

Set this as a GitHub Actions **repository variable** (not a secret) in the site
repo (`gsxhq/gsxhq.github.io`):

1. Go to **Settings → Secrets and variables → Actions → Variables**
2. Add a variable named `GSX_PLAYGROUND_API` with the Cloud Run URL as its value

The site's `deploy.yml` workflow passes `VITE_GSX_PLAYGROUND_API` to the
VitePress build step via that variable.  Until the variable is set, the
playground frontend falls back to `http://localhost:8088` and shows the
"API not reachable" message on the deployed site — this is acceptable until
the variable is wired.

## Monitoring & logs

```bash
# Stream live logs
gcloud run services logs read "$SERVICE" --region "$REGION" --project "$PROJECT"

# Check service status
gcloud run services describe "$SERVICE" --region "$REGION" --project "$PROJECT"
```

## Re-deploying

After code changes, repeat steps 1 and 2 above.  Cloud Run performs a
zero-downtime rollout.

CI (`.github/workflows/deploy-playground-server.yml`) tags each image with the
commit sha, so images do **not** overwrite one another — every deploy adds one.
See "Image retention" below.

## Image retention

Each deploy pushes a new `:$GITHUB_SHA` image (~100 MB of unique layers). Left
alone, Artifact Registry grows without bound. Two things hold it down:

1. **A cleanup policy on the `gsx` repo** (`cleanup-policy.json`). It keeps the
   5 most recent versions unconditionally and deletes anything older than a day.
   `Keep` rules take precedence over `Delete`, so the serving image can never be
   pruned — even if nobody deploys for a month. The cost is that traffic can only
   be rolled back about five deploys.

   Apply or update it with:

   ```bash
   gcloud artifacts repositories set-cleanup-policies gsx \
     --location=us-central1 \
     --policy=playground/server/cleanup-policy.json \
     --no-dry-run
   ```

   Swap `--no-dry-run` for `--dry-run` to have Artifact Registry log what it
   *would* delete without deleting it. Policies run asynchronously (roughly
   daily), so they do not reclaim space the instant they are applied.

2. **A `paths-ignore` filter on the deploy workflow.** The Dockerfile does
   `COPY . /gsx` because the playground compiles visitor code against the live
   gsx module — so a parser or codegen change genuinely does need a redeploy, and
   only inert paths (`docs/`, `skills/`, `*.md`) may be ignored. Never narrow
   this to `playground/**`; that would leave the playground running a stale
   compiler.
