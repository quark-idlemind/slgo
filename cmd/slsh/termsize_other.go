//go:build !unix

package main

import (
	"time"

	"golang.org/x/term"
)

// watchSize redraws at the new size when the console's changes.  A
// Windows console sends no signal when it is resized, so it is asked,
// a few times a second.
func (t *Term) watchSize(fd int) {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			w, h, err := term.GetSize(fd)
			if err != nil || w <= 0 {
				continue
			}
			t.mu.Lock()
			if w != t.width || h != t.height {
				t.width, t.height = w, h
				t.redrawLocked()
			}
			t.mu.Unlock()
		case <-t.done:
			return
		}
	}
}
