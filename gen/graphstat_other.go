//go:build !darwin && !linux

package gen

import "io/fs"

// statIdentity has no ctime/inode source here, so graph stores are skipped.
func statIdentity(fs.FileInfo) (fileIdentity, bool) { return fileIdentity{}, false }
