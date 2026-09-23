//go:build !unix

package logfile

import "io/fs"

// noFollow is nothing where there is no O_NOFOLLOW; see owner_unix.go.
const noFollow = 0

// ownedByMe has no owner to compare where files have no uid; see
// owner_unix.go.
func ownedByMe(fs.FileInfo) bool { return true }
