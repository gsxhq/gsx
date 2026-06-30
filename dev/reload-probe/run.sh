#!/usr/bin/env bash
#
# Local, repeatable test for `gsx dev`'s browser-reload behavior.
# NOT part of `go test` — run it by hand while iterating on the dev loop:
#
#   bash dev/reload-probe/run.sh            # reuses a cached scaffolded app (fast)
#   bash dev/reload-probe/run.sh --fresh    # re-scaffolds the app from scratch
#
# It runs `gsx dev --no-web` against a scaffolded app, with a tiny recorder
# (recorder.go) standing in for Vite to capture the codegen events + reload pings
# gsx dev POSTs. It then asserts, for BOTH a .gsx and a main.go error:
#   1. introducing the error posts an ok:false event (the overlay), and
#   2. fixing it posts a /__reload (the recovery reload) — the case that
#      regressed for .gsx, where a fixed file regenerates byte-identical .x.go.
#
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$HERE/../.." && pwd)"
WORK="${TMPDIR:-/tmp}/gsx-reload-probe"
APP="$WORK/app"; BIN="$WORK/gsx"; RECBIN="$WORK/recorder"
REC="$WORK/rec.log"; DEV="$WORK/dev.log"
mkdir -p "$WORK"

freeport() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()'; }

DEVPID=""; RECPID=""
cleanup() {
  [ -n "$DEVPID" ] && kill -INT  -"$DEVPID" 2>/dev/null
  sleep 1
  [ -n "$DEVPID" ] && kill -KILL -"$DEVPID" 2>/dev/null
  [ -n "$RECPID" ] && kill "$RECPID" 2>/dev/null
}
trap cleanup EXIT

echo "==> build gsx + recorder"
go -C "$REPO" build -o "$BIN" ./cmd/gsx                 || { echo "build gsx failed"; exit 2; }
go build -o "$RECBIN" "$HERE/recorder.go"               || { echo "build recorder failed"; exit 2; }

[ "${1:-}" = "--fresh" ] && rm -rf "$APP"
if [ ! -f "$APP/go.mod" ]; then
  echo "==> scaffold app (one-time; pass --fresh to redo)"
  rm -rf "$APP"; mkdir -p "$APP"
  "$BIN" init --module reloadprobe "$APP" </dev/null >/dev/null 2>&1 || { echo "gsx init failed"; exit 2; }
  printf '\nreplace github.com/gsxhq/gsx => %s\n' "$REPO" >> "$APP/go.mod"
  ( cd "$APP" && GOFLAGS=-mod=mod go mod tidy ) >/dev/null 2>&1     || { echo "go mod tidy failed"; exit 2; }
fi

GOP=$(freeport); RECP=$(freeport)
printf 'GO_PORT=%s\nVITE_PORT=%s\nVITE_DEV_URL=http://localhost:%s\n' "$GOP" "$RECP" "$RECP" > "$APP/.env"
cp "$APP/app.gsx" "$WORK/app.gsx.orig"
cp "$APP/main.go" "$WORK/main.go.orig"

echo "==> start recorder ($RECP) + gsx dev (GO_PORT=$GOP, --no-web)"
: > "$REC"
REC_PORT="$RECP" "$RECBIN" > "$REC" 2>/dev/null & RECPID=$!
sleep 0.4
( "$BIN" dev "$APP" --no-web ) > "$DEV" 2>&1 & DEVPID=$!

up=no
for _ in $(seq 1 60); do curl -s -o /dev/null "http://localhost:$GOP/healthz" && { up=yes; break; }; sleep 0.5; done
[ "$up" = yes ] || { echo "FAIL: server never came up"; echo "--- gsx dev output ---"; sed -n '1,40p' "$DEV"; exit 1; }
echo "    server healthy on $GOP"

pass=0; fail=0
nlines()  { wc -l < "$REC" | tr -d ' '; }
since()   { tail -n +"$1" "$REC"; }
expect()  { # $1=offset  $2=pattern  $3=label
  if since "$1" | grep -q "$2"; then echo "    PASS: $3"; pass=$((pass+1));
  else echo "    FAIL: $3"; echo "      (lines since:"; since "$1" | sed 's/^/        /'; echo "      )"; fail=$((fail+1)); fi
}

probe() { # $1=file  $2=error-snippet  $3=label
  local f="$APP/$1" off
  off=$(( $(nlines) + 1 ))
  printf '%s\n' "$2" >> "$f"; sleep 3
  expect "$off" "EVENT ok=false" "$1 error → overlay (ok:false)"
  off=$(( $(nlines) + 1 ))
  cp "$WORK/$1.orig" "$f"; sleep 3
  expect "$off" "RELOAD" "$1 fix → recovery reload"
}

echo "==> phase A: app.gsx"
probe "app.gsx" '<div>{ not valid go }</div>'
echo "==> phase B: main.go"
probe "main.go" 'var _reloadProbeBroken = undefinedSymbolXYZ'

echo "================================"
echo "PASS=$pass FAIL=$fail"
if [ "$fail" -eq 0 ]; then echo "RESULT: PASS"; else echo "RESULT: FAIL"; fi
exit "$fail"
