package main

// What ask searches, and the index it searches it with.
//
// ask answers "how do I ..." with a command, and the first half of
// that is finding which commands could be meant.  That is a word index
// over everything the shell already says about itself -- the command
// table and the man pages -- built by internal/askindex.  This file
// turns the shell's own tables into the documents that package indexes,
// and holds the index that ships.
//
// # The documents
//
// Five kinds, all belonging to one command each:
//
//	line     the command's names, its usage line, its brief, the help
//	         groups it is in, and its keywords
//	intro    its page up to the first heading
//	section  each "## " section of the page, by its heading
//	option   each flag under "## Options", a paragraph or more apiece
//	example  each indented line of the page that is a command line
//
// The line is what a help listing prints plus the keywords, which are
// on the command for exactly this (see keywords in shell.go): the words
// somebody would use who does not know the command's name.  A page is
// written by somebody who does, and says "landmark --set-home" where
// the question says "make this my home".
//
// The Options section is not also a section of its own.  Its whole text
// is the flags, each already a document, and indexing it twice would
// make every command with flags score twice for having them.
//
// An indented line is only an example when its first word is a command.
// Pages indent what the grid printed back as well -- a listing, a
// refusal quoted word for word -- and a line of somebody else's output
// is not a thing ask should ever offer to be typed.  The same example
// appears in a page's opening and again under Examples, often; it is
// kept once.
//
// # Why it is generated and embedded
//
// So that no work is done at startup, and little on the first question:
// the index is built here by a test and written to askindex.txt, which
// is embedded, and the first ask decodes it (askIndex, below).  Nothing
// touches it before that.
//
// A generated file can go stale, and one that went stale quietly would
// answer from the pages as they were.  TestAskIndexIsCurrent rebuilds
// the index from the table and the pages as they are now and fails,
// saying what to run, when the embedded copy differs from it by a byte.
// The generator is a test rather than a program because it needs this
// package's command table, and a program cannot import package main.

import (
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/quark-idlemind/slgo/internal/askindex"
)

// askIndexFile is where the generated index lives, beside this file.
const askIndexFile = "askindex.txt"

// askIndexUpdate is the command that regenerates it, for the message
// that says it is stale.
const askIndexUpdate = "go test ./cmd/slsh -run TestAskIndexIsCurrent -update"

//go:embed askindex.txt
var askIndexData []byte

var (
	askIndexOnce sync.Once
	askIndexVal  *askindex.Index
	askIndexErr  error
)

// askIndex is the index, decoded the first time it is asked for and
// kept.  Nothing calls it at startup: a shell that is never asked a
// question never pays for one.
func askIndex() (*askindex.Index, error) {
	askIndexOnce.Do(func() {
		askIndexVal, askIndexErr = askindex.Decode(askIndexData)
		if askIndexErr != nil {
			askIndexErr = fmt.Errorf("the index how searches is damaged in this build (%v); %s rebuilds it", askIndexErr, askIndexUpdate)
		}
	})
	return askIndexVal, askIndexErr
}

// askSearch is the commands that could answer a question, best first.
func askSearch(question string, hints ...askindex.Hint) ([]askindex.CommandHit, error) {
	ix, err := askIndex()
	if err != nil {
		return nil, err
	}
	return ix.SearchCommands(question, hints...), nil
}

// askCommand is one command as the corpus sees it: the name it is known
// by and the other names that share it.
type askCommand struct {
	name    string
	aliases []string
	c       *command
}

// askCommands is every command once, in name order.
//
// Two names can share one command -- quit and exit, "." and source,
// stand and unsit -- and each such command is indexed once, under the
// name its man page has, since that is the name the page is written
// about; the other names are in its line document so that they can be
// searched for.  A command with no page takes its first name in sorted
// order.
func askCommands() []askCommand {
	byCmd := map[*command][]string{}
	var order []*command
	for _, n := range commandNames() {
		c := commands[n]
		if _, ok := byCmd[c]; !ok {
			order = append(order, c)
		}
		byCmd[c] = append(byCmd[c], n)
	}
	out := make([]askCommand, 0, len(order))
	for _, c := range order {
		names := byCmd[c]
		main := names[0]
		if c.man != "" && commands[c.man] == c {
			main = c.man
		}
		var aliases []string
		for _, n := range names {
			if n != main {
				aliases = append(aliases, n)
			}
		}
		out = append(out, askCommand{name: main, aliases: aliases, c: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// askCorpus is every document ask searches, in a fixed order: commands
// by name, and within one command its line first and then its page from
// the top.  The order is part of what makes the encoded index the same
// bytes every time it is built.
func askCorpus() ([]askindex.Doc, error) {
	var docs []askindex.Doc
	for _, ac := range askCommands() {
		docs = append(docs, askLineDoc(ac))
		if ac.c.man == "" {
			continue
		}
		text, err := manRead(ac.c.man)
		if err != nil {
			return nil, err
		}
		docs = append(docs, askPageDocs(ac.name, text)...)
	}
	seen := map[string]bool{}
	for _, d := range docs {
		if seen[d.ID] {
			return nil, fmt.Errorf("two ask documents are called %q", d.ID)
		}
		seen[d.ID] = true
	}
	return docs, nil
}

// askLineDoc is the one document that stands for a command.  One fact
// to a line, each saying what it is, so that the text reads sensibly
// when it is shown as well as searched.
func askLineDoc(ac askCommand) askindex.Doc {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n", ac.c.usage(ac.name), ac.c.brief)
	if len(ac.aliases) > 0 {
		fmt.Fprintf(&b, "also called: %s\n", strings.Join(ac.aliases, " "))
	}
	if gs := askGroupsOf(ac.name); len(gs) > 0 {
		fmt.Fprintf(&b, "help groups: %s\n", strings.Join(gs, " "))
	}
	if ac.c.keywords != "" {
		fmt.Fprintf(&b, "keywords: %s\n", ac.c.keywords)
	}
	return askindex.Doc{
		ID:      ac.name,
		Command: ac.name,
		Kind:    askindex.KindLine,
		Text:    strings.TrimSuffix(b.String(), "\n"),
	}
}

// askGroupsOf is the help groups a command is listed in, by any of its
// names, sorted.
func askGroupsOf(name string) []string {
	c := commands[name]
	var out []string
	for _, g := range groups {
		for _, m := range g.members {
			if commands[m] == c {
				out = append(out, g.name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// askFlagLine is the line that starts one flag's paragraph under
// Options: the flag in bold, and its argument in italics after it.
//
//	**-w, --wait** *SECONDS*
var askFlagLine = regexp.MustCompile(`^\*\*(-[^*]*)\*\*(.*)$`)

// askPageDocs splits one man page into its documents.
//
// The page is read as the lines it was written in rather than through
// package md, because what matters here is where the headings and the
// indented blocks are, and those are plain at the start of a line.
// Text is kept as the page has it, markdown and all: it is what ask
// quotes from, and a quote that has to be found again in the page is
// easiest to find when nothing was done to it.
func askPageDocs(name, page string) []askindex.Doc {
	var docs []askindex.Doc
	var examples []askindex.Doc
	seenExample := map[string]bool{}

	heading := ""     // the section being read; "" is the opening
	var body []string // its lines
	flag := ""        // the flag being read, under Options
	var flagBody []string
	sections := map[string]int{}

	endFlag := func() {
		if flag == "" {
			return
		}
		docs = append(docs, askindex.Doc{
			ID:      name + ":option:" + flag,
			Command: name,
			Kind:    askindex.KindOption,
			Heading: flag,
			Text:    askTrimLines(flagBody),
		})
		flag, flagBody = "", nil
	}
	endSection := func() {
		endFlag()
		text := askTrimLines(body)
		switch {
		case heading == "":
			if text != "" {
				docs = append(docs, askindex.Doc{ID: name + ":intro", Command: name, Kind: askindex.KindIntro, Text: text})
			}
		case heading == "Options" && text == "":
			// Each flag is already a document; see the head of this file.
			// What is left is only what came before the first flag, and
			// no page has any today; if one ever does, it is a section.
		default:
			id := name + ":section:" + heading
			// Two sections of one page with one heading would be two
			// documents with one id.  No page has that today, and a
			// number keeps them apart if one ever does.
			if n := sections[heading]; n > 0 {
				id = fmt.Sprintf("%s#%d", id, n+1)
			}
			sections[heading]++
			docs = append(docs, askindex.Doc{ID: id, Command: name, Kind: askindex.KindSection, Heading: heading, Text: text})
		}
		// The examples of a section follow it, so that a page's
		// documents stay in the order the page has them.
		docs = append(docs, examples...)
		examples = nil
		body = nil
	}

	for _, line := range strings.Split(page, "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			endSection()
			heading = strings.TrimSpace(h)
			continue
		}
		if ex, ok := strings.CutPrefix(line, "    "); ok {
			ex = strings.TrimSpace(ex)
			if askIsCommandLine(ex) && !seenExample[ex] {
				seenExample[ex] = true
				examples = append(examples, askindex.Doc{
					ID:      fmt.Sprintf("%s:example:%d", name, len(seenExample)),
					Command: name,
					Kind:    askindex.KindExample,
					Heading: heading,
					Text:    ex,
				})
			}
		}
		if heading == "Options" {
			if m := askFlagLine.FindStringSubmatch(line); m != nil {
				endFlag()
				flag = strings.TrimSpace(m[1] + " " + strings.Trim(strings.TrimSpace(m[2]), "*"))
				continue
			}
			if flag != "" {
				flagBody = append(flagBody, line)
				continue
			}
		}
		body = append(body, line)
	}
	endSection()
	return docs
}

// askIsCommandLine is whether an indented line is a command line --
// its first word is a command -- rather than something printed back.
func askIsCommandLine(line string) bool {
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	_, ok := commands[f[0]]
	return ok
}

// askTrimLines joins lines with the blank ones at either end taken off.
// Only whole blank lines: a section that opens with an example keeps
// that example's indentation, which is what makes it an example.
func askTrimLines(lines []string) string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}
