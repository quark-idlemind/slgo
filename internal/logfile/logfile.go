// Package logfile opens the files a daemon here writes about itself --
// its log, and a packet trace -- so that only the user running it can
// read them.
//
// Both hold more than they look as if they do.  A log names the people
// who spoke to an avatar and what they said, and with -log-secrets it
// holds the session's credentials outright; a trace with bodies is every
// message both ways, session ids and other people's instant messages
// among them.  Created with the default umask they are readable by every
// account on the machine, and a log in /tmp has been exactly that.
//
// The mode is made rather than hoped for: a file is created 0600, one
// that already exists wider is narrowed before anything is written to
// it, and one that belongs to somebody else, or is a symbolic link, is
// refused -- a file somebody else put at the path first is theirs to
// read whatever mode it has.
package logfile

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// FileMode is what a log or trace is created with, and narrowed to.
const FileMode fs.FileMode = 0o600

// DirMode is what a log's directory is created with when it is missing,
// and the widest one Append accepts when it is not.
const DirMode fs.FileMode = 0o700

// Append opens path to append a daemon's log to, creating it 0600 and
// its directory 0700 if either is missing.
//
// A directory that already exists and that group or others can get into
// is refused, not narrowed and not just warned about.  The directory is
// what protects the files the daemon does not open itself: launchd's
// capture of stdout and stderr, which it creates with the default umask
// beside this one, a panic's trace, a copy somebody rotates the log
// into.  Narrowing a directory this program did not make could be
// narrowing /tmp.  And a warning would be written into the very file
// that is exposed, where the one person certain to read it is whoever
// it is exposed to.  -log is asked for on purpose, by somebody setting
// the log up, which is the moment a refusal costs one chmod.
func Append(path string) (*os.File, error) {
	dir := filepath.Dir(path)
	fi, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, DirMode); err != nil {
			return nil, err
		}
		// MkdirAll's mode passes through the umask, which can only
		// narrow it; but say it again, so that the mode is this
		// package's and not the environment's.
		if err := os.Chmod(dir, DirMode); err != nil {
			return nil, err
		}
	case err != nil:
		return nil, err
	case !fi.IsDir():
		return nil, fmt.Errorf("%s is not a directory", dir)
	case fi.Mode().Perm()&0o077 != 0:
		return nil, fmt.Errorf("%s is mode %03o, so other users can read or replace what is "+
			"logged there; a log directory has to be %03o (chmod %03o %s), or log somewhere else",
			dir, fi.Mode().Perm(), DirMode, DirMode, dir)
	}
	return open(path, os.O_APPEND)
}

// Create opens path for a new trace, emptying it if it exists.
//
// The directory is not checked, unlike Append's: a trace is taken by
// hand, into wherever the person taking it is standing, and the file's
// own mode is what keeps it.  Nothing else is written beside it.
func Create(path string) (*os.File, error) {
	f, err := open(path, 0)
	if err != nil {
		return nil, err
	}
	// Emptied only now, after the mode is narrowed: a file that was
	// readable a moment ago is not written to until it is not.
	if err := f.Truncate(0); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// open is the part the two share: create at 0600, refuse what is not a
// plain file of this user's, and narrow a mode that is wider.
func open(path string, flag int) (*os.File, error) {
	f, err := os.OpenFile(path, flag|os.O_WRONLY|os.O_CREATE|noFollow, FileMode)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s is not a plain file", path)
	}
	if !ownedByMe(fi) {
		f.Close()
		return nil, fmt.Errorf("%s belongs to another user, who can read whatever is written to it", path)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := f.Chmod(FileMode); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}
