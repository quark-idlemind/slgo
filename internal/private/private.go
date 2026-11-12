// Package private says whether a file the tree keeps secrets in is
// open to other users.
//
// On Unix that is the permission bits: anything for the group or for
// others is too wide.  On Windows it is not, because Go makes the bits
// up there -- 0666 for a file, 0444 for a read-only one, 0777 for a
// directory -- from the read-only attribute alone, and who can read a
// file is its access control list, which the bits do not show.  A file
// under the user's profile directory is the user's by that list, so on
// Windows nothing is reported open and nothing is refused.
// Why: doc/windows.md#file-permissions
package private

import (
	"io/fs"
	"runtime"
)

// Checked reports whether the mode bits mean anything here.  Where they
// do not, a check of them is skipped rather than failed.
var Checked = runtime.GOOS != "windows"

// Open reports whether fi lets users other than its owner in: a group
// or other bit is set, where the bits are Checked.
func Open(fi fs.FileInfo) bool {
	return Checked && fi.Mode().Perm()&0o077 != 0
}

// Wider reports whether fi has a permission bit want does not, where
// the bits are Checked.
func Wider(fi fs.FileInfo, want fs.FileMode) bool {
	return Checked && fi.Mode().Perm()&^want != 0
}
