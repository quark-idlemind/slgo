//go:build !unix && !windows

package main

import "os"

// lockConfigDir takes no lock where there is no flock or LockFileEx;
// -listen and -viewer are still taken first, which is what catches the
// usual second start.  See lock_unix.go and lock_windows.go.
func lockConfigDir(path string) (*os.File, error) { return nil, nil }
