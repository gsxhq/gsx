# playground/server/Dockerfile — build context is the gsx repo root.
FROM golang:1.26-bookworm

WORKDIR /gsx
# Copy the whole module so the playground's replace => /gsx resolves.
COPY . /gsx

ENV GOCACHE=/gocache

# Build the server.
RUN cd /gsx/playground/server && go build -o /usr/local/bin/gsxplayground .

# Pre-warm: build gsx + the prepared module so /gocache is populated in the image
# layer. This script runs the pool setup once and exits.
RUN cd /gsx/playground/server && /usr/local/bin/gsxplayground -prewarm -gsxmod /gsx -work /work

EXPOSE 8080
ENV ALLOWED_ORIGIN=https://gsxhq.github.io
ENTRYPOINT ["/usr/local/bin/gsxplayground", "-gsxmod", "/gsx", "-work", "/work"]
