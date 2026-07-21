package codegen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectComponentMatchesCurrentNodeAtFailingWriteBoundaries(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the Go toolchain")
	}

	root := tempModule(t, "example.com/directsemantics")
	dir := filepath.Join(root, "views")
	writeFile(t, dir, "helpers.go", `package views

var trace []string

func resetTrace() { trace = nil }
func markArgument() string { trace = append(trace, "argument"); return "value" }
func markBody(value string) string { trace = append(trace, "body"); return value }
`)
	writeFile(t, dir, "views.gsx", `package views

component ChildSentinel(result error) {
	<div>{ "x" }</div>
	{{ return result }}
}

component ChildNil() {
	<div>{ "x" }</div>
	{{ return nil }}
}

component ParentSentinel(result error) {
	<p>before</p><ChildSentinel result={ result }/><p>after</p>
}

component ParentNil() {
	<p>before</p><ChildNil/><p>after</p>
}

component Guarded(value string) {
	<span>{ markBody(value) }</span>
}

component PriorError() {
	<p>before</p><Guarded value={ markArgument() }/>
}
`)

	result, err := GenerateDirs(root, []string{dir}, Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	dirResult := result[dir]
	if hasError(dirResult.Diags) {
		t.Fatalf("diagnostics = %+v", dirResult.Diags)
	}
	generated := generatedFor(t, dirResult, "views.gsx")
	for _, want := range []string{
		`_gsxgw.NodeResult(_gsxrenderChildSentinel(ctx, _gsxgw, result))`,
		`_gsxgw.NodeResult(_gsxrenderChildNil(ctx, _gsxgw))`,
		`_gsxgw.NodeResult(_gsxrenderGuarded(ctx, _gsxgw, markArgument()))`,
	} {
		if !strings.Contains(generated, want) {
			t.Fatalf("generated source lacks %q:\n%s", want, generated)
		}
	}
	writeFile(t, dir, "views.x.go", generated)
	writeFile(t, dir, "semantics_test.go", `package views

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/gsxhq/gsx"
)

var (
	writeSentinel = errors.New("write sentinel")
	childSentinel = errors.New("child sentinel")
)

type failAtWriter struct {
	failAt   int
	attempts int
	buf      bytes.Buffer
}

func (w *failAtWriter) Write(p []byte) (int, error) {
	w.attempts++
	if w.attempts == w.failAt {
		return 0, writeSentinel
	}
	return w.buf.Write(p)
}

type result struct {
	output   string
	attempts int
	err      error
}

func render(node gsx.Node, failAt int) result {
	w := &failAtWriter{failAt: failAt}
	err := node.Render(context.Background(), w)
	return result{output: w.buf.String(), attempts: w.attempts, err: err}
}

func currentNodeSentinel(result error) gsx.Node {
	return gsx.Func(func(ctx context.Context, out io.Writer) error {
		gw := gsx.W(out)
		gw.S("<p>before</p>")
		gw.Node(ctx, ChildSentinel(result))
		gw.S("<p>after</p>")
		return gw.Err()
	})
}

func currentNodeNil() gsx.Node {
	return gsx.Func(func(ctx context.Context, out io.Writer) error {
		gw := gsx.W(out)
		gw.S("<p>before</p>")
		gw.Node(ctx, ChildNil())
		gw.S("<p>after</p>")
		return gw.Err()
	})
}

func currentNodePriorError() gsx.Node {
	return gsx.Func(func(ctx context.Context, out io.Writer) error {
		gw := gsx.W(out)
		gw.S("<p>before</p>")
		gw.Node(ctx, Guarded(markArgument()))
		return gw.Err()
	})
}

func assertExact(t *testing.T, name string, direct, reference result, want result) {
	t.Helper()
	for label, got := range map[string]result{"direct": direct, "current Node": reference} {
		if got.output != want.output || got.attempts != want.attempts || got.err != want.err {
			t.Errorf("%s %s = {output:%q attempts:%d err:%v}, want {output:%q attempts:%d err:%v}",
				name, label, got.output, got.attempts, got.err,
				want.output, want.attempts, want.err)
		}
	}
}

func TestGeneratedDirectMatchesCurrentNodeFailingWrites(t *testing.T) {
	sentinelCases := []result{
		{output: "", attempts: 1, err: writeSentinel},
		{output: "<p>before</p>", attempts: 2, err: childSentinel},
		{output: "<p>before</p><div>", attempts: 3, err: childSentinel},
		{output: "<p>before</p><div>x", attempts: 4, err: childSentinel},
	}
	for i, want := range sentinelCases {
		failAt := i + 1
		assertExact(t, "sentinel", render(ParentSentinel(childSentinel), failAt),
			render(currentNodeSentinel(childSentinel), failAt), want)
	}

	nilCases := []result{
		{output: "", attempts: 1, err: writeSentinel},
		{output: "<p>before</p><p>after</p>", attempts: 3, err: nil},
		{output: "<p>before</p><div><p>after</p>", attempts: 4, err: nil},
		{output: "<p>before</p><div>x<p>after</p>", attempts: 5, err: nil},
		{output: "<p>before</p><div>x</div>", attempts: 5, err: writeSentinel},
	}
	for i, want := range nilCases {
		failAt := i + 1
		assertExact(t, "nil", render(ParentNil(), failAt),
			render(currentNodeNil(), failAt), want)
	}
}

func TestGeneratedDirectEvaluatesArgumentsButSkipsChildAfterPriorError(t *testing.T) {
	resetTrace()
	direct := render(PriorError(), 1)
	directTrace := append([]string(nil), trace...)
	resetTrace()
	reference := render(currentNodePriorError(), 1)
	referenceTrace := append([]string(nil), trace...)
	want := result{output: "", attempts: 1, err: writeSentinel}
	assertExact(t, "prior error", direct, reference, want)
	for label, got := range map[string][]string{"direct": directTrace, "current Node": referenceTrace} {
		if len(got) != 1 || got[0] != "argument" {
			t.Errorf("%s trace = %v, want [argument]", label, got)
		}
	}
}
`)

	command := exec.Command("go", "test", "-count=1", ".")
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated direct/current-Node probe: %v\n%s", err, output)
	}
}
