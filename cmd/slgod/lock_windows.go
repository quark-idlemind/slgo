package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockConfigDir takes an exclusive lock on path, which is made if it is
// not there, and holds it until the returned file is closed or the
// process ends, as lock_unix.go does with flock.
//
// Windows locks a range of bytes, and a locked range cannot be read by
// anybody else, so the range is far past the end of the file: the pid
// at the start stays readable for the next instance's error message.
func lockConfigDir(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	ol := windows.Overlapped{OffsetHigh: 0x7fffffff}
	err = windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if err != nil {
		holder := holderOf(f)
		f.Close()
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, fmt.Errorf("cannot lock %s: %v", path, err)
		}
		return nil, fmt.Errorf("another slgod%s already holds %s, and logging the same "+
			"avatars in again would end its sessions; nobody has been logged in",
			holder, path)
	}
	if err := f.Truncate(0); err == nil {
		fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return f, nil
}
