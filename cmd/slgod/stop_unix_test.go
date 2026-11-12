//go:build unix

package main

import (
	"os"
	"syscall"
)

// stopTheDaemon sends this process the signal a daemon is stopped with.
func stopTheDaemon() error { return syscall.Kill(os.Getpid(), syscall.SIGTERM) }
