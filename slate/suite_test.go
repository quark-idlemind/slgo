package slate

import (
	"strings"
	"testing"
)

// Two signed ids, made with tools/new-id.
const (
	idOne = "23cd7e57-7e57-c0de-27fa-fbc0b66837de"
	idTwo = "4dfd7e57-7e57-c0de-1d51-fba1dc4bfc1a"
)

// suiteHdr declares a with a probe, b without, and a listen on 5.
const suiteHdr = "slate 1\nobject a is \"Example A\"\nobject b is \"Example B\"\nprobe a\nlisten 5\n"

// errPos checks that src parses, fails Check, and reports want at line:col.
func errPos(t *testing.T, src string, line, col int, want string) {
	t.Helper()
	s := mustParse(t, src)
	err := Check(s)
	if err == nil {
		t.Fatalf("check succeeded, want %q\n%s", want, src)
	}
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("got %T", err)
	}
	if pe.Line != line || pe.Column != col || !strings.Contains(pe.Msg, want) {
		t.Fatalf("got %d:%d %q\nwant %d:%d %q", pe.Line, pe.Column, pe.Msg, line, col, want)
	}
}

const fullSuite = suiteHdr + `
before each {
  touch a anywhere
  expect say "ready" on public from object a
}

sequence opener {
  touch a button text "Open"
  expect texture a face 0 is ` + idOne + `
}

sequence both {
  do opener
  touch a anywhere
}

test "first" {
  do both
  then
  expect no say "error" on public from object a within 1s
}

after each {
  stand
}

test "second" {
  touch a anywhere
}
`

func TestSuiteParses(t *testing.T) {
	s := mustCheck(t, fullSuite)
	if s.Tests[0].Implicit || len(s.Tests) != 2 || s.Tests[0].Name != "first" || s.Tests[1].Name != "second" {
		t.Fatalf("tests %+v", s.Tests)
	}
	if len(s.Befores) != 1 || len(s.Afters) != 1 || s.Before() == nil || s.After() == nil {
		t.Fatal("before/after missing")
	}
	if len(s.Before().Steps) != 1 || len(s.After().Steps) != 1 {
		t.Fatal("block steps")
	}
	if len(s.Sequences) != 2 || s.Sequence("both") == nil || s.Sequence("nope") != nil {
		t.Fatal("sequences")
	}
	if got := s.Source(s.Sequences[0].Name.Span); got != "opener" {
		t.Fatalf("sequence name %q", got)
	}
	d := s.Tests[0].Steps[0]
	if d.Do == nil || d.Do.Name.Text != "both" || d.Stimulus != nil || len(d.Expect) != 0 {
		t.Fatalf("do step %+v", d)
	}
	if got := s.Source(d.Span); got != "do both" {
		t.Fatalf("do source %q", got)
	}
	if !s.Tests[0].Steps[1].Then {
		t.Fatal("then after do")
	}
	if got := s.Source(s.Tests[1].NameSpan); got != `"second"` {
		t.Fatalf("name span %q", got)
	}
	if (*Script)(nil).Before() != nil || (*Script)(nil).After() != nil {
		t.Fatal("nil script")
	}
}

func TestTopLevelItemsComeInAnyOrder(t *testing.T) {
	mustCheck(t, suiteHdr+`
after each { stand }
test "t" { touch a anywhere }
sequence s { stand }
before each { do s }
`)
}

func TestImplicitTestIsNamedAfterTheFile(t *testing.T) {
	cases := map[string]string{
		"toggle.slate":      "toggle",
		"dir/sub/two.slate": "two",
		"noext":             "noext",
		"a.slate.slate":     "a.slate",
	}
	for file, want := range cases {
		s, err := Parse(file, []byte("slate 1\nobject a is \"Example A\"\ntouch a anywhere\n"))
		if err != nil {
			t.Fatal(err)
		}
		if len(s.Tests) != 1 || !s.Tests[0].Implicit || s.Tests[0].Name != want {
			t.Fatalf("%s -> %+v", file, s.Tests)
		}
		if len(s.Befores)+len(s.Afters)+len(s.Sequences) != 0 {
			t.Fatal("a plain file has no blocks")
		}
		if err := Check(s); err != nil {
			t.Fatal(err)
		}
	}
	s := mustParse(t, fullSuite)
	if s.Tests[0].Implicit {
		t.Fatal("a named test is not implicit")
	}
}

func TestMixingPlainStepsAndBlocksIsAParseError(t *testing.T) {
	const want = "a file is either plain steps or test blocks, not both"
	parseErr(t, suiteHdr+"touch a anywhere\ntest \"t\" { touch a anywhere }\n", want)
	parseErr(t, suiteHdr+"touch a anywhere\nbefore each { stand }\n", want)
	parseErr(t, suiteHdr+"touch a anywhere\nsequence s { stand }\n", want)
	parseErr(t, suiteHdr+"touch a anywhere\nafter each { stand }\n", want)
	parseErr(t, suiteHdr+"test \"t\" { touch a anywhere }\ntouch a anywhere\n", want)
	parseErr(t, suiteHdr+"test \"t\" { touch a anywhere }\nexpect say \"x\" on public from tester\n", want)
	parseErr(t, suiteHdr+"test \"t\" { touch a anywhere }\ndo s\n", want)
	// The error is at the first token that breaks the form.
	_, err := Parse("t.slate", []byte("slate 1\nobject a is \"A\"\ntouch a anywhere\ntest \"t\" { stand }\n"))
	if pe, ok := err.(*Error); !ok || pe.Line != 4 || pe.Column != 1 {
		t.Fatalf("%v", err)
	}
}

func TestBlockAndSuiteParseErrors(t *testing.T) {
	h := "slate 1\nobject a is \"A\"\n"
	cases := []struct{ src, want string }{
		{h + "test \"t\"\n", "expected {"},
		{h + "test \"t\" touch a anywhere\n", "expected {"},
		{h + "test t { stand }\n", "expected a string"},
		{h + "test \"t\" { }\n", "a block needs a step"},
		{h + "test \"t\" { stand\n", "expected a stimulus, expect, then, or do"},
		{h + "test \"t\" { stand }\n}\n", "expected test, before each, after each, or sequence"},
		{h + "test \"t\" { stand test \"u\" { stand } }\n", "a block cannot hold test"},
		{h + "test \"t\" { stand sequence s { stand } }\n", "a block cannot hold sequence"},
		{h + "test \"t\" { test \"u\" { stand } }\n", "expected a stimulus, expect, then, or do"},
		{h + "before { stand }\n", "expected each"},
		{h + "after each stand\n", "expected {"},
		{h + "sequence \"s\" { stand }\n", "expected a name"},
		{h + "sequence s { stand }\nobject b is \"B\"\n", "headers go before the first step or block"},
		{h + "test \"t\" { stand } object b is \"B\"\n", "headers go before the first step or block"},
		{h + "touch a anywhere\n}\n", "expected a stimulus, expect, then, or do, found }"},
		{h + "do\n", "expected a name"},
		{h + "test \"t\" { do }\n", "expected a name"},
		{h, "a file needs a step or a test"},
		{h + "touch a anywhere\nbefore\n", "a file is either plain steps or test blocks"},
	}
	for _, tc := range cases {
		parseErr(t, tc.src, tc.want)
	}
}

func TestAMisspelledStimulusNamesWhatCouldFollow(t *testing.T) {
	parseErr(t, "slate 1\nobject a is \"A\"\ntouch a anywhere\ntuch a anywhere\n",
		"expected a stimulus, expect, then, or do, found tuch")
	parseErr(t, "slate 1\nobject a is \"A\"\ntouch a anywhere\nexpect say \"x\" on public from object a\nfoo\n",
		"expected a stimulus, expect, then, or do, found foo")
	parseErr(t, "slate 1\nobject a is \"A\"\ntest \"t\" { touch a anywhere\n tuch a anywhere }\n",
		"expected a stimulus, expect, then, or do (or } to close the block), found tuch")
	// A stimulus word where only an expectation fits.
	parseErr(t, "slate 1\nobject a is \"A\"\nexpect touch\n", "expected an expectation")
}

func TestDoInAPlainFile(t *testing.T) {
	s := mustParse(t, "slate 1\nobject a is \"A\"\ndo x\ntouch a anywhere\ndo y\nthen\nexpect say \"x\" on public from tester\n")
	st := body(s)
	if len(st) != 4 || st[0].Do == nil || st[2].Do == nil || !st[3].Then || st[1].Stimulus == nil {
		t.Fatalf("%+v", st)
	}
	// A plain file may call a sequence only if it defines none: it cannot.
	checkErr(t, "slate 1\nobject a is \"A\"\ndo x\n", "do x: there is no sequence of that name")
}

func TestMatchingIsLegalWhereThePlanSaysAndFlagged(t *testing.T) {
	src := suiteHdr + `
test "t" {
  expect say matching "^Thanks, .+!$" on public from object a
  expect dialog from a text matching "^Choose" button "Red"
  expect textbox from a text matching "name"
  expect give matching "Swatch$" from a
  expect rez name matching "^B" description matching "left" from a as left
  expect link on a from link 1 num 1 text matching "^go"
}
test "u" {
  expect say "red" on public from object a
  expect dialog from a text "Choose" button "Red"
  expect textbox from a text "Name?"
  expect give "Example Red Swatch" from a
  expect rez name "B" description "left" from a as left
  expect link on a from link 1 num 1 text "go"
}
`
	s := mustCheck(t, src)
	pat := s.Tests[0].Steps[0].Expect
	if !pat[0].Say.Text.Pattern || pat[0].Say.Text.Value != "^Thanks, .+!$" {
		t.Fatalf("say %+v", pat[0].Say.Text)
	}
	if got := s.Source(pat[0].Say.Text.Span); got != `matching "^Thanks, .+!$"` {
		t.Fatalf("text span %q", got)
	}
	if got := s.Source(pat[0].Say.Text.ValueSpan); got != `"^Thanks, .+!$"` {
		t.Fatalf("value span %q", got)
	}
	if !pat[1].Dialog.Text.Pattern || !pat[2].TextBox.Text.Pattern || !pat[3].Give.Item.Pattern {
		t.Fatal("dialog, textbox or give not a pattern")
	}
	if !pat[4].Rez.Name.Pattern || !pat[4].Rez.Desc.Pattern || !pat[4].Rez.HasDesc || !pat[5].Link.Text.Pattern {
		t.Fatal("rez or link not a pattern")
	}
	lit := s.Tests[1].Steps[0].Expect
	for i, tx := range []Text{lit[0].Say.Text, lit[1].Dialog.Text, lit[2].TextBox.Text, lit[3].Give.Item, lit[4].Rez.Name, lit[4].Rez.Desc, lit[5].Link.Text} {
		if tx.Pattern || tx.Value == "" || s.Source(tx.Span) != s.Source(tx.ValueSpan) {
			t.Fatalf("literal %d: %+v", i, tx)
		}
	}
	// Negative forms take patterns too.
	mustCheck(t, suiteHdr+"expect no say matching \"x\" on public from anyone within 1s\nexpect no give matching \"y\" from a\nexpect no rez name matching \"z\" from a\n")
}

func TestMatchingIsRejectedWhereOnlyALiteralFits(t *testing.T) {
	const want = "matching is not legal here"
	h := suiteHdr
	for _, line := range []string{
		`say matching "x" on 0`,
		`send on a from link 1 to link all num 1 text matching "x"`,
		`answer matching "x" on a`,
		`pay a 5 reason matching "x"`,
		`say "x" on 0 as avatar matching "x"`,
		`touch a button text matching "x"`,
		`touch a button pattern matching "x"`,
		`expect say "x" on public from avatar matching "x"`,
		`expect say matching "x" on public from avatar matching "x"`,
	} {
		parseErr(t, h+line+"\n", want)
	}
	parseErr(t, h+"object c is matching \"x\"\ntouch a anywhere\n", want)
	parseErr(t, "slate 1\nobject a is \"A\"\ntest matching \"x\" { stand }\n", want)
	// Not the text position at all.
	parseErr(t, h+"expect say \"x\" matching on public from tester\n", "expected on")
	parseErr(t, h+"expect rez matching \"x\" from a as r\n", "expected name")
	parseErr(t, h+"expect link on a from link 1 num matching \"1\" text \"x\"\n", "expected an integer")
	parseErr(t, h+"expect dialog from a matching \"x\" button \"y\"\n", "found matching")
	parseErr(t, h+"expect say matching on public from tester\n", "expected a string")
	parseErr(t, h+"expect say matching matching \"x\" on public from tester\n", "matching is not legal here")
}

func TestABadPatternIsAStaticError(t *testing.T) {
	h := suiteHdr
	cases := []struct {
		line string
		col  int
	}{
		{`expect say matching "(" on public from object a`, 21},
		{`expect dialog from a text matching "[" button "x"`, 36},
		{`expect textbox from a text matching "*x"`, 37},
		{`expect give matching "a{2,1}" from a`, 22},
		{`expect rez name matching "(?P<n" from a as r`, 26},
		{`expect rez name "ok" description matching "\\" from a as r`, 43},
		{`expect link on a from link 1 num 1 text matching "(("`, 50},
	}
	for _, tc := range cases {
		s := mustParse(t, h+tc.line+"\n")
		err := Check(s)
		pe, ok := err.(*Error)
		if !ok || !strings.Contains(pe.Msg, "pattern") || pe.Line != 6 {
			t.Fatalf("%s: %v", tc.line, err)
		}
		if pe.Column != tc.col {
			t.Fatalf("%s: column %d, want %d (%v)", tc.line, pe.Column, tc.col, err)
		}
	}
	mustCheck(t, h+`expect say matching "(?i)^thanks" on public from object a`+"\n")
	mustCheck(t, h+`expect say matching "" on public from object a`+"\n")
	// A literal that looks like a broken pattern is only text.
	mustCheck(t, h+`expect say "(" on public from object a`+"\n")
}

func TestStateExpectationsTakeIsBecomesChangesAndOriginal(t *testing.T) {
	src := suiteHdr + `
expect texture b face 0 is ` + idOne + `
expect texture b face 1 becomes ` + idTwo + `
expect texture b face 2 changes
expect texture b face 3 is original
expect texture b face 4 becomes original
expect offset b face 0 is 0.25 0
expect offset b face 0 becomes 0 -0.5
expect offset b face 0 changes
expect offset b face 0 is original
expect repeats b face 0 becomes 2 1
expect repeats b face 0 changes
expect repeats b face 0 becomes original
expect rotation b face 0 is 0.25
expect rotation b face 0 becomes 0.5
expect rotation b face 0 changes
expect rotation b face 0 becomes original
expect click b is sit
expect click b becomes none
expect click b changes
expect click b is original
expect no texture b face 5 changes within 2s
expect no click b becomes zoom within 2s
`
	s := mustCheck(t, src)
	ex := body(s)[0].Expect
	kinds := []struct {
		kind StateKind
		orig bool
	}{
		{StateIs, false}, {StateBecomes, false}, {StateChanges, false}, {StateIs, true}, {StateBecomes, true},
		{StateIs, false}, {StateBecomes, false}, {StateChanges, false}, {StateIs, true},
		{StateBecomes, false}, {StateChanges, false}, {StateBecomes, true},
		{StateIs, false}, {StateBecomes, false}, {StateChanges, false}, {StateBecomes, true},
		{StateIs, false}, {StateBecomes, false}, {StateChanges, false}, {StateIs, true},
	}
	var states []State
	for _, e := range ex {
		switch {
		case e.Texture != nil:
			states = append(states, e.Texture.State)
		case e.Offset != nil:
			states = append(states, e.Offset.State)
		case e.Repeats != nil:
			states = append(states, e.Repeats.State)
		case e.Rot != nil:
			states = append(states, e.Rot.State)
		case e.Click != nil:
			states = append(states, e.Click.State)
		}
	}
	if len(states) != len(kinds)+2 {
		t.Fatalf("%d states", len(states))
	}
	for i, k := range kinds {
		if states[i].Kind != k.kind || states[i].Original != k.orig {
			t.Fatalf("state %d: %+v, want %+v", i, states[i], k)
		}
	}
	if ex[1].Texture.ID != idTwo || ex[2].Texture.ID != "" || ex[3].Texture.ID != "" {
		t.Fatal("texture ids")
	}
	if got := s.Source(states[1].Span); got != "becomes" {
		t.Fatalf("state span %q", got)
	}
	if ex[6].Offset.S.Value != 0 || ex[6].Offset.T.Value != -0.5 || ex[7].Offset.S.Value != 0 {
		t.Fatal("offset values")
	}
	if ex[17].Click.Action != "none" || ex[18].Click.Action != "" || ex[19].Click.Action != "" {
		t.Fatal("click actions")
	}
	last := ex[len(ex)-2]
	if !last.Neg || last.Texture.State.Kind != StateChanges || last.Within == nil {
		t.Fatalf("negative changes %+v", last)
	}
	// A changes expectation does not swallow the stimulus that follows.
	s = mustCheck(t, suiteHdr+"expect click b changes\ntouch a anywhere\nexpect texture b face 0 changes\nsit b\n")
	if len(body(s)) != 3 {
		t.Fatalf("%d steps", len(body(s)))
	}
}

func TestStateWordsAreRejectedOnOtherExpectations(t *testing.T) {
	h := suiteHdr
	for _, line := range []string{
		`expect say "x" on public from tester becomes`,
		`expect say "x" on public from tester changes`,
		`expect say "x" on public from tester original`,
		`expect say becomes "x" on public from tester`,
		`expect dialog from a text "x" button "y" changes`,
		`expect textbox from a text "x" becomes "y"`,
		`expect give "x" from a changes`,
		`expect give original from a`,
		`expect rez name original from a as r`,
		`expect link on a from link 1 num 1 text "x" becomes`,
		`expect no say "x" on public from tester original`,
		`expect becomes`,
		`expect changes`,
		`expect original`,
	} {
		_, err := Parse("t.slate", []byte(h+line+"\n"))
		if err == nil {
			t.Fatalf("accepted %s", line)
		}
	}
	parseErr(t, h+`expect say "x" on public from tester becomes`+"\n", "expected a stimulus, expect, then, or do, found becomes")
	parseErr(t, h+"expect texture b face 0 changes "+idOne+"\n", "changes takes no value")
	parseErr(t, h+"expect offset b face 0 changes 1 2\n", "changes takes no value")
	parseErr(t, h+"expect rotation b face 0 changes 0.5\n", "changes takes no value")
	parseErr(t, h+"expect click b changes original\n", "changes takes no value")
	parseErr(t, h+"expect texture b face 0 becomes\n", "expected a UUID or original")
	parseErr(t, h+"expect texture b face 0 is 5\n", "expected a UUID or original")
	parseErr(t, h+"expect click b becomes\n", "expected a click action or original")
	parseErr(t, h+"expect click b is 3\n", "expected a click action or original")
	parseErr(t, h+"expect offset b face 0 becomes original 1\n", "expected a stimulus, expect, then, or do")
	parseErr(t, h+"expect offset b face 0 becomes 1\n", "expected a number")
	parseErr(t, h+"expect rotation b face 0 becomes\n", "expected a number")
	parseErr(t, h+"expect texture b face 0 flips "+idOne+"\n", "expected is, becomes, or changes")
	parseErr(t, h+"expect click b original\n", "expected is, becomes, or changes")
	// original is a stimulus-side word nowhere.
	parseErr(t, h+"say original on 0\n", "expected a string")
	mustParse(t, h+"object original is \"x\"\ntouch original anywhere\n")
}

func TestOffsetAndRotationRangesStillApplyToLiterals(t *testing.T) {
	checkErr(t, suiteHdr+"expect offset b face 0 becomes 1.5 0\n", "outside -1 to 1")
	checkErr(t, suiteHdr+"expect rotation b face 0 becomes 2\n", "outside -1 to 1")
	mustCheck(t, suiteHdr+"expect offset b face 0 changes\nexpect rotation b face 0 is original\nexpect repeats b face 0 becomes 9 9\n")
}

func TestLinkOnStateExpectationsAndSpeakers(t *testing.T) {
	src := suiteHdr + `
expect texture a link 2 face 0 is ` + idOne + `
expect offset a link 0 face 0 becomes 0.5 0.5
expect repeats a link 1 face 0 changes
expect rotation a link 3 face 0 is original
expect click a link 4 changes
expect dialog from a link 2 text "x" button "y"
expect textbox from a link 5 text matching "z"
expect say "x" on public from object a link 2
expect say "x" on public from object a
expect say "x" on 5 from object a link 0
touch a link 1 button text "Go"
drag a link 2 face 0 from 0.1 0.5 to 0.9 0.5
`
	s := mustCheck(t, src)
	ex := body(s)[0].Expect
	links := []*Int{ex[0].Texture.Link, ex[1].Offset.Link, ex[2].Repeats.Link, ex[3].Rot.Link, ex[4].Click.Link,
		ex[5].Dialog.Link, ex[6].TextBox.Link, ex[7].Say.From.Link, ex[8].Say.From.Link, ex[9].Say.From.Link}
	want := []int64{2, 0, 1, 3, 4, 2, 5, 2, -1, 0}
	for i, w := range want {
		if w < 0 {
			if links[i] != nil {
				t.Fatalf("expectation %d has a link", i)
			}
			continue
		}
		if links[i] == nil || links[i].Value != w {
			t.Fatalf("expectation %d link %v, want %d", i, links[i], w)
		}
	}
	if got := s.Source(ex[7].Say.From.Span); got != "object a link 2" {
		t.Fatalf("speaker span %q", got)
	}
	if ex[7].Say.From.Kind != SpeakObject || ex[7].Say.From.Name.Text != "a" {
		t.Fatal("speaker")
	}
}

// b has no probe, and link N on it is still accepted: the store numbers it.
func TestLinkNeedsNoProbe(t *testing.T) {
	h := suiteHdr
	for _, line := range []string{
		`expect texture b link 2 face 0 is ` + idOne,
		`expect offset b link 2 face 0 changes`,
		`expect repeats b link 1 face 0 becomes 1 1`,
		`expect rotation b link 2 face 0 is original`,
		`expect click b link 2 is sit`,
		`expect dialog from b link 2 text "x" button "y"`,
		`expect textbox from b link 2 text "x"`,
		`expect say "x" on public from object b link 2`,
		`touch b link 2`,
		`drag b link 2 face 0 from 0.1 0.5 to 0.9 0.5`,
	} {
		mustCheck(t, h+line+"\n")
	}
	// A name bound with as has no probe either.
	mustCheck(t, h+"expect rez name \"R\" from a as r\ntouch a anywhere\nexpect click r link 1 is sit\n")
}

func TestLinkIsRangeCheckedWhateverTheProbe(t *testing.T) {
	h := suiteHdr
	for _, tc := range []struct{ line, want string }{
		{`expect texture a link -1 face 0 is ` + idOne, "link -1 is below 0"},
		{`expect click a link -3 is sit`, "link -3 is below 0"},
		{`expect dialog from a link -1 text "x" button "y"`, "link -1 is below 0"},
		{`expect say "x" on public from object a link -1`, "link -1 is below 0"},
		{`expect click a link 2147483648 is sit`, "outside -2147483648 to 2147483647"},
		{`touch a link -1`, "link -1 is below 0"},
		{`drag a link -1 face 0 from 0.1 0.5 to 0.9 0.5`, "link -1 is below 0"},
	} {
		checkErr(t, h+tc.line+"\n", tc.want)
	}
	// The error column is the number.
	errPos(t, h+"expect click b link -7 is sit\n", 6, 21, "is below 0")
}

func TestExpandListsStepsInRunOrder(t *testing.T) {
	s := mustCheck(t, fullSuite)
	tests, err := s.Expand()
	if err != nil {
		t.Fatal(err)
	}
	if len(tests) != 2 || tests[0].Test.Name != "first" {
		t.Fatalf("%d tests", len(tests))
	}
	first := tests[0].Steps
	// before each (1), both -> opener (1) + touch (1), then-step (1), after each (1)
	if len(first) != 5 {
		t.Fatalf("%d steps", len(first))
	}
	phases := []Phase{PhaseBefore, PhaseBody, PhaseBody, PhaseBody, PhaseAfter}
	seqs := []string{"", "opener", "both", "", ""}
	vias := []string{"", "via do opener at line 18 via do both at line 23", "via do both at line 23", "", ""}
	for i := range first {
		if first[i].Phase != phases[i] || first[i].Sequence != seqs[i] || Via(first[i].Via) != vias[i] {
			t.Fatalf("step %d: phase %s seq %q via %q", i+1, first[i].Phase, first[i].Sequence, Via(first[i].Via))
		}
		if first[i].Step.Do != nil {
			t.Fatalf("step %d is a do", i+1)
		}
	}
	// The spans are where the steps are written.
	if got := s.Source(first[1].Step.Span); !strings.HasPrefix(got, "touch a button") {
		t.Fatalf("source %q", got)
	}
	if first[1].Via[1].Span.Line != 18 || first[1].Via[0].Name != "both" {
		t.Fatalf("via %+v", first[1].Via)
	}
	second := tests[1].Steps
	if len(second) != 3 || second[0].Phase != PhaseBefore || second[1].Phase != PhaseBody || second[2].Phase != PhaseAfter {
		t.Fatalf("second %+v", second)
	}
	for _, p := range []Phase{PhaseBefore, PhaseBody, PhaseAfter} {
		if p.String() == "" {
			t.Fatal("phase name")
		}
	}
	if (PhaseBefore).String() != "before each" || (PhaseAfter).String() != "after each" || (PhaseBody).String() != "test" {
		t.Fatal("phase names")
	}
}

func TestExpandOfAPlainFile(t *testing.T) {
	s := mustCheck(t, "slate 1\nobject a is \"A\"\ntouch a anywhere\nexpect say \"x\" on public from object a\nthen\nexpect say \"y\" on public from object a\n")
	tests, err := s.Expand()
	if err != nil || len(tests) != 1 || len(tests[0].Steps) != 2 || !tests[0].Test.Implicit {
		t.Fatalf("%v %+v", err, tests)
	}
	if _, err := (*Script)(nil).Expand(); err == nil {
		t.Fatal("nil script expanded")
	}
}

func TestExpandFailsOnACycleWithoutCheck(t *testing.T) {
	s := mustParse(t, suiteHdr+"sequence x { do x }\ntest \"t\" { do x }\n")
	if _, err := s.Expand(); err == nil || !strings.Contains(err.Error(), "sequence cycle: x -> x") {
		t.Fatalf("%v", err)
	}
}

func TestNestedSequences(t *testing.T) {
	s := mustCheck(t, suiteHdr+`
sequence inner { touch a anywhere }
sequence middle {
  do inner
  do inner
}
sequence outer { do middle }
test "t" {
  do outer
  touch a anywhere
}
`)
	tests, _ := s.Expand()
	st := tests[0].Steps
	if len(st) != 3 {
		t.Fatalf("%d steps", len(st))
	}
	if got := Via(st[0].Via); got != "via do inner at line 9 via do middle at line 12 via do outer at line 14" {
		t.Fatalf("via %q", got)
	}
	if got := Via(st[1].Via); got != "via do inner at line 10 via do middle at line 12 via do outer at line 14" {
		t.Fatalf("via %q", got)
	}
	if len(st[2].Via) != 0 || st[2].Sequence != "" {
		t.Fatalf("direct step %+v", st[2])
	}
}

func TestSequenceCycles(t *testing.T) {
	errPos(t, suiteHdr+"sequence x { do x }\ntest \"t\" { stand }\n", 6, 14, "sequence cycle: x -> x")
	checkErr(t, suiteHdr+`
sequence a { do b }
sequence b { touch a anywhere
 do c }
sequence c { do a }
test "t" { do a }
`, "sequence cycle: a -> b -> c -> a")
	// A cycle in a sequence no test calls is still an error.
	checkErr(t, suiteHdr+"sequence p { do q }\nsequence q { do p }\ntest \"t\" { stand }\n", "sequence cycle")
	// Calling a sequence twice is not a cycle.
	mustCheck(t, suiteHdr+"sequence s { stand }\ntest \"t\" { do s\n do s }\n")
	// A diamond is not a cycle.
	mustCheck(t, suiteHdr+"sequence l { stand }\nsequence m1 { do l }\nsequence m2 { do l }\ntest \"t\" { do m1\n do m2 }\n")
}

func TestUndefinedDo(t *testing.T) {
	errPos(t, suiteHdr+"test \"t\" {\n  do missing\n}\n", 7, 6, "do missing: there is no sequence of that name")
	checkErr(t, suiteHdr+"before each { do missing }\ntest \"t\" { stand }\n", "do missing")
	checkErr(t, suiteHdr+"after each { do missing }\ntest \"t\" { stand }\n", "do missing")
	checkErr(t, suiteHdr+"sequence s { do missing }\ntest \"t\" { stand }\n", "do missing")
	// A sequence may be defined after its use.
	mustCheck(t, suiteHdr+"test \"t\" { do late }\nsequence late { stand }\n")
}

func TestTestAndSequenceNames(t *testing.T) {
	errPos(t, suiteHdr+"test \"t\" { stand }\ntest \"t\" { stand }\n", 7, 6, `test "t" is already defined at line 6`)
	checkErr(t, suiteHdr+"test \"\" { stand }\n", "a test name is empty")
	checkErr(t, suiteHdr+"before each { stand }\n", "a file needs at least one test")
	checkErr(t, suiteHdr+"sequence s { stand }\n", "a file needs at least one test")
	checkErr(t, suiteHdr+"before each { stand }\nbefore each { stand }\ntest \"t\" { stand }\n", "before each appears more than once")
	checkErr(t, suiteHdr+"after each { stand }\nafter each { stand }\ntest \"t\" { stand }\n", "after each appears more than once")
	errPos(t, suiteHdr+"sequence s { stand }\nsequence s { stand }\ntest \"t\" { stand }\n", 7, 10, "sequence s is already defined at line 6")
	mustCheck(t, suiteHdr+"test \"t\" { stand }\ntest \"T\" { stand }\ntest \"t \" { stand }\n")
	if err := Check(&Script{}); err == nil {
		t.Fatal("an empty script was accepted")
	}
}

func TestBaseChecksApplyToEveryExpandedStep(t *testing.T) {
	checkErr(t, suiteHdr+"test \"t\" { touch missing anywhere }\n", "missing is not an object")
	checkErr(t, suiteHdr+"before each { touch a face 0 at 0 0 }\ntest \"t\" { stand }\n", "middle of the face")
	checkErr(t, suiteHdr+"after each { pay a 5 }\ntest \"t\" { stand }\n", "pay needs allow pay")
	checkErr(t, suiteHdr+"test \"t\" { touch a link 1 }\ntest \"u\" { send on b from link 1 to link all num 1 text \"x\" }\n", "send on b needs a probe")
	checkErr(t, suiteHdr+"test \"t\" { expect say \"x\" on 9 from tester }\n", "channel 9 needs a listen")
}

func TestErrorInASequenceNamesTheTestAndTheDo(t *testing.T) {
	src := suiteHdr + `
sequence bad {
  touch nobody anywhere
}
sequence wrap {
  stand
  do bad
}
test "first" { stand }
test "second" {
  stand
  do wrap
}
`
	s := mustParse(t, src)
	err := Check(s)
	pe, ok := err.(*Error)
	if !ok {
		t.Fatalf("%v", err)
	}
	// The position is in the sequence, where the bad step is written.
	if pe.Line != 8 || pe.Column != 9 {
		t.Fatalf("position %d:%d", pe.Line, pe.Column)
	}
	for _, want := range []string{"nobody is not an object", `in test "second"`, "via do bad at line 12 via do wrap at line 17"} {
		if !strings.Contains(pe.Msg, want) {
			t.Fatalf("message %q lacks %q", pe.Msg, want)
		}
	}
	if !strings.HasPrefix(pe.Error(), "t.slate:8:9: ") {
		t.Fatalf("%s", pe.Error())
	}
	// An error written directly in a test names no do.
	err = Check(mustParse(t, suiteHdr+"test \"t\" { touch nobody anywhere }\n"))
	if strings.Contains(err.Error(), "via") {
		t.Fatalf("%v", err)
	}
}

func TestAsScopingAcrossBeforeBodyAndAfter(t *testing.T) {
	const rez = `expect rez name "R" from a as `
	// before each binds: visible in the body and in after each.
	mustCheck(t, suiteHdr+`
before each { `+rez+`left }
test "t" { touch left anywhere }
after each { touch left anywhere }
`)
	// The body binds: visible later in the body, not in after each.
	mustCheck(t, suiteHdr+`
test "t" {
  `+rez+`right
  touch right anywhere
}
`)
	checkErr(t, suiteHdr+`
test "t" { `+rez+`right }
after each { touch right anywhere }
`, `right is bound in test "t", which after each cannot rely on`)
	// A name bound in a step is usable only in later steps.
	checkErr(t, suiteHdr+"test \"t\" { "+rez+"r\n expect click r is sit }\n", "r is bound in this step")
	checkErr(t, suiteHdr+"test \"t\" { touch r anywhere\n stand\n "+rez+"r }\n", "r is not an object")
	// Names are unique within one expanded test.
	checkErr(t, suiteHdr+"before each { "+rez+"r }\ntest \"t\" { "+rez+"r }\n", "r is already bound")
	checkErr(t, suiteHdr+"test \"t\" { "+rez+"r }\nafter each { "+rez+"r }\n", "r is already bound")
	checkErr(t, suiteHdr+"test \"t\" { "+rez+"a }\n", "a is already bound")
	// ... but the same name in two tests is fine: each run is its own scope.
	mustCheck(t, suiteHdr+"test \"t\" { "+rez+"r\n touch r anywhere }\ntest \"u\" { "+rez+"r\n touch r anywhere }\n")
	// A body name does not leak into the next test either.
	checkErr(t, suiteHdr+"test \"t\" { "+rez+"r }\ntest \"u\" { touch r anywhere }\n", "r is not an object")
	// after each may bind and use its own names.
	mustCheck(t, suiteHdr+"test \"t\" { stand }\nafter each { "+rez+"z\n touch z anywhere }\n")
	// A before-each name used before it is bound inside before each.
	checkErr(t, suiteHdr+"before each { touch r anywhere\n stand\n "+rez+"r }\ntest \"t\" { stand }\n", "r is not an object")
}

func TestAsNamesThroughASequence(t *testing.T) {
	const rez = `expect rez name "R" from a as `
	// A name a sequence binds is visible to the caller after the do.
	mustCheck(t, suiteHdr+"sequence make { "+rez+"made }\ntest \"t\" { do make\n touch made anywhere }\n")
	// The same step list is not visible before the do.
	checkErr(t, suiteHdr+"sequence make { "+rez+"made }\ntest \"t\" { touch made anywhere\n do make }\n", "made is not an object")
	// A sequence may use a name the caller bound earlier, and fails where it is not.
	src := suiteHdr + "sequence use { touch r anywhere }\ntest \"ok\" { " + rez + "r\n do use }\ntest \"bad\" { do use }\n"
	pe := checkErrAs(t, src, "r is not an object")
	if !strings.Contains(pe.Msg, `in test "bad"`) || pe.Line != 6 {
		t.Fatalf("%d %q", pe.Line, pe.Msg)
	}
	// Calling a binding sequence twice binds the name twice.
	pe = checkErrAs(t, suiteHdr+"sequence make { "+rez+"made }\ntest \"t\" { do make\n do make }\n", "made is already bound")
	if !strings.Contains(pe.Msg, `in test "t"`) || !strings.Contains(pe.Msg, "do make at line 8") {
		t.Fatalf("%q", pe.Msg)
	}
	// A name a before-each sequence binds is visible in the body and after each.
	mustCheck(t, suiteHdr+"sequence make { "+rez+"made }\nbefore each { do make }\ntest \"t\" { touch made anywhere }\nafter each { touch made anywhere }\n")
	// A name a body sequence binds is not visible in after each.
	checkErr(t, suiteHdr+"sequence make { "+rez+"made }\ntest \"t\" { do make }\nafter each { touch made anywhere }\n", "which after each cannot rely on")
	// A name bound in a nested sequence reaches the outer caller.
	mustCheck(t, suiteHdr+"sequence in { "+rez+"deep }\nsequence out { do in }\ntest \"t\" { do out\n touch deep anywhere }\n")
}

func checkErrAs(t *testing.T, src, want string) *Error {
	t.Helper()
	err := Check(mustParse(t, src))
	pe, ok := err.(*Error)
	if !ok || !strings.Contains(pe.Msg, want) {
		t.Fatalf("got %v, want %q\n%s", err, want, src)
	}
	return pe
}

func TestLinkTextAcceptsTabAndNewline(t *testing.T) {
	h := suiteHdr
	mustCheck(t, h+`send on a from link 1 to link all num 1 text "a\tb\nc"`+"\n")
	mustCheck(t, h+`expect link on a from link 1 num 1 text "a\tb\nc"`+"\n")
	// Other control bytes and non-ASCII are still refused in a literal.
	parseErr(t, h+`send on a from link 1 to link all num 1 text "a\rb"`+"\n", "unknown string escape")
	for _, bad := range []string{"café", "\x01", "\x7f", "\x0b"} {
		checkErr(t, h+"expect link on a from link 1 num 1 text \""+bad+"\"\n", "bytes 0x20-0x7E, tab or newline")
		checkErr(t, h+"send on a from link 1 to link all num 1 text \""+bad+"\"\n", "bytes 0x20-0x7E, tab or newline")
	}
	// A pattern has no character rule.
	mustCheck(t, h+"expect link on a from link 1 num 1 text matching \"café|\x01\"\n")
	mustCheck(t, h+"expect link on a from link 1 num 1 text matching \"\x7f\"\n")
	// The tab survives into the command line, quoted.
	if got := relayLine(1, -1, 1, nullKey, "a\tb\nc"); !strings.HasSuffix(got, `"a\tb\nc"`) {
		t.Fatalf("%s", got)
	}
}

func TestParseKeepsSpansOfBlocks(t *testing.T) {
	s := mustParse(t, fullSuite)
	if got := s.Source(s.Before().Span); !strings.HasPrefix(got, "before each {") || !strings.HasSuffix(got, "}") {
		t.Fatalf("before span %q", got)
	}
	if got := s.Source(s.Sequences[1].Span); !strings.HasPrefix(got, "sequence both {") {
		t.Fatalf("sequence span %q", got)
	}
	if got := s.Source(s.Tests[1].Span); got != "test \"second\" {\n  touch a anywhere\n}" {
		t.Fatalf("test span %q", got)
	}
	if s.Tests[0].Span.Line != 22 {
		t.Fatalf("line %d", s.Tests[0].Span.Line)
	}
}

func TestEndOfFileInsideABlock(t *testing.T) {
	parseErr(t, "slate 1\nobject a is \"A\"\ntest \"t\" { touch a anywhere\n", "expected a stimulus, expect, then, or do")
	parseErr(t, "slate 1\nobject a is \"A\"\ntest \"t\" {\n", "expected a stimulus, expect, then, or do")
}

func TestBracesOutsideABlockAreErrors(t *testing.T) {
	parseErr(t, "slate 1\nobject a is \"A\"\n{ touch a anywhere }\n", "expected a step or a test block, found {")
	parseErr(t, "slate 1\nobject a is \"A\"\ntouch a anywhere {\n", "expected a stimulus, expect, then, or do, found {")
}

func TestLexicalBoundaryErrorsReachParse(t *testing.T) {
	h := "slate 1\nobject a is \"A\"\n"
	parseErr(t, "slate 1\ntimeout 10seconds\nobject a is \"A\"\ntouch a anywhere\n", "unknown duration unit")
	parseErr(t, h+"touch a anywhere\nexpect say \"x\" on public from tester within 5min\n", "unknown duration unit")
	parseErr(t, h+"expect texture a face 0 is "+idOne+"xyz\n", "a UUID is followed by more characters")
	parseErr(t, h+"expect texture a face 0 is "+idOne+"0\n", "a UUID is followed by more characters")
	parseErr(t, h+"touch a anywhere\nexpect click a is touch within 10s_\n", "unknown duration unit")
}
