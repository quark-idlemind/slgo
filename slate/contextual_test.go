package slate

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// formerKeywords is every word the lexer once reserved. None is reserved
// now: each has its meaning only where the grammar expects that word.
var formerKeywords = strings.Fields(`
slate timeout allow pay object is probe listen
touch anywhere link face at button text pattern symbol image box circle oval
drag from to over say on as tester owner of avatar anyone
public debug direct sit stand choose answer send num key
expect no within then dialog textbox texture offset repeats rotation
click give rez name description heard by reason only null
all others children this root
buy play open media zoom disabled none
matching becomes changes original test before after each sequence do
`)

func TestEveryFormerKeywordIsAName(t *testing.T) {
	if len(formerKeywords) != 85 {
		t.Fatalf("%d words", len(formerKeywords))
	}
	for _, w := range formerKeywords {
		// An object binding, a probe, a sequence name and a do target,
		// then the binding as the target of a stimulus and of each
		// expectation that names an object.
		mustCheck(t, "slate 1\nobject "+w+" is \"O\"\nprobe "+w+"\n"+
			"sequence "+w+" { touch "+w+" anywhere }\n"+
			"test \"t\" {\n"+
			"  do "+w+"\n"+
			"  touch "+w+" button text \"B\"\n"+
			"  expect say \"x\" on public from object "+w+"\n"+
			"  expect say \"x\" on public from owner of "+w+"\n"+
			"  expect dialog from "+w+" text \"T\" button \"b\"\n"+
			"  expect texture "+w+" face 0 changes\n"+
			"  expect click "+w+" changes\n"+
			"  expect give \"I\" from "+w+"\n"+
			"  choose \"a\" on "+w+"\n"+
			"  sit "+w+"\n"+
			"}\n")
		// A rez name is bound with as, and used after.
		mustCheck(t, "slate 1\nobject o is \"O\"\n"+
			"expect rez name \"R\" from o as "+w+"\n"+
			"touch "+w+" anywhere\n")
		// The same words as the name of a plain step's target.
		mustCheck(t, "slate 1\nobject "+w+" is \"O\"\ntouch "+w+" anywhere\n")
	}
}

func TestAwkwardPositionsForWords(t *testing.T) {
	for _, src := range []string{
		"slate 1\nobject then is \"T\"\ntouch then anywhere\n",
		"slate 1\nobject button is \"B\"\ntouch button anywhere\n",
		"slate 1\nobject button is \"B\"\ntouch button button text \"x\"\n",
		"slate 1\nobject button is \"B\"\nchoose \"a\" on button\n",
		"slate 1\nobject stand is \"S\"\nsit stand\n",
		"slate 1\nobject stand is \"S\"\nsit stand\nstand\n",
		"slate 1\nobject open is \"O\"\nexpect say \"x\" on public from object open\n",
		"slate 1\nobject open is \"O\"\nexpect rez name \"R\" from open as box\ntouch box anywhere\n",
		"slate 1\nobject open is \"O\"\nexpect rez name \"R\" from open as as\ntouch as anywhere\n",
		"slate 1\nobject from is \"F\"\nexpect give \"I\" from from\n",
		"slate 1\nobject is is \"I\"\nexpect click is is touch\n",
		"slate 1\nobject test is \"T\"\nsequence sequence { touch test anywhere }\ntest \"t\" { do sequence }\n",
		"slate 1\nobject o is \"O\"\nsequence then { touch o anywhere }\ntest \"t\" { do then do then }\n",
		"slate 1\nobject do is \"D\"\nsequence expect { touch do anywhere }\ntest \"t\" { do expect then expect say \"x\" on public from object do }\n",
	} {
		mustCheck(t, src)
	}
	// A word that has a meaning in a grammar position still has it there.
	parseErr(t, "slate 1\nobject a is \"A\"\ntouch a\n", "expected anywhere, link, face, button, or showing")
	parseErr(t, "slate 1\nobject a is \"A\"\ntouch\n", "expected a name")
	parseErr(t, "slate 1\nobject is \"A\"\ntouch a anywhere\n", "expected is")
}

// Every worked example in the language reference that starts with slate 1
// parses and passes the static checks.
func TestLanguageReferenceExamples(t *testing.T) {
	doc, err := os.ReadFile("../doc/slate-language.md")
	if err != nil {
		t.Fatalf("../doc/slate-language.md is tracked and must be readable: %v", err)
	}
	blocks := regexp.MustCompile("(?s)```slate\\n(.*?)```").FindAllStringSubmatch(string(doc), -1)
	n := 0
	for _, m := range blocks {
		if !strings.HasPrefix(m[1], "slate 1") {
			continue
		}
		n++
		s, err := Parse("doc.slate", []byte(m[1]))
		if err == nil {
			err = Check(s)
		}
		if err != nil {
			t.Errorf("example %d: %v\n%s", n, err, m[1])
		}
	}
	if n == 0 {
		t.Fatal("no examples found")
	}
}
