package lsp

import (
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"
)

func TestDidChangeSupersedesAnalysisAtMutationTime(t *testing.T) {
	for _, extension := range []string{".gsx", ".go"} {
		t.Run(extension, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "page"+extension)
			uri := pathToURI(path)
			dir := filepath.Dir(path)
			server := NewServer(nil, io.Discard, nilAnalyzer{})
			server.docs.open(uri, "before", 1)

			var scheduled bool
			server.schedule = func(time.Duration, func()) func() {
				scheduled = true
				return func() {}
			}
			before := server.gen[dir]
			params, err := json.Marshal(didChangeParams{
				TextDocument:   versionedTextDocumentIdentifier{URI: uri, Version: 2},
				ContentChanges: []contentChange{{Text: "after"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := server.handleDidChange(frame{Params: params}); err != nil {
				t.Fatal(err)
			}
			if got := server.gen[dir]; got != before+1 {
				t.Fatalf("generation after didChange = %d, want %d before debounce fires", got, before+1)
			}
			if extension == ".gsx" && !scheduled {
				t.Fatal("GSX change did not schedule settled analysis")
			}
		})
	}
}

func TestQueuedDebounceEventFromBeforeCloseIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "page.gsx")
	uri := pathToURI(path)
	server := NewServer(nil, io.Discard, nilAnalyzer{})
	server.docs.open(uri, "before", 1)

	var callback func()
	server.schedule = func(_ time.Duration, f func()) func() {
		callback = f
		return func() {} // simulate Timer.Stop losing the race with its callback
	}
	changeParams, err := json.Marshal(didChangeParams{
		TextDocument:   versionedTextDocumentIdentifier{URI: uri, Version: 2},
		ContentChanges: []contentChange{{Text: "after"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.handleDidChange(frame{Params: changeParams}); err != nil {
		t.Fatal(err)
	}
	if callback == nil {
		t.Fatal("didChange did not schedule debounce")
	}

	closeParams, err := json.Marshal(didCloseParams{
		TextDocument: textDocumentIdentifier{URI: uri},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.handleDidClose(frame{Params: closeParams}); err != nil {
		t.Fatal(err)
	}

	// The callback had already escaped cancellation, so it still queues its old
	// event. The close transition must make that event unacceptable.
	callback()
	event := <-server.fireC
	if _, _, ok := server.takeDebounce(event); ok {
		t.Fatal("queued pre-close debounce event was accepted after its document closed")
	}
}
