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
			continue // ".", which no question will say, and "How", which is how capitalised
		}
		if n == howName {
			// The one name that is meant to be dropped.  Every question
			// begins with it, so searching for it would find how's own
			// page for every question asked -- and how is never the
			// answer it is looking for (see howNeverAnswer).
			continue
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
	// The two questions ask was planned around.
	{"do I see the contents of a script in one of my attachments", "cat", 3},
	{"do I set my home landmark", "landmark", 3},

	{"do I see who is near me", "who", 3},
	{"do I give an object to a friend", "give", 3},
	{"do I teleport to another region", "tp", 3},
	{"do I get home", "tp", 3},
	{"do I pick an object up off the ground into my inventory", "take", 3},
	{"do I put an object from my inventory back into the world", "place", 3},
	{"do I delete an item from my inventory", "rm", 3},
	{"do I rename a folder", "mv", 3},
	{"do I make a new folder", "mkdir", 3},
	{"do I download a texture as a png", "get", 3},
	{"do I upload a photo as a texture", "put", 3},
	{"do I accept a friendship request", "accept", 3},
	{"do I ask somebody to be my friend", "offer", 3},
	{"do I send a private message to someone", "im", 3},
	{"do I say something in local chat", "say", 3},
	{"do I search for a person by name", "lookup", 3},
	{"do I find which of my friends are online", "friends", 3},
	{"do I find what I am wearing", "worn", 3},
	{"do I take off my hat", "detach", 3},
	{"do I put on a jacket from my inventory", "wear", 3},
	{"do I find who owns the land I am standing on", "parcel", 3},
	{"do I invite someone to join my group", "invite", 3},
	{"do I change my active group", "group", 3},
	{"do I sit on a chair", "sit", 3},
	{"do I stand up", "stand", 3},
	{"do I start a script running in an object", "start", 3},
	{"do I put a script inside an object", "drop", 3},
	{"do I copy a notecard out of an object into my inventory", "fetch", 3},
	{"do I write a file from my computer into a notecard", "save", 3},
	{"do I create a new notecard", "new", 3},
	{"do I empty the trash", "emptytrash", 3},
	{"do I list the objects around me", "objects", 3},
	{"do I find an item in my inventory by name", "find", 3},
	{"do I find where I am", "where", 3},
	// "where am I" is the avatar's place, but it could be the shell's
	// folder, and the model is shown both to choose from.  Said about
	// inventory it is the folder, and said about a thing it is a search.
	{"do I find where I am", "pwd", 8},
	{"do I find where I am in my inventory", "pwd", 3},
	{"do I find where my hair is", "find", 3},
	{"do I click on an object", "touch", 3},
	{"do I offer somebody a teleport to where I am", "lure", 3},
	{"do I link several prims together", "link", 3},
	{"do I find which link number each prim of an object has", "links", 3},
	{"do I change the colour of one face of an object", "texture", 3},
	{"do I let other people copy my object", "perms", 3},
	{"do I log another avatar in", "login", 3},
	{"do I change a setting", "set", 3},
	{"do I run the commands in a file", "source", 3},
	{"do I look up an LSL function", "lsl", 3},
	{"do I draw a map of the avatars nearby", "map", 3},
	{"do I refuse a teleport somebody offered me", "no", 3},
	{"do I press a button on a dialog", "answer", 3},
	{"do I read somebody's profile", "profile", 3},
	{"do I move an object to a new position", "move", 3},
	{"do I save an object as a json file", "dump", 3},
	{"do I build an object from a json file", "rez", 3},
	{"do I change my maturity rating to adult", "maturity", 3},
	{"do I walk across the border into the next region", "neighbours", 3},
	{"do I open a real viewer on this avatar", "viewer", 3},
	{"do I find what is waiting for me to answer", "waiting", 3},
	{"do I walk over to that spot", "walk", 3},
	{"do I turn to face somebody", "face", 3},
	{"do I stop walking", "halt", 3},

	// A viewer's words, including the questions the measured set used
	// to miss: "sitting" must not be the sit command, the newest
	// items must not be the get command, a UUID is a key, and
	// "come over" is a lure.
	{"do I send an IM to somebody", "im", 3},
	{"do I accept a teleport offer", "answer", 8},
	{"do I read my group notices", "notice", 3},
	{"do I see who shows up on the minimap", "map", 3},
	{"do I find what sim I am in", "where", 3},
	{"do I find a sim on the world map", "regions", 3},
	{"do I show the contents of that object", "ls", 3},
	{"do I attach a HUD from my inventory", "wear", 3},
	{"do I set home to where I am standing", "landmark", 3},
	{"do I find what is inside that box sitting on the floor", "ls", 8},
	{"do I find which items I got most recently", "ls", 8},
	{"do I find the uuid of an avatar whose name I know", "lookup", 8},
	{"do I tell my friend where I am so they can come over", "lure", 3},
	{"do I take a copy of an object and leave it standing", "take", 3},
	{"do I accept an inventory offer", "accept", 3},
	{"do I see who is on the radar", "map", 3},
	{"do I find out whether I am allowed to build on this land", "parcel", 3},
	{"do I find what group title I am using", "group", 3},
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
		// What was typed starts with the command's own word.
		asked := howQuestion(strings.Fields(c.q))
		got := ix.SearchCommands(asked)
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
		t.Logf("%-55q %d  %s", asked, rank+1, strings.Join(top[:min(3, len(top))], ", "))
		if rank < 0 || rank >= c.top {
			t.Errorf("%q: %s is at %d, wanted in the first %d; the first eight are %s",
				asked, c.want, rank+1, c.top, strings.Join(top, ", "))
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
		if strings.HasPrefix(strings.ToLower(c.q), "how ") {
			t.Errorf("%q starts with how; the command puts that word back", c.q)
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
