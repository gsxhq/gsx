package typebundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	goversion "go/version"
	"os/exec"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

func TestKnownGoTargetsMatchToolchain(t *testing.T) {
	out, err := exec.Command("go", "tool", "dist", "list").Output()
	if err != nil {
		t.Fatalf("go tool dist list: %v", err)
	}
	toolchainTargets := make(map[string]bool)
	for target := range strings.FieldsSeq(string(out)) {
		toolchainTargets[target] = true
	}
	if !reflect.DeepEqual(knownGoTargets, toolchainTargets) {
		t.Fatalf("known Go targets = %v, toolchain targets = %v; update the envelope target table with the pinned toolchain", knownGoTargets, toolchainTargets)
	}
}

func TestMaxLanguageVersionMatchesToolchain(t *testing.T) {
	if got := goversion.Lang(runtime.Version()); got != maxLanguageVersion {
		t.Fatalf("max language version = %s, toolchain language = %s; update the bundle reader contract with the pinned toolchain", maxLanguageVersion, got)
	}
}

func TestMaxToolchainVersionMatchesToolchain(t *testing.T) {
	out, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		t.Fatalf("go env GOVERSION: %v", err)
	}
	if got := strings.TrimSpace(string(out)); got != maxToolchainVersion {
		t.Fatalf("max toolchain version = %s, running toolchain = %s; update the bundle reader contract and generated archive with the pinned toolchain", maxToolchainVersion, got)
	}
}

type mapImporter map[string]*types.Package

func (m mapImporter) Import(path string) (*types.Package, error) {
	if p, ok := m[path]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("not bundled: %q", path)
}

func TestBundleTargetEnvelopeIsExactAndCrossArchitecture(t *testing.T) {
	target := Target{
		Compiler: "gc", GOOS: "linux", GOARCH: "arm", CGOEnabled: false,
		ToolchainVersion: "go1.26.1", LanguageVersion: "go1.23",
		BuildTags: []string{"playground"}, ToolTags: []string{"arm.7"}, ReleaseTags: []string{"go1.23", "go1.26"},
	}
	pkg := types.NewPackage("example.com/empty", "empty")
	pkg.MarkComplete()
	data, err := Write(token.NewFileSet(), target, []*types.Package{pkg})
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := Read(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bundle.Target, target) {
		t.Fatalf("target = %#v, want %#v", bundle.Target, target)
	}
	if got := bundle.Sizes.Sizeof(types.Typ[types.Uint]); got != 4 {
		t.Fatalf("Sizeof(uint) = %d, want 4 for gc/arm", got)
	}
}

func TestBundleEnvelopeRejectsMissingMalformedAndUnknownTarget(t *testing.T) {
	if _, err := Read([]byte("old raw export data")); err == nil {
		t.Fatal("Read accepted versionless archive")
	}
	if _, err := decodeTarget([]byte(`{"compiler":"gc"}`)); err == nil {
		t.Fatal("decodeTarget accepted incomplete metadata")
	}
	metadata, err := json.Marshal(testTarget())
	if err != nil {
		t.Fatal(err)
	}
	var noncanonical bytes.Buffer
	if err := json.Indent(&noncanonical, metadata, "", "  "); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeTarget(noncanonical.Bytes()); err == nil || !strings.Contains(err.Error(), "canonical") {
		t.Fatalf("decodeTarget noncanonical metadata error = %v, want canonical-form rejection", err)
	}
	if _, err := decodeTarget(append(metadata, []byte(` {}`)...)); err == nil {
		t.Fatal("decodeTarget accepted trailing JSON")
	}
	if _, err := decodeTarget([]byte(`{"compiler":"gc","compiler":"gccgo"}`)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("decodeTarget duplicate field error = %v, want duplicate-field rejection", err)
	}

	for name, mutate := range map[string]func(*Target){
		"unsupported compiler": func(target *Target) { target.Compiler = "gccgo" },
		"unknown GOOS":         func(target *Target) { target.GOOS = "definitely-not-a-goos" },
		"invalid OS arch pair": func(target *Target) { target.GOOS, target.GOARCH = "plan9", "arm64" },
		"unknown toolchain":    func(target *Target) { target.ToolchainVersion = "devel unknown" },
		"future toolchain":     func(target *Target) { target.ToolchainVersion = "go1.27" },
		"missing language":     func(target *Target) { target.LanguageVersion = "" },
		"language lacks minor": func(target *Target) { target.LanguageVersion = "go1" },
		"future language": func(target *Target) {
			target.ToolchainVersion, target.LanguageVersion = "go1.999", "go1.999"
		},
		"missing tag context": func(target *Target) { target.ToolTags = nil },
	} {
		t.Run(name, func(t *testing.T) {
			target := testTarget()
			mutate(&target)
			pkg := types.NewPackage("example.com/empty", "empty")
			pkg.MarkComplete()
			if _, err := Write(token.NewFileSet(), target, []*types.Package{pkg}); err == nil {
				t.Fatal("Write accepted invalid target")
			}
		})
	}

	// A valid magic prefix with an impossible metadata length must fail before
	// gcexportdata sees the payload.
	var malformed bytes.Buffer
	malformed.WriteString(envelopeMagic)
	if err := binary.Write(&malformed, binary.BigEndian, uint32(100)); err != nil {
		t.Fatal(err)
	}
	malformed.WriteString("{}")
	if _, err := Read(malformed.Bytes()); err == nil {
		t.Fatal("Read accepted truncated target metadata")
	}
}

func TestReadRejectsUnsupportedProducerTarget(t *testing.T) {
	pkg := types.NewPackage("example.com/empty", "empty")
	pkg.MarkComplete()
	data, err := Write(token.NewFileSet(), testTarget(), []*types.Package{pkg})
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*Target){
		"non-gc compiler":  func(target *Target) { target.Compiler = "gccgo" },
		"future toolchain": func(target *Target) { target.ToolchainVersion = "go1.27" },
	} {
		t.Run(name, func(t *testing.T) {
			forged := rewriteEnvelopeTarget(t, data, mutate)
			if _, err := Read(forged); err == nil {
				t.Fatalf("Read accepted forged %s target metadata", name)
			}
		})
	}
}

func rewriteEnvelopeTarget(t *testing.T, data []byte, mutate func(*Target)) []byte {
	t.Helper()
	headerOffset := len(envelopeMagic)
	metadataSize := binary.BigEndian.Uint32(data[headerOffset : headerOffset+4])
	payloadSize := binary.BigEndian.Uint64(data[headerOffset+4 : headerOffset+12])
	contentOffset := headerOffset + 12 + sha256.Size
	metadataEnd := contentOffset + int(metadataSize)
	var target Target
	if err := json.Unmarshal(data[contentOffset:metadataEnd], &target); err != nil {
		t.Fatal(err)
	}
	mutate(&target)
	metadata, err := json.Marshal(target.canonical())
	if err != nil {
		t.Fatal(err)
	}
	payload := data[metadataEnd:]
	if uint64(len(payload)) != payloadSize {
		t.Fatalf("payload size = %d, envelope = %d", len(payload), payloadSize)
	}
	var forged bytes.Buffer
	forged.WriteString(envelopeMagic)
	if err := binary.Write(&forged, binary.BigEndian, uint32(len(metadata))); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&forged, binary.BigEndian, payloadSize); err != nil {
		t.Fatal(err)
	}
	digest := sha256.New()
	digest.Write(metadata)
	digest.Write(payload)
	forged.Write(digest.Sum(nil))
	forged.Write(metadata)
	forged.Write(payload)
	return forged.Bytes()
}

func TestWriteRejectsDuplicatePackagePaths(t *testing.T) {
	first := types.NewPackage("example.com/duplicate", "duplicate")
	first.MarkComplete()
	second := types.NewPackage("example.com/duplicate", "duplicate")
	second.MarkComplete()
	if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{first, second}); err == nil {
		t.Fatal("Write accepted duplicate package paths")
	}
}

func TestWriteRejectsNonClosedOrIncoherentPackageSets(t *testing.T) {
	t.Run("missing imported package", func(t *testing.T) {
		dependency := types.NewPackage("example.com/dependency", "dependency")
		dependency.MarkComplete()
		root := types.NewPackage("example.com/root", "root")
		root.SetImports([]*types.Package{dependency})
		root.MarkComplete()
		if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{root}); err == nil || !strings.Contains(err.Error(), "not transitively closed") {
			t.Fatalf("Write error = %v, want transitive-closure rejection", err)
		}
	})

	t.Run("incomplete package", func(t *testing.T) {
		incomplete := types.NewPackage("example.com/incomplete", "incomplete")
		if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{incomplete}); err == nil || !strings.Contains(err.Error(), "incomplete") {
			t.Fatalf("Write error = %v, want incomplete-package rejection", err)
		}
	})

	t.Run("different imported identity", func(t *testing.T) {
		listed := types.NewPackage("example.com/dependency", "dependency")
		listed.MarkComplete()
		imported := types.NewPackage("example.com/dependency", "dependency")
		imported.MarkComplete()
		root := types.NewPackage("example.com/root", "root")
		root.SetImports([]*types.Package{imported})
		root.MarkComplete()
		if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{root, listed}); err == nil || !strings.Contains(err.Error(), "different package identity") {
			t.Fatalf("Write error = %v, want identity-coherence rejection", err)
		}
	})

	t.Run("duplicate imported package", func(t *testing.T) {
		dependency := types.NewPackage("example.com/dependency", "dependency")
		dependency.MarkComplete()
		root := types.NewPackage("example.com/root", "root")
		root.SetImports([]*types.Package{dependency, dependency})
		root.MarkComplete()
		if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{root, dependency}); err == nil || !strings.Contains(err.Error(), "duplicate import") {
			t.Fatalf("Write error = %v, want duplicate-import rejection", err)
		}
	})

	t.Run("forged top-level unsafe package", func(t *testing.T) {
		forged := types.NewPackage("unsafe", "unsafe")
		forged.MarkComplete()
		if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{forged}); err == nil || !strings.Contains(err.Error(), "types.Unsafe") {
			t.Fatalf("Write error = %v, want forged unsafe identity rejection", err)
		}
	})

	t.Run("forged imported unsafe package", func(t *testing.T) {
		forged := types.NewPackage("unsafe", "unsafe")
		forged.MarkComplete()
		root := types.NewPackage("example.com/root", "root")
		root.SetImports([]*types.Package{forged})
		root.MarkComplete()
		if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{root}); err == nil || !strings.Contains(err.Error(), "types.Unsafe") {
			t.Fatalf("Write error = %v, want forged unsafe import identity rejection", err)
		}
	})

	for _, name := range []string{"", "_", "not-valid!"} {
		t.Run("invalid package name "+name, func(t *testing.T) {
			pkg := types.NewPackage("example.com/invalid-name", name)
			pkg.MarkComplete()
			if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{pkg}); err == nil || !strings.Contains(err.Error(), "package name") {
				t.Fatalf("Write error = %v, want invalid package-name rejection", err)
			}
		})
	}

	t.Run("self import cycle", func(t *testing.T) {
		pkg := types.NewPackage("example.com/self", "self")
		pkg.SetImports([]*types.Package{pkg})
		pkg.MarkComplete()
		if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{pkg}); err == nil || !strings.Contains(err.Error(), "import cycle") {
			t.Fatalf("Write error = %v, want self-import-cycle rejection", err)
		}
	})

	t.Run("mutual import cycle", func(t *testing.T) {
		alpha := types.NewPackage("example.com/alpha", "alpha")
		beta := types.NewPackage("example.com/beta", "beta")
		alpha.SetImports([]*types.Package{beta})
		beta.SetImports([]*types.Package{alpha})
		alpha.MarkComplete()
		beta.MarkComplete()
		if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{alpha, beta}); err == nil || !strings.Contains(err.Error(), "import cycle") {
			t.Fatalf("Write error = %v, want mutual-import-cycle rejection", err)
		}
	})

	t.Run("foreign-owned scope object", func(t *testing.T) {
		foreign := types.NewPackage("example.com/foreign", "foreign")
		foreign.MarkComplete()
		root := types.NewPackage("example.com/root", "root")
		root.Scope().Insert(types.NewVar(token.NoPos, foreign, "Leaked", types.Typ[types.Int]))
		root.MarkComplete()
		if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{root, foreign}); err == nil || !strings.Contains(err.Error(), "owned by") {
			t.Fatalf("Write error = %v, want foreign scope-owner rejection", err)
		}
	})

	t.Run("semantic type reference missing from package set", func(t *testing.T) {
		dependency := types.NewPackage("example.com/hidden-dependency", "dependency")
		typeName := types.NewTypeName(token.NoPos, dependency, "Value", nil)
		valueType := types.NewNamed(typeName, types.Typ[types.Int], nil)
		dependency.Scope().Insert(typeName)
		dependency.MarkComplete()
		root := types.NewPackage("example.com/root", "root")
		root.Scope().Insert(types.NewVar(token.NoPos, root, "Value", valueType))
		root.MarkComplete()
		if _, err := Write(token.NewFileSet(), testTarget(), []*types.Package{root}); err == nil || !strings.Contains(err.Error(), "semantic package set") {
			t.Fatalf("Write error = %v, want hidden semantic-dependency rejection", err)
		}
	})
}

func TestReadRejectsTrailingBundlePayload(t *testing.T) {
	pkg := types.NewPackage("example.com/empty", "empty")
	pkg.MarkComplete()
	data, err := Write(token.NewFileSet(), testTarget(), []*types.Package{pkg})
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, []byte("trailing payload")...)
	if _, err := Read(data); err == nil {
		t.Fatal("Read accepted trailing bundle payload")
	}
}

func TestWriteCanonicalizesTargetTagOrder(t *testing.T) {
	pkg := types.NewPackage("example.com/empty", "empty")
	pkg.MarkComplete()
	unsorted := testTarget()
	unsorted.BuildTags = []string{"zeta", "alpha"}
	unsorted.ToolTags = []string{"tool.zeta", "tool.alpha"}
	unsorted.ReleaseTags = []string{"go1.26", "go1.25"}
	sorted := testTarget()
	sorted.BuildTags = []string{"alpha", "zeta"}
	sorted.ToolTags = []string{"tool.alpha", "tool.zeta"}
	sorted.ReleaseTags = []string{"go1.25", "go1.26"}

	first, err := Write(token.NewFileSet(), unsorted, []*types.Package{pkg})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Write(token.NewFileSet(), sorted, []*types.Package{pkg})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("semantically identical target tag sets produced different bundles")
	}
	bundle, err := Read(first)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bundle.Target, sorted) {
		t.Fatalf("decoded target = %#v, want canonical %#v", bundle.Target, sorted)
	}
}

func TestWriteCanonicalizesPackageOrder(t *testing.T) {
	alpha := types.NewPackage("example.com/alpha", "alpha")
	alpha.MarkComplete()
	beta := types.NewPackage("example.com/beta", "beta")
	beta.MarkComplete()

	first, err := Write(token.NewFileSet(), testTarget(), []*types.Package{beta, alpha})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Write(token.NewFileSet(), testTarget(), []*types.Package{alpha, beta})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("semantically identical package sets produced different bundles")
	}
}

func testTarget() Target {
	return Target{
		Compiler:         runtime.Compiler,
		GOOS:             runtime.GOOS,
		GOARCH:           runtime.GOARCH,
		CGOEnabled:       true,
		ToolchainVersion: "go1.26",
		LanguageVersion:  "go1.25",
		BuildTags:        []string{},
		ToolTags:         []string{"test.tool"},
		ReleaseTags:      []string{"go1.25", "go1.26"},
	}
}

// TestBundleRoundTripNoSubprocess is the core WASM-feasibility proof: bundle a
// fixed import allowlist at "build time" (packages.Load shells out — allowed
// here), then with PATH stripped so ANY exec("go") would fail, reconstruct the
// types and type-check a snippet that uses them. If the consume path shelled
// out, the empty PATH would break it. Passing proves the consume path is
// subprocess-free — exactly what a browser WASM build requires.
func TestBundleRoundTripNoSubprocess(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load test in -short mode")
	}

	// --- BUILD PHASE (shell-out allowed) ---
	fset := token.NewFileSet()
	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedTypes | packages.NeedTypesSizes | packages.NeedImports | packages.NeedDeps,
		Fset: fset,
	}
	loaded, err := packages.Load(cfg, "fmt", "strconv", "strings")
	if err != nil {
		t.Fatalf("packages.Load: %v", err)
	}
	// Collect the transitive closure (every loaded package + dep with type info).
	closure := map[string]*types.Package{}
	packages.Visit(loaded, nil, func(p *packages.Package) {
		if p.Types != nil {
			closure[p.PkgPath] = p.Types
		}
	})
	if len(closure) == 0 {
		t.Fatal("no packages loaded")
	}
	var pkgs []*types.Package
	var loadedSizes types.Sizes
	packages.Visit(loaded, nil, func(p *packages.Package) {
		if loadedSizes == nil && p.TypesSizes != nil {
			loadedSizes = p.TypesSizes
		}
	})
	for _, p := range closure {
		pkgs = append(pkgs, p)
	}
	if loadedSizes == nil {
		t.Fatal("packages.Load returned no target type sizes")
	}
	target := testTarget()
	data, err := Write(fset, target, pkgs)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	t.Logf("bundle: %d packages, %d bytes", len(pkgs), len(data))

	// --- CONSUME PHASE (prove NO subprocess) ---
	// Strip PATH and disable the packages driver: any attempt to exec `go` now
	// fails. go/types + gcexportdata must carry the whole load.
	t.Setenv("PATH", "")
	t.Setenv("GOPACKAGESDRIVER", "off")

	bundle, err := Read(data)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(bundle.Target, target) {
		t.Fatalf("target = %#v, want %#v", bundle.Target, target)
	}
	for _, want := range []string{"fmt", "strconv", "strings", "io"} {
		if bundle.Packages[want] == nil {
			t.Fatalf("reconstructed bundle missing %q", want)
		}
	}

	// Type-check a snippet against the reconstructed importer.
	const src = `package p

import (
	"fmt"
	"strconv"
)

func F() string { return fmt.Sprintf("%d", 42) + strconv.Itoa(7) }
`
	cfset := token.NewFileSet()
	f, perr := parser.ParseFile(cfset, "p.go", src, 0)
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	conf := types.Config{Importer: mapImporter(bundle.Packages), Sizes: bundle.Sizes, GoVersion: bundle.Target.LanguageVersion}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}
	pkg, cerr := conf.Check("p", cfset, []*ast.File{f}, info)
	if cerr != nil {
		t.Fatalf("type-check against reconstructed bundle: %v", cerr)
	}
	// F must resolve to func() string.
	obj := pkg.Scope().Lookup("F")
	if obj == nil {
		t.Fatal("F not found")
	}
	sig, ok := obj.Type().(*types.Signature)
	if !ok || sig.Results().Len() != 1 || sig.Results().At(0).Type().String() != "string" {
		t.Fatalf("F resolved to %s, want func() string", obj.Type())
	}
}
