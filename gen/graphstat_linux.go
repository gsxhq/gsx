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
	return fileIdentity{dev: uint64(st.Dev), ino: uint64(st.Ino), size: st.Size, mtime: st.Mtim.Nano(), ctime: st.Ctim.Nano()}, true
}
