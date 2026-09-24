// Package askindex finds the documents that answer a question in words,
// for slsh's ask: which command does this, and which paragraph says so.
//
// It is a plain word index scored with BM25, and nothing cleverer, for
// three reasons.  The documents are few (slsh has some eight hundred
// paragraphs and lines) and written by one hand, so the vocabulary of the pages is the
// vocabulary of the questions far more often than it would be on the
// open web.  It has to work with no model at all, since ask must be
// useful on a machine with no model running.  And what it hands on is
// read by a small model that cannot tell a plausible wrong candidate from
// a right one, so what matters is that the right command is somewhere
// in the first handful -- which a word index does well -- rather than
// that the ordering of the first handful is subtle.
//
// # The shape of it
//
// A Doc is one piece of text that belongs to one command: the line a
// help listing would print, a page's opening, one section, one flag, one
// example.  Build tokenizes them (see tokens.go) and counts; Encode
// writes the counts and the documents out as text, and Decode reads
// them back.  The encoding is the thing that ships: it is generated when
// the pages change and embedded in the binary, so that the first
// question costs a decode and no tokenizing, and no question costs
// anything at startup.
//
// The package knows nothing about slsh.  What a command is, which pages
// it has and what its keywords are is the caller's business, and all of
// it arrives here already turned into Docs.
//
// # Hints
//
// A question in a person's own words often uses none of the words the
// right page uses: "go back to my house" and a page about home.  Two
// remedies are offered, both through Hint.  A hint may add words to a
// question (the words the page would have used), and it may raise named
// commands outright.  Either may be made conditional on words the
// question contains.  The caller decides where hints come from; the
// first source is a file a person writes, one line per habit of speech
// the pages do not share.
package askindex

import (
	"math"
	"sort"
)

// The kinds of document the caller is expected to make.  Nothing here
// depends on them except the default weights; a caller may use kinds of
// its own, which weigh 1.
const (
	KindLine    = "line"    // name, usage, brief, groups and keywords of one command
	KindIntro   = "intro"   // a page's text before its first heading
	KindSection = "section" // one "## " section of a page
	KindOption  = "option"  // one flag's paragraph under Options
	KindExample = "example" // one example command line
)

// Doc is one searchable piece of text.
type Doc struct {
	// ID names the document uniquely within an index, so that a test or
	// a log can say which one it means.  Nothing parses it.
	ID string

	// Command is the command the document belongs to, by its main name.
	Command string

	// Kind is one of the Kind constants, or the caller's own.
	Kind string

	// Heading is the section heading or flag the text sits under, and is
	// searched as part of the document.  Empty where there is none.
	Heading string

	// Text is what the document says, as the page has it.
	Text string
}

// Index is a set of documents and the words in them.
type Index struct {
	docs []Doc

	// postings is, for every word, the documents it is in and how many
	// times, in document order.
	postings map[string][]posting

	// lens is each document's length in words, and avg their mean;
	// BM25 scores a word found in a short document above the same word
	// in a long one.
	lens []int
	avg  float64
}

type posting struct {
	doc int
	tf  int
}

// Build indexes docs.  The documents are kept in the order given, and
// that order is what breaks ties between equal scores, so the same docs
// in the same order always search the same way.
func Build(docs []Doc) *Index {
	ix := &Index{
		docs:     append([]Doc(nil), docs...),
		postings: map[string][]posting{},
	}
	for i, d := range ix.docs {
		counts := map[string]int{}
		for _, t := range Tokens(d.Heading + "\n" + d.Text) {
			counts[t]++
		}
		for t, n := range counts {
			ix.postings[t] = append(ix.postings[t], posting{doc: i, tf: n})
		}
	}
	// Documents were visited in order, so each list is already sorted;
	// only the lengths are left to work out.
	ix.finish()
	return ix
}

// finish works out what is derived from the postings.  Build and Decode
// both end here, so that an index read back is the index that was built
// and not a near copy of it.
func (ix *Index) finish() {
	ix.lens = make([]int, len(ix.docs))
	for _, ps := range ix.postings {
		for _, p := range ps {
			ix.lens[p.doc] += p.tf
		}
	}
	total := 0
	for _, n := range ix.lens {
		total += n
	}
	ix.avg = 1
	if len(ix.lens) > 0 && total > 0 {
		ix.avg = float64(total) / float64(len(ix.lens))
	}
}

// Docs is every document, in index order.  The slice is the index's
// own and is not to be changed.
func (ix *Index) Docs() []Doc { return ix.docs }

// Len is how many documents there are.
func (ix *Index) Len() int { return len(ix.docs) }

// Hint adds to a question, or raises commands, when the question
// contains certain words.
type Hint struct {
	// When are the words the question has to contain, every one of them,
	// for the hint to apply.  They go through Tokens like the question
	// does, so "homes" here matches "home" there.  Empty applies always.
	When []string

	// Words are searched for as well as the question's own words, at
	// half the weight: they are what the page would have said, and a
	// word the person actually typed should still count for more.
	Words []string

	// Commands are raised: each gets a hit on its own line document
	// worth as much as the best-scoring document found, on top of
	// whatever it scored by its words.  So a raised command reaches the
	// head of the list or near it without anything else being pushed
	// off, and a raised command nothing else matched still appears.
	Commands []string
}

// Hit is one document that matched.
type Hit struct {
	Doc   *Doc
	Score float64

	// Matched are the question's words that this document has, as
	// Tokens spells them.
	Matched []string

	// Hinted is whether a hint raised this document.
	Hinted bool

	n int // the document's place in the index, for ordering ties
}

// Default BM25 parameters.  k1 is how quickly more occurrences of a word
// stop counting; b is how much a long document is penalised for being
// long.  These are the usual values, and nothing here was tuned against
// them.
const (
	k1 = 1.2
	b  = 0.75

	// expandWeight is how much a word added by a hint counts against a
	// word typed in the question.
	expandWeight = 0.5
)

// kindWeight scales a document's score by what kind it is.
//
// A command's line counts double: it is short and every word in it was
// chosen to say what the command is for, where a section of a page is
// written to explain one of its traps and mentions half a dozen other
// commands on the way.  Without the weight, a command with a long page
// about something next door wins on breadth -- lure, whose page is all
// teleports and offers, came above no for "refuse a teleport somebody
// offered me" at 1.5 and fell below it at 2, with no other question in
// cmd/slsh's retrieval test changing place.  An example is lowered a
// little, since it is mostly names of made-up things and one flag.
var kindWeight = map[string]float64{
	KindLine:    2.0,
	KindIntro:   1.2,
	KindSection: 1.0,
	KindOption:  1.0,
	KindExample: 0.8,
}

// Search scores every document against a question and returns those that
// matched anything, best first.  Equal scores keep index order.
func (ix *Index) Search(question string, hints ...Hint) []Hit {
	weights := map[string]float64{}
	asked := Tokens(question)
	for _, t := range asked {
		weights[t] = 1
	}
	have := map[string]bool{}
	for _, t := range asked {
		have[t] = true
	}

	var raise []string
	for _, h := range hints {
		if !applies(h, have) {
			continue
		}
		for _, w := range h.Words {
			for _, t := range Tokens(w) {
				if weights[t] < expandWeight {
					weights[t] = expandWeight
				}
			}
		}
		raise = append(raise, h.Commands...)
	}

	scores := map[int]float64{}
	matched := map[int][]string{}
	n := float64(len(ix.docs))
	// Words in a fixed order, so that Matched comes out the same way on
	// every run; ranging over the map would shuffle it.
	words := make([]string, 0, len(weights))
	for t := range weights {
		words = append(words, t)
	}
	sort.Strings(words)
	for _, t := range words {
		ps := ix.postings[t]
		if len(ps) == 0 {
			continue
		}
		df := float64(len(ps))
		idf := math.Log(1 + (n-df+0.5)/(df+0.5))
		for _, p := range ps {
			tf := float64(p.tf)
			norm := tf * (k1 + 1) / (tf + k1*(1-b+b*float64(ix.lens[p.doc])/ix.avg))
			scores[p.doc] += weights[t] * idf * norm
			matched[p.doc] = append(matched[p.doc], t)
		}
	}

	hits := make([]Hit, 0, len(scores))
	best := 0.0
	for d, s := range scores {
		w, ok := kindWeight[ix.docs[d].Kind]
		if !ok {
			w = 1
		}
		s *= w
		if s > best {
			best = s
		}
		hits = append(hits, Hit{Doc: &ix.docs[d], Score: s, Matched: matched[d], n: d})
	}

	if len(raise) > 0 {
		if best == 0 {
			best = 1
		}
		hits = ix.raise(hits, raise, best)
	}

	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].n < hits[j].n
	})
	return hits
}

// applies is whether every one of a hint's When words is in the
// question.
//
// A When word that Tokens drops altogether -- "what", "is", "this" --
// can never be found in a question, since the question's copy of it is
// dropped too, and so a hint that needs it never applies.  Counting it
// as found instead would make a hint written as "what is this" apply to
// every question anybody asked, and raise its commands to the top of
// all of them.  An empty When is still the hint that always applies.
func applies(h Hint, have map[string]bool) bool {
	for _, w := range h.When {
		ts := Tokens(w)
		if len(ts) == 0 {
			return false
		}
		for _, t := range ts {
			if !have[t] {
				return false
			}
		}
	}
	return true
}

// raise adds by to the line document of each named command, and makes
// one a hit if it was not.  A command with no line document has its
// first document raised instead, and a name that is no command here is
// ignored: a hints file written for another build may name a command
// this one does not have, and that is not worth failing a question for.
func (ix *Index) raise(hits []Hit, commands []string, by float64) []Hit {
	at := map[int]int{}
	for i, h := range hits {
		at[h.n] = i
	}
	done := map[string]bool{}
	for _, c := range commands {
		if done[c] {
			continue
		}
		done[c] = true
		d := ix.lineDoc(c)
		if d < 0 {
			continue
		}
		if i, ok := at[d]; ok {
			hits[i].Score += by
			hits[i].Hinted = true
			continue
		}
		at[d] = len(hits)
		hits = append(hits, Hit{Doc: &ix.docs[d], Score: by, Hinted: true, n: d})
	}
	return hits
}

// lineDoc is the document that stands for a command: its line document,
// or failing that the first document it has.  -1 is no such command.
func (ix *Index) lineDoc(command string) int {
	first := -1
	for i, d := range ix.docs {
		if d.Command != command {
			continue
		}
		if d.Kind == KindLine {
			return i
		}
		if first < 0 {
			first = i
		}
	}
	return first
}

// CommandHit is one command and the documents of its that matched.
type CommandHit struct {
	Command string
	Score   float64
	Hits    []Hit // best first
}

// groupDecay is how much each further document of one command adds to
// its score, as a fraction of the one before: the best document counts
// whole, the second half, the third a quarter, and no more are counted.
//
// A command should not win because its page is long.  But a command
// three of whose paragraphs answer the question is more likely the
// answer than one with a single paragraph that happens to share a rare
// word with it, so the others count for something.
var groupDecay = []float64{1, 0.5, 0.25}

// Group gathers hits by command, best command first.  Equal scores go
// alphabetically, so that the order never depends on a map.
func Group(hits []Hit) []CommandHit {
	at := map[string]int{}
	var out []CommandHit
	for _, h := range hits {
		i, ok := at[h.Doc.Command]
		if !ok {
			i = len(out)
			at[h.Doc.Command] = i
			out = append(out, CommandHit{Command: h.Doc.Command})
		}
		out[i].Hits = append(out[i].Hits, h)
	}
	for i := range out {
		// hits arrive best first, and each command's list keeps that
		// order, so the first few are the ones to count.
		for k, h := range out[i].Hits {
			if k >= len(groupDecay) {
				break
			}
			out[i].Score += groupDecay[k] * h.Score
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Command < out[j].Command
	})
	return out
}

// SearchCommands is Search and then Group: the commands that answer a
// question, best first, each with its matching documents.
func (ix *Index) SearchCommands(question string, hints ...Hint) []CommandHit {
	return Group(ix.Search(question, hints...))
}

// Commands is every command that has a document, sorted.
func (ix *Index) Commands() []string {
	seen := map[string]bool{}
	var out []string
	for _, d := range ix.docs {
		if !seen[d.Command] {
			seen[d.Command] = true
			out = append(out, d.Command)
		}
	}
	sort.Strings(out)
	return out
}
