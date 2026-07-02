// Package txtar implements a minimal read/write of the txtar archive format.
//
// This is a vendored, stdlib-only copy of the txtar format used by the Go
// toolchain (golang.org/x/tools/txtar, BSD-licensed). The format is:
//
//	comment
//	-- file1 --
//	contents of file1
//	-- file2 --
//	contents of file2
package txtar

import (
	"bytes"
	"strings"
)

// Archive is a collection of files with an optional leading comment.
type Archive struct {
	Comment []byte
	Files   []File
}

// File is a single file in an archive.
type File struct {
	Name string
	Data []byte
}

// Parse parses a txtar-format archive from data. It never returns an error.
func Parse(data []byte) *Archive {
	a := new(Archive)
	var name string
	a.Comment, name, data = findFileMarker(data)
	for name != "" {
		var f File
		f.Name = name
		f.Data, name, data = findFileMarker(data)
		a.Files = append(a.Files, f)
	}
	return a
}

// Format formats a as a txtar archive.
func Format(a *Archive) []byte {
	var buf bytes.Buffer
	buf.Write(fixNL(a.Comment))
	for _, f := range a.Files {
		buf.WriteString("-- ")
		buf.WriteString(f.Name)
		buf.WriteString(" --\n")
		buf.Write(fixNL(f.Data))
	}
	return buf.Bytes()
}

// isMarkerLine reports whether line (without trailing newline) is a file-marker
// line, and if so returns the file name.
// A marker line has the exact form "-- NAME --" where NAME is non-empty
// and surrounded by exactly one space on each side of the dashes.
func isMarkerLine(line []byte) (name string, ok bool) {
	s := string(line)
	if !strings.HasPrefix(s, "-- ") || !strings.HasSuffix(s, " --") {
		return "", false
	}
	// Extract middle part between the two markers.
	name = s[3 : len(s)-3]
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	return name, true
}

// findFileMarker finds the next file-marker line in data.
// It returns the data before the marker, the marker name, and the data after
// the marker line. If there is no marker, it returns data, "", nil.
func findFileMarker(data []byte) (before []byte, name string, after []byte) {
	var b []byte
	for len(data) > 0 {
		var line []byte
		if before0, after0, ok := bytes.Cut(data, []byte{'\n'}); ok {
			line, data = before0, after0
		} else {
			line, data = data, nil
		}
		if n, ok := isMarkerLine(line); ok {
			return fixNL(b), n, data
		}
		b = append(b, line...)
		b = append(b, '\n')
	}
	return fixNL(b), "", nil
}

// fixNL ensures that non-empty data ends with a newline.
func fixNL(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	if data[len(data)-1] != '\n' {
		data = append(data[:len(data):len(data)], '\n')
	}
	return data
}
