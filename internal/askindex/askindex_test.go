package askindex

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestTokens(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"How do I set my home?", []string{"set", "hom"}},
		// A long flag is kept whole and in parts, so that both the flag
		// and the words in it find the paragraph about it.
		{"landmark --set-home", []string{"landmark", "--set-home", "set", "hom"}},
		{"ls -l Objects", []string{"ls", "-l", "object"}},
		// A dash inside a word divides it; it is not a flag.
		{"a no-copy item", []string{"no", "copy", "item"}},
		// An apostrophe divides too, and the letter left over is dropped.
		{"the grid's own word", []string{"grid", "own", "word"}},
		{"Sitting, rezzed, making, copies", []string{"sit", "rez", "mak", "copy"}},
		{"attachments attached attach", []string{"attach", "attach", "attach"}},
		{"settings", []string{"set"}},
		{"dresses dressed", []string{"dress", "dress"}},
		{"needs status", []string{"need", "status"}},
		// Short words are not stemmed: they are names.
		{"cat tp rez ls im", []string{"cat", "tp", "rez", "ls", "im"}},
		{"", nil},
		{"-- - ---", nil},
	} {
		if got := Tokens(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Tokens(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The same word on both sides has to come out the same, whatever ending
// either side gave it; that is all stemming is for here.
func TestStemMeets(t *testing.T) {
	for _, set := range [][]string{
		{"home", "homes"},
		{"make", "making", "makes"},
		{"take", "taking", "takes"},
		{"move", "moved", "moving", "moves"},
		{"place", "placed", "places"},
		{"sit", "sitting", "sits"},
		{"wear", "wearing", "wears"},
		{"copy", "copies", "copying", "copied"},
		{"attach", "attachment", "attachments", "attached"},
		{"teleport", "teleported", "teleporting", "teleports"},
		{"friend", "friends"},
		{"landmark", "landmarks"},
	} {
		want := stem(set[0])
		for _, w := range set[1:] {
			if got := stem(w); got != want {
				t.Errorf("stem(%q) = %q, stem(%q) = %q; they should meet", w, got, set[0], want)
			}
		}
	}
}

func TestStopword(t *testing.T) {
	for _, w := range []string{"the", "How", "my", "I"} {
		if !Stopword(w) {
			t.Errorf("%q should be a stopword", w)
		}
	}
	for _, w := range []string{"who", "where", "look", "find", "get", "give", "set", "no", "new", "all"} {
		if Stopword(w) {
			t.Errorf("%q is a stopword, and it is a command's name or means something here", w)
		}
	}
}

// A small corpus shaped like the one slsh builds.  Every name in it is
// made up.
func testDocs() []Doc {
	return []Doc{
		{ID: "landmark", Command: "landmark", Kind: KindLine, Text: "landmark [--go] [--set-home] [NAME]\nthe landmarks in inventory, going home or setting it"},
		{ID: "landmark:option:--set-home", Command: "landmark", Kind: KindOption, Heading: "--set-home", Text: "Make where this avatar is standing the place home is."},
		{ID: "landmark:example:0", Command: "landmark", Kind: KindExample, Heading: "Examples", Text: "landmark --set-home"},
		{ID: "cat", Command: "cat", Kind: KindLine, Text: "cat [--in OBJECT] PATH\nprint a notecard or a script"},
		{ID: "cat:intro", Command: "cat", Kind: KindIntro, Text: "`cat` prints a notecard or a script.  With `--in` it reads one from inside a rezzed object."},
		{ID: "who", Command: "who", Kind: KindLine, Text: "who\nwho else is in the region, nearest first"},
		{ID: "give", Command: "give", Kind: KindLine, Text: "give WHO PATH\noffer an inventory item to somebody"},
		{ID: "tp", Command: "tp", Kind: KindLine, Text: "tp [-w SECONDS] REGION|X Y Z|home\nmove to another region by name, or home"},
	}
}

func TestSearchFindsTheCommand(t *testing.T) {
	ix := Build(testDocs())
	for _, c := range []struct{ q, want string }{
		{"how do I set my home", "landmark"},
		{"print a script inside an object", "cat"},
		{"who is nearest", "who"},
		{"offer an item to somebody", "give"},
		{"--set-home", "landmark"},
	} {
		got := ix.SearchCommands(c.q)
		if len(got) == 0 || got[0].Command != c.want {
			t.Errorf("%q: got %v, want %s first", c.q, names(got), c.want)
		}
	}
	if got := ix.Search("nothing here matches xyzzy"); len(got) != 0 {
		t.Errorf("a question with no word in the index found %d documents", len(got))
	}
}

func names(cs []CommandHit) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Command)
	}
	return out
}

func TestSearchMatchedWords(t *testing.T) {
	ix := Build(testDocs())
	hits := ix.Search("set home")
	if len(hits) == 0 {
		t.Fatal("nothing found")
	}
	for _, h := range hits {
		if h.Doc.ID == "landmark:option:--set-home" {
			if !reflect.DeepEqual(h.Matched, []string{"hom", "set"}) {
				t.Errorf("matched %q, want [hom set]", h.Matched)
			}
			return
		}
	}
	t.Error("the --set-home paragraph was not found")
}

// A hint's words count for something, and only when its When words are
// in the question.
func TestHintWords(t *testing.T) {
	ix := Build(testDocs())
	q := "go back to my house"
	if got := ix.SearchCommands(q); len(got) > 0 && got[0].Command == "tp" {
		t.Fatalf("the test is no test: %q already finds tp", q)
	}
	h := Hint{When: []string{"house"}, Words: []string{"home"}}
	got := ix.SearchCommands(q, h)
	if len(got) == 0 {
		t.Fatalf("%q with a hint found nothing", q)
	}
	found := false
	for _, c := range got {
		if c.Command == "tp" || c.Command == "landmark" {
			found = true
		}
	}
	if !found {
		t.Errorf("%q with house->home found %v", q, names(got))
	}
	if got := ix.SearchCommands("go back to my place", h); len(got) > 0 {
		for _, c := range got {
			for _, hit := range c.Hits {
				for _, m := range hit.Matched {
					if m == "hom" {
						t.Errorf("a hint applied without its When word: %v", hit)
					}
				}
			}
		}
	}
}

// A raised command comes first or level with first, and a raised command
// nothing else matched still appears.
func TestHintCommands(t *testing.T) {
	ix := Build(testDocs())
	got := ix.SearchCommands("print a script", Hint{Commands: []string{"give", "no-such-command"}})
	if len(got) < 2 {
		t.Fatalf("got %v", names(got))
	}
	var give *CommandHit
	for i := range got {
		if got[i].Command == "give" {
			give = &got[i]
		}
	}
	if give == nil {
		t.Fatalf("give was raised and does not appear: %v", names(got))
	}
	if !give.Hits[0].Hinted {
		t.Error("give's hit is not marked as hinted")
	}
	if give.Score < got[0].Score/2 {
		t.Errorf("give scored %.2f against the best %.2f", give.Score, got[0].Score)
	}

	// Conditional: not raised when the When word is absent.
	got = ix.SearchCommands("print a script", Hint{When: []string{"friend"}, Commands: []string{"give"}})
	for _, c := range got {
		if c.Command == "give" {
			t.Errorf("give was raised without its When word")
		}
	}
}

func TestGroupOrder(t *testing.T) {
	ix := Build(testDocs())
	got := ix.SearchCommands("home")
	if len(got) < 2 || got[0].Command != "landmark" {
		t.Fatalf("got %v", names(got))
	}
	// landmark has three documents with "home"; its score counts more
	// than the best of them, and its hits are best first.
	lm := got[0]
	if len(lm.Hits) < 2 || lm.Score <= lm.Hits[0].Score {
		t.Errorf("landmark: score %.3f over %d hits, best %.3f", lm.Score, len(lm.Hits), lm.Hits[0].Score)
	}
	for i := 1; i < len(lm.Hits); i++ {
		if lm.Hits[i].Score > lm.Hits[i-1].Score {
			t.Errorf("hits out of order at %d", i)
		}
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	docs := testDocs()
	// Everything escape has to deal with.
	docs = append(docs, Doc{ID: "odd", Command: "odd", Kind: "weird", Heading: "tab\there", Text: "back\\slash\nnew line, café, \x01, \\u{41} literally"})
	ix := Build(docs)
	enc := ix.Encode()
	for i, c := range enc {
		if c >= 0x7f || (c < 0x20 && c != '\n' && c != '\t') {
			t.Fatalf("byte %d of the encoding is %#x, which is not printable ASCII", i, c)
		}
	}
	back, err := Decode(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.docs, ix.docs) {
		t.Errorf("documents differ after a round trip:\n got %q\nwant %q", back.docs, ix.docs)
	}
	if !reflect.DeepEqual(back.postings, ix.postings) {
		t.Error("postings differ after a round trip")
	}
	if !reflect.DeepEqual(back.lens, ix.lens) || back.avg != ix.avg {
		t.Error("lengths differ after a round trip")
	}
	if again := back.Encode(); !bytes.Equal(again, enc) {
		t.Error("encoding a decoded index gave different bytes")
	}
	// And it searches the same.
	a, b := ix.Search("script home"), back.Search("script home")
	if len(a) != len(b) {
		t.Fatalf("%d hits before, %d after", len(a), len(b))
	}
	for i := range a {
		if a[i].Doc.ID != b[i].Doc.ID || a[i].Score != b[i].Score {
			t.Errorf("hit %d: %s %.4f before, %s %.4f after", i, a[i].Doc.ID, a[i].Score, b[i].Doc.ID, b[i].Score)
		}
	}
}

// The bytes depend on nothing but the documents: building twice from
// the same input, with map order free to differ, gives the same bytes.
func TestEncodeDeterministic(t *testing.T) {
	first := Build(testDocs()).Encode()
	for i := 0; i < 20; i++ {
		if again := Build(testDocs()).Encode(); !bytes.Equal(again, first) {
			t.Fatalf("build %d encoded differently", i)
		}
	}
}

func TestDecodeRefuses(t *testing.T) {
	good := string(Build(testDocs()[:2]).Encode())
	for name, bad := range map[string]string{
		"empty":           "",
		"wrong magic":     strings.Replace(good, magic, "askindex 0", 1),
		"short":           good[:len(good)/2],
		"trailing":        good + "more\n",
		"bad posting":     strings.Replace(good, "\t0", "\tx", 1),
		"docs miscounted": strings.Replace(good, "docs 2", "docs 1", 1),
		"bad escape":      strings.Replace(good, "landmark", `land\qmark`, 1),
		"too few field":   strings.Replace(good, "\tlandmark\t", "\t", 1),
	} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Errorf("%s: decoded without complaint", name)
		}
	}
}
