package codegen

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"go/token"
	"io"
	"strings"

	gsxast "github.com/gsxhq/gsx/ast"
	"github.com/gsxhq/gsx/internal/sourceintel"
)

type skeletonWriter interface {
	io.Writer
	io.StringWriter
	Len() int
	String() string
	WriteByte(byte) error
}

func writeSkeletonGenerated(w skeletonWriter, text string) {
	if mapped, ok := w.(*skeletonSourceWriter); ok {
		mapped.writeGenerated(text)
		return
	}
	w.WriteString(text)
}

func writeSkeletonAuthoredAt(w skeletonWriter, fset *token.FileSet, pos token.Pos, emitted string, capabilities sourceintel.Capability) error {
	if mapped, ok := w.(*skeletonSourceWriter); ok {
		return mapped.writeAuthoredAt(fset, pos, emitted, capabilities)
	}
	w.WriteString(emitted)
	return nil
}

func newSkeletonWriterChild(parent skeletonWriter) skeletonWriter {
	if mapped, ok := parent.(*skeletonSourceWriter); ok {
		return mapped.child()
	}
	return &strings.Builder{}
}

func appendSkeletonWriter(parent skeletonWriter, child skeletonWriter) error {
	if mappedParent, ok := parent.(*skeletonSourceWriter); ok {
		mappedChild, ok := child.(*skeletonSourceWriter)
		if !ok {
			return fmt.Errorf("codegen: append plain child into mapped skeleton")
		}
		return mappedParent.appendMapped(mappedChild)
	}
	parent.WriteString(child.String())
	return nil
}

type skeletonBuild struct {
	source       string
	sourceHash   [sha256.Size]byte
	components   []*gsxast.Component
	imports      []importSpec
	ctrlStarts   map[gsxast.Node]int
	markupGroups [][]gsxast.Markup
	sourceMap    *sourceintel.SourceMap
}

type skeletonSourceWriter struct {
	sourcePath string
	source     []byte
	builder    strings.Builder
	segments   []sourceintel.Segment
	regions    []sourceintel.DeclarationRegion
	enabled    bool
	err        error
}

func newSkeletonSourceWriter(path string, source []byte) *skeletonSourceWriter {
	return &skeletonSourceWriter{sourcePath: path, source: source, enabled: true}
}

func newUnmappedSkeletonSourceWriter() *skeletonSourceWriter {
	return &skeletonSourceWriter{}
}

func (w *skeletonSourceWriter) child() *skeletonSourceWriter {
	if !w.enabled {
		return newUnmappedSkeletonSourceWriter()
	}
	return newSkeletonSourceWriter(w.sourcePath, w.source)
}

func (w *skeletonSourceWriter) Len() int {
	return w.builder.Len()
}

func (w *skeletonSourceWriter) String() string {
	return w.builder.String()
}

func (w *skeletonSourceWriter) Write(p []byte) (int, error) {
	return w.builder.Write(p)
}

func (w *skeletonSourceWriter) WriteString(text string) (int, error) {
	return w.builder.WriteString(text)
}

func (w *skeletonSourceWriter) WriteByte(b byte) error {
	return w.builder.WriteByte(b)
}

func (w *skeletonSourceWriter) writeGenerated(text string) {
	if w.err != nil {
		return
	}
	w.builder.WriteString(text)
}

func (w *skeletonSourceWriter) writeAuthored(start, end int, emitted string, capabilities sourceintel.Capability) error {
	if w.err != nil {
		return w.err
	}
	if !w.enabled {
		w.builder.WriteString(emitted)
		return nil
	}
	if start < 0 || end < start || end > len(w.source) {
		w.err = fmt.Errorf("codegen: authored source range [%d, %d) is outside source size %d", start, end, len(w.source))
		return w.err
	}
	if !bytes.Equal(w.source[start:end], []byte(emitted)) {
		w.err = fmt.Errorf("codegen: skeleton authored bytes [%d, %d) are not identical to emitted bytes", start, end)
		return w.err
	}
	generatedStart := w.builder.Len()
	w.builder.WriteString(emitted)
	w.segments = append(w.segments, sourceintel.Segment{
		Source: sourceintel.Span{
			Path:  w.sourcePath,
			Start: start,
			End:   end,
		},
		GeneratedStart: generatedStart,
		GeneratedEnd:   w.builder.Len(),
		Capabilities:   capabilities,
	})
	return nil
}

func (w *skeletonSourceWriter) writeAuthoredAt(fset *token.FileSet, pos token.Pos, emitted string, capabilities sourceintel.Capability) error {
	if !w.enabled {
		w.builder.WriteString(emitted)
		return nil
	}
	if fset == nil || !pos.IsValid() {
		w.err = fmt.Errorf("codegen: mapped skeleton authored write has no source position")
		return w.err
	}
	file := fset.File(pos)
	if file == nil {
		w.err = fmt.Errorf("codegen: mapped skeleton authored write has no token file")
		return w.err
	}
	start := file.Offset(pos)
	return w.writeAuthored(start, start+len(emitted), emitted, capabilities)
}

func (w *skeletonSourceWriter) addDeclarationRegion(source sourceintel.Span, generatedStart, generatedEnd int) error {
	if w.err != nil {
		return w.err
	}
	if !w.enabled {
		return nil
	}
	w.regions = append(w.regions, sourceintel.DeclarationRegion{
		Source:         source,
		GeneratedStart: generatedStart,
		GeneratedEnd:   generatedEnd,
	})
	return nil
}

func (w *skeletonSourceWriter) appendMapped(child *skeletonSourceWriter) error {
	if w.err != nil {
		return w.err
	}
	if child == nil {
		w.err = fmt.Errorf("codegen: append nil skeleton source writer")
		return w.err
	}
	if child.err != nil {
		w.err = child.err
		return w.err
	}
	if w.enabled {
		if !child.enabled {
			w.err = fmt.Errorf("codegen: append unmapped skeleton source into mapped skeleton")
			return w.err
		}
		if w.sourcePath != child.sourcePath || !bytes.Equal(w.source, child.source) {
			w.err = fmt.Errorf("codegen: append skeleton source from a different authored file")
			return w.err
		}
	}
	base := w.builder.Len()
	w.builder.WriteString(child.builder.String())
	if !w.enabled {
		return nil
	}
	for _, segment := range child.segments {
		segment.GeneratedStart += base
		segment.GeneratedEnd += base
		w.segments = append(w.segments, segment)
	}
	for _, region := range child.regions {
		region.GeneratedStart += base
		region.GeneratedEnd += base
		w.regions = append(w.regions, region)
	}
	return nil
}

func (w *skeletonSourceWriter) finish() (string, *sourceintel.SourceMap, error) {
	if w.err != nil {
		return "", nil, w.err
	}
	source := w.builder.String()
	if !w.enabled {
		return source, nil, nil
	}
	sourceMap, err := sourceintel.NewSourceMap(len(source), len(w.source), w.sourcePath, w.segments, w.regions)
	if err != nil {
		return "", nil, fmt.Errorf("codegen: finish skeleton source map: %w", err)
	}
	return source, sourceMap, nil
}
