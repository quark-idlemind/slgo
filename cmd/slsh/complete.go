package main

// Tab completion, in command mode only.
//
// In chat mode tab moves between conversations, which is why the two
// modes are worth having: the same key can mean the obvious thing in
// both places without either being a compromise.

import (
	"context"
	"sort"
	"strings"

	"github.com/quark-idlemind/slgo/sl"
)

// complete finishes the word under the cursor: a command if it is the
// first, an inventory path otherwise.
func (sh *Shell) complete(ctx context.Context) {
	line := sh.term.Line()
	head, word := lastWord(line)

	var options []string
	if head == "" {
		options = matching(commandNames(), word)
	} else {
		options = sh.completePath(ctx, word)
	}

	switch len(options) {
	case 0:
	case 1:
		sh.term.SetLine(head + quoteWord(options[0]))
	default:
		// A common prefix is progress even when the answer is not
		// settled; showing the rest is what a shell does.
		if p := commonPrefix(options); len(p) > len(word) {
			sh.term.SetLine(head + quoteWord(p))
		}
		// Shown unquoted.  These are for reading, and the quotes are
		// machinery for getting the name past the parser.
		sh.term.Print(strings.Join(options, "   "))
	}
}

// completePath finishes an inventory path, which may name a folder to
// carry on into.
func (sh *Shell) completePath(ctx context.Context, word string) []string {
	// The part already settled is everything up to the last
	// separator; what follows is being typed.
	dir, stem := "", word
	if i := strings.LastIndex(word, string(sl.PathSeparator)); i >= 0 {
		dir, stem = word[:i+1], word[i+1:]
	}

	_, id, err := sh.resolveDir(ctx, strings.TrimSuffix(dir, string(sl.PathSeparator)))
	if err != nil {
		return nil
	}
	es, err := sh.s.ListFolder(ctx, id, 0)
	if err != nil {
		return nil
	}

	var out []string
	for _, e := range es {
		name := sl.EscapeName(e.Name)
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(stem)) {
			continue
		}
		// A folder gets a separator, so the next tab carries on
		// inside it rather than stopping at its name.
		if e.Folder {
			name += string(sl.PathSeparator)
		}
		out = append(out, dir+name)
	}
	sort.Strings(out)
	return out
}

// lastWord divides a line into everything before the word under the
// cursor and that word, with its quoting taken off.
//
// Quote aware, because an inventory name may hold a space and a great
// many do: "Current Outfit" is a folder every avatar has.  A split at
// the last space finished that as "Outfit", and -- worse -- completing
// "Curr" produced the whole name unquoted, which the parser then read
// as two arguments.  Tab turned a correct line into a broken one, which
// is the one thing a completion must never do.
//
// The head is returned verbatim, opening quote and all, so that what
// goes back is head + a freshly quoted word: whatever the person had
// started typing of the name is replaced rather than appended to.
func lastWord(line string) (head, word string) {
	start := 0
	var quote rune
	for i, r := range line {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == ' ' || r == '\t':
			start = i + 1
		}
	}
	return line[:start], unquoteWord(line[start:])
}

// unquoteWord takes the quoting off one word, the way the parser does.
func unquoteWord(s string) string {
	var b strings.Builder
	var quote rune
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			b.WriteRune(r)
		case r == '"' || r == '\'':
			quote = r
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// quoteWord puts a word back in a shape the parser reads as one word.
//
// Only when it has to.  Most names need nothing, and a line full of
// quotes that earn nothing is harder to read and harder to edit.
//
// The quotes are closed even when the completion is a folder and there
// is obviously more to type.  An unterminated quote is refused by the
// parser outright -- "unclosed \" quote" -- so leaving one open would
// mean tab produced a line that could not be run, and a person who
// tabbed to a folder and pressed return is entitled to have that work.
// Typing carries on after the closing quote perfectly well, since the
// parser joins adjacent pieces: "Current Outfit/"Sen is one word.
//
// A name holding both kinds of quote is offered unquoted rather than
// mangled.  It can be written as one word only in pieces, each " in
// single quotes of its own, as quoteWords writes it; ids are for names
// like that.
func quoteWord(s string) string {
	if !strings.ContainsAny(s, " \t\"'><") {
		return s
	}
	if !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	return s
}

func matching(all []string, prefix string) []string {
	var out []string
	for _, s := range all {
		if strings.HasPrefix(s, prefix) {
			out = append(out, s)
		}
	}
	return out
}

func commonPrefix(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	p := ss[0]
	for _, s := range ss[1:] {
		for !strings.HasPrefix(s, p) {
			p = p[:len(p)-1]
			if p == "" {
				return ""
			}
		}
	}
	return p
}
