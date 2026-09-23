// Command md renders a markdown file as terminal text.
//
//	md FILE.md
//	md -width 60 FILE.md
//
// It exists to look at what package md produces: at a chosen width, or
// at the terminal's.  The name "-" is stdin.  Nothing else is parsed;
// this is not a pager.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/quark-idlemind/slgo/internal/version"
	"github.com/quark-idlemind/slgo/md"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, in io.Reader, out, errw io.Writer) int {
	fs := flag.NewFlagSet("md", flag.ContinueOnError)
	fs.SetOutput(errw)
	width := fs.Int("width", 0, "column count; the terminal's, or 80, if 0")
	ver := fs.Bool("version", false, "say which build this is, and exit")
	fs.Usage = func() {
		fmt.Fprintf(errw, "usage: md [-width N] FILE.md\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *ver {
		fmt.Fprintln(out, version.String("md"))
		return 0
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	body, err := read(fs.Arg(0), in)
	if err != nil {
		fmt.Fprintf(errw, "md: %v\n", err)
		return 1
	}

	var text string
	if *width > 0 {
		text = md.RenderWidth(string(body), *width)
	} else {
		text = md.Render(string(body))
	}
	if _, err := io.WriteString(out, text); err != nil {
		fmt.Fprintf(errw, "md: %v\n", err)
		return 1
	}
	return 0
}

func read(path string, in io.Reader) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(in)
	}
	return os.ReadFile(path)
}
