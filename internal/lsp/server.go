package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gsxhq/gsx/internal/diag"
	"github.com/gsxhq/gsx/internal/gsxfmt"
)

// defaultDebounce is how long the server waits for typing to settle before
// re-analyzing a changed package. Matches modern gopls's diagnosticsDelay
// default (1s; raised from 250ms years ago). It also reduces analysisMu
// collisions between a background re-analysis worker and completion's ephemeral
// analysis: a whole-package warm re-analyze is ~1.3s and blocks completion on
// analysisMu until it finishes, so re-analyzing less eagerly keeps completion at
// its floor during active typing (see perf investigation 2026-07-22).
const defaultDebounce = time.Second

// Analyzer computes diagnostics for the package in dir. Analysis override maps
// are immutable invocation snapshots for stateless implementations; they must
// never begin, update, or retain buffer lifetime. The serialized SetOverride
// and ClearOverride transitions below are the only lifetime authority.
type Analyzer interface {
	// SetOverride begins or updates the authoritative lifetime of one editor
	// buffer. The server invokes it synchronously on its state goroutine before
	// scheduling analysis, so superseded analysis workers never own mutations.
	// The returned directories are the exact package views invalidated by the
	// transition, including reverse dependants. A transition may return both an
	// affected set and an error: that means the previous view is no longer
	// authoritative and those directories must still be evicted and reanalyzed
	// against the analyzer's fail-closed state.
	SetOverride(path string, source []byte) (affected []string, err error)
	Analyze(dir string, override map[string][]byte) (*Package, error)
	// AnalyzeEphemeral runs one synchronous warm analysis of dir with path's
	// source replaced by content, WITHOUT touching override lifetime, caches, or
	// dirty tracking. Fails soft: source-level breakage returns a
	// diagnostics-only Package. Kept for e2e/tooling callers that want the
	// blocking guarantee; the completion/nav handlers use the non-blocking
	// variant below.
	AnalyzeEphemeral(dir, path string, content []byte) (*Package, error)
	// AnalyzeEphemeralNonBlocking is AnalyzeEphemeral that never blocks on the
	// warm Module's analysis lock: acquired=false (with a nil Package) means an
	// in-flight background analysis held the lock and this request declined to
	// wait. The completion/nav handlers run inline on the single dispatch
	// goroutine, so they call this unconditionally — when uncontended it acquires
	// at once and is identical to AnalyzeEphemeral; when contended they serve a
	// retained-snapshot fallback (or reply empty/null) rather than stall the whole
	// server behind a ~1 s background Package. Fails soft like AnalyzeEphemeral.
	AnalyzeEphemeralNonBlocking(dir, path string, content []byte) (*Package, bool, error)
	// ClearOverride ends the authoritative lifetime of one editor buffer. The
	// analyzer must restore the path to saved-disk or absent-source semantics;
	// omitting a path from a later Analyze override map is not that transition,
	// because analyzers may retain warm per-module state between calls.
	ClearOverride(path string) (affected []string, err error)
	// AnalyzeModule analyzes every gsx package in the module containing dir and
	// returns one flat cross-reference list (each component once; Refs span the
	// whole module). Used by find-references; failure is non-fatal (the server
	// falls back to the per-package CrossIndex).
	AnalyzeModule(dir string, override map[string][]byte) ([]CrossRef, error)
	// AnalyzeModuleParams returns complete, exact GSX parameter declarations,
	// semantic body uses, and invocation facts for rename. It must not return
	// partial families.
	AnalyzeModuleParams(dir string, override map[string][]byte) ([]ComponentParamRenameFact, error)
	// ModuleSymbols returns every symbol (component + top-level Go decl) declared
	// in every .gsx package in the module containing dir. Used by workspace/symbol.
	ModuleSymbols(dir string, override map[string][]byte) ([]Symbol, error)
	// FormatSettings returns the effective layout settings for path (defaults
	// Width 80, TabWidth pretty.DefaultTabWidth), applying gsx.toml [formatter] >
	// .editorconfig > built-in precedence. path must be absolute. Used by
	// textDocument/formatting and the organizeImports code action.
	FormatSettings(path string) gsxfmt.FormatSettings
	// ImportsMode returns the gsx.toml [formatter] imports mode for the given
	// directory (default goimports). Used by textDocument/formatting; the
	// source.organizeImports code action deliberately ignores it and always
	// organizes.
	ImportsMode(dir string) gsxfmt.ImportsMode
	// ResolveImport maps an undefined qualifier (name, and the selector symbol used
	// on it) to the import path(s) that could supply it. Exactly one candidate means
	// organizeImports may add it unattended; several means the user picks via a
	// quickfix; none means we offer nothing. It may read package export data, so it
	// is called ONLY from user-triggered code-action handlers, never during analysis.
	ResolveImport(dir, name, symbol string) []string
}

// diskRefresher is the saved-source transition paired with Analyzer. Keeping it
// as a focused capability lets small read-only analyzer implementations remain
// useful, while the production LSP analyzer must implement it or watched events
// fail closed and evict every open package.
type diskRefresher interface {
	RefreshDisk(paths []string) (affected []string, err error)
}

// Server is a stdio LSP server that publishes gsx diagnostics. It owns the
// protocol; code analysis is delegated to an injected Analyzer.
//
// Diagnostics are debounced: a burst of edits resets a per-directory timer and
// only the settled text is analyzed, so typing "|> upper" triggers one analysis
// instead of one per keystroke. The settled analysis then runs on a worker
// goroutine so a heavy package type-check never blocks hover/definition; its
// result is published back on the Run goroutine, and a per-directory generation
// counter discards a result that a newer edit has already superseded.
//
// All mutable server state (pkgs, timers, lastURI, gen), every buffer-lifetime
// transition, and all writes to conn happen on the Run goroutine. Worker
// goroutines only invoke analysis on a snapshot and send the result back; they
// never begin, update, or clear an override.
type Server struct {
	conn                      *conn
	docs                      *docStore
	analyzer                  Analyzer
	pkgs                      map[string]*Package // dir → latest analyzed package
	enc                       encoding
	shutdown                  bool
	exited                    bool
	watchDynamicRegistration  bool
	watchRegistrationActive   bool
	renameDynamicRegistration bool
	renamePrepareSupport      bool
	renameRegistrationActive  bool
	// snippetSupport mirrors the client's textDocument.completion.completionItem
	// .snippetSupport capability, captured once at initialize. When true, value-
	// taking attribute completions insert a `$1` tabstop inside the quotes
	// (InsertTextFormat = Snippet) instead of the plain `name=""` insert — see
	// htmlAttrItem.
	snippetSupport        bool
	diskViewValid         bool
	workspaceViewValid    bool
	pendingClientRequests map[string]func(frame) error

	moduleRefs        []CrossRef                 // whole-module cross-reference index (lazy; find-references)
	moduleRefsValid   bool                       // false ⇒ rebuild on next references request
	moduleParams      []ComponentParamRenameFact // complete GSX parameter families (lazy; rename)
	moduleParamsValid bool                       // false ⇒ rebuild on next rename request
	moduleParamsDir   string                     // request directory that owns the cached module view

	moduleSyms map[string]moduleSymbolCache // normalized module root → lazy workspace-symbol index

	// depDocs caches completionItem/resolve's dependency-file doc extraction
	// (see completion_resolve.go): module-cache/GOROOT source files are
	// immutable within a session, so entries never need invalidation.
	depDocs *depDocCache

	workspaceFolders         []workspaceFolder   // normalized initialized file workspace folders
	workspaceRoots           []string            // normalized absolute folder paths
	workspaceModules         []string            // sorted Go module roots owned by workspaceRoots
	workspaceModuleOwners    map[string]string   // module root → deterministic declaring workspace root
	workspaceModulePaths     map[string]string   // module root → parsed go.mod module directive
	pendingWorkspaceMetadata map[string]struct{} // normalized withheld go.mod/go.work paths awaiting replay

	debounce time.Duration
	// schedule arms a timer that calls f after d, returning a cancel func. It is a
	// field so tests can drive debouncing deterministically; production uses
	// time.AfterFunc.
	schedule func(d time.Duration, f func()) (cancel func())
	timers   map[string]debounceTimer // dir → pending debounce timer and its mutation epoch
	lastURI  map[string]string        // dir → most recently edited URI (fallback for positionless diags)
	fireC    chan debounceEvent       // a debounce event whose timer elapsed; drained by Run

	epoch    map[string]int      // dir → latest document-mutation epoch
	gen      map[string]int      // dir → generation of the latest requested analysis
	resultsC chan analysisResult // completed worker analyses; drained by Run
	doneC    chan struct{}       // closed when Run returns; releases blocked workers
}

// debounceTimer and debounceEvent carry the document-mutation epoch that armed
// the timer. Timer.Stop cannot retract a callback that has already started, so
// Run validates the event against current state before it can launch analysis.
type debounceTimer struct {
	cancel func()
	epoch  int
}

type debounceEvent struct {
	dir   string
	epoch int
	gen   int
}

// analysisResult is one worker's finished analysis, routed back to the Run
// goroutine for publishing. gen identifies which request it answers, so a result
// superseded by a newer edit can be discarded.
type analysisResult struct {
	dir         string
	gen         int
	fallbackURI string
	snap        map[string]docSnap
	pkg         *Package
	err         error
}

// NewServer builds a Server reading requests from r and writing responses and
// notifications to w. The default position encoding is UTF-16 until initialize
// negotiates otherwise.
func NewServer(r io.Reader, w io.Writer, a Analyzer) *Server {
	return &Server{
		conn:                     newConn(r, w),
		docs:                     newDocStore(),
		analyzer:                 a,
		pkgs:                     map[string]*Package{},
		enc:                      encUTF16,
		diskViewValid:            true,
		workspaceViewValid:       false,
		pendingClientRequests:    map[string]func(frame) error{},
		moduleSyms:               map[string]moduleSymbolCache{},
		depDocs:                  newDepDocCache(),
		workspaceModuleOwners:    map[string]string{},
		workspaceModulePaths:     map[string]string{},
		pendingWorkspaceMetadata: map[string]struct{}{},
		debounce:                 defaultDebounce,
		schedule: func(d time.Duration, f func()) func() {
			t := time.AfterFunc(d, f)
			return func() { t.Stop() }
		},
		timers:   map[string]debounceTimer{},
		lastURI:  map[string]string{},
		fireC:    make(chan debounceEvent, 16),
		epoch:    map[string]int{},
		gen:      map[string]int{},
		resultsC: make(chan analysisResult, 16),
	}
}

// readResult is one frame (or terminal error) forwarded from the reader
// goroutine to the Run loop.
type readResult struct {
	f   frame
	err error
}

// Run reads and dispatches messages until the stream closes or an `exit`
// notification is received. A reader goroutine forwards frames over a channel so
// the loop can also wake on debounce-timer fires and completed worker analyses;
// all message handling and publishing stay on this single goroutine.
func (s *Server) Run() error {
	frames := make(chan readResult)
	s.doneC = make(chan struct{})
	defer close(s.doneC) // stop the reader and release blocked workers when we return
	go s.readLoop(frames, s.doneC)

	for {
		select {
		case rr := <-frames:
			if errors.Is(rr.err, io.EOF) {
				return nil
			}
			if rr.err != nil {
				return rr.err
			}
			if err := s.handle(rr.f); err != nil {
				return err
			}
			if s.exited {
				return nil
			}
		case event := <-s.fireC:
			// A canceled timer callback may already have escaped Timer.Stop. Only
			// the event armed by the current mutation epoch can analyze.
			fallbackURI, generation, ok := s.takeDebounce(event)
			if !ok {
				continue
			}
			s.launchAnalysis(event.dir, fallbackURI, generation)
		case res := <-s.resultsC:
			// Discard a result a newer edit has already superseded; else publish it.
			if res.gen != s.gen[res.dir] {
				continue
			}
			if err := s.publishAnalysis(res.dir, res.fallbackURI, res.snap, res.pkg, res.err); err != nil {
				return err
			}
		}
	}
}

// readLoop reads frames until the stream closes or Run signals done, forwarding
// each over out. It is the only goroutine other than Run, and it touches no
// server state.
func (s *Server) readLoop(out chan<- readResult, done <-chan struct{}) {
	for {
		f, err := s.conn.read()
		select {
		case out <- readResult{f: f, err: err}:
		case <-done:
			return
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) handle(f frame) error {
	if f.Method == "" && len(f.ID) != 0 {
		if complete := s.pendingClientRequests[string(f.ID)]; complete != nil {
			delete(s.pendingClientRequests, string(f.ID))
			return complete(f)
		}
		return nil
	}
	switch f.Method {
	case "initialize":
		return s.handleInitialize(f)
	case "initialized":
		return s.handleInitialized()
	case "shutdown":
		s.shutdown = true
		return s.reply(f.ID, nil)
	case "exit":
		s.exited = true
		return nil
	case "textDocument/didOpen":
		return s.handleDidOpen(f)
	case "textDocument/didChange":
		return s.handleDidChange(f)
	case "textDocument/didClose":
		return s.handleDidClose(f)
	case "workspace/didChangeWatchedFiles":
		return s.handleDidChangeWatchedFiles(f)
	case "workspace/didChangeWorkspaceFolders":
		return s.handleDidChangeWorkspaceFolders(f)
	case "textDocument/definition":
		return s.handleDefinition(f)
	case "textDocument/references":
		return s.handleReferences(f)
	case "textDocument/prepareRename":
		return s.handlePrepareRename(f)
	case "textDocument/rename":
		return s.handleRename(f)
	case "textDocument/hover":
		return s.handleHover(f)
	case "textDocument/completion":
		return s.handleCompletion(f)
	case "completionItem/resolve":
		return s.handleCompletionResolve(f)
	case "textDocument/formatting":
		return s.handleFormatting(f)
	case "textDocument/codeAction":
		return s.handleCodeAction(f)
	case "textDocument/documentSymbol":
		return s.handleDocumentSymbol(f)
	case "workspace/symbol":
		return s.handleWorkspaceSymbol(f)
	default:
		if len(f.ID) > 0 {
			return s.replyError(f.ID, -32601, "method not found: "+f.Method)
		}
		return nil // ignore unknown notifications
	}
}

func (s *Server) handleInitialize(f frame) error {
	var p initializeParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return s.replyError(f.ID, -32602, "invalid initialize params: "+err.Error())
	}
	s.enc = encUTF16
	encName := "utf-16"
	if slices.Contains(p.Capabilities.General.PositionEncodings, "utf-8") {
		s.enc = encUTF8
		encName = "utf-8"
	}
	s.watchDynamicRegistration = p.Capabilities.Workspace.DidChangeWatchedFiles.DynamicRegistration
	s.renameDynamicRegistration = p.Capabilities.TextDocument.Rename.DynamicRegistration
	s.renamePrepareSupport = p.Capabilities.TextDocument.Rename.PrepareSupport
	s.snippetSupport = p.Capabilities.TextDocument.Completion.CompletionItem.SnippetSupport
	var folders []workspaceFolder
	switch {
	case p.WorkspaceFolders != nil:
		folders = p.WorkspaceFolders
	case p.RootURI != "":
		folders = []workspaceFolder{{URI: p.RootURI}}
	default:
		cwd, err := os.Getwd()
		if err != nil {
			return s.replyError(f.ID, -32602, "initialize workspace: process working directory: "+err.Error())
		}
		folders = []workspaceFolder{{URI: pathToURI(cwd)}}
	}
	if err := s.setWorkspaceFolders(folders); err != nil {
		return s.replyError(f.ID, -32602, "initialize workspace: "+err.Error())
	}
	return s.reply(f.ID, initializeResult{Capabilities: serverCapabilities{
		PositionEncoding:           encName,
		TextDocumentSync:           1, // full document sync
		DefinitionProvider:         true,
		ReferencesProvider:         true,
		RenameProvider:             nil,
		DocumentFormattingProvider: true,
		HoverProvider:              true,
		CompletionProvider:         &CompletionOptions{TriggerCharacters: []string{".", "<", ">", "\"", "|"}, ResolveProvider: true},
		DocumentSymbolProvider:     true,
		WorkspaceSymbolProvider:    true,
		CodeActionProvider:         &CodeActionOptions{CodeActionKinds: []string{organizeImportsKind, quickFixKind}},
		Workspace: workspaceServerCapabilities{WorkspaceFolders: workspaceFoldersServerCapabilities{
			Supported: true, ChangeNotifications: true,
		}},
	}})
}

const watchedFilesRegistrationID = "gsx-watched-files"
const renameRegistrationID = "gsx-rename"

func (s *Server) handleInitialized() error {
	if !s.watchDynamicRegistration {
		return s.notify("window/logMessage", struct {
			Type    int    `json:"type"`
			Message string `json:"message"`
		}{Type: 2, Message: "gsx: client does not support dynamic watched-file registration; closed-file disk changes cannot be observed safely"})
	}
	s.watchRegistrationActive = false
	return s.requestClient(watchedFilesRegistrationID, "client/registerCapability", registrationParams{Registrations: []registration{{
		ID:     watchedFilesRegistrationID,
		Method: "workspace/didChangeWatchedFiles",
		RegisterOptions: didChangeWatchedFilesRegistrationOptions{Watchers: []fileSystemWatcher{
			{GlobPattern: "**/*.gsx"},
			{GlobPattern: "**/gsx.toml"},
			{GlobPattern: "**/go.mod"},
			{GlobPattern: "**/go.work"},
		}},
	}}}, func(response frame) error {
		if len(response.Error) != 0 && string(response.Error) != "null" {
			return s.notify("window/logMessage", struct {
				Type    int    `json:"type"`
				Message string `json:"message"`
			}{Type: 1, Message: "gsx: client rejected watched-file registration; component parameter rename remains unavailable"})
		}
		s.watchRegistrationActive = true
		return s.registerRename()
	})
}

func (s *Server) registerRename() error {
	if !s.renameDynamicRegistration {
		return s.notify("window/logMessage", struct {
			Type    int    `json:"type"`
			Message string `json:"message"`
		}{Type: 2, Message: "gsx: client does not support dynamic rename registration; component parameter rename remains unavailable"})
	}
	s.renameRegistrationActive = false
	return s.requestClient(renameRegistrationID, "client/registerCapability", registrationParams{Registrations: []registration{{
		ID:     renameRegistrationID,
		Method: "textDocument/rename",
		RegisterOptions: renameRegistrationOptions{
			DocumentSelector: []documentFilter{{Scheme: "file", Pattern: "**/*.gsx"}},
			RenameOptions:    RenameOptions{PrepareProvider: s.renamePrepareSupport},
		},
	}}}, func(response frame) error {
		if len(response.Error) != 0 && string(response.Error) != "null" {
			return s.notify("window/logMessage", struct {
				Type    int    `json:"type"`
				Message string `json:"message"`
			}{Type: 1, Message: "gsx: client rejected dynamic rename registration; component parameter rename remains unavailable"})
		}
		s.renameRegistrationActive = true
		return nil
	})
}

func (s *Server) requestClient(id, method string, params any, complete func(frame) error) error {
	idJSON, err := json.Marshal(id)
	if err != nil {
		return err
	}
	key := string(idJSON)
	if _, exists := s.pendingClientRequests[key]; exists {
		return fmt.Errorf("lsp: duplicate pending client request %s", id)
	}
	s.pendingClientRequests[key] = complete
	if err := s.conn.writeMessage(struct {
		JSONRPC string `json:"jsonrpc"`
		ID      string `json:"id"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: params}); err != nil {
		delete(s.pendingClientRequests, key)
		return err
	}
	return nil
}

func (s *Server) handleDidChangeWatchedFiles(f frame) error {
	var params didChangeWatchedFilesParams
	if err := json.Unmarshal(f.Params, &params); err != nil {
		return nil
	}
	paths := make([]string, 0, len(params.Changes))
	fallbackDirs := map[string]bool{}
	for _, change := range params.Changes {
		path, err := localFileURIPath(change.URI)
		if err != nil || !watchedFileRelevant(path) {
			continue
		}
		paths = append(paths, path)
		fallbackDirs[filepath.Dir(path)] = true
	}
	if len(paths) == 0 {
		return nil
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	metadataPaths := slices.DeleteFunc(slices.Clone(paths), func(path string) bool { return !workspaceMetadataPath(path) })
	nonMetadataPaths := slices.DeleteFunc(slices.Clone(paths), workspaceMetadataPath)
	if s.pendingWorkspaceMetadata == nil {
		s.pendingWorkspaceMetadata = make(map[string]struct{})
	}
	for _, path := range metadataPaths {
		s.pendingWorkspaceMetadata[sourcePath(path)] = struct{}{}
	}
	var replay []string
	recoveringWorkspace := false
	if len(metadataPaths) != 0 {
		prepared, err := prepareWorkspaceFolders(s.workspaceFolders)
		if err != nil {
			s.workspaceViewValid = false
			s.invalidateModuleIndexes()
			if logErr := s.logWorkspaceViewRefreshError(metadataPaths, err); logErr != nil {
				return logErr
			}
		} else {
			replay = s.pendingMetadataFor(prepared)
			s.applyPreparedWorkspace(prepared)
			s.workspaceViewValid = false
			s.invalidateModuleIndexes()
			recoveringWorkspace = true
		}
	}
	if len(nonMetadataPaths) != 0 {
		// Ordinary saved-source/config changes may alter any retained module fact.
		// A mixed malformed-metadata batch also clears caches for the remaining
		// paths even though ownership itself stays transactionally unchanged.
		s.invalidateModuleIndexes()
	}
	var affected []string
	var failedMetadataDirs []string
	if recoveringWorkspace {
		metadataAffected, err := s.refreshDisk(replay)
		if err != nil {
			failedMetadataDirs = s.invalidatePackageDirs(metadataAffected)
			if logErr := s.logWorkspaceMetadataReplayError(replay, err); logErr != nil {
				return logErr
			}
			if publishErr := s.publishEmptyOpenDirs(failedMetadataDirs); publishErr != nil {
				return publishErr
			}
		} else {
			affected = append(affected, metadataAffected...)
			s.retainPendingMetadata(nil)
			s.workspaceViewValid = true
		}
	}

	ordinaryAffected, refreshErr := s.refreshDisk(nonMetadataPaths)
	if refreshErr == nil && len(failedMetadataDirs) != 0 {
		ordinaryAffected = slices.DeleteFunc(ordinaryAffected, func(dir string) bool {
			return slices.Contains(failedMetadataDirs, filepath.Clean(dir))
		})
	}
	affected = append(affected, ordinaryAffected...)
	if refreshErr != nil {
		s.diskViewValid = false
		for dir := range s.docs.byDirSnapshot() {
			fallbackDirs[dir] = true
		}
		for dir := range fallbackDirs {
			affected = append(affected, dir)
		}
	}
	if refreshErr != nil {
		affected = s.invalidatePackageDirs(affected)
		restartErr := fmt.Errorf("%w; saved-source intelligence remains disabled until the language server restarts", refreshErr)
		if err := s.logAnalyzerTransitionError("refresh watched files", strings.Join(nonMetadataPaths, ", "), restartErr); err != nil {
			return err
		}
		return s.publishEmptyOpenDirs(affected)
	}
	return s.applySuccessfulDiskRefresh(affected)
}

func (s *Server) refreshDisk(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	refresher, ok := s.analyzer.(diskRefresher)
	if !ok {
		return nil, errors.New("analyzer does not support saved-source refresh")
	}
	return refresher.RefreshDisk(paths)
}

func (s *Server) applySuccessfulDiskRefresh(affected []string) error {
	affected = s.invalidatePackageDirs(affected)
	if !s.diskViewValid {
		return s.publishEmptyOpenDirs(affected)
	}
	return s.analyzeDirsNow(s.openAffectedDirs(affected))
}

func (s *Server) invalidatePackageDirs(affected []string) []string {
	affected = sortedUniqueDirs(affected)
	for _, dir := range affected {
		s.beginMutation(dir)
		delete(s.pkgs, dir)
	}
	return affected
}

func workspaceMetadataPath(path string) bool {
	switch filepath.Base(path) {
	case "go.mod", "go.work":
		return true
	default:
		return false
	}
}

func (s *Server) logWorkspaceViewRefreshError(paths []string, err error) error {
	return s.notify("window/logMessage", struct {
		Type    int    `json:"type"`
		Message string `json:"message"`
	}{
		Type: 1,
		Message: "gsx: refresh workspace ownership for " + strings.Join(paths, ", ") + ": " + err.Error() +
			"; workspace-owned operations remain disabled until a relevant metadata event succeeds",
	})
}

func (s *Server) logWorkspaceMetadataReplayError(paths []string, err error) error {
	return s.notify("window/logMessage", struct {
		Type    int    `json:"type"`
		Message string `json:"message"`
	}{
		Type: 1,
		Message: "gsx: replay workspace metadata for " + strings.Join(paths, ", ") + ": " + err.Error() +
			"; workspace-owned operations remain disabled until a relevant metadata event succeeds",
	})
}

func watchedFileRelevant(path string) bool {
	if strings.HasSuffix(path, ".gsx") {
		return true
	}
	switch filepath.Base(path) {
	case "gsx.toml", "go.mod", "go.work":
		return true
	default:
		return false
	}
}

func (s *Server) publishEmptyOpenDirs(dirs []string) error {
	for _, dir := range s.openAffectedDirs(dirs) {
		if err := s.publishEmptyOpenGSX(s.docs.snapshotDir(dir)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) reply(id json.RawMessage, result any) error {
	return s.conn.writeMessage(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{"2.0", id, result})
}

func (s *Server) replyError(id json.RawMessage, code int, msg string) error {
	return s.conn.writeMessage(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{JSONRPC: "2.0", ID: id, Error: struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}{code, msg}})
}

func (s *Server) notify(method string, params any) error {
	return s.conn.writeMessage(struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
		Params  any    `json:"params"`
	}{"2.0", method, params})
}

// invalidateModuleIndexes drops every whole-module index. Workspace-structure
// and saved configuration transitions use this conservative path because they
// can change module ownership itself.
func (s *Server) invalidateModuleIndexes() {
	s.invalidateNonSymbolModuleIndexes()
	clear(s.moduleSyms)
}

func (s *Server) invalidateNonSymbolModuleIndexes() {
	s.moduleRefs = nil
	s.moduleRefsValid = false
	s.moduleParams = nil
	s.moduleParamsValid = false
	s.moduleParamsDir = ""
}

// invalidateModuleIndexesForPath keeps unrelated initialized modules warm.
// References and rename still use a single request-owned module index, so only
// workspace symbols can currently be invalidated at finer granularity.
func (s *Server) invalidateModuleIndexesForPath(path string) {
	s.invalidateNonSymbolModuleIndexes()
	if module := workspaceModuleForPath(s.workspaceModules, path); module != "" {
		delete(s.moduleSyms, module)
	}
}

func (s *Server) handleDidOpen(f frame) error {
	var p didOpenParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return nil
	}
	path, err := localFileURIPath(p.TextDocument.URI)
	if err != nil {
		return nil
	}
	uri := pathToURI(path)
	s.docs.open(uri, p.TextDocument.Text, p.TextDocument.Version)
	s.invalidateModuleIndexesForPath(path)
	dir := filepath.Dir(path)
	s.lastURI[dir] = uri
	s.beginMutation(dir)
	affected, transitionErr := s.analyzer.SetOverride(path, []byte(p.TextDocument.Text))
	affected = s.applyAffectedTransition(dir, affected, transitionErr)
	if transitionErr != nil {
		if err := s.logAnalyzerTransitionError("set override", path, transitionErr); err != nil {
			return err
		}
	}
	if !s.diskViewValid {
		delete(s.pkgs, dir)
		return s.publishEmptyOpenDirs(append(affected, dir))
	}

	// Open is not debounced: the buffer just became authoritative, so every open
	// affected package is refreshed promptly. An identical-byte transition may
	// legitimately affect nothing; in that case republish the retained package at
	// the new document version, or analyze once if no retained package exists.
	targets := s.openAffectedDirs(affected)
	if !slices.Contains(affected, dir) {
		if transitionErr != nil || s.pkgs[dir] == nil {
			targets = append(targets, dir)
		} else if err := s.republishDir(dir); err != nil {
			return err
		}
	}
	return s.analyzeDirsNow(targets)
}

func (s *Server) handleDidChange(f frame) error {
	var p didChangeParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return nil
	}
	if len(p.ContentChanges) == 0 {
		return nil
	}
	path, err := localFileURIPath(p.TextDocument.URI)
	if err != nil {
		return nil
	}
	uri := pathToURI(path)
	// Full-document sync: the last change carries the whole new text.
	text := p.ContentChanges[len(p.ContentChanges)-1].Text
	s.docs.update(uri, text, p.TextDocument.Version)
	s.invalidateModuleIndexesForPath(path)
	dir := filepath.Dir(path)
	s.lastURI[dir] = uri
	s.beginMutation(dir)
	affected, transitionErr := s.analyzer.SetOverride(path, []byte(text))
	affected = s.applyAffectedTransition(dir, affected, transitionErr)
	if transitionErr != nil {
		if err := s.logAnalyzerTransitionError("set override", path, transitionErr); err != nil {
			return err
		}
	}
	if !s.diskViewValid {
		delete(s.pkgs, dir)
		return s.publishEmptyOpenDirs(append(affected, dir))
	}

	// Debounce the exact open affected set. An identical-byte edit does not evict
	// retained analysis; it only needs a version-correct republish (or one analysis
	// when this directory has not been analyzed yet).
	targets := s.openAffectedDirs(affected)
	if !slices.Contains(affected, dir) {
		if transitionErr != nil || s.pkgs[dir] == nil {
			targets = append(targets, dir)
		} else if err := s.republishDir(dir); err != nil {
			return err
		}
	}
	for _, target := range sortedUniqueDirs(targets) {
		s.scheduleAnalysis(target)
	}
	return nil
}

func (s *Server) handleDidClose(f frame) error {
	var p didCloseParams
	if err := json.Unmarshal(f.Params, &p); err != nil {
		return nil
	}
	path, err := localFileURIPath(p.TextDocument.URI)
	if err != nil {
		return nil
	}
	uri := pathToURI(path)
	s.invalidateModuleIndexesForPath(path)
	dir := filepath.Dir(path)
	s.beginMutation(dir)
	s.docs.close(uri)
	affected, transitionErr := s.analyzer.ClearOverride(path)
	affected = s.applyAffectedTransition(dir, affected, transitionErr)
	// Clear diagnostics for the now-closed document.
	if err := s.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{URI: uri, Diagnostics: []Diagnostic{}}); err != nil {
		return err
	}
	if len(s.docs.snapshotDir(dir)) == 0 {
		delete(s.pkgs, dir)
		delete(s.lastURI, dir)
	}
	if transitionErr != nil {
		if err := s.logAnalyzerTransitionError("clear override", path, transitionErr); err != nil {
			return err
		}
	}
	if !s.diskViewValid {
		return s.publishEmptyOpenDirs(affected)
	}
	// Clear always ends override authority, even when the newly exposed saved
	// source is unreadable. Reanalyze every affected package that still has an
	// open document so stale facts and diagnostics cannot survive the failure.
	return s.analyzeDirsNow(s.openAffectedDirs(affected))
}

// applyAffectedTransition evicts every package view invalidated by one
// authoritative buffer transition and immediately supersedes any work already
// running for those directories. changedDir was superseded before the analyzer
// call, so it must not advance twice. An error with an empty/incomplete affected
// set still invalidates changedDir: failed root resolution must never leave its
// previous read-intelligence facts live.
func (s *Server) applyAffectedTransition(changedDir string, affected []string, transitionErr error) []string {
	affected = sortedUniqueDirs(affected)
	for _, dir := range affected {
		if dir != changedDir {
			s.beginMutation(dir)
		}
		delete(s.pkgs, dir)
	}
	if transitionErr != nil && !slices.Contains(affected, changedDir) {
		delete(s.pkgs, changedDir)
		affected = sortedUniqueDirs(append(affected, changedDir))
	}
	return affected
}

// openAffectedDirs intersects an analyzer transition's exact affected set with
// directories that currently have at least one open editor document. Closed
// package views are still evicted by applyAffectedTransition; they simply do not
// need eager reanalysis.
func (s *Server) openAffectedDirs(affected []string) []string {
	open := make([]string, 0, len(affected))
	for _, dir := range affected {
		if len(s.docs.snapshotDir(dir)) != 0 {
			open = append(open, dir)
		}
	}
	return open
}

func sortedUniqueDirs(dirs []string) []string {
	seen := make(map[string]bool, len(dirs))
	out := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		dir = filepath.Clean(dir)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		out = append(out, dir)
	}
	slices.Sort(out)
	return out
}

// analyzeDirsNow refreshes each open directory exactly once. A directory with
// only open Go documents is still analyzed so read intelligence stays current,
// but publishAnalysis deliberately emits no Go diagnostics.
func (s *Server) analyzeDirsNow(dirs []string) error {
	for _, dir := range sortedUniqueDirs(dirs) {
		if len(s.docs.snapshotDir(dir)) == 0 {
			continue
		}
		if err := s.analyzeAndPublishDir(dir, s.fallbackURI(dir)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) republishDir(dir string) error {
	pkg := s.pkgs[dir]
	if pkg == nil {
		return nil
	}
	snap, _ := s.snapshotOverride(dir)
	return s.publishAnalysis(dir, s.fallbackURIFromSnap(dir, snap), snap, pkg, nil)
}

func (s *Server) fallbackURI(dir string) string {
	return s.fallbackURIFromSnap(dir, s.docs.snapshotDir(dir))
}

// fallbackURIFromSnap always prefers an open GSX document. Go buffers can
// invalidate and trigger analysis, but gopls owns their diagnostics and they
// must never become the target for GSX's positionless diagnostics.
func (s *Server) fallbackURIFromSnap(dir string, snap map[string]docSnap) string {
	if uri := s.lastURI[dir]; uri != "" {
		path := uriToPath(uri)
		if _, open := snap[path]; open && strings.HasSuffix(path, ".gsx") {
			return uri
		}
	}
	paths := make([]string, 0, len(snap))
	for path := range snap {
		if strings.HasSuffix(path, ".gsx") {
			paths = append(paths, path)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	slices.Sort(paths)
	uri := pathToURI(paths[0])
	s.lastURI[dir] = uri
	return uri
}

func (s *Server) logAnalyzerTransitionError(operation, path string, err error) error {
	return s.notify("window/logMessage", struct {
		Type    int    `json:"type"`
		Message string `json:"message"`
	}{Type: 1, Message: fmt.Sprintf("gsx: %s for %s: %v", operation, path, err)})
}

// beginMutation immediately supersedes every pending or in-flight analysis for
// dir. The generation advances when editor state changes, not later when a
// debounce happens to fire; otherwise an old worker can publish in that gap.
func (s *Server) beginMutation(dir string) {
	if timer, ok := s.timers[dir]; ok {
		timer.cancel()
		delete(s.timers, dir)
	}
	s.epoch[dir]++
	s.gen[dir]++
}

// scheduleAnalysis arms one analysis for the current mutation epoch. A callback
// that races with cancellation still carries its old epoch and is rejected by
// takeDebounce on the Run goroutine.
func (s *Server) scheduleAnalysis(dir string) {
	event := debounceEvent{dir: dir, epoch: s.epoch[dir], gen: s.gen[dir]}
	cancel := s.schedule(s.debounce, func() { s.enqueueDebounce(event) })
	s.timers[dir] = debounceTimer{cancel: cancel, epoch: event.epoch}
}

func (s *Server) enqueueDebounce(event debounceEvent) {
	if s.doneC == nil {
		s.fireC <- event
		return
	}
	select {
	case s.fireC <- event:
	case <-s.doneC:
	}
}

// takeDebounce validates an elapsed timer against the state that armed it. It
// also rejects directories without open documents, so no queued callback can
// resurrect analysis after the final didClose transition.
func (s *Server) takeDebounce(event debounceEvent) (fallbackURI string, generation int, ok bool) {
	timer, pending := s.timers[event.dir]
	if !pending || timer.epoch != event.epoch ||
		event.epoch != s.epoch[event.dir] || event.gen != s.gen[event.dir] {
		return "", 0, false
	}
	delete(s.timers, event.dir)
	if len(s.docs.snapshotDir(event.dir)) == 0 {
		return "", 0, false
	}
	return s.fallbackURI(event.dir), event.gen, true
}

// snapshotOverride captures dir's open documents (text+version) and the override
// map the analyzer consumes. Both come from one atomic docStore snapshot so the
// version a publish is tagged with matches exactly the bytes that were analyzed.
func (s *Server) snapshotOverride(dir string) (map[string]docSnap, map[string][]byte) {
	snap := s.docs.snapshotDir(dir) // abs path -> {text, version}
	override := make(map[string][]byte, len(snap))
	for path, d := range snap {
		override[path] = []byte(d.text)
	}
	return snap, override
}

// launchAnalysis runs the analysis generation assigned at document mutation on
// a worker so the Run loop stays responsive during a heavy type-check. The
// worker sends its result back to Run, which publishes it only if no newer edit
// has superseded that generation.
func (s *Server) launchAnalysis(dir, fallbackURI string, generation int) {
	snap, override := s.snapshotOverride(dir)
	go func() {
		pkg, err := s.analyzer.Analyze(dir, override)
		select {
		case s.resultsC <- analysisResult{dir: dir, gen: generation, fallbackURI: fallbackURI, snap: snap, pkg: pkg, err: err}:
		case <-s.doneC: // Run has exited; drop the result
		}
	}()
}

// analyzeAndPublishDir analyzes dir inline (on the Run goroutine) and publishes.
// Used for the one-shot didOpen path; the bursty edit path goes through
// launchAnalysis instead.
func (s *Server) analyzeAndPublishDir(dir, fallbackURI string) error {
	snap, override := s.snapshotOverride(dir)
	pkg, err := s.analyzer.Analyze(dir, override)
	return s.publishAnalysis(dir, fallbackURI, snap, pkg, err)
}

// publishAnalysis publishes diagnostics for every open GSX document in dir
// (empty list when a document is now clean, so stale squiggles never linger).
// Open Go documents still trigger and retain analysis for read intelligence,
// but gopls owns their diagnostics and GSX never publishes to them. Each publish
// carries the open document's version so the editor can drop stale results.
// Diagnostics that carry no filename are attached to fallbackURI, which is
// either an open GSX document or empty. A nil pkg or non-nil err evicts retained
// facts and clears every open GSX document rather than leaving stale state live.
func (s *Server) publishAnalysis(dir, fallbackURI string, snap map[string]docSnap, pkg *Package, err error) error {
	if err != nil || pkg == nil {
		delete(s.pkgs, dir)
		return s.publishEmptyOpenGSX(snap)
	}
	s.pkgs[dir] = pkg
	diags := pkg.Diags

	// Group diagnostics by absolute GSX filename. Positionless diagnostics use
	// the selected GSX fallback; when no GSX document is open there is no valid
	// publish target and they remain available only through retained package data.
	fallbackPath := ""
	if fallbackURI != "" {
		fallbackPath = uriToPath(fallbackURI)
	}
	byPath := map[string][]diag.Diagnostic{}
	for _, d := range diags {
		key := d.Start.Filename
		if key == "" {
			key = fallbackPath
		}
		if key == "" || !strings.HasSuffix(key, ".gsx") {
			continue
		}
		byPath[key] = append(byPath[key], d)
	}

	// Publish for every open GSX doc in the dir (clearing clean ones), plus any GSX
	// file that has diagnostics even if it is not currently open.
	targets := map[string]bool{}
	for path := range snap {
		if strings.HasSuffix(path, ".gsx") {
			targets[path] = true
		}
	}
	for path := range byPath {
		targets[path] = true
	}

	for path := range targets {
		text := snap[path].text // "" if not open; positions still map (best effort)
		lineAt := lineAtFunc(text)
		ds := byPath[path]
		out := make([]Diagnostic, 0, len(ds))
		for _, d := range ds {
			out = append(out, convertDiag(d, lineAt, s.enc))
		}
		if err := s.publishDiags(pathToURI(path), snap, out); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) publishEmptyOpenGSX(snap map[string]docSnap) error {
	paths := make([]string, 0, len(snap))
	for path := range snap {
		if strings.HasSuffix(path, ".gsx") {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	for _, path := range paths {
		if err := s.publishDiags(pathToURI(path), snap, []Diagnostic{}); err != nil {
			return err
		}
	}
	return nil
}

// publishDiags sends a publishDiagnostics notification for uri, tagging it with
// the document's version when uri is an open document in snap (so the editor can
// discard a stale-version publish). diags must be non-nil so the wire form is an
// empty array, never JSON null.
func (s *Server) publishDiags(uri string, snap map[string]docSnap, diags []Diagnostic) error {
	var version *int
	if d, ok := snap[uriToPath(uri)]; ok {
		v := d.version
		version = &v
	}
	return s.notify("textDocument/publishDiagnostics", publishDiagnosticsParams{URI: uri, Version: version, Diagnostics: diags})
}
