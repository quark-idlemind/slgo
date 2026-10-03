package slate

import (
	"errors"
	"strings"
	"testing"
)

// Wave 1, sections 1 to 4 of the plan: the grammar and the static checks.
// The runner runs captures now; the forms it does not run yet are listed in notyet.go, and the last tests say so.

// posOf is the line and column of the first byte of marker in src, or of
// its last occurrence when marker begins with "$" or "last:": a use of a
// capture comes after the step that binds it.
func posOf(t *testing.T, src, marker string) (line, col int) {
	t.Helper()
	i := strings.Index(src, marker)
	if strings.HasPrefix(marker, "$") || strings.HasPrefix(marker, "last:") {
		marker = strings.TrimPrefix(marker, "last:")
		i = strings.LastIndex(src, marker)
	}
	if i < 0 {
		t.Fatalf("no %q in\n%s", marker, src)
	}
	line, col = 1, 1
	for _, c := range []byte(src[:i]) {
		if c == '\n' {
			line, col = line+1, 1
		} else {
			col++
		}
	}
	return line, col
}

// refuses checks that src parses, that Check refuses it with want, and
// that the error is at the first byte of marker.
func refuses(t *testing.T, src, marker, want string) *Error {
	t.Helper()
	s := mustParse(t, src)
	err := Check(s)
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("check gave %v, want %q\n%s", err, want, src)
	}
	line, col := posOf(t, src, marker)
	if pe.Line != line || pe.Column != col || !strings.Contains(pe.Msg, want) {
		t.Fatalf("got %d:%d %q\nwant %d:%d %q\n%s", pe.Line, pe.Column, pe.Msg, line, col, want, src)
	}
	return pe
}

func TestACaptureIsALexicalToken(t *testing.T) {
	got := lexAll(t, "$first $a_1 L$5 L$ \"L$5 $x\" pay x L$ 5")
	want := []struct {
		kind kind
		text string
	}{
		{kCapture, "$first"}, {kCapture, "$a_1"}, {kMoney, "L$"}, {kInt, "5"}, {kMoney, "L$"},
		{kString, "L$5 $x"}, {kWord, "pay"}, {kWord, "x"}, {kMoney, "L$"}, {kInt, "5"}, {kEOF, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("tokens %+v", got)
	}
	for i, w := range want {
		if got[i].kind != w.kind || (w.text != "" && got[i].text != w.text) {
			t.Errorf("token %d = %v %q, want %v %q", i, got[i].kind, got[i].text, w.kind, w.text)
		}
	}
	for _, bad := range []string{"$", "$1", "$_x", "$ x", "$-"} {
		parseErr(t, "slate 1\n"+bad+"\n", "illegal character '$'")
	}
	// A capture is not a name.
	parseErr(t, "slate 1\nobject $a is \"A\"\ntouch a anywhere\n", "expected a name, found $a")
}

func TestOpenDialogsParse(t *testing.T) {
	s := mustCheck(t, suiteHdr+`
expect dialog from a
expect dialog from b text "Pick" button "Red" button 2 "Blue" button matching "^[0-9]+$" button 3 matching "(?P<n>x)" only count 4
expect dialog from a link 2 text "t" count 12
`)
	ex := body(s)[0].Expect
	d := ex[0].Dialog
	if d.HasText || len(d.Clauses) != 0 || d.Only || d.Count != nil || len(d.Buttons) != 0 {
		t.Fatalf("bare dialog %+v", d)
	}
	d = ex[1].Dialog
	if !d.HasText || d.Text.Value != "Pick" || len(d.Clauses) != 4 || !d.Only || d.Count == nil || d.Count.Value != 4 {
		t.Fatalf("dialog %+v", d)
	}
	c := d.Clauses
	if c[0].Nth != nil || c[0].Text.Value != "Red" || c[1].Nth == nil || c[1].Nth.Value != 2 || c[1].Text.Value != "Blue" ||
		!c[2].Text.Pattern || c[2].Nth != nil || !c[3].Text.Pattern || c[3].Nth.Value != 3 {
		t.Fatalf("clauses %+v", c)
	}
	// Buttons keeps the plain literals without a number, which the runner reads today.
	if len(d.Buttons) != 1 || d.Buttons[0] != "Red" {
		t.Fatalf("legacy Buttons %q", d.Buttons)
	}
	if got := s.Source(c[1].Span); got != `button 2 "Blue"` {
		t.Fatalf("clause span %q", got)
	}
}

func TestChooseLabels(t *testing.T) {
	s := mustCheck(t, suiteHdr+`
expect say matching "^(?P<first>.+)$" on public from object a
choose "Red" on a
choose matching "R.d" on a
choose button 3 on a
choose $first on a
`)
	st := body(s)
	kinds := []ChooseKind{ChooseLiteral, ChooseMatching, ChooseButton, ChooseCapture}
	for i, k := range kinds {
		if got := st[i+1].Stimulus.Choose.Kind; got != k {
			t.Errorf("choose %d kind %d, want %d", i, got, k)
		}
	}
	if c := st[1].Stimulus.Choose; c.Label != "Red" || c.Name.Text != "a" {
		t.Errorf("literal %+v", c)
	}
	if c := st[2].Stimulus.Choose; c.Label != "R.d" {
		t.Errorf("matching %+v", c)
	}
	if c := st[3].Stimulus.Choose; c.Index == nil || c.Index.Value != 3 {
		t.Errorf("button %+v", c)
	}
	if c := st[4].Stimulus.Choose; c.Use == nil || c.Use.Name != "first" {
		t.Errorf("capture %+v", c)
	}
	parseErr(t, suiteHdr+"choose button on a\n", "expected an integer")
	parseErr(t, suiteHdr+"choose matching $x on a\n", "a capture is not a pattern")
	parseErr(t, suiteHdr+"choose button 1.5 on a\n", "expected an integer")
	// textbox keeps its required text clause; the text may be a capture.
	parseErr(t, suiteHdr+"expect textbox from a\n", "expected text")
}

const capturesBound = `
expect say matching "^(?P<name>.+)$" on public from object a as $line
expect dialog from a text matching "(?P<m>.*)" button matching "^(?P<lab>.)$" as $msg
expect texture a face 0 is any as $tex
expect offset a face 0 is any as $off
expect repeats a face 0 is any as $rep
expect rotation a face 0 is any as $rot
expect click a is any as $clk
expect fullbright a face 0 is any as $fb
expect colour a face 0 is any as $col
expect glow a face 0 is any as $gl
expect alpha a face 0 is any as $al
expect give "Example Item" from a as $item
expect textbox from a text "Name?" as $tb
`

func TestCapturesParseInEveryPosition(t *testing.T) {
	s := mustCheck(t, suiteHdr+capturesBound+`
touch a button text $line
expect say $line on public from object a
expect dialog from a text $msg button $lab
expect texture a face 0 is $tex
expect offset a face 0 becomes $off
expect repeats a face 0 is $rep
expect rotation a face 0 is $rot
expect click a is $clk
expect fullbright a face 1 is $fb
expect colour a face 0 becomes $col
expect glow a face 0 is $gl
expect alpha a face 0 is $al
expect give $item from a
expect textbox from a text $tb
expect link on a from link 1 num 1 text $line key $tex
send on a from link 1 to link all num 1 text $line key $tex
touch a showing $tex
touch a button 2 text $line pattern "x"
`)
	st := body(s)
	bound := st[0].Expect
	for i, name := range []string{"line", "msg", "tex", "off", "rep", "rot", "clk", "fb", "col", "gl", "al", "item", "tb"} {
		if bound[i].As == nil || bound[i].As.Name != name || bound[i].As.String() != "$"+name {
			t.Errorf("expectation %d binds %+v, want $%s", i, bound[i].As, name)
		}
	}
	if got := s.Source(bound[0].As.Span); got != "$line" {
		t.Errorf("as span %q", got)
	}
	if !bound[2].Texture.Any || !bound[3].Offset.Any || !bound[4].Repeats.Any || !bound[5].Rot.Any || !bound[6].Click.Any ||
		!bound[7].Fullbright.Any || !bound[8].Colour.Any || !bound[9].Glow.Any || !bound[10].Alpha.Any {
		t.Error("an is any lost its Any")
	}
	use := st[1]
	if c := use.Stimulus.Touch.Button.Parts[0].Capture; c == nil || c.Name != "line" {
		t.Errorf("button part %+v", use.Stimulus.Touch.Button.Parts[0])
	}
	e := use.Expect
	if e[0].Say.Text.Capture == nil || e[0].Say.Text.Value != "" || e[0].Say.Text.Pattern {
		t.Errorf("say text %+v", e[0].Say.Text)
	}
	if e[1].Dialog.Text.Capture == nil || e[1].Dialog.Clauses[0].Text.Capture == nil || len(e[1].Dialog.Buttons) != 0 {
		t.Errorf("dialog %+v", e[1].Dialog)
	}
	if e[2].Texture.Use == nil || e[3].Offset.Use == nil || e[4].Repeats.Use == nil || e[5].Rot.Use == nil || e[6].Click.Use == nil ||
		e[7].Fullbright.Use == nil || e[8].Colour.Use == nil || e[9].Glow.Use == nil || e[10].Alpha.Use == nil {
		t.Error("a capture use was lost")
	}
	if e[11].Give.Item.Capture == nil || e[12].TextBox.Text.Capture == nil || e[13].Link.Text.Capture == nil || e[13].Link.Key.Use == nil {
		t.Error("give, textbox or link capture lost")
	}
	if sd := st[2].Stimulus.Send; sd.TextCapture == nil || sd.Text != "" || sd.Key.Use == nil || sd.Key.ID != "" {
		t.Errorf("send %+v", sd)
	}
	if sh := st[3].Stimulus.Touch.Showing; sh == nil || sh.Use == nil || sh.ID != "" {
		t.Errorf("showing %+v", sh)
	}
	if parts := st[4].Stimulus.Touch.Button.Parts; parts[0].Capture == nil || parts[1].Capture != nil || parts[1].Text != "x" {
		t.Errorf("parts %+v", parts)
	}
	// is any is also legal with a literal-free state, and a capture binds a changes.
	mustCheck(t, suiteHdr+"expect texture a face 0 changes within 1s as $t\ntouch a anywhere\nexpect texture a face 0 is $t\n")
}

func TestFacePropertiesParse(t *testing.T) {
	s := mustCheck(t, suiteHdr+`
expect fullbright a face 0 is on
expect fullbright a link 1 face all becomes off
expect fullbright a face 1 is original
expect fullbright a face all changes
expect glow a face 0 is 0.5
expect glow a face all becomes 1
expect glow a face 0 changes
expect colour a face 0 is 1 0.5 0
expect colour a face all becomes original
expect colour a face 3 changes within 2s
expect alpha a face 0 is 0.25
expect alpha a face all is original
expect no alpha a face 2 changes
expect texture a face all is `+idOne+`
expect offset a face all becomes 0.5 0.5
expect repeats a face all changes
expect rotation a face all is 0.25
`)
	e := body(s)[0].Expect
	if x := e[0].Fullbright; !x.On || x.FaceAll || x.State.Kind != StateIs {
		t.Errorf("fullbright on %+v", x)
	}
	if x := e[1].Fullbright; x.On || !x.FaceAll || x.Link == nil || x.State.Kind != StateBecomes {
		t.Errorf("fullbright off %+v", x)
	}
	if x := e[2].Fullbright; !x.State.Original || x.On {
		t.Errorf("fullbright original %+v", x)
	}
	if x := e[3].Fullbright; x.State.Kind != StateChanges || !x.FaceAll || x.Face.Text != "all" {
		t.Errorf("fullbright changes %+v", x)
	}
	if x := e[4].Glow; x.Value.Value != 0.5 || x.FaceAll {
		t.Errorf("glow %+v", x)
	}
	if x := e[5].Glow; x.Value.Value != 1 || !x.FaceAll {
		t.Errorf("glow all %+v", x)
	}
	if x := e[7].Colour; x.R.Value != 1 || x.G.Value != 0.5 || x.B.Value != 0 {
		t.Errorf("colour %+v", x)
	}
	if x := e[8].Colour; !x.State.Original || !x.FaceAll {
		t.Errorf("colour original %+v", x)
	}
	if x := e[9].Colour; x.State.Kind != StateChanges || x.Face.Value != 3 || x.Face.OK != true {
		t.Errorf("colour changes %+v", x)
	}
	if x := e[10].Alpha; x.Value.Value != 0.25 {
		t.Errorf("alpha %+v", x)
	}
	if !e[11].Alpha.State.Original || !e[12].Neg || e[12].Alpha == nil {
		t.Error("alpha original or negative")
	}
	if !e[13].Texture.FaceAll || e[13].Texture.ID != idOne || !e[14].Offset.FaceAll || !e[15].Repeats.FaceAll || !e[16].Rot.FaceAll {
		t.Error("face all on the older face expectations")
	}
	parseErr(t, suiteHdr+"expect glow a face 0 is\n", "expected a number")
	parseErr(t, suiteHdr+"expect colour a face 0 is 1 0\n", "expected a number")
	parseErr(t, suiteHdr+"expect fullbright a face 0 is 1\n", "expected on, off, original, or a capture")
	parseErr(t, suiteHdr+"expect glow a face 0 changes 0.5\n", "changes takes no value")
	parseErr(t, suiteHdr+"expect glow a face\n", "expected an integer")
	parseErr(t, suiteHdr+"expect fullbright a face 0 changes $x\n", "changes takes no value")
	// The face number is still checked.
	checkErr(t, suiteHdr+"expect glow a face 2147483648 is 0.5\n", "outside")
}

func TestShowingParses(t *testing.T) {
	s := mustCheck(t, suiteHdr+`
touch a showing `+idOne+`
touch b showing `+idTwo+` at 0.25 0.75
`)
	st := body(s)
	sh := st[0].Stimulus.Touch.Showing
	if sh == nil || sh.ID != idOne || sh.Use != nil || sh.At != nil || s.Source(sh.Span) != "showing "+idOne {
		t.Fatalf("showing %+v", sh)
	}
	sh = st[1].Stimulus.Touch.Showing
	if sh == nil || sh.ID != idTwo || sh.At == nil || sh.At.S.Value != 0.25 || sh.At.T.Value != 0.75 {
		t.Fatalf("showing at %+v", sh)
	}
	parseErr(t, suiteHdr+"touch a showing any\n", "any is only a reading")
	parseErr(t, suiteHdr+"touch a showing \"x\"\n", "expected a UUID or a capture")
	parseErr(t, suiteHdr+"touch a showing\n", "expected a UUID or a capture")
	parseErr(t, suiteHdr+"touch a showing "+idOne+" at 0.5\n", "expected a number")
}

func TestShowingCannotBeCombined(t *testing.T) {
	const want = "showing cannot be combined with"
	h := suiteHdr
	for _, c := range []struct{ line, marker, what string }{
		{"touch a link 1 showing " + idOne, "showing", "link"},
		{"touch a face 0 showing " + idOne, "showing", "face"},
		{"touch a showing " + idOne + " face 0", "showing", "face"},
		{"touch a button text \"x\" showing " + idOne, "showing", "button"},
		{"touch a showing " + idOne + " button text \"x\"", "showing", "button"},
		{"touch a anywhere showing " + idOne, "showing", "anywhere"},
		{"touch a face 0 at 0.5 0.5 showing " + idOne, "showing", "face"},
	} {
		pe := refuses(t, h+c.line+"\n", c.marker, want+" "+c.what)
		_ = pe
	}
	refuses(t, h+"touch a showing "+idOne+" at 0 0\n", "0 0\n", "at 0 0 is the middle of the face")
	refuses(t, h+"touch a showing "+idOne+" at 0.0 0\n", "0.0 0\n", originError)
}

func TestDialogStaticChecks(t *testing.T) {
	h := suiteHdr
	refuses(t, h+"expect dialog from a button 13 \"x\"\n", "13", "button 13 is outside 1 to 12")
	refuses(t, h+"expect dialog from a button 0 \"x\"\n", "0 \"x\"", "button 0 is outside 1 to 12")
	refuses(t, h+"expect dialog from a button 2 matching \"x\" button 99999999999 \"y\"\n", "99999999999", "outside -2147483648")
	refuses(t, h+"expect dialog from a count 0\n", "0", "count 0 is outside 1 to 12")
	refuses(t, h+"expect dialog from a count 13\n", "13", "count 13 is outside 1 to 12")
	refuses(t, h+"expect dialog from a button \"a\" button \"b\" button \"c\" only count 2\n", "2\n", "count 2: the 3 button clauses")
	refuses(t, h+"expect dialog from a button \"a\" button \"b\" button \"c\" count 2\n", "2\n", "count 2: the 3 button clauses")
	refuses(t, h+"expect dialog from a button 2 \"a\" button 2 \"b\"\n", "2 \"b\"", "two button clauses are pinned to button 2")
	refuses(t, h+"expect dialog from a button 1 matching \"x\" only count 3\n", "3\n", "only with count 3 needs 3 button clauses")
	refuses(t, h+"expect dialog from a text matching \"(\"\n", "\"(\"", "pattern")
	refuses(t, h+"expect dialog from a button matching \"[\"\n", "\"[\"", "pattern")
	refuses(t, h+"expect dialog from a button 2 matching \"a{2,1}\"\n", "\"a{2,1}\"", "pattern")
	refuses(t, h+"choose button 13 on a\n", "13", "button 13 is outside 1 to 12")
	refuses(t, h+"choose button 0 on a\n", "0", "button 0 is outside 1 to 12")
	refuses(t, h+"choose matching \"(\" on a\n", "\"(\"", "pattern")
	refuses(t, h+"choose \"x\" on missing\n", "missing", "missing is not an object")
	// The edges and the cases that are fine.
	mustCheck(t, h+"expect dialog from a button 12 \"x\" button 1 matching \"y\" count 12\n")
	mustCheck(t, h+"expect dialog from a button \"a\" button \"b\" only count 2\n")
	mustCheck(t, h+"expect no dialog from a text \"\"\n")
	mustCheck(t, h+"expect dialog from a link 3 count 1\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\nexpect dialog from a link 3\n")
}

func TestCaptureTypesAreChecked(t *testing.T) {
	h := suiteHdr + capturesBound + "touch b anywhere\n"
	refuses(t, h+"expect say $tex on public from object a\n", "$tex on", "capture type mismatch: $tex holds uuid (bound at line 9), and this place needs text")
	refuses(t, h+"expect texture a face 0 is $line\n", "last:$line", "$line holds text (bound at line 7), and this place needs uuid")
	refuses(t, h+"expect texture a face 0 is $off\n", "$off", "holds pair")
	refuses(t, h+"expect offset a face 0 is $rot\n", "$rot", "$rot holds number (bound at line 12), and this place needs pair")
	refuses(t, h+"expect rotation a face 0 is $off\n", "$off", "this place needs number")
	refuses(t, h+"expect rotation a face 0 is $gl\n"+"expect glow a face 0 is $col\n", "$col", "$col holds colour triple (bound at line 15), and this place needs number")
	refuses(t, h+"expect click a is $line\n", "$line", "this place needs click")
	refuses(t, h+"expect fullbright a face 0 is $gl\n", "$gl", "this place needs on or off")
	refuses(t, h+"expect colour a face 0 is $fb\n", "$fb", "holds on or off")
	refuses(t, h+"choose $tex on a\n", "$tex", "$tex holds uuid")
	refuses(t, h+"touch a button text $tex\n", "$tex", "$tex holds uuid")
	refuses(t, h+"touch a showing $line\n", "$line", "$line holds text")
	refuses(t, h+"send on a from link 1 to link all num 1 text \"x\" key $line\n", "$line", "this place needs uuid")
	refuses(t, h+"send on a from link 1 to link all num 1 text $tex\n", "$tex", "this place needs text")
	refuses(t, h+"expect link on a from link 1 num 1 text \"x\" key $line\n", "$line", "this place needs uuid")
	refuses(t, h+"expect give $off from a\n", "$off", "this place needs text")
	// A text is never a uuid, even one that looks like it; and the other way.
	refuses(t, h+"expect texture a face 0 is $line\n", "$line", "capture type mismatch")
	// Names of numbers are the same type: a glow can be a rotation's value.
	mustCheck(t, h+"expect rotation a face 0 is $gl\n")
}

func TestACaptureIsBoundBeforeItIsUsed(t *testing.T) {
	h := suiteHdr
	refuses(t, h+"choose $nope on a\n", "$nope", "$nope is not bound")
	// Its own step: by an expectation in the same step, as a stimulus or as an expectation.
	refuses(t, h+"expect texture a face 0 is any as $t\nexpect texture a face 1 is $t\n", "last:$t", "$t is bound in this step; it can be used in a later step")
	refuses(t, h+"touch a button text $t\nexpect say matching \"(?P<t>.)\" on public from anyone\n", "$t\n", "$t is bound in this step")
	refuses(t, h+"expect say matching \"(?P<t>.)\" on public from anyone\nexpect say $t on public from anyone\n", "$t on", "$t is bound in this step")
	// The next step is fine.
	mustCheck(t, h+"expect texture a face 0 is any as $t\nthen expect texture a face 1 is $t\n")
	mustCheck(t, h+"expect say matching \"(?P<t>.)\" on public from anyone\nchoose $t on a\n")
}

func TestOnlyAPositiveExpectationBinds(t *testing.T) {
	h := suiteHdr
	refuses(t, h+"expect no texture a face 0 is "+idOne+" as $t\n", "$t", "a negative expectation matches nothing to bind")
	refuses(t, h+"expect no say \"x\" on public from anyone as $t\n", "$t", "a negative expectation")
	refuses(t, h+"expect rez name \"x\" from a as r within 1s as $t\n", "$t", "a rez names the new object with as NAME")
	refuses(t, h+"expect link on a from link 1 num 1 text \"x\" as $t\n", "$t", "this expectation has no reading to bind")
	// A named group in a negative expectation is only a group.
	s := mustCheck(t, h+"expect no say matching \"(?P<t>x)\" on public from anyone within 1s\nthen expect texture a face 0 is any as $t\n")
	if len(body(s)) != 2 {
		t.Fatal("steps")
	}
	refuses(t, h+"expect no say matching \"(?P<t>x)\" on public from anyone within 1s\nthen expect say $t on public from anyone\n", "$t on", "$t is not bound")
	parseErr(t, h+"expect texture a face 0 is any as t\n", "expected a capture such as $name after as")
	parseErr(t, h+"expect texture a face 0 is any as\n", "expected a capture such as $name after as")
}

func TestIsAnyNeedsAsAndAStateExpectation(t *testing.T) {
	h := suiteHdr
	refuses(t, h+"expect texture a face 0 is any\n", "is any", "is any needs as $name")
	refuses(t, h+"expect click a is any\n", "is any", "is any needs as $name")
	refuses(t, h+"expect glow a face 0 is any within 1s\n", "is any", "is any needs as $name")
	refuses(t, h+"expect texture a face 0 becomes any as $t\n", "becomes", "any is a reading of is, not of becomes")
	refuses(t, h+"expect no texture a face 0 is any\n", "is any", "is any needs as $name")
	refuses(t, h+"expect no texture a face 0 is any as $t\n", "$t", "a negative expectation matches nothing to bind")
	// A say has no is any; any is not a text.
	parseErr(t, h+"expect say any on public from anyone as $t\n", "any is only a reading of a state expectation")
	parseErr(t, h+"expect say is any on public from anyone as $t\n", "expected a string")
	parseErr(t, h+"expect dialog from a text any\n", "any is only a reading")
	parseErr(t, h+"expect texture a face 0 changes any as $t\n", "changes takes no value")
	parseErr(t, h+"touch a showing any\n", "any is only a reading")
	mustCheck(t, h+"expect texture a face 0 is any as $t\n")
}

func TestACaptureIsNotAPattern(t *testing.T) {
	h := suiteHdr
	parseErr(t, h+"expect say matching $x on public from anyone\n", "a capture is not a pattern")
	parseErr(t, h+"expect dialog from a button matching $x\n", "a capture is not a pattern")
	parseErr(t, h+"expect give matching $x from a\n", "a capture is not a pattern")
	// A dollar inside a string stays a dollar.
	s := mustCheck(t, h+"expect say matching \"L\\\\$[0-9]+ $x\" on public from anyone\nsay \"L$5 $x\" on 0\n")
	if got := body(s)[1].Stimulus.Say.Text; got != "L$5 $x" {
		t.Fatalf("say %q", got)
	}
}

func TestNamedGroupsBindCaptures(t *testing.T) {
	h := suiteHdr
	refuses(t, h+"expect say matching \"(?P<n>a)(?P<n>b)\" on public from anyone\n", "\"(?P<n>a)(?P<n>b)\"", "group name \"n\" appears twice")
	refuses(t, h+"expect dialog from a button matching \"(?P<n>a)|(?P<n>b)\"\n", "\"(?P<n>a)|(?P<n>b)\"", "appears twice")
	refuses(t, h+"expect say matching \"(?P<_n>a)\" on public from anyone\n", "\"(?P<_n>a)\"", "cannot be written as a capture")
	// Two patterns of one expectation, or two steps, bind one name once.
	refuses(t, h+"expect dialog from a text matching \"(?P<n>a)\" button matching \"(?P<n>b)\"\n", "\"(?P<n>b)\"", "$n is already bound at line 6")
	refuses(t, h+"expect say matching \"(?P<n>a)\" on public from anyone\nthen expect say matching \"(?P<n>b)\" on public from anyone\n", "\"(?P<n>b)\"", "$n is already bound at line 6")
	refuses(t, h+"expect say matching \"(?P<n>a)\" on public from anyone as $n\n", "$n", "$n is already bound")
	// Unnamed and non-capturing groups bind nothing.
	mustCheck(t, h+"expect say matching \"(a)(?:b)(?i:c)\" on public from anyone\n")
	// Every kind of text that matches binds.
	mustCheck(t, h+`
expect say matching "(?P<a1>.)" on public from anyone
expect dialog from a text matching "(?P<a2>.)" button matching "(?P<a3>.)"
expect textbox from a text matching "(?P<a4>.)"
expect give matching "(?P<a5>.)" from a
expect rez name matching "(?P<a6>.)" description matching "(?P<a7>.)" from a as r
expect link on a from link 1 num 1 text matching "(?P<a8>.)"
choose $a1 on a
choose $a2 on a
choose $a3 on a
choose $a4 on a
choose $a5 on a
choose $a6 on a
choose $a7 on a
choose $a8 on a
`)
	// A group's capture is text.
	refuses(t, h+"expect say matching \"(?P<n>.)\" on public from anyone\nthen expect texture a face 0 is $n\n", "$n\n", "$n holds text")
}

func TestFacePropertyRanges(t *testing.T) {
	h := suiteHdr
	refuses(t, h+"expect glow a face 0 is 1.5\n", "1.5", "glow 1.5 is outside 0 to 1")
	refuses(t, h+"expect glow a face 0 is -0.1\n", "-0.1", "glow -0.1 is outside 0 to 1")
	refuses(t, h+"expect alpha a face all becomes 2\n", "2", "alpha 2 is outside 0 to 1")
	refuses(t, h+"expect colour a face 0 is 0.5 0.5 1.01\n", "1.01", "colour 1.01 is outside 0 to 1")
	refuses(t, h+"expect colour a face 0 is 2 0 0\n", "2 0 0", "colour 2 is outside 0 to 1")
	refuses(t, h+"expect colour a face 0 is 0 -1 0\n", "-1", "colour -1 is outside 0 to 1")
	mustCheck(t, h+"expect glow a face 0 is 0\nexpect glow a face 1 is 1\nexpect alpha a face 0 is 0.0\nexpect colour a face 0 is 0 1 0.5\n")
	// changes and original carry no literal.
	mustCheck(t, h+"expect glow a face 0 changes\nexpect colour a face 0 is original\n")
	// Binding and link rules are the texture's.
	refuses(t, h+"expect glow missing face 0 changes\n", "missing", "missing is not an object")
	mustCheck(t, h+"expect colour b link 2 face 0 changes\n")
}

func TestCaptureScope(t *testing.T) {
	// Bound in before each: visible in the body and in after each.
	mustCheck(t, suiteHdr+`
before each {
  touch a anywhere
  expect say matching "(?P<first>.+)" on public from object a
}
test "t" {
  choose $first on a
}
after each {
  choose $first on a
}
`)
	// Bound in the body: not in after each.
	src := suiteHdr + `
test "t" {
  expect say matching "(?P<first>.+)" on public from object a
  choose $first on a
}
after each {
  choose $first on b
}
`
	refuses(t, src, "$first on b", "$first is bound in test \"t\", which after each cannot rely on")
	// Visible after a do, and for a capture a sequence binds.
	mustCheck(t, suiteHdr+`
sequence grab {
  expect texture a face 0 is any as $t
}
test "t" {
  do grab
  touch a showing $t
}
`)
	// Bound once per expanded test: a second call of the sequence rebinds it.
	src = suiteHdr + `
sequence grab {
  expect texture a face 0 is any as $t
}
test "twice" {
  do grab
  touch a anywhere
  do grab
}
`
	pe := refuses(t, src, "$t\n}\ntest", "$t is already bound at line 8; a capture is bound once per test")
	if !strings.Contains(pe.Msg, `(in test "twice", via do grab at line 13)`) {
		t.Fatalf("message %q does not name the test and the call", pe.Msg)
	}
	// Through two levels, the chain is innermost first.
	src = suiteHdr + `
sequence inner {
  expect texture a face 0 is any as $t
}
sequence outer {
  do inner
}
test "nested" {
  do outer
  do outer
}
`
	pe = refuses(t, src, "$t\n}\nsequence outer", "already bound")
	if !strings.Contains(pe.Msg, `(in test "nested", via do inner at line 11 via do outer at line 15)`) {
		t.Fatalf("message %q", pe.Msg)
	}
	// Once per test: two tests may each call it once, and before each and the body share the one name.
	mustCheck(t, suiteHdr+`
sequence grab {
  expect texture a face 0 is any as $t
}
test "one" {
  do grab
}
test "two" {
  do grab
}
`)
	refuses(t, suiteHdr+`
before each {
  expect texture a face 0 is any as $t
}
test "t" {
  expect texture a face 1 is any as $t
}
`, "$t\n}\n", "$t is already bound at line 8")
	// A capture and an object may share a word: $a is not a.
	mustCheck(t, suiteHdr+"expect texture a face 0 is any as $a\nthen expect texture a face 1 is $a\n")
	// The first use of a bound capture does not need its step to pass: only order counts.
	refuses(t, suiteHdr+"test \"t\" {\n  choose $x on a\n  touch a anywhere\n  expect texture a face 0 is any as $x\n}\n", "$x on", "$x is not bound")
}

func TestAFaceAllCaptureIsUsedOnlyByAFaceAll(t *testing.T) {
	h := suiteHdr + "expect fullbright a face all is any as $all\nexpect fullbright a face 0 is any as $one\nthen\n"
	mustCheck(t, h+"expect fullbright a face all becomes $all\n")
	mustCheck(t, h+"expect fullbright a face 1 becomes $one\n")
	refuses(t, h+"expect fullbright a face 1 becomes $all\n", "$all\n", "$all holds every face (bound by a face all at line")
	refuses(t, h+"expect fullbright a face all becomes $one\n", "$one\n", "$one holds one face (bound at line")
}
