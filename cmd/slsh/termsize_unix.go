//go:build unix

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// watchSize redraws at the new size each time the terminal says it has
// been resized.
func (t *Term) watchSize(fd int) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	defer signal.Stop(ch)
	for {
		select {
		case <-ch:
			if w, h, err := term.GetSize(fd); err == nil && w > 0 {
				t.mu.Lock()
				t.width, t.height = w, h
				t.redrawLocked()
				t.mu.Unlock()
			}
		case <-t.done:
			return
		}
	}
}
