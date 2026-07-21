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
	Rename renameClientCapabilities `json:"rename"`
}

type renameClientCapabilities struct {
	DynamicRegistration bool `json:"dynamicRegistration"`
	PrepareSupport      bool `json:"prepareSupport"`
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
