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
	head, word := splitLast(line)

	var options []string
	if head == "" {
		options = matching(commandNames(), word)
	} else {
		options = sh.completePath(ctx, word)
	}

	switch len(options) {
	case 0:
	case 1:
		sh.term.SetLine(head + options[0])
	default:
		// A common prefix is progress even when the answer is not
		// settled; showing the rest is what a shell does.
		if p := commonPrefix(options); len(p) > len(word) {
			sh.term.SetLine(head + p)
		}
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

// splitLast divides a line into everything before the last word and the
// last word itself.
func splitLast(line string) (head, word string) {
	i := strings.LastIndexAny(line, " \t")
	if i < 0 {
		return "", line
	}
	return line[:i+1], line[i+1:]
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
