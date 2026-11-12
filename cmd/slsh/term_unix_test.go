//go:build unix

package main

import (
	"bytes"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestARealTerminalIsPutInRawMode.
//
// This is the half of NewTerm a pipe never reaches: raw mode, the size,
// the byte reader and the decoder.  A pty is a terminal in every way
// that matters here and needs no window on the screen.
func TestARealTerminalIsPutInRawMode(t *testing.T) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("no pty available: %v", err)
	}
	defer ptmx.Close()
	defer tty.Close()
	if err := pty.Setsize(tty, &pty.Winsize{Rows: 30, Cols: 100}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	tm, err := NewTerm(tty, &out)
	if err != nil {
		t.Fatal(err)
	}
	defer tm.Close()

	if tm.Plain() {
		t.Error("a pty is a terminal")
	}
	if got := tm.Rows(); got != 30 {
		t.Errorf("the terminal is 30 rows, got %d", got)
	}

	// Typing at it arrives as keys, through the reader and the decoder.
	if _, err := ptmx.WriteString("hi\x1b[A"); err != nil {
		t.Fatal(err)
	}
	want := []rune{'h', 'i', keyUp}
	for i, w := range want {
		select {
		case got := <-tm.Keys():
			if got != w {
				t.Errorf("key %d = %d, want %d", i, got, w)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("key %d never arrived", i)
		}
	}

	// A window that changes size is noticed, which is what keeps a
	// long line drawn as a window on to the right number of columns.
	if err := pty.Setsize(tty, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	syscall.Kill(os.Getpid(), syscall.SIGWINCH)
	deadline := time.Now().Add(5 * time.Second)
	for tm.Rows() != 40 {
		if time.Now().After(deadline) {
			t.Fatalf("the resize was not noticed; still %d rows", tm.Rows())
		}
		time.Sleep(2 * time.Millisecond)
	}

	// Closing restores the terminal and stops both goroutines.
	tm.Close()
	select {
	case <-tm.done:
	case <-time.After(5 * time.Second):
		t.Error("Close did not finish")
	}
}
