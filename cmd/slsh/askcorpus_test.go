package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/internal/askindex"
)

// update rewrites generated files from what they are generated from,
// instead of checking them.  askindex.txt is the one there is.
var update = flag.Bool("update", false, "rewrite "+askIndexFile+" from the command table and the man pages")

// TestAskIndexIsCurrent is the generator of askindex.txt and the check
// that it has been run.
//
// The comparison is of bytes, and the encoding is built to make that
// fair (see internal/askindex/encode.go): the same table and pages give
// the same file on every machine, so any difference at all means the
// table or a page changed and the index did not.
func TestAskIndexIsCurrent(t *testing.T) {
	docs, err := askCorpus()
	if err != nil {
		t.Fatal(err)
	}
	want := askindex.Build(docs).Encode()
	if *update {
		if err := os.WriteFile(askIndexFile, want, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s: %d documents, %d bytes", askIndexFile, len(docs), len(want))
		return
	}
	if !bytes.Equal(askIndexData, want) {
		t.Fatalf("cmd/slsh/%s is out of date with the command table or the man pages "+
			"(%d bytes embedded, %d now).  Regenerate it with:\n\n\t%s\n\nand commit it with the change.",
			askIndexFile, len(askIndexData), len(want), askIndexUpdate)
	}
}

// The embedded index decodes, and holds what the corpus holds.  The
// byte comparison above already implies it; this is the path a shell
// takes, and a failure here says which half is wrong.
func TestAskIndexLoads(t *testing.T) {
	ix, err := askIndex()
	if err != nil {
		t.Fatal(err)
	}
	docs, err := askCorpus()
	if err != nil {
		t.Fatal(err)
	}
	if ix.Len() != len(docs) {
		t.Fatalf("the embedded index has %d documents and the corpus %d; %s", ix.Len(), len(docs), askIndexUpdate)
	}
}

// Every command has keywords, since the keywords are the only part of
// what is indexed that is written for somebody who does not know the
// command's name.  A command without them is one ask can find only by
// its own vocabulary.
func TestEveryCommandHasKeywords(t *testing.T) {
	for _, ac := range askCommands() {
		words := strings.Fields(ac.c.keywords)
		if len(words) < 3 {
			t.Errorf("%s has %d keywords; say in keywords, in shell.go's sense, what somebody who does not know its name would call it", ac.name, len(words))
		}
		if ac.c.keywords != strings.ToLower(ac.c.keywords) {
			t.Errorf("%s: keywords are lower case", ac.name)
		}
	}
}

// A command's own name has to survive tokenizing, or a question that
// names the command cannot find it -- the stopword list is where that
// would go wrong, since several names are ordinary words.
func TestCommandNamesAreSearchable(t *testing.T) {
	for _, n := range commandNames() {
		if strings.Trim(n, "abcdefghijklmnopqrstuvwxyz") != "" {
			continue // ".", which no question will say
		}
		if askindex.Stopword(n) || len(askindex.Tokens(n)) == 0 {
			t.Errorf("%s is dropped by the tokenizer, so no question can name it", n)
		}
	}
}

// The corpus has the shape askcorpus.go says: a line for every command,
// an opening for every page, a document for every flag the Options
// sections name, and examples that are command lines.
func TestAskCorpusShape(t *testing.T) {
	docs, err := askCorpus()
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]map[string]int{}
	for _, d := range docs {
		if kinds[d.Command] == nil {
			kinds[d.Command] = map[string]int{}
		}
		kinds[d.Command][d.Kind]++
		if d.Kind == askindex.KindExample && !askIsCommandLine(d.Text) {
			t.Errorf("%s: example %q is not a command line", d.ID, d.Text)
		}
		if strings.TrimSpace(d.Text) == "" {
			t.Errorf("%s is empty", d.ID)
		}
	}
	for _, ac := range askCommands() {
		k := kinds[ac.name]
		if k[askindex.KindLine] != 1 {
			t.Errorf("%s has %d line documents", ac.name, k[askindex.KindLine])
		}
		if ac.c.man != "" && k[askindex.KindIntro] != 1 {
			t.Errorf("%s has %d openings", ac.name, k[askindex.KindIntro])
		}
	}
	// Aliases are not commands of their own.
	for _, alias := range []string{"exit", "unsit", "."} {
		if kinds[alias] != nil {
			t.Errorf("%s is indexed as a command of its own", alias)
		}
	}

	// landmark's page names five flags, and --set-home is one.
	var flags []string
	for _, d := range docs {
		if d.Command == "landmark" && d.Kind == askindex.KindOption {
			flags = append(flags, d.Heading)
		}
	}
	if got := strings.Join(flags, "|"); got != "--make|--go|--home|--set-home|-w, --wait SECONDS" {
		t.Errorf("landmark's flags are %q", got)
	}
}

// askQuestion is one question somebody might type, and the command that
// answers it.  Every question here is invented, and so is every name in
// them.
type askQuestion struct {
	q    string
	want string

	// top is how far down the list want may be: 3 for a question whose
	// answer ought to be plain, 8 for one where several commands are
	// honestly in play.  8 is what ask hands a model to choose from, so
	// nothing may be further down than that.
	top int
}

var askQuestions = []askQuestion{
	// The two the plan asked for by name.
	{"see the contents of a script in one of my attachments", "cat", 3},
	{"set my home landmark", "landmark", 3},

	{"who is near me", "who", 3},
	{"give an object to a friend", "give", 3},
	{"how do I teleport to another region", "tp", 3},
	{"take me home", "tp", 3},
	{"pick an object up off the ground into my inventory", "take", 3},
	{"put an object from my inventory back into the world", "place", 3},
	{"delete an item from my inventory", "rm", 3},
	{"rename a folder", "mv", 3},
	{"make a new folder", "mkdir", 3},
	{"download a texture as a png", "get", 3},
	{"upload a photo as a texture", "put", 3},
	{"accept a friendship request", "accept", 3},
	{"ask somebody to be my friend", "offer", 3},
	{"send a private message to someone", "im", 3},
	{"say something in local chat", "say", 3},
	{"search for a person by name", "lookup", 3},
	{"which of my friends are online", "friends", 3},
	{"what am I wearing", "worn", 3},
	{"take off my hat", "detach", 3},
	{"put on a jacket from my inventory", "wear", 3},
	{"who owns the land I am standing on", "parcel", 3},
	{"invite someone to join my group", "invite", 3},
	{"change my active group", "group", 3},
	{"sit on a chair", "sit", 3},
	{"stand up", "stand", 3},
	{"start a script running in an object", "start", 3},
	{"put a script inside an object", "drop", 3},
	{"copy a notecard out of an object into my inventory", "fetch", 3},
	{"write a file from my computer into a notecard", "save", 3},
	{"create a new notecard", "new", 3},
	{"empty the trash", "emptytrash", 3},
	{"list the objects around me", "objects", 3},
	{"find an item in my inventory by name", "find", 3},
	{"where am I", "where", 3},
	{"click on an object", "touch", 3},
	{"offer somebody a teleport to where I am", "lure", 3},
	{"link several prims together", "link", 3},
	{"change the colour of one face of an object", "texture", 3},
	{"let other people copy my object", "perms", 3},
	{"log another avatar in", "login", 3},
	{"change a setting", "set", 3},
	{"run the commands in a file", "source", 3},
	{"look up an LSL function", "lsl", 3},
	{"draw a map of the avatars nearby", "map", 3},
	{"refuse a teleport somebody offered me", "no", 3},
	{"press a button on a dialog", "answer", 3},
	{"read somebody's profile", "profile", 3},
	{"move an object to a new position", "move", 3},
	{"save an object as a json file", "dump", 3},
	{"build an object from a json file", "rez", 3},
	{"change my maturity rating to adult", "maturity", 3},
	{"walk across the border into the next region", "neighbours", 3},
	{"open a real viewer on this avatar", "viewer", 3},
	{"what is waiting for me to answer", "waiting", 3},
}

// TestAskRetrieval asks the embedded index each question and checks
// that the command that answers it is near enough the top.
//
// When one fails, the fix is in what is indexed -- a keyword the command
// is missing, a word the tokenizer mangles -- and not in the question or
// its limit: the questions are what people type, and the point of this
// test is that the index is shaped to them rather than the other way
// round.
func TestAskRetrieval(t *testing.T) {
	ix, err := askIndex()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range askQuestions {
		got := ix.SearchCommands(c.q)
		rank := -1
		var top []string
		for i, h := range got {
			if h.Command == c.want && rank < 0 {
				rank = i
			}
			if i < 8 {
				top = append(top, fmt.Sprintf("%s %.1f", h.Command, h.Score))
			}
		}
		t.Logf("%-55q %d  %s", c.q, rank+1, strings.Join(top[:min(3, len(top))], ", "))
		if rank < 0 || rank >= c.top {
			t.Errorf("%q: %s is at %d, wanted in the first %d; the first eight are %s",
				c.q, c.want, rank+1, c.top, strings.Join(top, ", "))
		}
	}
}

// The questions are held to their word: at least a dozen must be answered
// in the first three, which is what makes the list a test of ranking and
// not only of recall.
func TestAskQuestionsAreStrict(t *testing.T) {
	strict := 0
	for _, c := range askQuestions {
		if c.top <= 3 {
			strict++
		}
		if c.top > 8 {
			t.Errorf("%q allows rank %d; ask shows a model eight", c.q, c.top)
		}
		if _, ok := commands[c.want]; !ok {
			t.Errorf("%q expects %q, which is no command", c.q, c.want)
		}
	}
	if strict < 12 {
		t.Errorf("only %d questions must be in the first three; keep at least a dozen", strict)
	}
}

// What the first question pays before it is searched.
func BenchmarkAskIndexDecode(b *testing.B) {
	for i := 0; i < b.N; i++ {
		if _, err := askindex.Decode(askIndexData); err != nil {
			b.Fatal(err)
		}
	}
}

// What each question pays after that.
func BenchmarkAskSearch(b *testing.B) {
	ix, err := askIndex()
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < b.N; i++ {
		ix.SearchCommands("see the contents of a script in one of my attachments")
	}
}
