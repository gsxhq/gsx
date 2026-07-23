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
	Capabilities     clientCapabilities `json:"capabilities"`
	RootURI          string             `json:"rootUri,omitempty"`
	WorkspaceFolders []workspaceFolder  `json:"workspaceFolders,omitempty"`
}

type workspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type didChangeWorkspaceFoldersParams struct {
	Event workspaceFoldersChangeEvent `json:"event"`
}

type workspaceFoldersChangeEvent struct {
	Added   []workspaceFolder `json:"added"`
	Removed []workspaceFolder `json:"removed"`
}

type clientCapabilities struct {
	General      generalCapabilities      `json:"general"`
	Workspace    workspaceCapabilities    `json:"workspace"`
	TextDocument textDocumentCapabilities `json:"textDocument"`
}

type textDocumentCapabilities struct {
	Rename     renameClientCapabilities     `json:"rename"`
	Completion completionClientCapabilities `json:"completion"`
}

type renameClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration"`
	PrepareSupport      bool `json:"prepareSupport"`
}

type completionClientCapabilities struct {
	CompletionItem completionItemClientCapabilities `json:"completionItem"`
}

// completionItemClientCapabilities carries the completion-item capabilities gsx
// reads:
//   - SnippetSupport: whether the client can render a snippet's `$1` tabstops
//     and place the cursor accordingly. Gated rather than assumed, per the LSP
//     spec — a client that never sets it would otherwise see a literal `$1`
//     typed into the buffer.
//   - LabelDetailsSupport: whether the client renders CompletionItem.labelDetails
//     (the modern label-adjacent detail/description fields). When set,
//     auto-import items put the import path in labelDetails.description; when
//     unset they fall back to the plain detail string.
type completionItemClientCapabilities struct {
	SnippetSupport      bool `json:"snippetSupport"`
	LabelDetailsSupport bool `json:"labelDetailsSupport"`
}

type generalCapabilities struct {
	PositionEncodings []string `json:"positionEncodings"`
}

type workspaceCapabilities struct {
	DidChangeWatchedFiles didChangeWatchedFilesClientCapabilities `json:"didChangeWatchedFiles"`
}

type didChangeWatchedFilesClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration"`
}

type registrationParams struct {
	Registrations []registration `json:"registrations"`
}

type registration struct {
	ID              string `json:"id"`
	Method          string `json:"method"`
	RegisterOptions any    `json:"registerOptions"`
}

type didChangeWatchedFilesRegistrationOptions struct {
	Watchers []fileSystemWatcher `json:"watchers"`
}

type fileSystemWatcher struct {
	GlobPattern string `json:"globPattern"`
}

const (
	fileChangeCreated = 1
	fileChangeChanged = 2
	fileChangeDeleted = 3
)

type didChangeWatchedFilesParams struct {
	Changes []fileEvent `json:"changes"`
}

type fileEvent struct {
	URI  string `json:"uri"`
	Type int    `json:"type"`
}

type initializeResult struct {
	Capabilities serverCapabilities `json:"capabilities"`
}

type serverCapabilities struct {
	PositionEncoding           string                      `json:"positionEncoding"`
	TextDocumentSync           int                         `json:"textDocumentSync"`
	DefinitionProvider         bool                        `json:"definitionProvider"`
	ReferencesProvider         bool                        `json:"referencesProvider"`
	RenameProvider             *RenameOptions              `json:"renameProvider,omitempty"`
	DocumentFormattingProvider bool                        `json:"documentFormattingProvider"`
	HoverProvider              bool                        `json:"hoverProvider"`
	CompletionProvider         *CompletionOptions          `json:"completionProvider,omitempty"`
	DocumentSymbolProvider     bool                        `json:"documentSymbolProvider"`
	WorkspaceSymbolProvider    bool                        `json:"workspaceSymbolProvider"`
	CodeActionProvider         *CodeActionOptions          `json:"codeActionProvider,omitempty"`
	Workspace                  workspaceServerCapabilities `json:"workspace"`
}

type workspaceServerCapabilities struct {
	WorkspaceFolders workspaceFoldersServerCapabilities `json:"workspaceFolders"`
}

type workspaceFoldersServerCapabilities struct {
	Supported           bool `json:"supported"`
	ChangeNotifications bool `json:"changeNotifications"`
}

type RenameOptions struct {
	PrepareProvider bool `json:"prepareProvider"`
}

type renameRegistrationOptions struct {
	DocumentSelector []documentFilter `json:"documentSelector"`
	RenameOptions
}

type documentFilter struct {
	Scheme  string `json:"scheme,omitempty"`
	Pattern string `json:"pattern,omitempty"`
}

// CodeActionOptions advertises which code-action kinds the server produces. It
// is a struct rather than a bare `true` so clients know they can wire
// editor.codeActionsOnSave to source.organizeImports.
type CodeActionOptions struct {
	CodeActionKinds []string `json:"codeActionKinds"`
}

// organizeImportsKind is the LSP kind for the organize-imports source action.
const organizeImportsKind = "source.organizeImports"

// quickFixKind is the LSP kind for a quick fix attached to a diagnostic.
const quickFixKind = "quickfix"

type codeActionContext struct {
	// Only restricts the kinds the client wants. Empty means "any".
	Only []string `json:"only"`
	// Diagnostics are the diagnostics the client believes overlap Range. Decoded
	// for protocol completeness and deliberately ignored: handleCodeAction offers
	// a quickfix for every missing qualifier in the whole file rather than
	// scoping to this set, because the only position available to match against
	// (MissingImport.Pos) carries a deliberately-wrong column for child-prop
	// expressions — matching on it, or on Diagnostic.Message text, would be
	// exactly the kind of unsound heuristic this project rejects.
	Diagnostics []Diagnostic `json:"diagnostics"`
}

type codeActionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Range        Range                  `json:"range"`
	Context      codeActionContext      `json:"context"`
}

// WorkspaceEdit maps a document URI to the edits to apply to it.
type WorkspaceEdit struct {
	Changes map[string][]TextEdit `json:"changes"`
}

// CodeAction is one entry of the textDocument/codeAction result. Edit is carried
// inline, so the server advertises no resolveProvider.
type CodeAction struct {
	Title string         `json:"title"`
	Kind  string         `json:"kind"`
	Edit  *WorkspaceEdit `json:"edit,omitempty"`
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

type renameParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	NewName      string                 `json:"newName"`
}

type prepareRenameResult struct {
	Range       Range  `json:"range"`
	Placeholder string `json:"placeholder"`
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

type workspaceSymbolParams struct {
	Query string `json:"query"`
}

// SymbolInformation is the workspace/symbol result entry (the flat, universally
// supported form): a named symbol with its location and containing scope.
type SymbolInformation struct {
	Name          string   `json:"name"`
	Kind          int      `json:"kind"`
	ContainerName string   `json:"containerName,omitempty"`
	Location      Location `json:"location"`
}

// CompletionOptions advertises completion support and the characters that
// re-trigger it as the user types (beyond the client's default identifier
// trigger). ResolveProvider advertises completionItem/resolve support: the
// client may send back a completion item (with its Data untouched) to fetch
// documentation lazily rather than eagerly on every item in a full list.
type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
	ResolveProvider   bool     `json:"resolveProvider,omitempty"`
}

// completionResolveData is the payload CompletionItem.Data carries for a
// candidate whose documentation is fetched lazily via completionItem/resolve
// rather than filled in eagerly at list-build time (see completion_docs.go
// and completion_resolve.go). The LSP client round-trips CompletionItem.Data
// through completionItem/resolve UNCHANGED — it is never interpreted or
// mutated by the client — so encoding it as a plain typed struct (rather than
// json.RawMessage) lets encoding/json marshal it on the way out and unmarshal
// it directly back into the same shape on the way in, with no intermediate
// any/RawMessage re-encoding step in either direction.
//
// File+Line is a location the SERVER computed from its own analysis (a real
// go/token position it already resolved via go/packages or //line-mapped
// skeleton coordinates) — NEVER a client-supplied path. Because Data
// round-trips through the client, a hostile or buggy client could still send
// an ARBITRARY {file,line} pair with completionItem/resolve; the handler
// (handleCompletionResolve) treats File as untrusted input and revalidates it
// against an allow-list before reading anything — see resolvablePath in
// completion_resolve.go for the threat model and the gate itself.
type completionResolveData struct {
	File string `json:"file"`
	Line int    `json:"line"`
}

type completionParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// CompletionList is the textDocument/completion result. Items is always a
// non-nil slice: clients treat a JSON null items array differently from an
// empty one.
type CompletionList struct {
	IsIncomplete bool             `json:"isIncomplete"`
	Items        []CompletionItem `json:"items"`
}

// CompletionItemLabelDetails is the LSP labelDetails field: label-adjacent
// annotations a capable client renders next to (Detail) and after (Description)
// the label. gsx uses Description to carry an auto-import candidate's import
// path. Emitted only when the client advertises labelDetailsSupport.
type CompletionItemLabelDetails struct {
	Detail      string `json:"detail,omitempty"`
	Description string `json:"description,omitempty"`
}

// CompletionItem is one completion candidate.
type CompletionItem struct {
	Label         string                      `json:"label"`
	LabelDetails  *CompletionItemLabelDetails `json:"labelDetails,omitempty"`
	Kind          int                         `json:"kind,omitempty"`
	Detail        string                      `json:"detail,omitempty"`
	Documentation *MarkupContent              `json:"documentation,omitempty"`
	SortText      string                      `json:"sortText,omitempty"`
	FilterText    string                      `json:"filterText,omitempty"`
	TextEdit      *TextEdit                   `json:"textEdit,omitempty"`
	// AdditionalTextEdits are edits applied together with TextEdit on accept,
	// used by auto-import completion to insert the package import. LSP requires
	// they not overlap TextEdit; importEditFor enforces this (the import region
	// is above the cursor). nil for every ordinary item.
	AdditionalTextEdits []TextEdit `json:"additionalTextEdits,omitempty"`
	// Data carries a lazy-doc lookup payload (see completionResolveData) that
	// the client returns unchanged with a completionItem/resolve request. nil
	// for every item whose Documentation (if any) was already filled in eagerly.
	Data *completionResolveData `json:"data,omitempty"`
	// InsertTextFormat selects how the client interprets TextEdit.NewText:
	// insertTextFormatPlainText (the default; omitted so the field disappears
	// entirely for every item that does not opt in) or insertTextFormatSnippet,
	// which lets NewText carry `$1`/`$0` tabstops. Only ever set to the snippet
	// value, and only when the negotiated client capability
	// (textDocument.completion.completionItem.snippetSupport) says the client
	// can render one — see Server.snippetSupport.
	InsertTextFormat int `json:"insertTextFormat,omitempty"`
}

// LSP InsertTextFormat constants (textDocument/completion).
const (
	insertTextFormatPlainText = 1 // PlainText: NewText is inserted verbatim.
	// insertTextFormatSnippet marks NewText as an LSP snippet: `$1`, `$2`, ...
	// are tabstops the client cycles through, an unnumbered `$0` (or the
	// implicit end of the snippet when no `$0` appears) is the final cursor
	// position. gsx only ever emits a single `$1` tabstop, so there is no `$0`
	// to place and no risk of tabstop-ordering ambiguity.
	insertTextFormatSnippet = 2
)

// LSP CompletionItemKind constants used across completion tasks.
const (
	ciKindText        = 1  // Text
	ciKindMethod      = 2  // Method
	ciKindFunction    = 3  // Function
	ciKindConstructor = 4  // Constructor
	ciKindField       = 5  // Field
	ciKindVariable    = 6  // Variable
	ciKindClass       = 7  // Class
	ciKindInterface   = 8  // Interface
	ciKindModule      = 9  // Module
	ciKindProperty    = 10 // Property
	ciKindEnum        = 13 // Enum
	ciKindKeyword     = 14 // Keyword
	ciKindEnumMember  = 20 // EnumMember
	ciKindConstant    = 21 // Constant
	ciKindStruct      = 22 // Struct
	// ciKindOperator marks pipe filter items: a bare filter name (`f`) and a
	// parameterized call (`f()`) are distinct, semantically different pipe
	// stages, so accepting the item must never auto-append `()` the way
	// editors do for Function/Method kinds.
	ciKindOperator      = 24 // Operator
	ciKindTypeParameter = 25 // TypeParameter
)
