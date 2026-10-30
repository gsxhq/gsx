package gen

import (
	"fmt"
	"os"
	"runtime"
)

// fileIdentity is what a stat says about one path: two equal identities mean
// the path was not written, replaced, created or removed in between, even when
// its bytes came back the same. The zero value is an absent path.
type fileIdentity struct {
	exists       bool
	dev, ino     uint64
	size         int64
	mtime, ctime int64 // nanoseconds
}

// statSnapshot returns the identity of each path, in order. It fails when a
// path cannot be stat'd or the platform has no stat identity (see
// statIdentity), so the caller cannot mistake "unknown" for "unchanged".
func statSnapshot(paths []string) ([]fileIdentity, error) {
	out := make([]fileIdentity, len(paths))
	for i, p := range paths {
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		identity, ok := statIdentity(info)
		if !ok {
			return nil, fmt.Errorf("file stat identity is unsupported on %s", runtime.GOOS)
		}
		identity.exists = true
		out[i] = identity
	}
	return out, nil
}

// firstChanged returns the first path whose identity differs between two
// snapshots of paths.
func firstChanged(paths []string, before, after []fileIdentity) (string, bool) {
	for i, p := range paths {
		if before[i] != after[i] {
			return p, true
		}
	}
	return "", false
}
