# Immutable Spread Policy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` to execute this plan task by task,
> and `superpowers:verification-before-completion` before every commit or
> completion claim.

**Goal:** Decide, with exact security semantics and interleaved end-to-end
evidence, whether replacing `Writer.Spread`'s repeated linear exact-name scans
with immutable package-level policies is worth retaining.

**Architecture:** Codegen will group its already deterministic URL attribute
names by sink once per generated file and register each distinct triple as a
package-level `*gsx.SpreadPolicy`. The runtime policy copies constructor input,
uses allocation-free ASCII FNV-1a lookup with equality verification, and falls
back to `strings.EqualFold` whenever either side is non-ASCII. `Writer.Spread`
keeps its ordered duplicate, exclusion, `Toggle`, prefix, sink, escaping, and
error behavior; only the three exact-name scans change. Candidate 2 (folded
attribute materialisation), style work, and component ABI work are outside this
plan and require separate post-slice plans.

**Tech stack:** Go 1.26.1, standard-library-only root runtime, GSX codegen and
txtar corpus, sibling `gsx-bench`, `gopls`, race/fuzz/profile tooling, and
`golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d`.

## Scope and invariants

- Execute from clean worktrees at
  `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx` and
  `/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench`.
- The plan file itself must already be committed. Core correction commit
  `614c4c58` and benchmark correction commit `5badb81` must be ancestors of the
  active revisions.
- Keep the root runtime standard-library only and use unexported implementation
  details. `SpreadPolicy` and `NewSpreadPolicy` are exported only because
  generated packages cross the runtime package boundary.
- Preserve exact Unicode simple-fold behavior. In particular,
  `strings.EqualFold("ſrc", "src")` is true and must classify through the same
  sink as ASCII `src`.
- Preserve image, then srcset, then navigation precedence; URL-prefix rules
  remain strict navigation rules evaluated after exact-name classification.
- Preserve slice order, exact-key last-wins duplicate handling, validity checks,
  caller-owned exclusions, class/style aggregation, boolean-name behavior,
  `Toggle`, `RawURL`, escaping, first-error retention, and concurrent reads.
- Do not add a compatibility overload or retain the seven-argument spread
  shape. GSX is pre-alpha; all handwritten and generated callers move together.
- Do not hand-edit generated `.x.go` or txtar golden sections. Regenerate them
  from authored source.
- Raw benchmark output, diffs, test binaries, and profiles live under `/tmp`.
  No profiling command may leave a `*.test` binary in a repository.
- A failed command must fail its shell block. Evidence commands use checked
  redirection followed by `cat`; they never pipe a producer into `tee`.
- Every shell block below is self-contained: it uses absolute `git -C`, an
  explicit `cd`, and reloads saved revisions or directories itself.
- Candidate 2 and Candidate 3 are not implemented here. The only possible
  follow-up artifact is a separate Candidate 2 plan written from the measured
  post-Candidate-1 state.

## Files

Core prerequisite:

- Modify `root_attr_bench_test.go` with a committed 70-entry benchmark before
  the candidate base is recorded.

Core candidate:

- Create `spread_policy.go`.
- Create `spread_policy_test.go`.
- Create `spread_policy_fuzz_test.go`.
- Create `spread_policy_bench_test.go`.
- Modify `attrs.go` and every handwritten `Writer.Spread` caller returned by
  `gopls references -d attrs.go:326:20`.
- Modify the authored Go fixture in
  `internal/corpus/testdata/cases/xpkg/go_component_attrs_literal.txtar`; it is
  a handwritten runtime caller, not a generated golden.
- Modify `internal/codegen/rtimports.go`, `internal/codegen/emit.go`, and
  `internal/codegen/component_positional_emit_test.go`.
- Create `internal/codegen/spread_policy_emit_test.go`.
- Modify `internal/attrclass/attrclass.go` comment only; classifier behavior
  stays unchanged.
- Create
  `internal/corpus/testdata/cases/spread-sanitize/policy_unicode_fold.txtar`
  and regenerate semantic-corpus goldens.
- Regenerate `examples/tailwind-merge/views/card.x.go`.

Sibling prerequisite and generated candidate:

- Create `scripts/benchcmp.sh` and `scripts/benchcmp_test.sh`.
- Modify `README.md` with the interleaved command contract.
- Regenerate `gsxr/*.x.go` and `tw/*.x.go`; `templr/*_templ.go` must not drift.

Decision and documentation:

- Modify
  `docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md`.
- Modify `docs/guide/performance.md` for either retained or rejected outcome.
- Review `docs/ROADMAP.md`; change it only if the outcome makes a current claim
  inaccurate.
- Conditional create after the post-slice profile gate:
  `docs/superpowers/plans/2026-07-21-folded-element-attribute-materialisation.md`.

---

### Task 1: Commit the measurement prerequisites and record candidate bases

- [ ] **Step 1: Prove the execution state is committed and clean**

Run exactly:

```sh
set -eu
core=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
bench=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
git -C "$core" cat-file -e HEAD:docs/superpowers/plans/2026-07-21-runtime-render-optimisations.md
git -C "$core" merge-base --is-ancestor 614c4c58 HEAD
git -C "$bench" merge-base --is-ancestor 5badb81 HEAD
test -z "$(git -C "$core" status --porcelain=v1)"
test -z "$(git -C "$bench" status --porcelain=v1)"
test "$(go env GOVERSION)" = go1.26.1
test "$(templ version)" = v0.3.1020
mkdir -p /tmp/gsx-runtime-optimisations
```

Expected: every command succeeds. Stop rather than absorb unrelated dirty
state, a missing correction commit, or a different Go/templ toolchain.

- [ ] **Step 2: Add and commit the missing large-bag benchmark before the candidate base**

Add `strconv` to `root_attr_bench_test.go`'s imports and add this complete
benchmark. It intentionally contains no URL-classified key, so the old path
performs every exact-name scan for each of 70 valid scalar attributes and the
candidate path performs one verified policy lookup per key. Setup is outside
the timed loop.

```go
func BenchmarkSpreadNoURLLarge(b *testing.B) {
	b.ReportAllocs()
	attrs := make(Attrs, 0, 70)
	for i := 0; i < 70; i++ {
		attrs = append(attrs, Attr{
			Key:   "data-field-" + strconv.Itoa(i),
			Value: "value",
		})
	}
	navNames := []string{"action", "cite", "data", "formaction", "href", "manifest", "ping", "poster", "src", "xlink:href"}
	imageNames := []string{"background"}
	srcsetNames := []string{"imagesrcset", "srcset"}
	gw := W(io.Discard)
	ctx := context.Background()
	for b.Loop() {
		gw.Spread(ctx, attrs, navNames, imageNames, srcsetNames, nil, nil)
	}
}
```

Verify and commit it before any policy code exists:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
gofmt -w root_attr_bench_test.go
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -run '^$' \
  -bench '^BenchmarkSpreadNoURLLarge$' -benchmem -count=10 . \
  > /tmp/gsx-runtime-optimisations/large-bag-prerequisite.txt
cat /tmp/gsx-runtime-optimisations/large-bag-prerequisite.txt
gopls check -severity=hint root_attr_bench_test.go
git diff --check
git add root_attr_bench_test.go
git commit -m 'test(perf): cover large forwarded attribute bags'
```

Expected: the benchmark reports 70 entries and the previously observed
large-bag allocation regime (three `lastValidAttrIndexes` allocations; byte
rounding may move with the compiler). This is workload coverage, not a
performance claim.

- [ ] **Step 3: Write the failing shell test for the comparison harness**

Create the source directory first:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
mkdir -p scripts
```

Create `scripts/benchcmp_test.sh` with this complete content:

```sh
#!/bin/sh
set -eu

repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd -P)
tmp=$(mktemp -d /tmp/gsx-benchcmp-test.XXXXXX)
cleanup() {
  rm -f "$tmp"/bin/go "$tmp"/bin/git "$tmp"/trace "$tmp"/fail-trace
  rm -f "$tmp"/usage "$tmp"/untracked "$tmp"/unstaged
  rm -f "$tmp"/out/* "$tmp"/out-fail/*
  rmdir "$tmp"/out "$tmp"/out-fail "$tmp"/bin "$tmp"/before "$tmp"/after "$tmp" 2>/dev/null || true
}
trap cleanup EXIT HUP INT TERM
mkdir "$tmp/bin" "$tmp/before" "$tmp/after"

cat >"$tmp/bin/go" <<'EOF'
#!/bin/sh
set -eu
printf '%s|%s\n' "$PWD" "$*" >>"$TRACE"
case "$1" in
version)
  printf '%s\n' 'go version go1.26.1 darwin/arm64'
  ;;
test)
  printf '%s\n' \
    'goos: darwin' \
    'goarch: arm64' \
    'pkg: probe' \
    'cpu: fake' \
    'BenchmarkProbe-32 1 100 ns/op 0 B/op 0 allocs/op' \
    'PASS'
  ;;
run)
  if [ "${FAIL_BENCHSTAT:-}" = 1 ]; then
    exit 72
  fi
  printf '%s\n' 'name old time/op new time/op delta'
  ;;
*) exit 64 ;;
esac
EOF

cat >"$tmp/bin/git" <<'EOF'
#!/bin/sh
set -eu
case "$*" in
'ls-files --others --exclude-standard')
  [ -z "${FAKE_UNTRACKED:-}" ] || printf '%s\n' "$FAKE_UNTRACKED"
  ;;
'diff --quiet --')
  [ -z "${FAKE_UNSTAGED:-}" ]
  ;;
'diff --cached --binary HEAD')
  printf '%s\n' 'staged candidate diff'
  ;;
'rev-parse HEAD')
  printf '%s\n' '0123456789abcdef0123456789abcdef01234567'
  ;;
'status --short')
  printf '%s\n' 'M  staged.go'
  ;;
*) exit 64 ;;
esac
EOF
chmod +x "$tmp/bin/go" "$tmp/bin/git"

TRACE="$tmp/trace" PATH="$tmp/bin:$PATH" "$repo/scripts/benchcmp.sh" \
  "$tmp/before" "$tmp/after" '^BenchmarkProbe$' "$tmp/out" .

set +e
TRACE="$tmp/fail-trace" PATH="$tmp/bin:$PATH" "$repo/scripts/benchcmp.sh" \
  "$tmp/before" "$tmp/after" '^BenchmarkProbe$' > /dev/null 2>"$tmp/usage"
status=$?
set -e
test "$status" -eq 64
grep -q '^usage: benchcmp.sh ' "$tmp/usage"

set +e
FAKE_UNTRACKED=probe.go TRACE="$tmp/fail-trace" PATH="$tmp/bin:$PATH" \
  "$repo/scripts/benchcmp.sh" "$tmp/before" "$tmp/after" \
  '^BenchmarkProbe$' "$tmp/out-untracked" . > /dev/null 2>"$tmp/untracked"
status=$?
set -e
test "$status" -eq 66
grep -q 'untracked files' "$tmp/untracked"

set +e
FAKE_UNSTAGED=1 TRACE="$tmp/fail-trace" PATH="$tmp/bin:$PATH" \
  "$repo/scripts/benchcmp.sh" "$tmp/before" "$tmp/after" \
  '^BenchmarkProbe$' "$tmp/out-unstaged" . > /dev/null 2>"$tmp/unstaged"
status=$?
set -e
test "$status" -eq 67
grep -q 'unstaged changes' "$tmp/unstaged"

set +e
FAIL_BENCHSTAT=1 TRACE="$tmp/fail-trace" PATH="$tmp/bin:$PATH" \
  "$repo/scripts/benchcmp.sh" "$tmp/before" "$tmp/after" \
  '^BenchmarkProbe$' "$tmp/out-fail" . > /dev/null 2>&1
status=$?
set -e
test "$status" -eq 72

test "$(grep -c "$tmp/before|test" "$tmp/trace")" -eq 10
test "$(grep -c "$tmp/after|test" "$tmp/trace")" -eq 10
grep '|test' "$tmp/trace" >"$tmp/test-trace"
awk -F'|' -v before="$tmp/before" -v after="$tmp/after" '
  NR % 2 == 1 && $1 != before { exit 1 }
  NR % 2 == 0 && $1 != after { exit 1 }
  END { if (NR != 20) exit 1 }
' "$tmp/test-trace"
rm -f "$tmp/test-trace"
test "$(grep -c '^BenchmarkProbe-32' "$tmp/out/before.txt")" -eq 10
test "$(grep -c '^BenchmarkProbe-32' "$tmp/out/after.txt")" -eq 10
test -s "$tmp/out/before.diff"
test -s "$tmp/out/after.diff"
test -s "$tmp/out/benchstat.txt"
test -s "$tmp/out/environment.txt"
grep -q '^before-commit=0123456789abcdef' "$tmp/out/environment.txt"
grep -q '^after-diff-sha256=' "$tmp/out/environment.txt"
```

Make it executable, run it, and observe failure because `benchcmp.sh` is absent:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
chmod +x scripts/benchcmp_test.sh
sh scripts/benchcmp_test.sh
```

- [ ] **Step 4: Implement the exact interleaved harness**

Create `scripts/benchcmp.sh` with this complete content:

```sh
#!/bin/sh
set -eu

if [ "$#" -lt 4 ] || [ "$#" -gt 5 ]; then
  printf '%s\n' 'usage: benchcmp.sh BEFORE_DIR AFTER_DIR BENCH_REGEX OUTPUT_DIR [PACKAGE]' >&2
  exit 64
fi

before=$(CDPATH= cd -- "$1" && pwd -P)
after=$(CDPATH= cd -- "$2" && pwd -P)
regex=$3
out_arg=$4
pkg=${5:-.}
benchstat=golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d

if [ "$before" = "$after" ]; then
  printf '%s\n' 'benchcmp: before and after must be different worktrees' >&2
  exit 65
fi

check_repo() {
  repo_dir=$1
  untracked=$(cd "$repo_dir" && git ls-files --others --exclude-standard)
  if [ -n "$untracked" ]; then
    printf 'benchcmp: untracked files in %s:\n%s\n' "$repo_dir" "$untracked" >&2
    exit 66
  fi
  if ! (cd "$repo_dir" && git diff --quiet --); then
    printf 'benchcmp: unstaged changes in %s; stage the exact candidate first\n' "$repo_dir" >&2
    exit 67
  fi
}
check_repo "$before"
check_repo "$after"

case "$out_arg" in
/*) out=$out_arg ;;
*) out=$PWD/$out_arg ;;
esac
out_parent=$(dirname -- "$out")
out_name=$(basename -- "$out")
mkdir -p "$out_parent"
out_parent=$(CDPATH= cd -- "$out_parent" && pwd -P)
out=$out_parent/$out_name
case "$out/" in
"$before/"*|"$after/"*)
  printf '%s\n' 'benchcmp: output directory must be outside both repositories' >&2
  exit 68
  ;;
esac
if [ -e "$out" ]; then
  printf 'benchcmp: output path already exists: %s\n' "$out" >&2
  exit 69
fi
mkdir "$out"

go_version=$(go version)
case "$go_version" in
'go version go1.26.1 '*) ;;
*) printf 'benchcmp: need Go 1.26.1, got %s\n' "$go_version" >&2; exit 70 ;;
esac

(cd "$before" && git diff --cached --binary HEAD) >"$out/before.diff"
(cd "$after" && git diff --cached --binary HEAD) >"$out/after.diff"
before_diff_sha=$(shasum -a 256 "$out/before.diff")
before_diff_sha=${before_diff_sha%% *}
after_diff_sha=$(shasum -a 256 "$out/after.diff")
after_diff_sha=${after_diff_sha%% *}

{
  printf '%s\n' "$go_version"
  uname -m
  sysctl -n machdep.cpu.brand_string 2>/dev/null || true
  printf 'GOMAXPROCS=32\n'
  printf 'before-path=%s\n' "$before"
  printf 'after-path=%s\n' "$after"
  printf 'before-commit=%s\n' "$(cd "$before" && git rev-parse HEAD)"
  printf 'after-commit=%s\n' "$(cd "$after" && git rev-parse HEAD)"
  printf 'before-status=%s\n' "$(cd "$before" && git status --short)"
  printf 'after-status=%s\n' "$(cd "$after" && git status --short)"
  printf 'before-diff-sha256=%s\n' "$before_diff_sha"
  printf 'after-diff-sha256=%s\n' "$after_diff_sha"
  printf 'regex=%s\n' "$regex"
  printf 'package=%s\n' "$pkg"
} >"$out/environment.txt"

: >"$out/before.txt"
: >"$out/after.txt"
i=1
while [ "$i" -le 10 ]; do
  (
    cd "$before"
    GOFLAGS= GOMAXPROCS=32 GOCACHE=/tmp/gsx-runtime-optimisations-cache \
      go test -run '^$' -bench "$regex" -benchmem -count=1 "$pkg"
  ) >>"$out/before.txt"
  (
    cd "$after"
    GOFLAGS= GOMAXPROCS=32 GOCACHE=/tmp/gsx-runtime-optimisations-cache \
      go test -run '^$' -bench "$regex" -benchmem -count=1 "$pkg"
  ) >>"$out/after.txt"
  i=$((i + 1))
done

go run "$benchstat" "$out/before.txt" "$out/after.txt" >"$out/benchstat.txt"
cat "$out/benchstat.txt"
```

Run the smoke test, document the exact contract in `README.md`, verify, and
commit the sibling prerequisite:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
chmod +x scripts/benchcmp.sh scripts/benchcmp_test.sh
sh scripts/benchcmp_test.sh
git diff --check
git add scripts/benchcmp.sh scripts/benchcmp_test.sh README.md
git commit -m 'test(perf): add interleaved benchmark comparisons'
```

The README example must use an output directory under `/tmp` and state that the
script runs ten distinct before/after process pairs at `GOMAXPROCS=32`, rejects
untracked or unstaged inputs, records commits plus staged-diff hashes, and uses
the pinned benchstat revision.

- [ ] **Step 5: Record the exact post-prerequisite candidate bases**

```sh
set -eu
core=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
bench=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
test -z "$(git -C "$core" status --porcelain=v1)"
test -z "$(git -C "$bench" status --porcelain=v1)"
git -C "$core" rev-parse HEAD > /tmp/gsx-runtime-optimisations/spread-policy.core-base
git -C "$bench" rev-parse HEAD > /tmp/gsx-runtime-optimisations/spread-policy.bench-base
cat /tmp/gsx-runtime-optimisations/spread-policy.core-base
cat /tmp/gsx-runtime-optimisations/spread-policy.bench-base
```

These two revisions are the only permitted sources for the detached before
pair and for a rejected-candidate restore.

---

### Task 2: Implement and prove the exact runtime classifier with TDD

- [ ] **Step 1: Add failing semantic, collision, copy, allocation, and race tests**

Create `spread_policy_test.go`. The test file must be package `gsx` and contain
these complete test bodies (imports: `bytes`, `context`, `fmt`, `strings`,
`sync`, `testing`):

```go
func TestSpreadPolicyExactClassification(t *testing.T) {
	policy := NewSpreadPolicy(
		[]string{"href", "src", "candidates"},
		[]string{"src", "overlap"},
		[]string{"srcset", "imagesrcset", "candidates", "overlap"},
	)
	tests := []struct {
		name string
		key  string
		val  any
		want string
	}{
		{"navigation case fold", "HREF", "javascript:alert(1)", ` HREF="about:invalid#gsx"`},
		{"image beats navigation", "SRC", "data:image/png;base64,AA", ` SRC="data:image/png;base64,AA"`},
		{"image beats srcset", "OvErLaP", "data:image/png;base64,AA", ` OvErLaP="data:image/png;base64,AA"`},
		{"srcset beats navigation", "CaNdIdAtEs", "javascript:bad 1x, /ok.png 2x", ` CaNdIdAtEs="about:invalid#gsx, /ok.png 2x"`},
		{"unicode long s", "ſrc", "data:image/gif;base64,R0lGOD", ` ſrc="data:image/gif;base64,R0lGOD"`},
		{"plain", "data-id", "7", ` data-id="7"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			gw := W(&buf)
			gw.Spread(context.Background(), Attrs{{Key: tt.key, Value: tt.val}}, policy, nil, nil)
			if err := gw.Err(); err != nil {
				t.Fatal(err)
			}
			if got := buf.String(); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSpreadPolicyUnicodeEqualFoldBothDirections(t *testing.T) {
	if !strings.EqualFold("ſrc", "src") {
		t.Fatal("test precondition: long-s must simple-fold to ASCII s")
	}
	tests := []struct {
		name   string
		policy *SpreadPolicy
		key    string
		want   spreadSink
	}{
		{"non-ASCII key ASCII policy", NewSpreadPolicy(nil, []string{"src"}, nil), "ſRC", spreadImage},
		{"ASCII key non-ASCII policy", NewSpreadPolicy([]string{"ſrc"}, nil, nil), "SRC", spreadNav},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.sink(tt.key); got != tt.want {
				t.Fatalf("sink(%q) = %v, want %v", tt.key, got, tt.want)
			}
		})
	}
}

func TestSpreadPolicyExactNameBeatsPrefix(t *testing.T) {
	policy := NewSpreadPolicy([]string{"src"}, []string{"src"}, nil)
	var buf bytes.Buffer
	gw := W(&buf)
	gw.Spread(context.Background(), Attrs{{Key: "SRC", Value: "data:image/png;base64,AA"}}, policy, []string{"s"}, nil)
	if err := gw.Err(); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), ` SRC="data:image/png;base64,AA"`; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestNewSpreadPolicyCopiesInputs(t *testing.T) {
	nav := []string{"href"}
	image := []string{"src"}
	srcset := []string{"srcset"}
	policy := NewSpreadPolicy(nav, image, srcset)
	nav[0], image[0], srcset[0] = "changed-nav", "changed-image", "changed-srcset"
	if got := policy.sink("href"); got != spreadNav {
		t.Fatalf("href sink = %v, want navigation", got)
	}
	if got := policy.sink("src"); got != spreadImage {
		t.Fatalf("src sink = %v, want image", got)
	}
	if got := policy.sink("srcset"); got != spreadSrcset {
		t.Fatalf("srcset sink = %v, want srcset", got)
	}
}

func TestSpreadPolicyNilAndZeroValueArePlain(t *testing.T) {
	tests := []struct {
		name   string
		policy *SpreadPolicy
	}{
		{"nil", nil},
		{"zero", &SpreadPolicy{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.policy.sink("href"); got != spreadPlain {
				t.Fatalf("sink(href) = %v, want plain", got)
			}
		})
	}
}

func TestFoldedNameSetChecksHashCollisions(t *testing.T) {
	h := asciiFoldHash("href")
	withoutMatch := foldedNameSet{ascii: map[uint64][]string{h: {"src"}}}
	if withoutMatch.contains("href") {
		t.Fatal("hash collision classified a different name")
	}
	withMatch := foldedNameSet{ascii: map[uint64][]string{h: {"src", "HREF"}}}
	if !withMatch.contains("href") {
		t.Fatal("verified bucket did not find case-folded href")
	}
}

func TestSpreadPolicyASCIILookupAllocs(t *testing.T) {
	policy := NewSpreadPolicy(
		[]string{"action", "cite", "data", "formaction", "href", "manifest", "ping", "poster", "src", "xlink:href"},
		[]string{"background"},
		[]string{"imagesrcset", "srcset"},
	)
	var got spreadSink
	allocs := testing.AllocsPerRun(1000, func() { got = policy.sink("HREF") })
	if allocs != 0 {
		t.Fatalf("ASCII lookup allocations = %v, want 0", allocs)
	}
	if got != spreadNav {
		t.Fatalf("HREF sink = %v, want navigation", got)
	}
}

func TestSpreadPolicyConcurrentReads(t *testing.T) {
	policy := NewSpreadPolicy([]string{"href"}, []string{"src"}, []string{"srcset"})
	attrs := Attrs{
		{Key: "HREF", Value: "javascript:alert(1)"},
		{Key: "ſrc", Value: "data:image/png;base64,AA"},
		{Key: "SRCSET", Value: "javascript:bad 1x, /ok.png 2x"},
	}
	render := func() (string, error) {
		var buf bytes.Buffer
		gw := W(&buf)
		gw.Spread(context.Background(), attrs, policy, nil, nil)
		return buf.String(), gw.Err()
	}
	want, err := render()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := render()
			if err != nil {
				errs <- err
				return
			}
			if got != want {
				errs <- fmt.Errorf("concurrent output = %q, want %q", got, want)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
```

Create `spread_policy_fuzz_test.go` with this complete reference and fuzzer:

```go
package gsx

import (
	"strings"
	"testing"
)

func referenceSpreadSink(key string, nav, image, srcset []string) spreadSink {
	switch {
	case equalFoldNameIn(key, image):
		return spreadImage
	case equalFoldNameIn(key, srcset):
		return spreadSrcset
	case equalFoldNameIn(key, nav):
		return spreadNav
	default:
		return spreadPlain
	}
}

func equalFoldNameIn(key string, names []string) bool {
	for _, name := range names {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func encodePolicyNames(names ...string) []byte {
	var out []byte
	for _, name := range names {
		if len(name) > 255 {
			panic("policy fuzz seed name is too long")
		}
		out = append(out, byte(len(name)))
		out = append(out, name...)
	}
	return out
}

func decodePolicyNames(data []byte) []string {
	const maxNames = 64
	var names []string
	for len(data) > 0 && len(names) < maxNames {
		n := int(data[0])
		data = data[1:]
		if n > len(data) {
			n = len(data)
		}
		names = append(names, string(data[:n]))
		data = data[n:]
	}
	return names
}

func FuzzSpreadPolicyMatchesEqualFoldReference(f *testing.F) {
	f.Add(encodePolicyNames("href", "src"), encodePolicyNames("src", "background"), encodePolicyNames("srcset", "imagesrcset"), []byte("HREF"))
	f.Add(encodePolicyNames("src"), encodePolicyNames("src"), encodePolicyNames("src"), []byte("ſrc"))
	f.Add(encodePolicyNames("ſrc"), []byte(nil), []byte(nil), []byte("SRC"))
	f.Add([]byte(nil), []byte(nil), []byte(nil), []byte(nil))
	f.Fuzz(func(t *testing.T, navData, imageData, srcsetData, keyData []byte) {
		if len(navData) > 1024 || len(imageData) > 1024 || len(srcsetData) > 1024 || len(keyData) > 256 {
			t.Skip()
		}
		nav := decodePolicyNames(navData)
		image := decodePolicyNames(imageData)
		srcset := decodePolicyNames(srcsetData)
		key := string(keyData)
		policy := NewSpreadPolicy(nav, image, srcset)
		want := referenceSpreadSink(key, nav, image, srcset)
		if got := policy.sink(key); got != want {
			t.Fatalf("sink(%q; nav=%q image=%q srcset=%q) = %v, want %v", key, nav, image, srcset, got, want)
		}
	})
}
```

Run the red tests:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -run 'Test(SpreadPolicy|NewSpreadPolicy|FoldedNameSet)' .
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -run '^$' \
  -fuzz '^FuzzSpreadPolicyMatchesEqualFoldReference$' -fuzztime=5s .
```

Expected: compilation fails because the new policy API does not exist.

- [ ] **Step 2: Add the complete immutable classifier**

Create `spread_policy.go` with exactly this production implementation:

```go
package gsx

import (
	"slices"
	"strings"
	"unicode/utf8"
)

type spreadSink uint8

const (
	spreadPlain spreadSink = iota
	spreadNav
	spreadImage
	spreadSrcset
)

const (
	fnvOffset64 = uint64(14695981039346656037)
	fnvPrime64  = uint64(1099511628211)
)

type foldedNameSet struct {
	names    []string
	ascii    map[uint64][]string
	nonASCII []string
}

// SpreadPolicy is immutable exact-name metadata shared by generated renderers.
// Construct it with NewSpreadPolicy and reuse it concurrently.
type SpreadPolicy struct {
	nav    foldedNameSet
	image  foldedNameSet
	srcset foldedNameSet
}

// NewSpreadPolicy copies and classifies its inputs. Sink precedence is image,
// then srcset, then navigation when a name occurs in more than one set.
func NewSpreadPolicy(navNames, imageNames, srcsetNames []string) *SpreadPolicy {
	return &SpreadPolicy{
		nav:    newFoldedNameSet(navNames),
		image:  newFoldedNameSet(imageNames),
		srcset: newFoldedNameSet(srcsetNames),
	}
}

func newFoldedNameSet(names []string) foldedNameSet {
	set := foldedNameSet{names: slices.Clone(names)}
	for _, name := range set.names {
		if isASCII(name) {
			if set.ascii == nil {
				set.ascii = make(map[uint64][]string)
			}
			hash := asciiFoldHash(name)
			set.ascii[hash] = append(set.ascii[hash], name)
			continue
		}
		set.nonASCII = append(set.nonASCII, name)
	}
	return set
}

func asciiFoldByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func asciiFoldHash(s string) uint64 {
	hash := fnvOffset64
	for i := 0; i < len(s); i++ {
		hash ^= uint64(asciiFoldByte(s[i]))
		hash *= fnvPrime64
	}
	return hash
}

func asciiEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if asciiFoldByte(a[i]) != asciiFoldByte(b[i]) {
			return false
		}
	}
	return true
}

func (set foldedNameSet) contains(key string) bool {
	if !isASCII(key) {
		for _, name := range set.names {
			if strings.EqualFold(key, name) {
				return true
			}
		}
		return false
	}
	for _, name := range set.ascii[asciiFoldHash(key)] {
		if asciiEqualFold(key, name) {
			return true
		}
	}
	for _, name := range set.nonASCII {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func (policy *SpreadPolicy) sink(name string) spreadSink {
	if policy == nil {
		return spreadPlain
	}
	switch {
	case policy.image.contains(name):
		return spreadImage
	case policy.srcset.contains(name):
		return spreadSrcset
	case policy.nav.contains(name):
		return spreadNav
	default:
		return spreadPlain
	}
}
```

The hash is only a bucket selector. `asciiEqualFold` verifies every candidate;
therefore collisions cannot change classification. The ASCII-key path also
checks configured non-ASCII names, and the non-ASCII-key path checks the whole
copied set, which is what preserves `src`/`ſrc` symmetry.

- [ ] **Step 3: Replace `Writer.Spread` with the complete five-argument body**

Rewrite the documentation above `Spread` so it names `policy`, `prefixes`, and
`excluded`; states that a nil policy means no exact-name classification; and
retains the full ordering, sink-precedence, `RawURL`, and generated-code safety
contract. Rewrite `attrNameExcluded`'s comment to describe only the exclusion
set; it no longer classifies URL exact names. Replace the function with this
exact body:

```go
func (gw *Writer) Spread(ctx context.Context, a Attrs, policy *SpreadPolicy, prefixes, excluded []string) {
	if gw.err != nil || len(a) == 0 {
		return
	}
	last := lastValidAttrIndexes(a)
	for i, kv := range a {
		if !validAttrName(kv.Key) || last[kv.Key] != i {
			continue
		}
		if attrNameExcluded(kv.Key, excluded) {
			continue
		}
		if tg, ok := kv.Value.(Toggle); ok {
			gw.BoolAttr(kv.Key, bool(tg))
			continue
		}
		switch policy.sink(kv.Key) {
		case spreadImage:
			gw.writeStr(" ")
			gw.writeStr(kv.Key)
			gw.writeStr(`="`)
			gw.URLImageVal(kv.Value)
			gw.writeStr(`"`)
		case spreadSrcset:
			gw.writeStr(" ")
			gw.writeStr(kv.Key)
			gw.writeStr(`="`)
			gw.SrcsetVal(kv.Value)
			gw.writeStr(`"`)
		case spreadNav:
			gw.writeStr(" ")
			gw.writeStr(kv.Key)
			gw.writeStr(`="`)
			gw.URLVal(kv.Value)
			gw.writeStr(`"`)
		default:
			if URLPrefixMatch(kv.Key, prefixes) {
				gw.writeStr(" ")
				gw.writeStr(kv.Key)
				gw.writeStr(`="`)
				gw.URLVal(kv.Value)
				gw.writeStr(`"`)
				continue
			}
			switch kv.Key {
			case "class":
				kv.Value = a.Class()
			case "style":
				kv.Value = a.Style()
			}
			if kv.Value == nil {
				continue
			}
			vs, vk, ok := anyRenderVal(kv.Value)
			if !ok {
				if gw.err == nil {
					gw.err = fmt.Errorf("gsx: attribute %q: unsupported dynamic type %T", kv.Key, kv.Value)
				}
				return
			}
			if vk == kindBool && IsBooleanAttr(kv.Key) {
				gw.BoolAttr(kv.Key, vs == "true")
				continue
			}
			gw.writeStr(" ")
			gw.writeStr(kv.Key)
			gw.writeStr(`="`)
			gw.AttrValue(vs)
			gw.writeStr(`"`)
		}
	}
}
```

Do not extract the four write sequences in this slice; keeping their call
boundaries identical makes the comparison single-variable. `ctx` remains
reserved as before. `attrNameExcluded` remains the exclusion authority, and
`URLPrefixMatch` remains unchanged.

Use `gopls references -d attrs.go:326:20` before editing callers. At each
handwritten caller, construct `NewSpreadPolicy(navNames, imageNames,
srcsetNames)` outside a benchmark/render loop and replace the three exact-name
arguments with that policy. A caller that previously passed all three name
sets as nil passes a nil policy. Update test helper signatures where necessary;
do not weaken any expected output. Separately change the authored call in
`internal/corpus/testdata/cases/xpkg/go_component_attrs_literal.txtar` to
`gw.Spread(ctx, p.Attrs, nil, nil, nil)` before corpus regeneration.

- [ ] **Step 4: Add the exact differential microbenchmark and turn tests green**

Create `spread_policy_bench_test.go`:

```go
package gsx

import (
	"runtime"
	"testing"
)

func BenchmarkSpreadNameClassification(b *testing.B) {
	nav := []string{"action", "cite", "data", "formaction", "href", "manifest", "ping", "poster", "src", "xlink:href"}
	image := []string{"background"}
	srcset := []string{"imagesrcset", "srcset"}
	keys := []string{"id", "class", "HREF", "data-kind", "SRC", "ſrc", "srcset"}
	policy := NewSpreadPolicy(nav, image, srcset)
	b.Run("EqualFoldReference", func(b *testing.B) {
		var sink spreadSink
		for i := 0; b.Loop(); i++ {
			sink = referenceSpreadSink(keys[i%len(keys)], nav, image, srcset)
		}
		runtime.KeepAlive(sink)
	})
	b.Run("Policy", func(b *testing.B) {
		var sink spreadSink
		for i := 0; b.Loop(); i++ {
			sink = policy.sink(keys[i%len(keys)])
		}
		runtime.KeepAlive(sink)
	})
}
```

Run:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
gofmt -w spread_policy.go spread_policy_test.go spread_policy_fuzz_test.go spread_policy_bench_test.go attrs.go attrs_test.go attrs_fold_fuzz_test.go toggle_test.go gsx_test.go root_attr_bench_test.go cond_merge_bench_test.go internal/codegen/spread_fold_diff_test.go
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -run 'Test(Spread|SpreadPolicy|NewSpreadPolicy|FoldedNameSet|Toggle)' -count=1 .
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -run '^$' \
  -fuzz '^FuzzSpreadPolicyMatchesEqualFoldReference$' -fuzztime=30s .
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -race \
  -run 'TestSpreadPolicyConcurrentReads' -count=1 .
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -run '^$' \
  -bench '^BenchmarkSpreadNameClassification$' -benchmem -count=10 . \
  > /tmp/gsx-runtime-optimisations/spread-classifier-local.txt
cat /tmp/gsx-runtime-optimisations/spread-classifier-local.txt
gopls check -severity=hint spread_policy.go spread_policy_test.go spread_policy_fuzz_test.go spread_policy_bench_test.go attrs.go attrs_test.go attrs_fold_fuzz_test.go toggle_test.go gsx_test.go root_attr_bench_test.go cond_merge_bench_test.go internal/codegen/spread_fold_diff_test.go
```

Expected: exact output/reference/race tests pass; ASCII lookup reports zero
allocations. The local benchmark explains classifier movement but cannot retain
the candidate by itself.

---

### Task 3: Emit and reuse deterministic package-level policies

- [ ] **Step 1: Add failing registry, reuse, and package-uniqueness tests**

Generated Go identifiers are package-scoped. A file-local `_gsxsp0` counter
would therefore redeclare the same variable when a package has spreads in two
`.gsx` files. Policy names use the lossless hexadecimal encoding of the source
basename plus a first-use index; basenames are unique inside one package and
the encoding cannot collide.

Create `internal/codegen/spread_policy_emit_test.go` with this complete content:

```go
package codegen

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRTImportsSpreadPolicyRegistrySharesStateAcrossCopies(t *testing.T) {
	rt := newRTImports("views.gsx")
	copied := rt

	nav := []string{"href"}
	image := []string{"src"}
	srcset := []string{"srcset"}

	const first = "_gsxsp_76696577732e677378_0"
	if got := copied.spreadPolicy(nav, image, srcset); got != first {
		t.Fatalf("first policy = %q, want %q", got, first)
	}
	nav[0], image[0], srcset[0] = "changed-nav", "changed-image", "changed-srcset"
	if got := rt.spreadPolicy([]string{"href"}, []string{"src"}, []string{"srcset"}); got != first {
		t.Fatalf("copied-state lookup = %q, want %q", got, first)
	}

	const second = "_gsxsp_76696577732e677378_1"
	if got := copied.spreadPolicy([]string{"action"}, nil, nil); got != second {
		t.Fatalf("second policy = %q, want %q", got, second)
	}
	if !rt.has(gsxRuntimePath) {
		t.Fatal("policy registration did not record the runtime import")
	}

	var b bytes.Buffer
	rt.writeSpreadPolicies(&b)
	want := "var " + first + ` = _gsxrt.NewSpreadPolicy([]string{"href"}, []string{"src"}, []string{"srcset"})` + "\n" +
		"var " + second + ` = _gsxrt.NewSpreadPolicy([]string{"action"}, nil, nil)` + "\n\n"
	if got := b.String(); got != want {
		t.Fatalf("declarations:\n%s\nwant:\n%s", got, want)
	}
}

func TestGeneratedSpreadPolicies(t *testing.T) {
	tmp := tempModule(t, "example.com/spreadpolicy")
	viewsDir := makeSubPkg(t, tmp, "views", `package views

import "github.com/gsxhq/gsx"

component Links(on bool, attrs gsx.Attrs) {
	<a { if on { { attrs... } } }>one</a>
	<a { if on { { attrs... } } }>two</a>
}

component Picture(on bool, attrs gsx.Attrs) {
	<img { if on { { attrs... } } }/>
}
`)

	result, err := GenerateDirs(tmp, []string{viewsDir}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dirResult := result[viewsDir]
	if hasDiagErrors(dirResult.Diags) {
		t.Fatalf("diagnostics: %+v", dirResult.Diags)
	}
	src := generatedFor(t, dirResult, "views.gsx")

	const anchorPolicy = "_gsxsp_76696577732e677378_0"
	const imagePolicy = "_gsxsp_76696577732e677378_1"
	anchorDecl := `var ` + anchorPolicy + ` = _gsxrt.NewSpreadPolicy([]string{"action", "cite", "data", "formaction", "href", "manifest", "ping", "poster", "src", "xlink:href"}, []string{"background"}, []string{"imagesrcset", "srcset"})`
	imageDecl := `var ` + imagePolicy + ` = _gsxrt.NewSpreadPolicy([]string{"action", "cite", "data", "formaction", "href", "manifest", "ping", "poster", "xlink:href"}, []string{"background", "src"}, []string{"imagesrcset", "srcset"})`
	for _, declaration := range []string{anchorDecl, imageDecl} {
		if !strings.Contains(src, declaration) {
			t.Fatalf("missing declaration %q:\n%s", declaration, src)
		}
	}
	if got := strings.Count(src, "_gsxrt.NewSpreadPolicy("); got != 2 {
		t.Fatalf("policy declarations = %d, want 2:\n%s", got, src)
	}
	anchorCall := "_gsxgw.Spread(ctx, attrs, " + anchorPolicy + ", nil, nil)"
	if got := strings.Count(src, anchorCall); got != 2 {
		t.Fatalf("anchor calls = %d, want 2:\n%s", got, src)
	}
	imageCall := "_gsxgw.Spread(ctx, attrs, " + imagePolicy + ", nil, nil)"
	if !strings.Contains(src, imageCall) {
		t.Fatalf("missing image call:\n%s", src)
	}
	if strings.Contains(src, "_gsxgw.Spread(ctx, attrs, []string{") {
		t.Fatalf("render-time exact-name literal remains:\n%s", src)
	}

	again, err := GenerateDirs(tmp, []string{viewsDir}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !equalGenerated(dirResult.Files, again[viewsDir].Files) {
		t.Fatal("second generation changed policy names or output")
	}
}

func TestGeneratedSpreadPolicyNamesArePackageUnique(t *testing.T) {
	tmp := tempModule(t, "example.com/spreadpolicyfiles")
	viewsDir := filepath.Join(tmp, "views")
	writeFile(t, viewsDir, "a.gsx", `package views

import "github.com/gsxhq/gsx"

component A(on bool, attrs gsx.Attrs) {
	<a { if on { { attrs... } } }>a</a>
}
`)
	writeFile(t, viewsDir, "b.gsx", `package views

import "github.com/gsxhq/gsx"

component B(on bool, attrs gsx.Attrs) {
	<a { if on { { attrs... } } }>b</a>
}
`)

	result, err := GenerateDirs(tmp, []string{viewsDir}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dirResult := result[viewsDir]
	if hasDiagErrors(dirResult.Diags) {
		t.Fatalf("diagnostics: %+v", dirResult.Diags)
	}
	aSrc := generatedFor(t, dirResult, "a.gsx")
	bSrc := generatedFor(t, dirResult, "b.gsx")
	const aPolicy = "_gsxsp_612e677378_0"
	const bPolicy = "_gsxsp_622e677378_0"
	if !strings.Contains(aSrc, "var "+aPolicy+" = ") {
		t.Fatalf("a.gsx policy name missing:\n%s", aSrc)
	}
	if !strings.Contains(bSrc, "var "+bPolicy+" = ") {
		t.Fatalf("b.gsx policy name missing:\n%s", bSrc)
	}

	for sourcePath, generated := range dirResult.Files {
		base := strings.TrimSuffix(filepath.Base(sourcePath), ".gsx")
		writeFile(t, viewsDir, base+".x.go", string(generated))
	}
	if testing.Short() {
		return
	}
	cmd := exec.Command("go", "test", "./views")
	cmd.Dir = tmp
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated multi-file package does not compile: %v\n%s", err, out)
	}
}
```

Run it and observe a compilation failure because the registry API is absent;
after Step 2 supplies the registry, its generated-shape assertions remain red
until Step 3 changes the emitter:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test ./internal/codegen \
  -run '^TestGeneratedSpreadPolicies$' -count=1
```

- [ ] **Step 2: Replace `rtImports` with complete shared per-file state**

In `internal/codegen/rtimports.go`, add imports `bytes`, `encoding/hex`, `fmt`,
`path/filepath`, and `slices`. Make these as three explicit edits so the
existing `gsxRuntimePath` and alias constants are retained exactly once:

1. replace only the explanatory `// rtImports records ...` comment and
   `type rtImports map[string]bool` declaration with the new policy/state types,
   `rtImports` type, and `newRTImports` constructor below;
2. replace each of the five existing one-line accessor bodies (`rt`, `ctx`,
   `io`, `sc`, and `st`) with its corresponding pointer-state body below while
   retaining its nearby accessor comment; and
3. add `has`, `spreadPolicy`, and `writeSpreadPolicies` after `st`.

The complete declarations and method bodies to distribute at those exact
locations are:

```go
type spreadPolicySpec struct {
	name                string
	nav, image, srcset []string
}

type rtImportState struct {
	needs           map[string]bool
	policyNamePrefix string
	policies        []spreadPolicySpec
}

type rtImports struct {
	state *rtImportState
}

func newRTImports(sourcePath string) rtImports {
	sourceName := filepath.Base(sourcePath)
	return rtImports{state: &rtImportState{
		needs:            make(map[string]bool),
		policyNamePrefix: "_gsxsp_" + hex.EncodeToString([]byte(sourceName)) + "_",
	}}
}

func (r rtImports) has(path string) bool {
	return r.state.needs[path]
}

func (r rtImports) rt() string {
	r.state.needs[gsxRuntimePath] = true
	return rtAlias
}

func (r rtImports) ctx() string {
	r.state.needs["context"] = true
	return ctxAlias
}

func (r rtImports) io() string {
	r.state.needs["io"] = true
	return ioAlias
}

func (r rtImports) sc() string {
	r.state.needs["strconv"] = true
	return scAlias
}

func (r rtImports) st() string {
	r.state.needs["strings"] = true
	return stAlias
}

func (r rtImports) spreadPolicy(nav, image, srcset []string) string {
	r.rt()
	for _, policy := range r.state.policies {
		if slices.Equal(policy.nav, nav) &&
			slices.Equal(policy.image, image) &&
			slices.Equal(policy.srcset, srcset) {
			return policy.name
		}
	}
	name := fmt.Sprintf("%s%d", r.state.policyNamePrefix, len(r.state.policies))
	r.state.policies = append(r.state.policies, spreadPolicySpec{
		name:   name,
		nav:    slices.Clone(nav),
		image:  slices.Clone(image),
		srcset: slices.Clone(srcset),
	})
	return name
}

func (r rtImports) writeSpreadPolicies(b *bytes.Buffer) {
	for _, policy := range r.state.policies {
		fmt.Fprintf(b, "var %s = %s.NewSpreadPolicy(%s, %s, %s)\n",
			policy.name,
			rtAlias,
			goStringSliceLit(policy.nav),
			goStringSliceLit(policy.image),
			goStringSliceLit(policy.srcset),
		)
	}
	if len(r.state.policies) != 0 {
		b.WriteByte('\n')
	}
}
```

All `rtImports` values for one generated source file share the pointer, so
existing value-parameter plumbing cannot lose a slice append. The lossless
basename token prevents package-scope collisions across generated files without
depending on checkout paths. Calling `r.rt()` during registration is required:
imports are written before declarations, so recording the need from
`writeSpreadPolicies` would be too late.

At the start of `generateFile`, initialize exactly:

```go
sourcePath := fset.Position(file.Pos()).Filename
rt := newRTImports(sourcePath)
```

Reuse that `sourcePath` in the later positional-import block instead of
redeclaration:

```go
if sourcePath != "" {
	if allocator := positionalPlan.imports[sourcePath]; allocator != nil {
		aliased = append(aliased, allocator.specs()...)
	}
}
```

In `writeImports`, replace `if rt[e.path]` with `if rt.has(e.path)`. Replace the
output sequence with this exact sequence:

```go
writeImports(&b, imports, rt, aliased, filterAlias, usedFilterPkg, userPlainImports, typeArgAliases)
rt.writeSpreadPolicies(&b)
b.Write(body.Bytes())
```

Replace every explicit `rtImports{}` in
`component_positional_emit_test.go` with
`newRTImports("component_positional_emit_test.gsx")`. Run
`rg -n 'rtImports\{\}' internal/codegen`; expected output is empty.

- [ ] **Step 3: Replace the generator call emitter completely**

Update `goStringSliceLit`'s comment to state that it serves policy declarations
and `Spread` prefix/exclusion arguments. Replace `emitSpreadCall` with this full
function:

```go
func emitSpreadCall(b *bytes.Buffer, rt rtImports, expr, tag string, cls *attrclass.Classifier, excludedExpr string) {
	var navNames, imageNames, srcsetNames []string
	for _, name := range cls.URLExactNames() {
		switch urlWriterMethod(tag, name) {
		case "URLImage":
			imageNames = append(imageNames, name)
		case "Srcset":
			srcsetNames = append(srcsetNames, name)
		default:
			navNames = append(navNames, name)
		}
	}
	policy := rt.spreadPolicy(navNames, imageNames, srcsetNames)
	fmt.Fprintf(b, "\t\t_gsxgw.Spread(ctx, %s, %s, %s, %s)\n",
		expr, policy, goStringSliceLit(cls.URLPrefixes()), excludedExpr)
}
```

Replace the five calls exactly:

```go
emitSpreadCall(b, rt, bagExpr, tag, cls, excludedExpr)
emitSpreadCall(b, rt, tmp, tag, cls, "nil")
emitSpreadCall(b, rt, spreadExpr, tag, cls, "nil")
emitSpreadCall(b, rt, tmp, tag, cls, "nil")
emitSpreadCall(b, rt, spreadExpr, tag, cls, "nil")
```

Do not alter
`urlWriterMethod`, `goStringSliceLit`, prefix emission, excluded-name emission,
or attribute ordering. Update the `URLExactNames` comment in
`internal/attrclass/attrclass.go` to say that codegen groups its deterministic
names by sink into immutable package-level spread policies; do not change the
method body or tests.

Run:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
gofmt -w internal/codegen/rtimports.go internal/codegen/emit.go internal/codegen/component_positional_emit_test.go internal/codegen/spread_policy_emit_test.go internal/attrclass/attrclass.go
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test ./internal/codegen \
  -run 'Test(RTImportsSpreadPolicyRegistrySharesStateAcrossCopies|GeneratedSpreadPolicies|GeneratedSpreadPolicyNamesArePackageUnique|NormalizePositionalAttrsContributor|ApplyPositionalOperandAdapter|SpreadFold)' -count=1
gopls check -severity=hint internal/codegen/rtimports.go internal/codegen/emit.go internal/codegen/component_positional_emit_test.go internal/codegen/spread_policy_emit_test.go internal/attrclass/attrclass.go
```

- [ ] **Step 4: Pin Unicode classification through generated code**

Create
`internal/corpus/testdata/cases/spread-sanitize/policy_unicode_fold.txtar`
with this authored archive only; the update command owns generated sections:

```text
# Unicode simple folding is part of spread URL classification: long-s U+017F
# folds to ASCII s, so ſrc on img must use the image URL sink.
-- input.gsx --
package views

import "github.com/gsxhq/gsx"

component UnicodePolicy(attrs gsx.Attrs) {
	<img { attrs... }/>
}
-- invoke --
UnicodePolicy(gsx.Attrs{{Key: "ſrc", Value: "javascript:alert(1)"}})
-- diagnostics.golden --
-- generated.x.go.golden --
-- render.golden --
<img ſrc="about:invalid#gsx"/>
```

Regenerate and reach a fixed point:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test ./internal/corpus -run TestCorpus -update
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test ./internal/corpus -run TestCorpus -count=1
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test ./internal/corpus -run TestExamples -count=1
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test ./internal/codegen -run 'Test(GeneratedSpreadPolicies|SpreadFold)' -count=1
GOCACHE=/tmp/gsx-runtime-optimisations-cache go run ./cmd/gsx \
  -C examples/tailwind-merge generate ./views
git add internal/corpus examples/tailwind-merge/views/card.x.go
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test ./internal/corpus -run TestCorpus -update
GOCACHE=/tmp/gsx-runtime-optimisations-cache go run ./cmd/gsx \
  -C examples/tailwind-merge generate ./views
git diff --exit-code -- internal/corpus examples/tailwind-merge/views/card.x.go
```

The final `git diff --exit-code` compares worktree against the staged index and
must be empty; it proves the second regeneration made no unstaged change.

- [ ] **Step 5: Regenerate the sibling and stage the exact candidate**

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-optimisations-cache make generate
git diff --exit-code -- templr
git add gsxr tw
GOCACHE=/tmp/gsx-runtime-optimisations-cache make generate
git diff --exit-code -- gsxr tw templr
GOCACHE=/tmp/gsx-runtime-optimisations-cache make test
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -count=1 ./...

cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
git add spread_policy.go spread_policy_test.go spread_policy_fuzz_test.go spread_policy_bench_test.go attrs.go attrs_test.go attrs_fold_fuzz_test.go toggle_test.go gsx_test.go root_attr_bench_test.go cond_merge_bench_test.go internal/codegen internal/attrclass internal/corpus examples/tailwind-merge/views/card.x.go
test -z "$(git diff --name-only)"
test -z "$(git ls-files --others --exclude-standard)"
test -z "$(git -C /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench diff --name-only)"
test -z "$(git -C /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench ls-files --others --exclude-standard)"
```

The two final tests require every candidate change to be staged. The comparison
harness rejects untracked and unstaged state, and fingerprints the staged diff.

---

### Task 4: Measure one variable and retain or restore it

- [ ] **Step 1: Create a fresh detached before pair without deleting any directory**

```sh
set -eu
core_repo=/Users/jackieli/personal/gsxhq/gsx
bench_repo=/Users/jackieli/personal/gsxhq/gsx-bench
core_base=$(cat /tmp/gsx-runtime-optimisations/spread-policy.core-base)
bench_base=$(cat /tmp/gsx-runtime-optimisations/spread-policy.bench-base)
before_root=$(mktemp -d /tmp/gsx-runtime-spread-before.XXXXXX)
rmdir "$before_root"
git -C "$core_repo" worktree add --detach "$before_root/gsx" "$core_base"
git -C "$bench_repo" worktree add --detach "$before_root/gsx-bench" "$bench_base"
printf '%s\n' "$before_root" > /tmp/gsx-runtime-optimisations/spread-policy.before-root
GOCACHE=/tmp/gsx-runtime-optimisations-cache make -C "$before_root/gsx-bench" generate
git -C "$before_root/gsx-bench" diff --exit-code -- gsxr tw templr
test "$(git -C "$before_root/gsx" rev-parse HEAD)" = "$core_base"
test "$(git -C "$before_root/gsx-bench" rev-parse HEAD)" = "$bench_base"
```

The sibling layout preserves `gsx-bench`'s `replace ../gsx`. If any command
fails, remove only the worktree paths registered by the successful
`git worktree add` commands; do not recursively delete an unknown path.

- [ ] **Step 2: Allocate a fresh result root and run all focused comparisons**

```sh
set -eu
result_root=$(mktemp -d /tmp/gsx-runtime-spread-results.XXXXXX)
printf '%s\n' "$result_root" > /tmp/gsx-runtime-optimisations/spread-policy.results-root
before_root=$(cat /tmp/gsx-runtime-optimisations/spread-policy.before-root)
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
scripts/benchcmp.sh \
  "$before_root/gsx-bench" \
  /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench \
  '^Benchmark(ForwardedAttrsGSX(Pooled|Discard)|FoldedAttrsGSX(Pooled|Discard))$' \
  "$result_root/external" .
scripts/benchcmp.sh \
  "$before_root/gsx" \
  /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx \
  '^Benchmark(RootAttrMachineryEmpty|ForwardingLeafNoURL|SpreadNoURLLarge)$' \
  "$result_root/core" .
```

Expected: each selected benchmark has ten samples in each raw file. Verify the
before commits and candidate diffs explicitly:

```sh
set -eu
result_root=$(cat /tmp/gsx-runtime-optimisations/spread-policy.results-root)
core_base=$(cat /tmp/gsx-runtime-optimisations/spread-policy.core-base)
bench_base=$(cat /tmp/gsx-runtime-optimisations/spread-policy.bench-base)
grep -q "^before-commit=$bench_base$" "$result_root/external/environment.txt"
grep -q "^before-commit=$core_base$" "$result_root/core/environment.txt"
test -s "$result_root/external/after.diff"
test -s "$result_root/core/after.diff"
```

The SHA-256 values in each `environment.txt` are the exact candidate identities.

- [ ] **Step 3: Run the complete external regression screen**

```sh
set -eu
result_root=$(cat /tmp/gsx-runtime-optimisations/spread-policy.results-root)
before_root=$(cat /tmp/gsx-runtime-optimisations/spread-policy.before-root)
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
scripts/benchcmp.sh \
  "$before_root/gsx-bench" \
  /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench \
  '^Benchmark.*GSX(Pooled|Discard|Builder|Parallel)$' \
  "$result_root/full" .
```

- [ ] **Step 4: Apply the numeric keep/reject gate**

Retain the candidate only when every condition holds:

1. `BenchmarkForwardedAttrsGSXDiscard`, `BenchmarkForwardingLeafNoURL`, and
   `BenchmarkSpreadNoURLLarge` each improve by at least **7%** with benchstat
   `p < 0.05`. Seven percent is above the largest 5% spread observed in the
   corrected focused baseline.
2. `BenchmarkForwardedAttrsGSXPooled` improves by at least **5%** with
   `p < 0.05`; its corrected baseline spread was 1%.
3. All focused benchmarks retain exactly the same `B/op` and `allocs/op`.
   This candidate moves immutable metadata out of renders and is not allowed to
   hide an allocation trade.
4. Neither FoldedAttrs destination regresses by **7% or more** with `p < 0.05`.
5. No non-parallel benchmark in the full GSX screen regresses by **7% or more**
   with `p < 0.05`; no parallel benchmark regresses by **12% or more** with
   `p < 0.05`. The separate parallel limit is above the baseline's 7% spread.
6. `BenchmarkRootAttrMachineryEmpty` does not regress by **5% or more** with
   `p < 0.05`, remains zero-allocation, and static/no-spread generated paths
   have no policy declaration.

Do not average a passing benchmark with a failing one. Do not use the local
classifier microbenchmark, templ numbers, or a non-significant delta to waive a
gate.

If any gate fails, restore both candidate trees from the saved bases:

```sh
set -eu
core=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
bench=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
core_base=$(cat /tmp/gsx-runtime-optimisations/spread-policy.core-base)
bench_base=$(cat /tmp/gsx-runtime-optimisations/spread-policy.bench-base)
git -C "$core" restore --source "$core_base" --staged --worktree -- .
git -C "$bench" restore --source "$bench_base" --staged --worktree -- .
test -z "$(git -C "$core" status --porcelain=v1)"
test -z "$(git -C "$bench" status --porcelain=v1)"
```

Then edit only the audit/performance documentation in Task 5. If every gate
passes, keep the staged candidate and continue.

- [ ] **Step 5: Perform two independent reviews and commit a retained candidate**

For a retained candidate, first run a specification review that traces every
scope invariant against tests and generated output. Then run a code-quality
review that challenges constructor copying, hash-collision verification,
non-ASCII fallback, map immutability, registry determinism, declaration scope,
and whether any render-time work moved rather than disappeared. Resolve both
reviews before committing.

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -race -run 'TestSpreadPolicyConcurrentReads' -count=1 .
GOCACHE=/tmp/gsx-runtime-optimisations-cache make check
test -z "$(git diff --name-only)"
test -z "$(git ls-files --others --exclude-standard)"
git diff --check --cached
gopls check -severity=hint spread_policy.go attrs.go internal/codegen/rtimports.go internal/codegen/emit.go
git commit -m 'perf: reuse exact spread classification policies'

cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-optimisations-cache make generate
git diff --exit-code -- gsxr tw templr
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -count=1 ./...
git commit -m 'chore: regenerate spread policy benchmarks'
```

These commits are conditional. A rejected candidate produces no runtime,
codegen, corpus, or generated-output commit.

---

### Task 5: Record the outcome, run authoritative gates, and decide the next plan

- [ ] **Step 1: Update the audit note and current performance snapshot for either outcome**

The audit-note experiment section must record:

- prerequisite, candidate-base, and outcome commit IDs for both repositories;
- Go version, `GOMAXPROCS`, machine, staged-diff hashes, and all raw paths;
- median before/after time, delta, p-value, bytes, and allocations for every
  focused benchmark;
- every full-screen regression checked against its applicable 7% or 12% gate;
- the retained or rejected decision with no rounding that changes a gate;
- classifier-local results clearly labelled explanatory rather than decisive;
- the unchanged Unicode, ordering, duplicate, escaping, error, and race results.

Produce a fresh absolute full-suite snapshot after the keep/restore decision:

```sh
set -eu
result_root=$(cat /tmp/gsx-runtime-optimisations/spread-policy.results-root)
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOMAXPROCS=32 GOCACHE=/tmp/gsx-runtime-optimisations-cache \
  go test -run '^$' -bench . -benchmem -count=10 . \
  > "$result_root/final-full-suite.txt"
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d \
  "$result_root/final-full-suite.txt" \
  > "$result_root/final-full-suite.benchstat.txt"
cat "$result_root/final-full-suite.benchstat.txt"
```

Update `docs/guide/performance.md` from that snapshot, whether the candidate was
retained or rejected. Cite the exact outcome core/benchmark commits, date,
machine, Go version, inputs, and pooled destination. Do not copy values from the
July 1 snapshot or from templ into a GSX acceptance claim. Review
`docs/ROADMAP.md` line by line and edit only a claim made inaccurate by the
outcome.

- [ ] **Step 2: Build canonical docs with the CI Node major**

```sh
set -eu
site_repo=/Users/jackieli/personal/gsxhq/gsxhq.github.io
site_worktree=$(mktemp -d /tmp/gsx-runtime-docs-site.XXXXXX)
rmdir "$site_worktree"
cleanup_site() {
  git -C "$site_repo" worktree remove --force "$site_worktree" 2>/dev/null || true
}
trap cleanup_site EXIT HUP INT TERM
git -C "$site_repo" worktree add --detach "$site_worktree" HEAD
cd "$site_worktree"
GSX_DOCS_SRC=/Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx \
VITE_GSX_PLAYGROUND_API=https://example.invalid \
npx --yes --package=node@24 --call \
  'test "$(node -p process.versions.node | cut -d. -f1)" = 24 && npm ci && npm run build'
cleanup_site
trap - EXIT HUP INT TERM
```

Expected: the VitePress build succeeds against the active canonical guide with
Node 24, matching the core CI docs job. The detached site worktree is removed
through Git's worktree command, not recursive deletion.

- [ ] **Step 3: Run every authoritative core and sibling gate from a fixed point**

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test ./internal/corpus -run TestCorpus -count=1
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test ./internal/gsxfmt -run TestFmtCorpus -count=1
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -run 'Test(URLSanitize|URLSanitizeImage|SrcsetSanitize|Spread)' -count=1 .
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -race -count=1 .
GOCACHE=/tmp/gsx-runtime-optimisations-cache make ci
GOCACHE=/tmp/gsx-runtime-optimisations-cache make lint
git diff --check

cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
GOCACHE=/tmp/gsx-runtime-optimisations-cache make generate
git diff --exit-code -- gsxr tw templr
GOCACHE=/tmp/gsx-runtime-optimisations-cache make test
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -count=1 ./...
GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -race -count=1 ./...
```

Run `gopls check -severity=hint` on every changed Go file relative to the saved
candidate base without relying on a pipeline:

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
core_base=$(cat /tmp/gsx-runtime-optimisations/spread-policy.core-base)
git diff --name-only "$core_base" -- '*.go' > /tmp/gsx-runtime-optimisations/changed-go-files.txt
while IFS= read -r file; do
  if [ -n "$file" ]; then
    gopls check -severity=hint "$file"
  fi
done < /tmp/gsx-runtime-optimisations/changed-go-files.txt
```

- [ ] **Step 4: Collect clean post-slice profiles outside the repositories**

Profiles must be CPU-only and memory-only runs so allocation instrumentation
does not contaminate normal CPU attribution:

```sh
set -eu
profile_dir=$(mktemp -d /tmp/gsx-runtime-post-spread-profiles.XXXXXX)
printf '%s\n' "$profile_dir" > /tmp/gsx-runtime-optimisations/post-spread.profile-dir
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench
for name in ForwardedAttrs FoldedAttrs; do
  GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -run '^$' \
    -bench "^Benchmark${name}GSXPooled$" -benchtime=5s \
    -cpuprofile "$profile_dir/${name}.cpu" -outputdir "$profile_dir" .
  GOCACHE=/tmp/gsx-runtime-optimisations-cache go test -run '^$' \
    -bench "^Benchmark${name}GSXPooled$" -benchtime=5s \
    -memprofile "$profile_dir/${name}.mem" -memprofilerate=1 \
    -outputdir "$profile_dir" .
  go tool pprof -top -nodecount=40 "$profile_dir/${name}.cpu" \
    > "$profile_dir/${name}.cpu.top"
  go tool pprof -top -alloc_objects -nodecount=40 "$profile_dir/${name}.mem" \
    > "$profile_dir/${name}.objects.top"
  go tool pprof -top -alloc_space -nodecount=40 "$profile_dir/${name}.mem" \
    > "$profile_dir/${name}.space.top"
done
test ! -e /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench/gsx-bench.test
```

Update the audit note with post-slice attribution without summing overlapping
cumulative frames.

- [ ] **Step 5: Make the explicit Candidate 2 planning decision; keep Candidate 3 deferred**

Candidate 2 is eligible for its own plan only if the fresh FoldedAttrs
allocation profiles show both:

- the flat `ConcatAttrs` frame plus the two selected-branch literal allocation
  frames own at least **30% of allocation objects**; and
- the same non-overlapping flat frames own at least **50% of allocated bytes**.

These thresholds are below the audited pre-slice 39.7% objects and 86.0% bytes,
but high enough to require that generated folded-bag materialisation remains a
dominant, removable cost after Candidate 1. If either threshold fails, record
Candidate 2 as unselected and do not create a plan.

If both pass, use `superpowers:writing-plans` to create the separate
`docs/superpowers/plans/2026-07-21-folded-element-attribute-materialisation.md`
from the committed post-Candidate-1 state. That plan must receive its own
checksum-stable adversarial review and must not implement work during this
task. Record Candidate 3 as deferred until after Candidate 2 has a measured
outcome; do not create or prototype a component ABI here.

- [ ] **Step 6: Commit documentation and the conditional next plan, then clean up comparison worktrees**

```sh
set -eu
cd /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx
git add docs/superpowers/notes/2026-07-21-runtime-render-performance-audit.md docs/guide/performance.md
if ! git diff --quiet -- docs/ROADMAP.md; then
  git add docs/ROADMAP.md
fi
if [ -f docs/superpowers/plans/2026-07-21-folded-element-attribute-materialisation.md ]; then
  git add docs/superpowers/plans/2026-07-21-folded-element-attribute-materialisation.md
fi
git diff --check --cached
git commit -m 'docs(perf): record spread policy decision'

before_root=$(cat /tmp/gsx-runtime-optimisations/spread-policy.before-root)
git -C /Users/jackieli/personal/gsxhq/gsx worktree remove --force "$before_root/gsx"
git -C /Users/jackieli/personal/gsxhq/gsx-bench worktree remove --force "$before_root/gsx-bench"
rmdir "$before_root"
rm -f /tmp/gsx-runtime-optimisations/spread-policy.before-root
test -z "$(git -C /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx status --porcelain=v1)"
test -z "$(git -C /Users/jackieli/personal/gsxhq/.worktrees/runtime-render-audit/gsx-bench status --porcelain=v1)"
```

Do not publish, push, open a pull request, merge, release, or implement the
conditional Candidate 2 plan under this task.
