//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// lockConfigDir takes an exclusive lock on path, which is made if it is
// not there, and holds it until the returned file is closed or the
// process ends.
//
// flock rather than a pid file: the operating system lets go of it when
// the process dies, however it dies, so there is no stale lock to find
// and delete and no pid to guess at the liveness of.  The pid written in
// is only for the next instance's error message, and is never trusted for
// anything else.
func lockConfigDir(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := holderOf(f)
		f.Close()
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("cannot lock %s: %v", path, err)
		}
		return nil, fmt.Errorf("another slgod%s already holds %s, and logging the same "+
			"avatars in again would end its sessions; nobody has been logged in",
			holder, path)
	}
	// Held.  Say whose it is, for the next one to be told.
	if err := f.Truncate(0); err == nil {
		fmt.Fprintf(f, "%d\n", os.Getpid())
	}
	return f, nil
}

// holderOf is " (pid N)" from what the holder wrote, or "" when there is
// nothing readable there.
func holderOf(f *os.File) string {
	b := make([]byte, 32)
	n, _ := f.ReadAt(b, 0)
	pid, err := strconv.Atoi(strings.TrimSpace(string(b[:n])))
	if err != nil || pid <= 0 {
		return ""
	}
	return fmt.Sprintf(" (pid %d)", pid)
}
