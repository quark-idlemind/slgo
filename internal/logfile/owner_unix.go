//go:build unix

package logfile

import (
	"io/fs"
	"os"
	"syscall"
)

// noFollow refuses to open a path whose last component is a symbolic
// link.  A link planted where a log is about to be opened would
// otherwise have the daemon append its log to whatever the link names.
const noFollow = syscall.O_NOFOLLOW

// ownedByMe reports whether this process's user owns the file.  A file
// somebody else made at the path first is one they can read whatever
// its mode says, and one this user cannot narrow.
func ownedByMe(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return true
	}
	return int(st.Uid) == os.Getuid()
}
