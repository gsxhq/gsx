package lsp

// Position is a 0-based LSP position; Character is counted in the negotiated
// encoding (UTF-16 by default).
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// Range is a half-open [Start, End) span.
type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

// Diagnostic is the LSP wire form of one problem.
type Diagnostic struct {
	Range    Range  `json:"range"`
	Severity int    `json:"severity"`
	Code     string `json:"code,omitempty"`
	Source   string `json:"source,omitempty"`
	Message  string `json:"message"`
}

type publishDiagnosticsParams struct {
	URI string `json:"uri"`
	// Version is the document version the diagnostics were computed on. The editor
	// discards a publish whose version is older than the live buffer, so stale
	// squiggles from a superseded analysis never linger. Omitted (nil) for files
	// that are not open (version unknown) and for clear-on-close.
	Version     *int         `json:"version,omitempty"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type initializeParams struct {
	Capabilities clientCapabilities `json:"capabilities"`
}

type clientCapabilities struct {
	General generalCapabilities `json:"general"`
}

type generalCapabilities struct {
	PositionEncodings []string `json:"positionEncodings"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
}

type serverCapabilities struct {
	PositionEncoding           string `json:"positionEncoding"`
	TextDocumentSync           int    `json:"textDocumentSync"`
	DefinitionProvider         bool   `json:"definitionProvider"`
	ReferencesProvider         bool   `json:"referencesProvider"`
	DocumentFormattingProvider bool   `json:"documentFormattingProvider"`
	HoverProvider              bool   `json:"hoverProvider"`
	DocumentSymbolProvider     bool   `json:"documentSymbolProvider"`
}

// TextEdit is a single text replacement: NewText replaces the span at Range.
type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}

// Hover is the textDocument/hover result. Range (the span the editor highlights)
// is optional.
type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

// MarkupContent is LSP markup content; Kind is "markdown" or "plaintext".
type MarkupContent struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// documentFormattingParams is the payload of textDocument/formatting. Options
// (tab size / spaces) are accepted but ignored: gsx has one canonical form, like
// gofmt.
type documentFormattingParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type referenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

type referenceParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      referenceContext       `json:"context"`
}

type textDocumentPositionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// Location is the LSP Location type: a URI and a range within it.
type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

type textDocumentItem struct {
	URI     string `json:"uri"`
	Text    string `json:"text"`
	Version int    `json:"version"`
}

type didOpenParams struct {
	TextDocument textDocumentItem `json:"textDocument"`
}

type versionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type contentChange struct {
	Text string `json:"text"`
}

type didChangeParams struct {
	TextDocument   versionedTextDocumentIdentifier `json:"textDocument"`
	ContentChanges []contentChange                 `json:"contentChanges"`
}

type textDocumentIdentifier struct {
	URI string `json:"uri"`
}

type didCloseParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

type documentSymbolParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
}

// DocumentSymbol is the hierarchical textDocument/documentSymbol result. gsx
// decls do not nest, so Children is always omitted.
type DocumentSymbol struct {
	Name           string `json:"name"`
	Kind           int    `json:"kind"`
	Range          Range  `json:"range"`
	SelectionRange Range  `json:"selectionRange"`
}
