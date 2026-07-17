// Package typebundle serializes a transitively-closed set of go/types packages
// together with the exact build context that selected them. It reconstructs
// both without a Go toolchain or subprocess, which lets a browser type-check
// against the same Go universe as the server compiling generated source.
package typebundle

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"go/token"
	"go/types"
	"go/version"
	"io"
	"sort"
	"strings"

	"golang.org/x/tools/go/gcexportdata"
)

const envelopeMagic = "GSXTYPEBUNDLE\x00\x02"

// maxLanguageVersion is the newest go/types language understood by the pinned
// toolchain that builds the bundle reader. Keep it explicit: accepting a newer
// language merely defers failure until every snippet is type-checked. The
// toolchain parity test makes a Go upgrade fail closed until this contract and
// its generated archive are updated together.
const maxLanguageVersion = "go1.26"

// maxToolchainVersion is the newest producer whose opaque export payload this
// reader accepts. gcexportdata owns that payload format; accepting a bundle
// written by a newer Go toolchain would claim compatibility this pinned reader
// has not verified. The toolchain parity test makes upgrades explicit.
const maxToolchainVersion = "go1.26.1"

// Target is the authoritative build context and language contract represented
// by a bundle. ToolchainVersion describes the go command that selected and
// exported the packages. LanguageVersion is the go/types language version for
// user snippets; they are deliberately separate because a newer toolchain can
// compile a module whose go directive selects an older language.
type Target struct {
	Compiler         string   `json:"compiler"`
	GOOS             string   `json:"goos"`
	GOARCH           string   `json:"goarch"`
	CGOEnabled       bool     `json:"cgo_enabled"`
	ToolchainVersion string   `json:"toolchain_version"`
	LanguageVersion  string   `json:"language_version"`
	BuildTags        []string `json:"build_tags"`
	ToolTags         []string `json:"tool_tags"`
	ReleaseTags      []string `json:"release_tags"`
}

func (t Target) canonical() Target {
	clone := func(tags []string) []string {
		if tags == nil {
			return nil
		}
		return append(make([]string, 0, len(tags)), tags...)
	}
	t.BuildTags = clone(t.BuildTags)
	t.ToolTags = clone(t.ToolTags)
	t.ReleaseTags = clone(t.ReleaseTags)
	sort.Strings(t.BuildTags)
	sort.Strings(t.ToolTags)
	sort.Strings(t.ReleaseTags)
	return t
}

// Sizes validates the target and returns its exact go/types size model.
func (t Target) Sizes() (types.Sizes, error) {
	if t.Compiler != "gc" {
		return nil, fmt.Errorf("typebundle: target compiler %q is unsupported; only gc bundles are accepted", t.Compiler)
	}
	if t.GOOS == "" {
		return nil, fmt.Errorf("typebundle: target GOOS is required")
	}
	if t.GOARCH == "" {
		return nil, fmt.Errorf("typebundle: target GOARCH is required")
	}
	if !knownGoTargets[t.GOOS+"/"+t.GOARCH] {
		return nil, fmt.Errorf("typebundle: unknown Go target %q", t.GOOS+"/"+t.GOARCH)
	}
	if !version.IsValid(t.ToolchainVersion) {
		return nil, fmt.Errorf("typebundle: target ToolchainVersion %q is not a known Go version", t.ToolchainVersion)
	}
	if version.Compare(t.ToolchainVersion, maxToolchainVersion) > 0 {
		return nil, fmt.Errorf("typebundle: target ToolchainVersion %s is newer than this reader's supported toolchain %s", t.ToolchainVersion, maxToolchainVersion)
	}
	if !version.IsValid(t.LanguageVersion) {
		return nil, fmt.Errorf("typebundle: target LanguageVersion %q is not a known Go language version", t.LanguageVersion)
	}
	languageVersion := version.Lang(t.LanguageVersion)
	_, minor, hasMinor := strings.Cut(strings.TrimPrefix(languageVersion, "go"), ".")
	if languageVersion == "" || !hasMinor || minor == "" {
		return nil, fmt.Errorf("typebundle: target LanguageVersion %q does not name a Go major.minor language", t.LanguageVersion)
	}
	if version.Compare(languageVersion, maxLanguageVersion) > 0 {
		return nil, fmt.Errorf("typebundle: target LanguageVersion %s is newer than this reader's supported language %s", t.LanguageVersion, maxLanguageVersion)
	}
	if version.Compare(t.LanguageVersion, t.ToolchainVersion) > 0 {
		return nil, fmt.Errorf("typebundle: language version %s is newer than toolchain %s", t.LanguageVersion, t.ToolchainVersion)
	}
	if t.BuildTags == nil || t.ToolTags == nil || t.ReleaseTags == nil {
		return nil, fmt.Errorf("typebundle: target build, tool, and release tag sets must be recorded")
	}
	for _, tagSet := range []struct {
		name string
		tags []string
	}{
		{name: "build", tags: t.BuildTags},
		{name: "tool", tags: t.ToolTags},
		{name: "release", tags: t.ReleaseTags},
	} {
		seen := map[string]bool{}
		for _, tag := range tagSet.tags {
			if tag == "" {
				return nil, fmt.Errorf("typebundle: target %s tags contain an empty tag", tagSet.name)
			}
			if seen[tag] {
				return nil, fmt.Errorf("typebundle: target %s tags contain duplicate %q", tagSet.name, tag)
			}
			seen[tag] = true
		}
	}
	sizes := types.SizesFor(t.Compiler, t.GOARCH)
	if sizes == nil {
		return nil, fmt.Errorf("typebundle: no go/types size model for compiler %q and GOARCH %q", t.Compiler, t.GOARCH)
	}
	return sizes, nil
}

// Bundle is the fully reconstructed type universe and its target semantics.
type Bundle struct {
	Target   Target
	Sizes    types.Sizes
	Packages map[string]*types.Package
}

// Write serializes pkgs, which must be transitively closed, under the explicit
// target captured by the producer. Target acquisition and packages.Load must
// share one immutable command environment; Write intentionally does not infer
// provenance from the opaque types.Sizes interface.
func Write(fset *token.FileSet, target Target, pkgs []*types.Package) ([]byte, error) {
	if fset == nil {
		return nil, fmt.Errorf("typebundle: FileSet is required")
	}
	if _, err := target.Sizes(); err != nil {
		return nil, err
	}
	target = target.canonical()

	filtered := make([]*types.Package, 0, len(pkgs))
	seenPaths := make(map[string]bool, len(pkgs))
	for _, p := range pkgs {
		if p == nil {
			return nil, fmt.Errorf("typebundle: package set contains nil")
		}
		if seenPaths[p.Path()] {
			return nil, fmt.Errorf("typebundle: duplicate package path %q", p.Path())
		}
		seenPaths[p.Path()] = true
		if p.Path() == "unsafe" {
			if p != types.Unsafe {
				return nil, fmt.Errorf("typebundle: package path %q must use the types.Unsafe singleton", p.Path())
			}
			continue
		}
		filtered = append(filtered, p)
	}
	if err := validateClosedPackages(filtered); err != nil {
		return nil, err
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Path() < filtered[j].Path()
	})
	var payload bytes.Buffer
	if err := gcexportdata.WriteBundle(&payload, fset, filtered); err != nil {
		return nil, err
	}
	payloadBytes := payload.Bytes()
	expectedPaths := make(map[string]bool, len(filtered))
	for _, pkg := range filtered {
		expectedPaths[pkg.Path()] = true
	}
	if err := validateEncodedPackageUniverse(payloadBytes, expectedPaths); err != nil {
		return nil, err
	}
	metadata, err := json.Marshal(target)
	if err != nil {
		return nil, err
	}
	if uint64(len(metadata)) > uint64(^uint32(0)) {
		return nil, fmt.Errorf("typebundle: target metadata is too large")
	}

	var envelope bytes.Buffer
	envelope.WriteString(envelopeMagic)
	if err := binary.Write(&envelope, binary.BigEndian, uint32(len(metadata))); err != nil {
		return nil, err
	}
	if err := binary.Write(&envelope, binary.BigEndian, uint64(len(payloadBytes))); err != nil {
		return nil, err
	}
	digest := sha256.New()
	digest.Write(metadata)
	digest.Write(payloadBytes)
	envelope.Write(digest.Sum(nil))
	envelope.Write(metadata)
	envelope.Write(payloadBytes)
	return envelope.Bytes(), nil
}

type targetWire struct {
	Compiler         *string   `json:"compiler"`
	GOOS             *string   `json:"goos"`
	GOARCH           *string   `json:"goarch"`
	CGOEnabled       *bool     `json:"cgo_enabled"`
	ToolchainVersion *string   `json:"toolchain_version"`
	LanguageVersion  *string   `json:"language_version"`
	BuildTags        *[]string `json:"build_tags"`
	ToolTags         *[]string `json:"tool_tags"`
	ReleaseTags      *[]string `json:"release_tags"`
}

func decodeTarget(data []byte) (Target, error) {
	if err := rejectDuplicateTargetFields(data); err != nil {
		return Target{}, err
	}
	var wire targetWire
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Target{}, fmt.Errorf("typebundle: decode target metadata: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Target{}, fmt.Errorf("typebundle: target metadata contains trailing JSON")
		}
		return Target{}, fmt.Errorf("typebundle: decode trailing target metadata: %w", err)
	}
	if wire.Compiler == nil || wire.GOOS == nil || wire.GOARCH == nil || wire.CGOEnabled == nil ||
		wire.ToolchainVersion == nil || wire.LanguageVersion == nil || wire.BuildTags == nil ||
		wire.ToolTags == nil || wire.ReleaseTags == nil {
		return Target{}, fmt.Errorf("typebundle: target metadata is incomplete")
	}
	target := Target{
		Compiler:         *wire.Compiler,
		GOOS:             *wire.GOOS,
		GOARCH:           *wire.GOARCH,
		CGOEnabled:       *wire.CGOEnabled,
		ToolchainVersion: *wire.ToolchainVersion,
		LanguageVersion:  *wire.LanguageVersion,
		BuildTags:        *wire.BuildTags,
		ToolTags:         *wire.ToolTags,
		ReleaseTags:      *wire.ReleaseTags,
	}
	canonical, err := json.Marshal(target.canonical())
	if err != nil {
		return Target{}, fmt.Errorf("typebundle: encode canonical target metadata: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return Target{}, fmt.Errorf("typebundle: target metadata is not in canonical envelope form")
	}
	return target, nil
}

func rejectDuplicateTargetFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("typebundle: decode target metadata: %w", err)
	}
	if opening != json.Delim('{') {
		return fmt.Errorf("typebundle: target metadata must be a JSON object")
	}
	seen := map[string]bool{}
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("typebundle: decode target metadata field: %w", err)
		}
		name, ok := nameToken.(string)
		if !ok {
			return fmt.Errorf("typebundle: target metadata field name is not a string")
		}
		if seen[name] {
			return fmt.Errorf("typebundle: target metadata contains duplicate field %q", name)
		}
		seen[name] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("typebundle: decode target metadata field %q: %w", name, err)
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("typebundle: decode target metadata: %w", err)
	}
	if closing != json.Delim('}') {
		return fmt.Errorf("typebundle: target metadata is not a complete JSON object")
	}
	return nil
}

// Read reconstructs the exact target and package set from a Write envelope.
// It performs no subprocess: only go/types and gcexportdata are used.
func Read(data []byte) (*Bundle, error) {
	if len(data) < len(envelopeMagic) || string(data[:len(envelopeMagic)]) != envelopeMagic {
		return nil, fmt.Errorf("typebundle: unsupported or corrupt bundle envelope")
	}
	reader := bytes.NewReader(data[len(envelopeMagic):])
	var metadataSize uint32
	if err := binary.Read(reader, binary.BigEndian, &metadataSize); err != nil {
		return nil, fmt.Errorf("typebundle: read target metadata size: %w", err)
	}
	var payloadSize uint64
	if err := binary.Read(reader, binary.BigEndian, &payloadSize); err != nil {
		return nil, fmt.Errorf("typebundle: read package payload size: %w", err)
	}
	wantDigest := make([]byte, sha256.Size)
	if _, err := io.ReadFull(reader, wantDigest); err != nil {
		return nil, fmt.Errorf("typebundle: read bundle content digest: %w", err)
	}
	if uint64(metadataSize) > uint64(reader.Len()) {
		return nil, fmt.Errorf("typebundle: target metadata length exceeds bundle size")
	}
	remainingPayload := uint64(reader.Len()) - uint64(metadataSize)
	if payloadSize != remainingPayload {
		return nil, fmt.Errorf("typebundle: package payload length is %d bytes, bundle contains %d bytes", payloadSize, remainingPayload)
	}
	metadata := make([]byte, int(metadataSize))
	if _, err := io.ReadFull(reader, metadata); err != nil {
		return nil, fmt.Errorf("typebundle: read target metadata: %w", err)
	}
	target, err := decodeTarget(metadata)
	if err != nil {
		return nil, err
	}
	sizes, err := target.Sizes()
	if err != nil {
		return nil, err
	}
	target = target.canonical()

	payload := make([]byte, reader.Len())
	if _, err := io.ReadFull(reader, payload); err != nil {
		return nil, fmt.Errorf("typebundle: read package payload: %w", err)
	}
	digest := sha256.New()
	digest.Write(metadata)
	digest.Write(payload)
	if !bytes.Equal(wantDigest, digest.Sum(nil)) {
		return nil, fmt.Errorf("typebundle: bundle content digest does not match metadata and package payload")
	}
	fset := token.NewFileSet()
	imports := map[string]*types.Package{"unsafe": types.Unsafe}
	pkgs, err := gcexportdata.ReadBundle(bytes.NewReader(payload), fset, imports)
	if err != nil {
		return nil, fmt.Errorf("typebundle: decode package payload: %w", err)
	}
	if err := validateDecodedPackageUniverse(pkgs, imports); err != nil {
		return nil, fmt.Errorf("typebundle: decoded package set: %w", err)
	}
	packagesByPath := make(map[string]*types.Package, len(pkgs)+1)
	packagesByPath["unsafe"] = types.Unsafe
	for _, p := range pkgs {
		if packagesByPath[p.Path()] != nil {
			return nil, fmt.Errorf("typebundle: duplicate package path %q", p.Path())
		}
		packagesByPath[p.Path()] = p
	}
	return &Bundle{Target: target, Sizes: sizes, Packages: packagesByPath}, nil
}

func validateClosedPackages(pkgs []*types.Package) error {
	byPath := make(map[string]*types.Package, len(pkgs))
	for _, pkg := range pkgs {
		if pkg == nil {
			return fmt.Errorf("typebundle: package set contains nil")
		}
		if pkg.Path() == "" {
			return fmt.Errorf("typebundle: package set contains an empty import path")
		}
		if !token.IsIdentifier(pkg.Name()) || pkg.Name() == "_" {
			return fmt.Errorf("typebundle: package %q has invalid package name %q", pkg.Path(), pkg.Name())
		}
		if pkg.Path() == "unsafe" && pkg != types.Unsafe {
			return fmt.Errorf("typebundle: package path %q must use the types.Unsafe singleton", pkg.Path())
		}
		if previous := byPath[pkg.Path()]; previous != nil && previous != pkg {
			return fmt.Errorf("typebundle: duplicate package path %q", pkg.Path())
		}
		byPath[pkg.Path()] = pkg
		if !pkg.Complete() {
			return fmt.Errorf("typebundle: package %q is incomplete", pkg.Path())
		}
		for _, name := range pkg.Scope().Names() {
			object := pkg.Scope().Lookup(name)
			if object == nil || object.Pkg() != pkg {
				owner := "<nil>"
				if object != nil && object.Pkg() != nil {
					owner = object.Pkg().Path()
				}
				return fmt.Errorf("typebundle: package %q scope object %q is owned by package %q", pkg.Path(), name, owner)
			}
		}
	}
	for _, pkg := range pkgs {
		seenImports := make(map[string]bool, len(pkg.Imports()))
		for _, imported := range pkg.Imports() {
			if imported == nil {
				return fmt.Errorf("typebundle: package %q has a nil import", pkg.Path())
			}
			if seenImports[imported.Path()] {
				return fmt.Errorf("typebundle: package %q has duplicate import %q", pkg.Path(), imported.Path())
			}
			seenImports[imported.Path()] = true
			if imported.Path() == "unsafe" {
				if imported != types.Unsafe {
					return fmt.Errorf("typebundle: package %q imports path %q with a package other than the types.Unsafe singleton", pkg.Path(), imported.Path())
				}
				continue
			}
			listed := byPath[imported.Path()]
			if listed == nil {
				return fmt.Errorf("typebundle: package set is not transitively closed: %q imports missing package %q", pkg.Path(), imported.Path())
			}
			if listed != imported {
				return fmt.Errorf("typebundle: package %q imports a different package identity for %q", pkg.Path(), imported.Path())
			}
		}
	}
	state := make(map[*types.Package]uint8, len(pkgs))
	var visit func(*types.Package) error
	visit = func(pkg *types.Package) error {
		state[pkg] = 1
		for _, imported := range pkg.Imports() {
			if imported == types.Unsafe {
				continue
			}
			switch state[imported] {
			case 1:
				return fmt.Errorf("typebundle: package import cycle reaches %q from %q", imported.Path(), pkg.Path())
			case 0:
				if err := visit(imported); err != nil {
					return err
				}
			}
		}
		state[pkg] = 2
		return nil
	}
	for _, pkg := range pkgs {
		if state[pkg] == 0 {
			if err := visit(pkg); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateEncodedPackageUniverse(payload []byte, expectedPaths map[string]bool) error {
	imports := map[string]*types.Package{"unsafe": types.Unsafe}
	pkgs, err := gcexportdata.ReadBundle(bytes.NewReader(payload), token.NewFileSet(), imports)
	if err != nil {
		return fmt.Errorf("typebundle: validate encoded package universe: %w", err)
	}
	if err := validateDecodedPackageUniverse(pkgs, imports); err != nil {
		return fmt.Errorf("typebundle: validate encoded package universe: %w", err)
	}
	for _, pkg := range pkgs {
		if !expectedPaths[pkg.Path()] {
			return fmt.Errorf("typebundle: semantic package set unexpectedly contains %q", pkg.Path())
		}
		delete(expectedPaths, pkg.Path())
	}
	if len(expectedPaths) != 0 {
		missing := make([]string, 0, len(expectedPaths))
		for path := range expectedPaths {
			missing = append(missing, path)
		}
		sort.Strings(missing)
		return fmt.Errorf("typebundle: semantic package set omitted bundled packages %q", missing)
	}
	return nil
}

func validateDecodedPackageUniverse(pkgs []*types.Package, imports map[string]*types.Package) error {
	if err := validateClosedPackages(pkgs); err != nil {
		return err
	}
	byPath := make(map[string]*types.Package, len(pkgs))
	for _, pkg := range pkgs {
		if previous := byPath[pkg.Path()]; previous != nil {
			return fmt.Errorf("typebundle: semantic package set contains duplicate path %q", pkg.Path())
		}
		byPath[pkg.Path()] = pkg
	}
	for path, imported := range imports {
		if path == "unsafe" {
			if imported != types.Unsafe {
				return fmt.Errorf("typebundle: semantic package set replaced the types.Unsafe singleton")
			}
			continue
		}
		bundled := byPath[path]
		if bundled == nil {
			return fmt.Errorf("typebundle: semantic package set is not transitively closed: encoded types reference package %q outside the bundled package list", path)
		}
		if bundled != imported {
			return fmt.Errorf("typebundle: semantic package set has different decoded identities for %q", path)
		}
	}
	return nil
}
