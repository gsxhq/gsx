package gen

import (
	"io/fs"
	"syscall"
)

func statIdentity(info fs.FileInfo) (fileIdentity, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}, false
	}
	return fileIdentity{dev: uint64(st.Dev), ino: st.Ino, size: st.Size, mtime: st.Mtimespec.Nano(), ctime: st.Ctimespec.Nano()}, true
}
