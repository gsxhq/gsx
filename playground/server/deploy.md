# Deploying the gsx playground to Cloud Run (free tier)

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
IMAGE="gcr.io/$PROJECT/$SERVICE"

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

After code changes, repeat steps 1 and 2 above.  Cloud Build tags overwrite the
previous image and Cloud Run performs a zero-downtime rollout.
