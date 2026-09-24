//go:build !unix

package main

import "os/exec"

// stopTogether leaves the default, killing the one process, where there
// are no process groups to signal; see program_unix.go.  WaitDelay in
// runProgram still bounds how long a surviving child can hold the run.
func stopTogether(*exec.Cmd) {}
