//go:build unix

package main

import (
	"os/exec"
	"syscall"
)

// stopTogether puts the program in a process group of its own and makes
// stopping it stop the whole group.
//
// exec.CommandContext on its own kills the one process it started and
// nothing else.  A program that is a shell script -- or anything else
// that starts children -- leaves those children running, and they hold
// the other end of the output pipe, so Wait goes on waiting for them
// past the run's deadline.
// Why: doc/slbotd.md#stopping-a-program-and-what-it-started
//
// A group is the unit the kernel can signal all at once, so the program
// leads one and the cancel signals the group rather than the leader.
func stopTogether(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
