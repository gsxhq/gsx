//go:build windows

package lsp

import "testing"

func TestWindowsFileURIConversion(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		wantPath string
		wantURI  string
	}{
		{
			name:     "encoded lowercase drive",
			raw:      "file:///d%3A/Projects/eos/page.gsx",
			wantPath: `D:\Projects\eos\page.gsx`,
			wantURI:  "file:///D:/Projects/eos/page.gsx",
		},
		{
			name:     "literal uppercase drive",
			raw:      "file:///D:/Projects/eos/page.gsx",
			wantPath: `D:\Projects\eos\page.gsx`,
			wantURI:  "file:///D:/Projects/eos/page.gsx",
		},
		{
			name:     "escaped characters",
			raw:      "file:///d:/Projects/space%20%C3%BC/page.gsx",
			wantPath: `D:\Projects\space ü\page.gsx`,
			wantURI:  "file:///D:/Projects/space%20%C3%BC/page.gsx",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotPath, err := localFileURIPath(tc.raw)
			if err != nil {
				t.Fatalf("localFileURIPath(%q): %v", tc.raw, err)
			}
			if gotPath != tc.wantPath {
				t.Fatalf("localFileURIPath(%q) = %q, want %q", tc.raw, gotPath, tc.wantPath)
			}
			gotURI, ok := canonicalDocumentURI(tc.raw)
			if !ok || gotURI != tc.wantURI {
				t.Fatalf("canonicalDocumentURI(%q) = (%q, %t), want (%q, true)", tc.raw, gotURI, ok, tc.wantURI)
			}
			if roundTrip := pathToURI(gotPath); roundTrip != tc.wantURI {
				t.Fatalf("pathToURI(%q) = %q, want %q", gotPath, roundTrip, tc.wantURI)
			}
		})
	}
}

func TestWindowsFileURIFromPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{name: "lowercase drive", path: `d:\Projects\eos\page.gsx`, want: "file:///D:/Projects/eos/page.gsx"},
		{name: "drive root", path: `d:\`, want: "file:///D:/"},
		{name: "escaped characters", path: `d:\Projects\space ü\page.gsx`, want: "file:///D:/Projects/space%20%C3%BC/page.gsx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pathToURI(tc.path); got != tc.want {
				t.Fatalf("pathToURI(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestWindowsFileURIRejectsUNC(t *testing.T) {
	for _, raw := range []string{"file://server/share/page.gsx", "file:////server/share/page.gsx"} {
		if got, err := localFileURIPath(raw); err == nil || got != "" {
			t.Fatalf("localFileURIPath(%q) = (%q, %v), want rejection", raw, got, err)
		}
	}
	if got := pathToURI(`\\server\share\page.gsx`); got != "" {
		t.Fatalf("pathToURI(UNC) = %q, want rejection", got)
	}
}
