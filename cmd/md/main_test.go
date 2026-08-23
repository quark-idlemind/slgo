package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestItRendersTheFileItWasGiven(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "page.md")
	if err := os.WriteFile(path, []byte("# Hello\n\n**world**\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out, errw bytes.Buffer
	if code := run([]string{"-width", "40", path}, nil, &out, &errw); code != 0 {
		t.Fatalf("run = %d, stderr %q", code, errw.String())
	}
	got := out.String()
	if strings.Contains(got, "# Hello") {
		t.Errorf("heading marker reached the screen:\n%s", got)
	}
	if !strings.Contains(got, "Hello") || !strings.Contains(got, "world") {
		t.Errorf("rendered text missing:\n%s", got)
	}
	if !strings.Contains(got, "\x1b[1m") {
		t.Errorf("heading is not bold:\n%q", got)
	}
}

func TestWidthIsTheOneAskedFor(t *testing.T) {
	src := "one two three four five six seven eight nine ten eleven twelve"
	var in, out, errw bytes.Buffer
	in.WriteString(src)
	if code := run([]string{"-width", "20", "-"}, &in, &out, &errw); code != 0 {
		t.Fatalf("run = %d, stderr %q", code, errw.String())
	}
	for _, line := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		vis := stripANSI(line)
		if len([]rune(vis)) > 20 {
			t.Errorf("line past 20 columns: %q", vis)
		}
	}
}

func TestAMissingFileIsAnError(t *testing.T) {
	var out, errw bytes.Buffer
	code := run([]string{filepath.Join(t.TempDir(), "no-such.md")}, nil, &out, &errw)
	if code != 1 {
		t.Fatalf("run = %d, want 1", code)
	}
	if out.Len() != 0 {
		t.Errorf("wrote to stdout on error: %q", out.String())
	}
	if !strings.Contains(errw.String(), "no-such.md") {
		t.Errorf("stderr should name the file: %q", errw.String())
	}
}

func TestNoNameIsUsage(t *testing.T) {
	var out, errw bytes.Buffer
	code := run(nil, nil, &out, &errw)
	if code != 2 {
		t.Fatalf("run = %d, want 2", code)
	}
	if !strings.Contains(errw.String(), "usage:") {
		t.Errorf("stderr should be usage: %q", errw.String())
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\x1b' {
			i++
			if i < len(s) && s[i] == '[' {
				i++
				for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
					i++
				}
				if i < len(s) {
					i++
				}
			}
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
