package main

// Which character an avatar wears, for the person it is talking to.
//
// A backstory line names a file or a DIRECTORY.  A file is one
// character, worn with everybody, which is where this started and still
// what most avatars want.  A directory is a character that knows who it
// is talking to: "default" in it is what everybody gets, and a file
// named for one person is used instead when that person is the one
// speaking.
//
// One file is chosen and not two: the person's if there is one, the
// default otherwise.  What that file is MADE of is the file's own
// business, because a line in it that is nothing but a relative path
// includes another file there:
//
//	./default.txt
//
//	You have known this one for years.  They helped build the dock.
//
// Composition belongs to the file rather than to this code.  Joining
// two files here in a fixed order can only ever say one thing -- the
// character, then the person -- and the useful arrangements are not all
// that shape: the shared part often wants to come LAST, nearest the
// conversation, where a model weighs hardest; two people may share a
// third file that is neither of those; and a paragraph several
// characters have in common should be written once.  An include says
// all of them and a rule here says one.
//
// What it costs is that a person's file can forget the character and
// nothing will say so -- an avatar that is suddenly nobody, quietly.
// That is the price of the file deciding, and --check assembles every
// character so the answer is at least visible before anybody is logged
// in.
//
// Nothing here is held in memory.  The files are read on the turn they
// are used, so adding a character for somebody, or editing one it
// includes, is a file appearing and not a restart.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/quark-idlemind/slgo/msg"
)

// Default names the character in a directory that everybody gets.
//
// "default" rather than "backstory": every file in there is a
// backstory, so that name says nothing, and what this one IS is the one
// used when nothing more specific matches.
const Default = "default"

// characterSuffix may be left off.  A directory of characters is a
// directory somebody edits by hand, and insisting on an extension there
// would be one more way to get no character and no message about it.
const characterSuffix = ".txt"

// readCharacter is the system prompt for one avatar talking to one
// person: the whole of what it is, from a file or a directory.
//
// An error is a path that was named and could not be read, which is
// worth saying out loud.  A directory that simply holds nothing for
// this person is not an error -- it is an avatar with no character,
// which is what an avatar with no backstory line is too.
func readCharacter(path string, who msg.UUID, name string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return assemble(path)
	}

	general, mine, err := characterFiles(path, who, name)
	if err != nil {
		return "", err
	}
	switch {
	case mine != "":
		return assemble(filepath.Join(path, mine))
	case general != "":
		return assemble(filepath.Join(path, general))
	}
	return "", nil
}

// assemble is one character file with its includes in it.
func assemble(path string) (string, error) {
	return include(path, map[string]bool{}, 0)
}

// maxIncludeDepth stops a chain that is not a cycle but is not a
// character either.
const maxIncludeDepth = 8

// include reads one file and puts the files it names in place of the
// lines that name them.
//
// A cycle is an error and not a silent stop.  A file that includes
// itself, directly or round a ring of four, is a mistake in a file
// somebody wrote; a quiet truncation would leave a character that is
// half of what it says it is, and nothing anywhere would mention it
// again.
func include(path string, seen map[string]bool, depth int) (string, error) {
	if depth > maxIncludeDepth {
		return "", fmt.Errorf("%s: includes are more than %d deep", path, maxIncludeDepth)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if seen[abs] {
		return "", fmt.Errorf("%s includes itself", path)
	}
	seen[abs] = true
	defer delete(seen, abs)

	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	dir := filepath.Dir(abs)
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		rel, ok := includePath(line)
		if !ok {
			continue
		}
		text, err := include(filepath.Join(dir, rel), seen, depth+1)
		if err != nil {
			return "", err
		}
		lines[i] = text
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), nil
}

// includePath is the file a line names, if it names one.
//
// A line that is a relative path and nothing else.  No prose begins
// "./", so there is nothing to escape and no way for a sentence to
// become an include by accident -- which is the whole reason for
// spelling it this way rather than inventing a directive.
//
// Relative only.  Not for safety: whoever writes a backstory can
// already name any program in the configuration, and there is no
// boundary here to defend.  It is so that a directory of characters can
// be moved, copied or kept somewhere else and still be itself.
func includePath(line string) (string, bool) {
	p := strings.TrimSpace(line)
	if !strings.HasPrefix(p, "./") && !strings.HasPrefix(p, "../") {
		return "", false
	}
	return p, true
}

// characterFiles finds the two files that may be a person's character
// in a directory: the default, and their own if there is one.  Either
// may be empty.
//
// The directory is listed rather than guessed at, so that one rule
// covers every way somebody might have spelt the name.  Guessing means
// a fixed set of candidate filenames, and the one spelling that was
// used is always the one nobody thought of.
func characterFiles(dir string, who msg.UUID, name string) (general, mine string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", "", err
	}

	// Sorted, so that a directory holding two spellings of one person
	// picks the same file every time rather than whichever the
	// filesystem happened to hand back first.
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	wantName := canonPerson(name)
	wantID := ""
	if !who.IsZero() {
		wantID = canonPerson(who.String())
	}
	for _, n := range names {
		switch key := canonPerson(stripSuffix(n)); {
		case key == Default:
			if general == "" {
				general = n
			}
		case mine != "":
			// Already found somebody's.
		case wantName != "" && key == wantName:
			mine = n
		case wantID != "" && key == wantID:
			mine = n
		}
	}
	return general, mine, nil
}

// stripSuffix takes ".txt" off a filename, however it was capitalised.
//
// Case-insensitively, because the name in front of it is matched that
// way too and a directory where "Example Resident.TXT" is somebody
// different from "example resident.txt" would be a directory nobody
// could reason about.
func stripSuffix(name string) string {
	cut := len(name) - len(characterSuffix)
	if cut > 0 && strings.EqualFold(name[cut:], characterSuffix) {
		return name[:cut]
	}
	return name
}

// canonPerson is the one spelling two names are compared in.
//
// Lower case, and every run of spaces, hyphens and underscores is one
// space: "Example Resident", "example-resident" and "EXAMPLE_RESIDENT"
// are one person, because a directory is typed into by hand and a shell
// makes a space in a filename tiresome enough that somebody will
// reasonably avoid it.
//
// A uuid goes through this as well, and comes out with its hyphens as
// spaces.  That is not a problem and not worth special-casing: both
// sides of the comparison come through here, so it only has to be
// consistent.
func canonPerson(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.TrimSpace(strings.ToLower(s)) {
		if r == '-' || r == '_' || unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteRune(' ')
		}
		space = false
		b.WriteRune(r)
	}
	return b.String()
}

// describeCharacters says what a backstory path holds, for --check.
//
// Every character is ASSEMBLED here, includes and all, and reported by
// the size it came to.  That is the thing includes take away: what a
// file says is no longer what an avatar gets, and a person's file that
// forgot to include the character is a few hundred characters short in
// a way nothing else would ever mention.  A broken include is found
// here too, which is where somebody editing a directory would want it
// found rather than in the middle of a conversation.
//
// The first line describes the path; the rest are one character each.
func describeCharacters(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		text, err := assemble(path)
		if err != nil {
			return nil, err
		}
		return []string{fmt.Sprintf("%s, %s", path, sizeOf(text))}, nil
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	general, people := false, 0
	out := make([]string, 1, len(names)+1)
	for _, n := range names {
		if canonPerson(stripSuffix(n)) == Default {
			general = true
		} else {
			people++
		}
		what := ""
		if text, err := assemble(filepath.Join(path, n)); err != nil {
			what = fmt.Sprintf("BROKEN: %v", err)
		} else {
			what = sizeOf(text)
		}
		out = append(out, fmt.Sprintf("%-24s %s", n, what))
	}

	who := fmt.Sprintf("%d for particular people", people)
	if people == 1 {
		who = "1 for one person"
	}
	switch {
	case general && people == 0:
		out[0] = path + ": a default, and nobody in particular"
	case general:
		out[0] = fmt.Sprintf("%s: a default, and %s", path, who)
	case people == 0:
		out[0] = path + ": EMPTY, so no character at all"
	default:
		out[0] = fmt.Sprintf("%s: %s, and NO DEFAULT", path, who)
	}
	return out, nil
}

// sizeOf is how much character a file came to once it was assembled.
//
// In characters and not tokens, because counting tokens means asking
// the model and --check deliberately connects to nothing.  What it is
// for is noticing that a character came out at forty characters or at
// four thousand, and either of those is plain at this resolution.
func sizeOf(text string) string {
	switch n := len([]rune(text)); {
	case n == 0:
		return "EMPTY"
	case n == 1:
		return "1 character"
	default:
		return fmt.Sprintf("%d characters", n)
	}
}
