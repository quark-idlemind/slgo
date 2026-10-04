// Package md renders markdown as text for a terminal.
//
// The output is wrapped to the terminal's width, or to 80 columns when
// the width cannot be told -- a pipe, a test, a file.  Styling that a
// terminal can show is emitted as the usual ECMA-48 SGR sequences
// (bold, italic, underline, dim, strike); a heading is bold rather
// than shouted, because shouting is what a renderer does when it has
// no sequences to send, and this one has.  Anything that is not ASCII
// in the output is a Unicode code point a terminal already draws: list
// bullets, table rules, the quote bar.
//
// The markdown it takes is the subset a person actually writes in a
// README: headings, paragraphs, emphasis, links, images, lists, fenced
// and indented code, block quotes, rules, and GFM tables.  It is not a
// CommonMark conformance suite.  Where the two disagree, the output
// still has to be readable, not interchangeable with someone else's
// parser.
package md

import (
	"os"

	"golang.org/x/term"
)

// DefaultWidth is used when the terminal will not say how wide it is.
const DefaultWidth = 80

// termSize is term.GetSize, replaced in tests so a Width claim does
// not depend on whether "go test" inherited a tty.
var termSize = term.GetSize

// Width is the terminal's column count.
//
// stdout is asked first, then stderr, then stdin: a pipe on the writer
// should not hide a terminal still attached to the others.  If none of
// them will say, the width is DefaultWidth.
func Width() int {
	for _, f := range []*os.File{os.Stdout, os.Stderr, os.Stdin} {
		w, _, err := termSize(int(f.Fd()))
		if err == nil && w > 0 {
			return w
		}
	}
	return DefaultWidth
}

// Render markdown wrapped to Width.
func Render(src string) string {
	return RenderWidth(src, Width())
}

// RenderWidth is Render at an explicit column count.  A width of 0 or
// less is DefaultWidth: that is the same answer Width gives when the
// terminal will not say, and is what a caller who has not measured
// should pass rather than inventing a third number.
func RenderWidth(src string, width int) string {
	if width <= 0 {
		width = DefaultWidth
	}
	return renderBlocks(parse(src), width, false)
}
