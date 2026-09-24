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
// the other end of the output pipe, so Wait goes on waiting for them:
// the run's deadline passes, the script is killed, and the answer still
// does not come until the last grandchild exits on its own.  Measured
// 2026-09-23 with a script that was only "sleep 7": killed at 200 ms,
// Run returned after 7.0 s on an M1 Max.  The same script on an Intel
// i9 returned at 200 ms, but only because its /bin/sh had not yet
// started the sleep (about 350 ms there); killed at 1 s, it waited the
// full 7 s as well.
//
// A group is the unit the kernel can signal all at once, so the program
// leads one and the cancel signals the group rather than the leader.
func stopTogether(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
