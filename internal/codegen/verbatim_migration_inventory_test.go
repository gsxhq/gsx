package codegen

// Section-aware structural migration ledger for the verbatim-component-signature
// cutover (Task 7 of the migration plan). This file is a metadata-only inventory
// utility; it is deleted by Task 8 once the cutover lands.
//
// WHAT THIS IS
//
// The verbatim-signature cutover removes the synthesized <Name>Props struct ABI
// and re-homes children/attrs onto declared reserved parameters. That touches a
// large, fixed set of source units (txtar corpus cases, Go fixtures, docs,
// scaffolds). Before the atomic cutover (Task 8) can edit them, we need a pinned,
// reviewed inventory that enumerates every unit and records the migration action
// Task 8 must take. This test IS that inventory.
//
// WHAT THIS IS NOT
//
// Deliberately, there is NO replacement engine, no migrationEdit, no decoded-GSX
// offset mapping back into host Go source, and no write-migration flag. The ledger
// never modifies, unquotes, or rewrites any enumerated source. It only records
// metadata (path, section identity, content hashes) and a reviewed classification.
//
// CLASSIFICATION METHODOLOGY (the "review")
//
// Enumeration assigns each unit a migrationAction via a deterministic, inspectable
// ruleset (classifyUnit) rather than an ad-hoc eyeball of thousands of sections:
//
//   - Generated outputs (golden sections, *.x.go sections, and the standalone
//     generated files coverage.golden / examples.json / card.x.go /
//     docs/guide/syntax/_generated/**) are `regenerate`: Task 8 re-runs the
//     generator, never hand-edits them.
//   - Every other (authored) section is scanned for the exact syntactic surface
//     the cutover removes -- the reserved `children`/`attrs` identifiers, the cut
//     component struct-splat, `<Name>Props` struct literals, RenderComponent call
//     sites, WithFieldMatcher, unnamed attrs-only factory return types, and BYO
//     struct-shaped sole params. The BYO marker is STRUCTURAL, not name-based: a
//     component's sole non-receiver parameter whose type is a bare (unqualified,
//     non-pointer/slice/map/variadic) named type that is not a builtin scalar and
//     not the component's own generic type parameter -- mirroring the exact
//     exclusion shape of soleParamTypeName in byo.go, the real BYO trigger. A
//     section with any such marker is `manual-edit` carrying a review note built
//     from the matched audit labels; a section with no marker is
//     `reviewed-no-change`.
//
// The marker scan is a conservative over-approximation for the known removed
// syntax: it never leaves a migration-affected unit as reviewed-no-change for
// those markers. Its one genuine blind spot -- a BYO struct component whose
// call-site field-address fill is lexically identical to an ordinary named-prop
// fill (`<Card Title=.../>`) -- cannot be resolved without the Task-8 signature
// analyzer and is surfaced as a review-note ambiguity / escalation, never guessed.
//
// The manifest is committed as the reviewed artifact. `-update-verbatim-inventory`
// refreshes metadata while preserving any hand-refined record whose path, section
// identity, and before-hash still match, so re-running it is idempotent and does
// not clobber human review.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gsxhq/gsx/internal/txtar"
)

var updateVerbatimInventory = flag.Bool("update-verbatim-inventory", false,
	"regenerate the verbatim-signature migration manifest metadata (Task 7)")

var recordVerbatimAfter = flag.Bool("record-verbatim-after", false,
	"record only applied after-hashes in the reviewed verbatim-signature migration manifest")

const migrationManifestRel = "docs/superpowers/plans/2026-07-14-verbatim-component-signatures-migration-manifest.json"

const migrationManifestVersion = 1

// ledgerToolBase is this file's own basename, special-cased in classification.
const ledgerToolBase = "verbatim_migration_inventory_test.go"

// migrationAction is the disposition Task 8 must apply to a source unit.
type migrationAction string

// migrationKind is an audit label recording why a unit needs migration.
type migrationKind string

const (
	migrationUnreviewed       migrationAction = "unreviewed"
	migrationManualEdit       migrationAction = "manual-edit"
	migrationDelete           migrationAction = "delete"
	migrationRegenerate       migrationAction = "regenerate"
	migrationReviewedNoChange migrationAction = "reviewed-no-change"
)

const (
	kindDeclareChildren      migrationKind = "declare-children"
	kindDeclareAttrs         migrationKind = "declare-attrs"
	kindDirectPropsInvoke    migrationKind = "direct-props-invoke"
	kindBYOWholeValue        migrationKind = "byo-whole-value"
	kindBYOFieldAddress      migrationKind = "byo-field-address"
	kindComponentStructSplat migrationKind = "component-struct-splat"
	kindAttrsOnlyParamRename migrationKind = "attrs-only-param-rename"
	kindFieldMatcher         migrationKind = "field-matcher-expectation"
	kindGeneratedOutput      migrationKind = "generated-output"
	kindManualSemanticChoice migrationKind = "manual-semantic-choice"
)

// migrationUnit is one migratable slice of a source file: a raw Go/gsx file, a
// txtar archive comment, or a single txtar section.
type migrationUnit struct {
	Kind         string          `json:"kind"` // raw-file, txtar-comment, txtar-section
	SectionIndex *int            `json:"section_index,omitempty"`
	SectionName  string          `json:"section_name,omitempty"`
	BeforeSHA256 string          `json:"before_sha256"`
	AfterSHA256  string          `json:"after_sha256"`
	Action       migrationAction `json:"action"`
	Kinds        []migrationKind `json:"kinds,omitempty"`
	ReviewNote   string          `json:"review_note,omitempty"`
}

// migrationEntry is every unit of one source path.
type migrationEntry struct {
	Path         string          `json:"path"`
	BeforeSHA256 string          `json:"before_sha256"`
	AfterSHA256  string          `json:"after_sha256"`
	Units        []migrationUnit `json:"units"`
}

// migrationManifest is the whole reviewed ledger.
type migrationManifest struct {
	Version int              `json:"version"`
	Phase   string           `json:"phase"` // planned or applied
	Entries []migrationEntry `json:"entries"`
}

// sourceScopes are the fixed source-universe globs at the pre-cutover revision.
// `**` matches any number of path segments (including zero).
var sourceScopes = []string{
	"internal/corpus/testdata/cases/**/*.txtar",
	"internal/corpus/testdata/loadertest/**/*.txtar",
	"internal/examplegen/testdata/**/*.txtar",
	"examples/*.txtar",
	"internal/codegen/**/*_test.go",
	"internal/corpus/**/*_test.go",
	"internal/examplegen/**/*_test.go",
	"internal/lsp/**/*_test.go",
	"gen/**/*_test.go",
	"playground/playbundle/**/*_test.go",
	"playground/server/**/*.go",
	"examples/tailwind-merge/**/*.go",
	"examples/tailwind-merge/**/*.gsx",
	"gen/templates/init/simple/app.gsx",
	"gen/templates/init/simple/main.go.tmpl",
}

// generatedOutputScopes are outputs Task 8 must regenerate (recorded, not
// hand-classified for content).
var generatedOutputScopes = []string{
	"internal/corpus/testdata/coverage.golden",
	"docs/examples.json",
	"playground/server/examples.json",
	"docs/guide/syntax/_generated/**",
	"examples/tailwind-merge/views/card.x.go",
}

func TestVerbatimMigrationInventory(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}

	universe, err := enumerateUniverse(repoRoot)
	if err != nil {
		t.Fatalf("enumerate source universe: %v", err)
	}

	manifestPath := filepath.Join(repoRoot, migrationManifestRel)
	if *updateVerbatimInventory && *recordVerbatimAfter {
		t.Fatal("-update-verbatim-inventory and -record-verbatim-after are mutually exclusive")
	}

	if *updateVerbatimInventory {
		prev, _ := loadManifest(manifestPath) // best-effort; absent is fine
		next := buildManifest(t, repoRoot, universe, prev)
		if err := writeManifest(manifestPath, next); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
		t.Logf("wrote %d entries to %s", len(next.Entries), migrationManifestRel)
		return
	}
	if *recordVerbatimAfter {
		planned, err := loadManifest(manifestPath)
		if err != nil {
			t.Fatalf("load reviewed manifest for after recording: %v", err)
		}
		applied, err := recordAppliedManifest(repoRoot, universe, planned)
		if err != nil {
			t.Fatalf("record applied manifest: %v", err)
		}
		if err := writeManifest(manifestPath, applied); err != nil {
			t.Fatalf("write applied manifest: %v", err)
		}
		t.Logf("recorded applied after-hashes for %d reviewed entries in %s", len(applied.Entries), migrationManifestRel)
		return
	}

	man, err := loadManifest(manifestPath)
	if err != nil {
		t.Fatalf("migration manifest missing or unreadable (%v).\n"+
			"Generate it with:\n"+
			"  go test ./internal/codegen -run TestVerbatimMigrationInventory -update-verbatim-inventory -count=1",
			err)
	}

	verifyManifest(t, repoRoot, universe, man)
}

// ---------------------------------------------------------------------------
// Universe enumeration
// ---------------------------------------------------------------------------

// enumerateUniverse returns the repo-relative paths of every file in the fixed
// source universe plus the recorded generated outputs, sorted and deduplicated.
func enumerateUniverse(repoRoot string) ([]string, error) {
	seen := map[string]bool{}
	add := func(rel string) { seen[filepath.ToSlash(rel)] = true }

	for _, scope := range append(append([]string{}, sourceScopes...), generatedOutputScopes...) {
		matches, err := globScope(repoRoot, scope)
		if err != nil {
			return nil, fmt.Errorf("scope %q: %w", scope, err)
		}
		for _, m := range matches {
			add(m)
		}
	}

	out := make([]string, 0, len(seen))
	for rel := range seen {
		out = append(out, rel)
	}
	sort.Strings(out)
	return out, nil
}

// globScope walks the literal prefix of a `**`/`*` glob and returns matching
// regular files as repo-relative slash paths.
func globScope(repoRoot, scope string) ([]string, error) {
	base := globBase(scope)
	root := filepath.Join(repoRoot, filepath.FromSlash(base))

	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var out []string
	if !info.IsDir() {
		// Literal file scope.
		if globMatch(scope, base) {
			out = append(out, base)
		}
		return out, nil
	}

	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(repoRoot, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if globMatch(scope, rel) {
			out = append(out, rel)
		}
		return nil
	})
	return out, err
}

// globBase returns the longest leading path with no glob metacharacter.
func globBase(scope string) string {
	segs := strings.Split(scope, "/")
	var keep []string
	for _, s := range segs {
		if strings.ContainsAny(s, "*?[") {
			break
		}
		keep = append(keep, s)
	}
	return strings.Join(keep, "/")
}

// globMatch reports whether name matches a slash-separated glob supporting `**`
// (any number of segments, including zero) and per-segment filepath.Match.
func globMatch(pattern, name string) bool {
	return matchSegs(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegs(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegs(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := filepath.Match(pat[0], name[0])
	if err != nil || !ok {
		return false
	}
	return matchSegs(pat[1:], name[1:])
}

// ---------------------------------------------------------------------------
// Manifest construction
// ---------------------------------------------------------------------------

func buildManifest(t *testing.T, repoRoot string, universe []string, prev *migrationManifest) *migrationManifest {
	t.Helper()

	// Index preserved (already-reviewed) units by path + section identity +
	// before-hash so a metadata refresh does not clobber human review.
	type key struct {
		path, section, before string
		idx                   int
	}
	preserved := map[key]migrationUnit{}
	if prev != nil {
		for _, e := range prev.Entries {
			for _, u := range e.Units {
				idx := -1
				if u.SectionIndex != nil {
					idx = *u.SectionIndex
				}
				preserved[key{e.Path, u.SectionName, u.BeforeSHA256, idx}] = u
			}
		}
	}

	man := &migrationManifest{Version: migrationManifestVersion, Phase: "planned"}
	for _, rel := range universe {
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		entry := migrationEntry{Path: rel, BeforeSHA256: hashBytes(data)}

		for _, u := range unitsFor(rel, data) {
			idx := -1
			if u.SectionIndex != nil {
				idx = *u.SectionIndex
			}
			if kept, ok := preserved[key{rel, u.SectionName, u.BeforeSHA256, idx}]; ok {
				// Preserve human classification; refresh metadata fields only.
				u.Action = kept.Action
				u.Kinds = kept.Kinds
				u.ReviewNote = kept.ReviewNote
				u.AfterSHA256 = kept.AfterSHA256
			}
			entry.Units = append(entry.Units, u)
		}
		man.Entries = append(man.Entries, entry)
	}
	return man
}

// unitsFor splits a source file into classified migration units.
func unitsFor(rel string, data []byte) []migrationUnit {
	units := unitSnapshotsFor(rel, data)
	if !strings.HasSuffix(rel, ".txtar") {
		classifyUnit(rel, "", data, &units[0])
		return units
	}
	archive := txtar.Parse(data)
	classifyUnit(rel, "", archive.Comment, &units[0])
	for index, file := range archive.Files {
		classifyUnit(rel, file.Name, file.Data, &units[index+1])
	}
	return units
}

// unitSnapshotsFor splits a file into stable unit identities and hashes without
// classifying it. Applied recording deliberately uses this metadata-only path:
// a new unit has no reviewed action and must be rejected, never reclassified.
func unitSnapshotsFor(rel string, data []byte) []migrationUnit {
	if !strings.HasSuffix(rel, ".txtar") {
		return []migrationUnit{{Kind: "raw-file", BeforeSHA256: hashBytes(data)}}
	}
	arch := txtar.Parse(data)
	var units []migrationUnit

	comment := migrationUnit{Kind: "txtar-comment", BeforeSHA256: hashBytes(arch.Comment)}
	units = append(units, comment)

	for i, f := range arch.Files {
		idx := i
		u := migrationUnit{
			Kind:         "txtar-section",
			SectionIndex: &idx,
			SectionName:  f.Name,
			BeforeSHA256: hashBytes(f.Data),
		}
		units = append(units, u)
	}
	return units
}

// ---------------------------------------------------------------------------
// Classification ruleset
// ---------------------------------------------------------------------------

// generatedFilePaths are standalone generated outputs (not txtar sections).
func isGeneratedOutputPath(rel string) bool {
	switch rel {
	case "internal/corpus/testdata/coverage.golden",
		"docs/examples.json",
		"playground/server/examples.json",
		"examples/tailwind-merge/views/card.x.go":
		return true
	}
	return strings.HasPrefix(rel, "docs/guide/syntax/_generated/")
}

// isGeneratedSectionName reports whether a txtar section is a generated output.
func isGeneratedSectionName(name string) bool {
	return strings.HasSuffix(name, ".golden") || strings.HasSuffix(name, ".x.go")
}

// Marker patterns for the syntactic surface the cutover removes. Applied to
// authored (non-generated) sections only.
var (
	// Bare reserved `children` identifier (gsx magic body identifier).
	reChildren = regexp.MustCompile(`(^|[^A-Za-z0-9_.])children([^A-Za-z0-9_]|$)`)
	// Bare reserved `attrs` identifier: spread `{ attrs... }`, `attrs.Class()`,
	// `attrs={...}`. Excludes gsx.Attrs, .Attrs, containerAttrs, etc. via the
	// leading non-word/non-dot boundary and trailing non-word boundary.
	reAttrs = regexp.MustCompile(`(^|[^A-Za-z0-9_.])attrs([^A-Za-z0-9_]|$)`)
	// A <Name>Props struct literal (generated-ABI construction).
	rePropsLit    = regexp.MustCompile(`\b[A-Za-z0-9_]*Props\{`)
	reRenderComp  = regexp.MustCompile(`\bRenderComponent\b`)
	reFieldMatch  = regexp.MustCompile(`\b(WithFieldMatcher|FieldMatcher)\b`)
	reFieldMethod = regexp.MustCompile(`\bfieldMatcher\b`)
	// A spread `{ SUBJECT... }` inside a component tag. A component tag is
	// capitalized (`<Card`) or a receiver/package selector (`<p.Content`,
	// `<pkg.Foo`); a lowercase single-word tag (`<div`) is an element and its
	// attr-spread is retained, so it is excluded here. SUBJECT allows one level of
	// nested braces so composite literals (`cardData{Title: x}...`) are captured.
	reCompTagSpread = regexp.MustCompile(`<(?:[A-Z][A-Za-z0-9_]*|[a-z][A-Za-z0-9_]*\.[A-Za-z0-9_]+)[^<>]*?\{\s*((?:[^{}]|\{[^{}]*\})*?)\.\.\.\s*\}`)
	reBareIdent     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	// Unnamed attrs-only factory return type: `func(...gsx.Attr) gsx.Node` with no
	// parameter name published in the static type.
	reUnnamedAttrsFactory = regexp.MustCompile(`func\s*\(\s*\.\.\.\s*gsx\.Attr\s*\)`)
	// A non-reserved named variadic attr param (`func(extra ...gsx.Attr)`), where
	// the name is not the reserved `attrs`.
	reNamedAttrsFactory = regexp.MustCompile(`func\s*\(\s*([A-Za-z_][A-Za-z0-9_]*)\s+\.\.\.\s*gsx\.Attr\s*\)`)
	// A component declaration header, function or method-receiver form, capturing
	// its optional generic type-param list and its raw (single-line) parameter
	// list. Matched greedily enough to see a sole non-receiver param's type text;
	// multi-param lists are rejected downstream (no top-level comma allowed), and
	// a parenthesized (func-typed) param type simply fails to match here -- that
	// shape is never byo anyway (see soleParamTypeName in byo.go).
	reComponentHeader = regexp.MustCompile(
		`\bcomponent\s+(?:\(\s*[A-Za-z_][A-Za-z0-9_]*\s+\*?[A-Za-z_][A-Za-z0-9_]*\s*\)\s+)?` +
			`[A-Za-z_][A-Za-z0-9_]*(?:\[([^\]]*)\])?\s*\(([^()]*)\)`)
)

// scalarSoleParamTypes mirrors, EXACTLY, the builtin-scalar exclusion switch in
// soleParamTypeName (internal/codegen/byo.go) -- the ledger's structural BYO
// marker must reject the same bare-identifier types the real BYO trigger
// rejects, or it drifts from the thing it is standing in for.
var scalarSoleParamTypes = map[string]bool{
	"string": true, "bool": true, "byte": true, "rune": true, "error": true, "any": true,
	"int": true, "int8": true, "int16": true, "int32": true, "int64": true,
	"uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true, "uintptr": true,
	"float32": true, "float64": true, "complex64": true, "complex128": true,
}

// classifyUnit sets Action/Kinds/ReviewNote on u from its path, section name, and
// content. See the file header for the methodology.
func classifyUnit(rel, section string, data []byte, u *migrationUnit) {
	// 0. This ledger utility itself is removed by Task 8. (Classifying it by
	// content would self-match on the marker regexps it defines.)
	if rel == "internal/codegen/"+ledgerToolBase {
		u.Action = migrationDelete
		u.Kinds = []migrationKind{kindManualSemanticChoice}
		u.ReviewNote = "the Task-7 migration ledger utility; deleted by Task 8 once the cutover lands"
		return
	}

	// 1. Generated outputs -> regenerate (never hand-classified for content).
	if isGeneratedOutputPath(rel) || isGeneratedSectionName(section) {
		u.Action = migrationRegenerate
		u.Kinds = []migrationKind{kindGeneratedOutput}
		u.ReviewNote = ""
		return
	}

	text := string(data)
	isGSX := section == "" && (strings.HasSuffix(rel, ".gsx"))
	if strings.HasSuffix(section, ".gsx") || section == "input.gsx" {
		isGSX = true
	}
	// `invoke` sections are Go call expressions; treat like Go for markers.

	var kinds []migrationKind
	var notes []string
	addKind := func(k migrationKind, note string) {
		kinds = append(kinds, k)
		notes = append(notes, note)
	}

	// 2. WithFieldMatcher / fuzzy matcher expectations -> removed outright.
	if reFieldMatch.MatchString(text) || reFieldMethod.MatchString(text) {
		addKind(kindFieldMatcher,
			"references WithFieldMatcher/field-matcher expectation; that config and fuzzy attr->field matching are removed under exact-name matching (edit or delete the expectation)")
	}

	// 3. RenderComponent / <Name>Props literal -> direct positional/options value.
	if reRenderComp.MatchString(text) {
		addKind(kindDirectPropsInvoke,
			"manual RenderComponent(...) call site; convert to a direct positional/options invocation of the verbatim signature")
	}
	if rePropsLit.MatchString(text) {
		addKind(kindDirectPropsInvoke,
			"constructs a <Name>Props struct literal (generated ABI removed); pass values directly to the verbatim signature")
	}

	// 4. BYO struct-shaped sole param: STRUCTURAL, not name-based. A component's
	// sole non-receiver param whose type is a bare named type -- unqualified,
	// non-pointer, non-slice, non-map, non-variadic, not a builtin scalar, not
	// the component's own generic type parameter -- is a candidate BYO trigger,
	// mirroring soleParamTypeName's exact exclusion shape (byo.go). This is a
	// conservative over-approximation (Task 8's signature analyzer re-decides
	// each candidate); missing a real BYO candidate here is the unsafe direction,
	// flagging a false one is not.
	if types := soleNamedParamTypes(text); len(types) > 0 {
		addKind(kindBYOFieldAddress,
			"BYO struct-shaped sole param ("+strings.Join(types, ", ")+"): call sites addressing struct fields (<C Field=...>) become individual ordinary params; a whole-value fill (<C p={val}/>) stays whole-value. Resolve per call site (see ESCALATE if ambiguous)")
	}

	// 5. Component struct-splat -> CUT; must be replaced. Distinguished from
	// retained attrs-forwarding (`<C {attrs...}/>`) by the spread subject's type.
	if isGSX {
		if subs := structSplatSubjects(text); len(subs) > 0 {
			addKind(kindComponentStructSplat,
				"component struct-splat "+splatList(subs)+" is CUT; replace each with a named-prop fill (<C prop={x}/>) or individual params -- retention is not an option. (attrs-bag forwarding <C {attrs...}/> is excluded and retained)")
		}
	}

	// 6. Reserved children / attrs identifiers -> declared reserved params.
	if isGSX {
		if reChildren.MatchString(text) {
			addKind(kindDeclareChildren,
				"uses the magic `children` identifier; the component must declare a `children gsx.Node` (or `...gsx.Node`) reserved param")
		}
		if reAttrs.MatchString(text) {
			addKind(kindDeclareAttrs,
				"uses the magic `attrs` bag ({attrs...}/attrs.*); the component must declare an `attrs` reserved param (gsx.Attrs/[]gsx.Attr/defined-slice/...gsx.Attr)")
		}
	}

	// 7. Attrs-only factories: publish the reserved `attrs` name in the static type.
	if isGSX || strings.HasSuffix(rel, ".go") || section == "invoke" || strings.HasSuffix(section, ".go") {
		if reUnnamedAttrsFactory.MatchString(text) {
			addKind(kindAttrsOnlyParamRename,
				"attrs-only factory publishes an unnamed `func(...gsx.Attr) gsx.Node` return type; rename to publish the reserved `attrs` name (func(attrs ...gsx.Attr) gsx.Node)")
		}
		if m := reNamedAttrsFactory.FindStringSubmatch(text); m != nil && m[1] != "attrs" && !strings.HasPrefix(m[1], "_gsx") {
			addKind(kindAttrsOnlyParamRename,
				"variadic attr factory names its bag `"+m[1]+"` (not reserved `attrs`); under the universal model it is an ordinary non-bindable variadic. Rename to `attrs` to keep markup binding, or leave as Go-only")
		}
	}

	if len(kinds) == 0 {
		u.Action = migrationReviewedNoChange
		u.Kinds = nil
		u.ReviewNote = ""
		return
	}

	u.Action = migrationManualEdit
	u.Kinds = dedupeKinds(kinds)
	u.ReviewNote = strings.Join(dedupeNotes(notes), " | ")
}

// soleNamedParamTypes returns the distinct bare type names of every component
// declaration's sole non-receiver parameter that is BYO-shaped: a single param
// (no top-level comma -- a multi-param component always keeps the generated
// <Name>Props wrapper) whose type is a plain identifier (rejecting pointer,
// slice, map, qualified `pkg.Name`, and variadic `...Type` shapes, all of which
// are the GENERATED path per soleParamTypeName in byo.go), that is not one of
// soleParamTypeName's builtin-scalar exclusions, and that is not the
// component's own generic type parameter (`component Generic[T any](v T)` is
// generic dispatch, not a struct).
func soleNamedParamTypes(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range reComponentHeader.FindAllStringSubmatch(text, -1) {
		generics, params := m[1], strings.TrimSpace(m[2])
		if params == "" || strings.Contains(params, ",") {
			continue // nullary or multi-param: never byo
		}
		fields := strings.Fields(params)
		if len(fields) != 2 {
			continue // not a bare `name Type` shape
		}
		typ := fields[1]
		if !reBareIdent.MatchString(typ) {
			continue // pointer/slice/map/qualified/variadic: not the byo shape
		}
		if scalarSoleParamTypes[typ] || isGenericTypeParam(typ, generics) {
			continue
		}
		if !seen[typ] {
			seen[typ] = true
			out = append(out, typ)
		}
	}
	sort.Strings(out)
	return out
}

// isGenericTypeParam reports whether typ names one of the component's own
// declared generic type parameters (the `[T any, ...]` list), in which case a
// sole param of that type is generic dispatch, not a byo struct.
func isGenericTypeParam(typ, generics string) bool {
	if generics == "" {
		return false
	}
	for part := range strings.SplitSeq(generics, ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(part), " ")
		if name == typ {
			return true
		}
	}
	return false
}

// structSplatSubjects returns the distinct component struct-splat subjects in a
// gsx section, excluding attrs-bag forwarding (which is retained). A spread
// subject is a bag (forwarding, excluded) when it names or resolves to a
// gsx.Attrs/gsx.Attr value; otherwise it is a struct-splat (cut).
func structSplatSubjects(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range reCompTagSpread.FindAllStringSubmatch(text, -1) {
		subj := strings.TrimSpace(m[1])
		if isBagSubject(subj, text) {
			continue
		}
		key := subj
		if key == "" {
			key = "<empty>"
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// isBagSubject reports whether a spread subject is a gsx.Attrs bag (retained
// attrs-forwarding) rather than a struct value (cut struct-splat).
func isBagSubject(subj, sectionText string) bool {
	if subj == "" {
		return false // empty splat `{...}` -- a struct-splat rejection case
	}
	if strings.Contains(strings.ToLower(subj), "attr") {
		return true // attrs, extraAttrs, attrs.Without(...), getAttrs(), ...
	}
	if reBareIdent.MatchString(subj) {
		// Resolve the subject's declared type in this section: a param/field/var
		// bound to an attrs-bag type is forwarding, not a struct.
		typed := regexp.MustCompile(`\b` + regexp.QuoteMeta(subj) +
			`\s+(?:gsx\.Attrs|gsx\.Attr\b|\[\]gsx\.Attr|[A-Za-z0-9_]*Attrs)\b`)
		if typed.MatchString(sectionText) {
			return true
		}
	}
	return false
}

func splatList(subs []string) string {
	quoted := make([]string, len(subs))
	for i, s := range subs {
		quoted[i] = "{" + s + "...}"
	}
	return strings.Join(quoted, ", ")
}

func dedupeKinds(in []migrationKind) []migrationKind {
	seen := map[migrationKind]bool{}
	var out []migrationKind
	for _, k := range in {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

func dedupeNotes(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, n := range in {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Verification
// ---------------------------------------------------------------------------

type migrationUnitIdentity struct {
	kind        string
	sectionName string
}

func recordAppliedManifest(repoRoot string, universe []string, planned *migrationManifest) (*migrationManifest, error) {
	if planned == nil {
		return nil, fmt.Errorf("nil migration manifest")
	}
	if planned.Version != migrationManifestVersion {
		return nil, fmt.Errorf("manifest version = %d, want %d", planned.Version, migrationManifestVersion)
	}
	if planned.Phase != "planned" {
		return nil, fmt.Errorf("manifest phase = %q, want %q before recording", planned.Phase, "planned")
	}

	entries := make(map[string]int, len(planned.Entries))
	for entryIndex, entry := range planned.Entries {
		if _, exists := entries[entry.Path]; exists {
			return nil, fmt.Errorf("duplicate manifest entry for %s", entry.Path)
		}
		entries[entry.Path] = entryIndex
		if _, err := reviewedUnitsByIdentity(entry.Path, entry.Units); err != nil {
			return nil, err
		}
		for unitIndex, unit := range entry.Units {
			if err := reviewedUnitError(entry.Path, unitIndex, unit); err != nil {
				return nil, err
			}
		}
	}

	universeSet := make(map[string]bool, len(universe))
	for _, rel := range universe {
		if universeSet[rel] {
			return nil, fmt.Errorf("duplicate universe file %s", rel)
		}
		universeSet[rel] = true
		if _, ok := entries[rel]; !ok {
			return nil, fmt.Errorf("current file %s is not in reviewed manifest", rel)
		}
	}

	applied := cloneManifest(planned)
	for entryIndex := range applied.Entries {
		entry := &applied.Entries[entryIndex]
		if !universeSet[entry.Path] {
			if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(entry.Path))); err == nil {
				return nil, fmt.Errorf("%s: planned deletion still exists", entry.Path)
			} else if !os.IsNotExist(err) {
				return nil, fmt.Errorf("stat %s: %w", entry.Path, err)
			}
			for unitIndex, unit := range entry.Units {
				if unit.Action != migrationDelete {
					return nil, fmt.Errorf("%s unit %d (%s): reviewed unit is absent without delete action", entry.Path, unitIndex, unit.SectionName)
				}
				entry.Units[unitIndex].AfterSHA256 = ""
			}
			entry.AfterSHA256 = ""
			continue
		}

		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(entry.Path)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Path, err)
		}
		current, err := currentUnitsByIdentity(entry.Path, unitSnapshotsFor(entry.Path, data))
		if err != nil {
			return nil, err
		}
		reviewed, err := reviewedUnitsByIdentity(entry.Path, entry.Units)
		if err != nil {
			return nil, err
		}
		for identity := range current {
			if _, ok := reviewed[identity]; !ok {
				return nil, fmt.Errorf("%s: unreviewed current unit %s", entry.Path, formatUnitIdentity(identity))
			}
		}
		for unitIndex := range entry.Units {
			unit := &entry.Units[unitIndex]
			identity := unitIdentity(*unit)
			currentUnit, exists := current[identity]
			if unit.Action == migrationDelete {
				if exists {
					return nil, fmt.Errorf("%s unit %d (%s): planned deletion still exists", entry.Path, unitIndex, unit.SectionName)
				}
				unit.AfterSHA256 = ""
				continue
			}
			if !exists {
				return nil, fmt.Errorf("%s unit %d (%s): reviewed unit is absent", entry.Path, unitIndex, unit.SectionName)
			}
			unit.AfterSHA256 = currentUnit.BeforeSHA256
		}
		entry.AfterSHA256 = hashBytes(data)
	}
	applied.Phase = "applied"
	return applied, nil
}

func cloneManifest(manifest *migrationManifest) *migrationManifest {
	clone := *manifest
	clone.Entries = append([]migrationEntry(nil), manifest.Entries...)
	for entryIndex := range clone.Entries {
		original := manifest.Entries[entryIndex]
		clone.Entries[entryIndex].Units = append([]migrationUnit(nil), original.Units...)
		for unitIndex := range clone.Entries[entryIndex].Units {
			unit := &clone.Entries[entryIndex].Units[unitIndex]
			originalUnit := original.Units[unitIndex]
			unit.Kinds = append([]migrationKind(nil), originalUnit.Kinds...)
			if originalUnit.SectionIndex != nil {
				index := *originalUnit.SectionIndex
				unit.SectionIndex = &index
			}
		}
	}
	return &clone
}

func unitIdentity(unit migrationUnit) migrationUnitIdentity {
	return migrationUnitIdentity{kind: unit.Kind, sectionName: unit.SectionName}
}

func reviewedUnitsByIdentity(rel string, units []migrationUnit) (map[migrationUnitIdentity]int, error) {
	indexed := make(map[migrationUnitIdentity]int, len(units))
	for index, unit := range units {
		identity := unitIdentity(unit)
		if _, exists := indexed[identity]; exists {
			return nil, fmt.Errorf("%s: duplicate reviewed unit identity %s", rel, formatUnitIdentity(identity))
		}
		indexed[identity] = index
	}
	return indexed, nil
}

func currentUnitsByIdentity(rel string, units []migrationUnit) (map[migrationUnitIdentity]migrationUnit, error) {
	indexed := make(map[migrationUnitIdentity]migrationUnit, len(units))
	for _, unit := range units {
		identity := unitIdentity(unit)
		if _, exists := indexed[identity]; exists {
			return nil, fmt.Errorf("%s: duplicate current unit identity %s", rel, formatUnitIdentity(identity))
		}
		indexed[identity] = unit
	}
	return indexed, nil
}

func formatUnitIdentity(identity migrationUnitIdentity) string {
	if identity.sectionName == "" {
		return identity.kind
	}
	return fmt.Sprintf("%s %q", identity.kind, identity.sectionName)
}

func reviewedUnitError(rel string, index int, unit migrationUnit) error {
	switch unit.Action {
	case migrationUnreviewed:
		return fmt.Errorf("%s unit %d (%s): action still unreviewed", rel, index, unit.SectionName)
	case migrationRegenerate, migrationReviewedNoChange, migrationManualEdit, migrationDelete:
		// Reviewed action.
	default:
		return fmt.Errorf("%s unit %d (%s): unknown action %q", rel, index, unit.SectionName, unit.Action)
	}
	generated := isGeneratedOutputPath(rel) || isGeneratedSectionName(unit.SectionName)
	if generated && unit.Action != migrationRegenerate {
		return fmt.Errorf("%s unit %d (%s): generated output must be regenerate, got %q", rel, index, unit.SectionName, unit.Action)
	}
	if !generated && unit.Action == migrationRegenerate {
		return fmt.Errorf("%s unit %d (%s): non-generated unit classified regenerate", rel, index, unit.SectionName)
	}
	needsNote := unit.Action == migrationManualEdit || unit.Action == migrationDelete || containsKind(unit.Kinds, kindManualSemanticChoice)
	if needsNote && strings.TrimSpace(unit.ReviewNote) == "" {
		return fmt.Errorf("%s unit %d (%s): %s requires a non-empty review_note", rel, index, unit.SectionName, unit.Action)
	}
	return nil
}

func verifyManifest(t *testing.T, repoRoot string, universe []string, man *migrationManifest) {
	t.Helper()
	for _, err := range verifyManifestErrors(repoRoot, universe, man) {
		t.Error(err)
	}
}

func verifyManifestErrors(repoRoot string, universe []string, man *migrationManifest) []error {
	if man == nil {
		return []error{fmt.Errorf("nil migration manifest")}
	}
	var problems []error
	if man.Version != migrationManifestVersion {
		problems = append(problems, fmt.Errorf("manifest version = %d, want %d", man.Version, migrationManifestVersion))
	}
	if man.Phase != "planned" && man.Phase != "applied" {
		problems = append(problems, fmt.Errorf("manifest phase = %q, want planned or applied", man.Phase))
		return problems
	}

	// One-to-one universe <-> manifest match.
	manifestPaths := map[string]migrationEntry{}
	for _, e := range man.Entries {
		if _, dup := manifestPaths[e.Path]; dup {
			problems = append(problems, fmt.Errorf("duplicate manifest entry for %s", e.Path))
		}
		manifestPaths[e.Path] = e
	}
	universeSet := map[string]bool{}
	for _, rel := range universe {
		if universeSet[rel] {
			problems = append(problems, fmt.Errorf("duplicate universe file %s", rel))
			continue
		}
		universeSet[rel] = true
		if _, ok := manifestPaths[rel]; !ok {
			problems = append(problems, fmt.Errorf("universe file missing from manifest: %s", rel))
		}
	}
	for _, e := range man.Entries {
		if !universeSet[e.Path] && man.Phase == "planned" {
			problems = append(problems, fmt.Errorf("manifest entry not in universe: %s", e.Path))
		}
	}
	if man.Phase == "applied" {
		return append(problems, verifyAppliedManifestErrors(repoRoot, universeSet, man.Entries)...)
	}

	// Recompute current units and compare identity + hashes; verify actions.
	for _, rel := range universe {
		entry, ok := manifestPaths[rel]
		if !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			problems = append(problems, fmt.Errorf("read %s: %v", rel, err))
			continue
		}
		if got := hashBytes(data); got != entry.BeforeSHA256 {
			problems = append(problems, fmt.Errorf("%s: entry before-hash %s != current %s (regenerate the manifest)", rel, entry.BeforeSHA256, got))
		}

		want := unitsFor(rel, data)
		if len(want) != len(entry.Units) {
			problems = append(problems, fmt.Errorf("%s: %d manifest units, %d current units", rel, len(entry.Units), len(want)))
			continue
		}
		for i, wu := range want {
			gu := entry.Units[i]
			if gu.Kind != wu.Kind || gu.SectionName != wu.SectionName || !intPtrEq(gu.SectionIndex, wu.SectionIndex) {
				problems = append(problems, fmt.Errorf("%s unit %d: identity mismatch (kind/section/index)", rel, i))
			}
			if gu.BeforeSHA256 != wu.BeforeSHA256 {
				problems = append(problems, fmt.Errorf("%s unit %d (%s): before-hash mismatch (regenerate the manifest)", rel, i, gu.SectionName))
			}
			if err := reviewedUnitError(rel, i, gu); err != nil {
				problems = append(problems, err)
			}
		}
	}
	return problems
}

func verifyAppliedManifestErrors(repoRoot string, universeSet map[string]bool, entries []migrationEntry) []error {
	var problems []error
	for _, entry := range entries {
		for unitIndex, unit := range entry.Units {
			if err := reviewedUnitError(entry.Path, unitIndex, unit); err != nil {
				problems = append(problems, err)
			}
		}
		if _, err := reviewedUnitsByIdentity(entry.Path, entry.Units); err != nil {
			problems = append(problems, err)
			continue
		}
		if !universeSet[entry.Path] {
			if _, err := os.Stat(filepath.Join(repoRoot, filepath.FromSlash(entry.Path))); err == nil {
				problems = append(problems, fmt.Errorf("%s: planned deletion still exists", entry.Path))
			} else if !os.IsNotExist(err) {
				problems = append(problems, fmt.Errorf("stat %s: %v", entry.Path, err))
			}
			for unitIndex, unit := range entry.Units {
				if unit.Action != migrationDelete || unit.AfterSHA256 != "" {
					problems = append(problems, fmt.Errorf("%s unit %d (%s): absent file is not a pinned planned deletion", entry.Path, unitIndex, unit.SectionName))
				}
			}
			if entry.AfterSHA256 != "" {
				problems = append(problems, fmt.Errorf("%s: planned file deletion has after-hash", entry.Path))
			}
			continue
		}

		data, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(entry.Path)))
		if err != nil {
			problems = append(problems, fmt.Errorf("read %s: %v", entry.Path, err))
			continue
		}
		if got := hashBytes(data); got != entry.AfterSHA256 {
			problems = append(problems, fmt.Errorf("%s: entry after-hash %s != current %s", entry.Path, entry.AfterSHA256, got))
		}
		current, err := currentUnitsByIdentity(entry.Path, unitSnapshotsFor(entry.Path, data))
		if err != nil {
			problems = append(problems, err)
			continue
		}
		reviewed, _ := reviewedUnitsByIdentity(entry.Path, entry.Units)
		for identity := range current {
			if _, ok := reviewed[identity]; !ok {
				problems = append(problems, fmt.Errorf("%s: unreviewed current unit %s", entry.Path, formatUnitIdentity(identity)))
			}
		}
		for unitIndex, unit := range entry.Units {
			currentUnit, exists := current[unitIdentity(unit)]
			if unit.Action == migrationDelete {
				if exists {
					problems = append(problems, fmt.Errorf("%s unit %d (%s): planned deletion still exists", entry.Path, unitIndex, unit.SectionName))
				}
				if unit.AfterSHA256 != "" {
					problems = append(problems, fmt.Errorf("%s unit %d (%s): planned deletion has after-hash", entry.Path, unitIndex, unit.SectionName))
				}
				continue
			}
			if !exists {
				problems = append(problems, fmt.Errorf("%s unit %d (%s): reviewed unit is absent", entry.Path, unitIndex, unit.SectionName))
				continue
			}
			if currentUnit.BeforeSHA256 != unit.AfterSHA256 {
				problems = append(problems, fmt.Errorf("%s unit %d (%s): after-hash mismatch", entry.Path, unitIndex, unit.SectionName))
			}
		}
	}
	return problems
}

func containsKind(ks []migrationKind, want migrationKind) bool {
	return slices.Contains(ks, want)
}

// ---------------------------------------------------------------------------
// IO helpers
// ---------------------------------------------------------------------------

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func loadManifest(path string) (*migrationManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m migrationManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func writeManifest(path string, m *migrationManifest) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func intPtrEq(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func TestRecordVerbatimAfterPreservesReviewedMetadataAndStableTxtarIdentity(t *testing.T) {
	root, universe, planned, current := recordAfterFixture(t)
	before := cloneMigrationManifest(t, planned)

	applied, err := recordAppliedManifest(root, universe, planned)
	if err != nil {
		t.Fatal(err)
	}
	if applied.Phase != "applied" {
		t.Fatalf("phase = %q, want applied", applied.Phase)
	}
	assertOnlyAfterMetadataChanged(t, before, applied)

	entries := migrationEntriesByPath(applied)
	archive := entries["case.txtar"]
	if archive.AfterSHA256 != hashBytes(current["case.txtar"]) {
		t.Fatalf("archive after hash = %q, want current content hash", archive.AfterSHA256)
	}
	units := migrationUnitsByIdentity(t, archive.Units)
	parsed := txtar.Parse(current["case.txtar"])
	wantHashes := map[string]string{
		"txtar-comment\x00":              hashBytes(parsed.Comment),
		"txtar-section\x00input.gsx":     hashBytes(parsed.Files[1].Data),
		"txtar-section\x00render.golden": hashBytes(parsed.Files[0].Data),
	}
	for identity, want := range wantHashes {
		if got := units[identity].AfterSHA256; got != want {
			t.Errorf("%s after hash = %q, want %q", identity, got, want)
		}
	}
	deleted := units["txtar-section\x00obsolete.txt"]
	if deleted.Action != migrationDelete || deleted.AfterSHA256 != "" {
		t.Fatalf("planned section deletion = %+v, want retained with empty after hash", deleted)
	}
	if deleted.SectionIndex == nil || *deleted.SectionIndex != 1 {
		t.Fatalf("planned section index changed during name-based reconciliation: %+v", deleted.SectionIndex)
	}
	if gone := entries["gone.go"]; len(gone.Units) != 1 || gone.Units[0].Action != migrationDelete || gone.AfterSHA256 != "" || gone.Units[0].AfterSHA256 != "" {
		t.Fatalf("planned file deletion = %+v, want retained and absent", gone)
	}

	for path, want := range current {
		got, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("recorder rewrote source %s", path)
		}
	}
}

func TestRecordVerbatimAfterRejectsUnreviewedOrUnexplainedChanges(t *testing.T) {
	tests := []struct {
		name string
		want string
		edit func(t *testing.T, root string, universe *[]string, manifest *migrationManifest)
	}{
		{
			name: "unreviewed unit", want: "unreviewed",
			edit: func(_ *testing.T, _ string, _ *[]string, manifest *migrationManifest) {
				manifest.Entries[0].Units[0].Action = migrationUnreviewed
			},
		},
		{
			name: "unknown action", want: "unknown action",
			edit: func(_ *testing.T, _ string, _ *[]string, manifest *migrationManifest) {
				manifest.Entries[0].Units[0].Action = migrationAction("invented")
			},
		},
		{
			name: "new file", want: "not in reviewed manifest",
			edit: func(t *testing.T, root string, universe *[]string, _ *migrationManifest) {
				writeInventoryFixtureFile(t, root, "new.go", []byte("package p\n"))
				*universe = append(*universe, "new.go")
			},
		},
		{
			name: "new txtar section", want: "unreviewed current unit",
			edit: func(t *testing.T, root string, _ *[]string, _ *migrationManifest) {
				data, err := os.ReadFile(filepath.Join(root, "case.txtar"))
				if err != nil {
					t.Fatal(err)
				}
				writeInventoryFixtureFile(t, root, "case.txtar", append(data, []byte("-- new.txt --\nnew\n")...))
			},
		},
		{
			name: "missing reviewed section", want: "reviewed unit is absent",
			edit: func(t *testing.T, root string, _ *[]string, _ *migrationManifest) {
				writeInventoryFixtureFile(t, root, "case.txtar", []byte("after comment\n-- render.golden --\nafter golden\n"))
			},
		},
		{
			name: "planned deletion survives", want: "planned deletion still exists",
			edit: func(t *testing.T, root string, _ *[]string, _ *migrationManifest) {
				data, err := os.ReadFile(filepath.Join(root, "case.txtar"))
				if err != nil {
					t.Fatal(err)
				}
				writeInventoryFixtureFile(t, root, "case.txtar", append(data, []byte("-- obsolete.txt --\nstill here\n")...))
			},
		},
		{
			name: "duplicate current section identity", want: "duplicate current unit identity",
			edit: func(t *testing.T, root string, _ *[]string, _ *migrationManifest) {
				data, err := os.ReadFile(filepath.Join(root, "case.txtar"))
				if err != nil {
					t.Fatal(err)
				}
				writeInventoryFixtureFile(t, root, "case.txtar", append(data, []byte("-- input.gsx --\nduplicate\n")...))
			},
		},
		{
			name: "duplicate reviewed section identity", want: "duplicate reviewed unit identity",
			edit: func(_ *testing.T, _ string, _ *[]string, manifest *migrationManifest) {
				manifest.Entries[0].Units = append(manifest.Entries[0].Units, manifest.Entries[0].Units[1])
			},
		},
		{
			name: "duplicate manifest file", want: "duplicate manifest entry",
			edit: func(_ *testing.T, _ string, _ *[]string, manifest *migrationManifest) {
				manifest.Entries = append(manifest.Entries, manifest.Entries[0])
			},
		},
		{
			name: "duplicate universe file", want: "duplicate universe file",
			edit: func(_ *testing.T, _ string, universe *[]string, _ *migrationManifest) {
				*universe = append(*universe, (*universe)[0])
			},
		},
		{
			name: "missing file without delete action", want: "absent without delete action",
			edit: func(t *testing.T, root string, universe *[]string, _ *migrationManifest) {
				if err := os.Remove(filepath.Join(root, "raw.go")); err != nil {
					t.Fatal(err)
				}
				*universe = slices.DeleteFunc(*universe, func(path string) bool { return path == "raw.go" })
			},
		},
		{
			name: "already applied", want: "phase",
			edit: func(_ *testing.T, _ string, _ *[]string, manifest *migrationManifest) {
				manifest.Phase = "applied"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, universe, manifest, _ := recordAfterFixture(t)
			test.edit(t, root, &universe, manifest)
			_, err := recordAppliedManifest(root, universe, manifest)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestAppliedManifestVerificationPinsAfterHashesAndDeletions(t *testing.T) {
	root, universe, planned, _ := recordAfterFixture(t)
	applied, err := recordAppliedManifest(root, universe, planned)
	if err != nil {
		t.Fatal(err)
	}
	if problems := verifyManifestErrors(root, universe, applied); len(problems) != 0 {
		t.Fatalf("fresh applied manifest verification: %v", problems)
	}

	t.Run("surviving file hash", func(t *testing.T) {
		writeInventoryFixtureFile(t, root, "raw.go", []byte("package changed_again\n"))
		if problems := verifyManifestErrors(root, universe, applied); !problemsContain(problems, "after-hash") {
			t.Fatalf("problems = %v, want after-hash mismatch", problems)
		}
	})

	t.Run("planned file deletion", func(t *testing.T) {
		root, universe, planned, _ := recordAfterFixture(t)
		applied, err := recordAppliedManifest(root, universe, planned)
		if err != nil {
			t.Fatal(err)
		}
		writeInventoryFixtureFile(t, root, "gone.go", []byte("package p\n"))
		universe = append(universe, "gone.go")
		if problems := verifyManifestErrors(root, universe, applied); !problemsContain(problems, "planned deletion still exists") {
			t.Fatalf("problems = %v, want planned deletion failure", problems)
		}
	})

	t.Run("planned file deletion has after hash", func(t *testing.T) {
		root, universe, planned, _ := recordAfterFixture(t)
		applied, err := recordAppliedManifest(root, universe, planned)
		if err != nil {
			t.Fatal(err)
		}
		applied.Entries[2].AfterSHA256 = hashBytes([]byte("must stay empty"))
		if problems := verifyManifestErrors(root, universe, applied); !problemsContain(problems, "planned file deletion has after-hash") {
			t.Fatalf("problems = %v, want planned file deletion after-hash failure", problems)
		}
	})

	t.Run("new section", func(t *testing.T) {
		root, universe, planned, _ := recordAfterFixture(t)
		applied, err := recordAppliedManifest(root, universe, planned)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(root, "case.txtar"))
		if err != nil {
			t.Fatal(err)
		}
		writeInventoryFixtureFile(t, root, "case.txtar", append(data, []byte("-- new.txt --\nnew\n")...))
		if problems := verifyManifestErrors(root, universe, applied); !problemsContain(problems, "unreviewed current unit") {
			t.Fatalf("problems = %v, want unreviewed unit failure", problems)
		}
	})
}

func recordAfterFixture(t *testing.T) (string, []string, *migrationManifest, map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	beforeArchive := []byte("before comment\n-- input.gsx --\nbefore input\n-- obsolete.txt --\nremove me\n-- render.golden --\nbefore golden\n")
	current := map[string][]byte{
		"case.txtar": []byte("after comment\n-- render.golden --\nafter golden\n-- input.gsx --\nafter input\n"),
		"raw.go":     []byte("package after\n"),
	}
	for path, data := range current {
		writeInventoryFixtureFile(t, root, path, data)
	}
	archiveUnits := unitsFor("case.txtar", beforeArchive)
	archiveUnits[0].Action = migrationReviewedNoChange
	archiveUnits[1].Action = migrationManualEdit
	archiveUnits[1].Kinds = []migrationKind{kindDeclareAttrs}
	archiveUnits[1].ReviewNote = "reviewed input migration"
	archiveUnits[2].Action = migrationDelete
	archiveUnits[2].Kinds = []migrationKind{kindManualSemanticChoice}
	archiveUnits[2].ReviewNote = "reviewed section deletion"
	archiveUnits[3].Action = migrationRegenerate
	archiveUnits[3].Kinds = []migrationKind{kindGeneratedOutput}
	archiveUnits[3].ReviewNote = ""
	rawBefore := []byte("package before\n")
	goneBefore := []byte("package gone\n")
	planned := &migrationManifest{
		Version: migrationManifestVersion,
		Phase:   "planned",
		Entries: []migrationEntry{
			{Path: "case.txtar", BeforeSHA256: hashBytes(beforeArchive), Units: archiveUnits},
			{Path: "raw.go", BeforeSHA256: hashBytes(rawBefore), Units: []migrationUnit{{
				Kind: "raw-file", BeforeSHA256: hashBytes(rawBefore), Action: migrationManualEdit,
				Kinds: []migrationKind{kindDirectPropsInvoke}, ReviewNote: "reviewed raw migration",
			}}},
			{Path: "gone.go", BeforeSHA256: hashBytes(goneBefore), Units: []migrationUnit{{
				Kind: "raw-file", BeforeSHA256: hashBytes(goneBefore), Action: migrationDelete,
				Kinds: []migrationKind{kindManualSemanticChoice}, ReviewNote: "reviewed file deletion",
			}}},
		},
	}
	return root, []string{"case.txtar", "raw.go"}, planned, current
}

func writeInventoryFixtureFile(t *testing.T, root, rel string, data []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func cloneMigrationManifest(t *testing.T, manifest *migrationManifest) *migrationManifest {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var clone migrationManifest
	if err := json.Unmarshal(data, &clone); err != nil {
		t.Fatal(err)
	}
	return &clone
}

func assertOnlyAfterMetadataChanged(t *testing.T, before, after *migrationManifest) {
	t.Helper()
	normalized := cloneMigrationManifest(t, after)
	normalized.Phase = before.Phase
	for entryIndex := range normalized.Entries {
		normalized.Entries[entryIndex].AfterSHA256 = before.Entries[entryIndex].AfterSHA256
		for unitIndex := range normalized.Entries[entryIndex].Units {
			normalized.Entries[entryIndex].Units[unitIndex].AfterSHA256 = before.Entries[entryIndex].Units[unitIndex].AfterSHA256
		}
	}
	if beforeJSON, _ := json.Marshal(before); string(beforeJSON) != mustJSON(t, normalized) {
		t.Fatalf("recorder changed reviewed metadata\nbefore: %s\nafter:  %s", beforeJSON, mustJSON(t, normalized))
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func migrationEntriesByPath(manifest *migrationManifest) map[string]migrationEntry {
	entries := make(map[string]migrationEntry, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		entries[entry.Path] = entry
	}
	return entries
}

func migrationUnitsByIdentity(t *testing.T, units []migrationUnit) map[string]migrationUnit {
	t.Helper()
	indexed := make(map[string]migrationUnit, len(units))
	for _, unit := range units {
		identity := unit.Kind + "\x00" + unit.SectionName
		if _, exists := indexed[identity]; exists {
			t.Fatalf("duplicate unit identity %q", identity)
		}
		indexed[identity] = unit
	}
	return indexed
}

func problemsContain(problems []error, substring string) bool {
	for _, problem := range problems {
		if strings.Contains(problem.Error(), substring) {
			return true
		}
	}
	return false
}
