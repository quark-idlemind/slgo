package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// pipeOf is a standard input that is not a terminal, holding text.
func pipeOf(t *testing.T, text string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		w.WriteString(text)
		w.Close()
	}()
	t.Cleanup(func() { r.Close() })
	return r
}

func hash(t *testing.T, args []string, stdin string) (string, int) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run(args, pipeOf(t, stdin), &out, &errw)
	return strings.TrimSpace(out.String()), code
}

// TestAPasswordGivenOrPipedHashesTheSame: an argument, a line with a
// newline and a line without one are the same password.
func TestAPasswordGivenOrPipedHashesTheSame(t *testing.T) {
	// md5("foo"), as the login server is sent it.
	const want = "$1$acbd18db4cc2f85cedef654fccc4a4d8"
	for _, c := range []struct {
		args  []string
		stdin string
	}{
		{[]string{"foo"}, ""},
		{nil, "foo\n"},
		{nil, "foo\r\n"},
		{nil, "foo"},
		{nil, "foo\nsecond line\n"},
	} {
		if got, code := hash(t, c.args, c.stdin); got != want || code != 0 {
			t.Errorf("args %q, stdin %q: got %q (exit %d), want %q", c.args, c.stdin, got, code, want)
		}
	}
}

// TestSpacesArePartOfThePassword: only the line's end is taken off.
func TestSpacesArePartOfThePassword(t *testing.T) {
	a, _ := hash(t, []string{" foo "}, "")
	b, _ := hash(t, nil, " foo \n")
	c, _ := hash(t, []string{"foo"}, "")
	if a != b || a == c {
		t.Errorf("%q and %q should agree and differ from %q", a, b, c)
	}
}

func TestNothingToHashIsAnError(t *testing.T) {
	if got, code := hash(t, nil, ""); code == 0 || got != "" {
		t.Errorf("empty standard input gave %q, exit %d", got, code)
	}
	if _, code := hash(t, []string{"one", "two"}, ""); code != 2 {
		t.Errorf("two passwords gave exit %d, want 2", code)
	}
}

// TestAnEmptyPasswordIsRefused however it arrives: no login accepts one,
// so its digest is only ever a slip waiting to be found out.
func TestAnEmptyPasswordIsRefused(t *testing.T) {
	for _, c := range []struct {
		args  []string
		stdin string
	}{
		{[]string{""}, ""},
		{nil, "\n"},
		{nil, "\r\n"},
	} {
		if got, code := hash(t, c.args, c.stdin); code != 1 || got != "" {
			t.Errorf("args %q, stdin %q: printed %q, exit %d; want nothing and exit 1", c.args, c.stdin, got, code)
		}
	}
}
