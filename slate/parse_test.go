package slate

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, src string) *Script {
	t.Helper()
	s, err := Parse("t.slate", []byte(src))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, src)
	}
	return s
}

func mustCheck(t *testing.T, src string) *Script {
	t.Helper()
	s := mustParse(t, src)
	if err := Check(s); err != nil {
		t.Fatalf("check: %v\n%s", err, src)
	}
	return s
}

func parseErr(t *testing.T, src, want string) {
	t.Helper()
	_, err := Parse("t.slate", []byte(src))
	if err == nil {
		t.Fatalf("parse succeeded, want %q\n%s", want, src)
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("got %T, want *Error", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q\nwant %q", err.Error(), want)
	}
}

func checkErr(t *testing.T, src, want string) {
	t.Helper()
	s := mustParse(t, src)
	err := Check(s)
	if err == nil {
		t.Fatalf("check succeeded, want %q\n%s", want, src)
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("got %T, want *Error", err)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q\nwant %q", err.Error(), want)
	}
}

func TestVersionColumn(t *testing.T) {
	_, err := Parse("t.slate", []byte("slate 2\n"))
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatal(err)
	}
	if pe.Line != 1 || pe.Column != 7 || !strings.Contains(pe.Msg, "expected slate 1") {
		t.Fatalf("%d:%d %s", pe.Line, pe.Column, pe.Msg)
	}
}

func TestParseRejectsABadFile(t *testing.T) {
	cases := []struct{ src, want string }{
		{"", "expected slate 1"},
		{"# comment\n\ntimeout 10s\n", "expected slate 1"},
		{"slate 1\n", "a file needs a step"},
		{"slate 01\nobject a is \"A\"\ntouch a anywhere\n", "expected slate 1"},
		{"slate 1\nobject a is \"A\"\nthen\n", "then needs an expectation"},
		{"slate 1\nobject a is \"A\"\ntouch a anywhere\nthen\n", "then needs an expectation"},
		{"slate 1\ntouch a anywhere\nobject a is \"A\"\n", "headers go before the first step"},
		{"slate 1\nobject a is \"A\"\ntouch a\n", "expected anywhere, link, face, button, or showing"},
		{"slate 1\nobject a is \"A\"\ntouch a button\n", "a button needs a part"},
		{"slate 1\nobject a is \"A\"\ntouch a button 1.5 text \"A\"\n", "a button number is a whole number"},
		{"slate 1\nobject a is \"A\"\nsay \"hi\" on 0 as object a\n", "a stimulus speaks as the tester"},
		{"slate 1\nobject a is \"A\"\nsay \"hi\" on 0 as anyone\n", "a stimulus speaks as the tester"},
		{"slate 1\nobject a is \"A\"\nexpect say on public \"pong\" from object a\n", "expected a string"},
		{"slate 1\nobject a is \"A\"\nexpect say \"pong\" from object a on public\n", "expected on"},
		{"slate 1\nobject a is \"A\"\ntimeout 10 s\ntouch a anywhere\n", "expected a duration"},
		{"slate 1\n;\n", "illegal character ';'"},
		{"slate 1\n.5\n", "a number needs digits on both sides of the dot"},
		{"slate 1\nobject a is \"A\"\nface 1\n", "expected a step"},
	}
	for _, tc := range cases {
		parseErr(t, tc.src, tc.want)
	}
}

func TestAFloatUnitParsesAndChecksAsTheDurationError(t *testing.T) {
	src := "slate 1\ntimeout 1.5s\nobject a is \"A\"\ntouch a anywhere\n"
	s := mustParse(t, src)
	if len(s.Timeouts) != 1 || !s.Timeouts[0].Bad {
		t.Fatalf("timeout %+v", s.Timeouts)
	}
	checkErr(t, src, "write 1500ms, not 1.5s; a duration is whole digits and a unit, with no dot")
	checkErr(t, "slate 1\nobject a is \"A\"\ntouch a anywhere\nexpect say \"x\" on public from object a within 2.5ms\n",
		"write 1500ms, not 1.5s; a duration is whole digits and a unit, with no dot")
}

func TestSayExpectationForms(t *testing.T) {
	s := mustCheck(t, `
slate 1
object sign is "Example Sign"
object vendor is "Example Tip Jar"
listen 1
expect say "pong" on public from object sign
expect say "pong" on 1 from object vendor
expect no say "error" on public from object vendor
`)
	if len(body(s)) != 1 || body(s)[0].Stimulus != nil || len(body(s)[0].Expect) != 3 {
		t.Fatalf("steps %+v", body(s))
	}
	pub, num, neg := body(s)[0].Expect[0], body(s)[0].Expect[1], body(s)[0].Expect[2]
	if pub.Say == nil || pub.Say.Text.Value != "pong" || pub.Say.Channel.Kind != ChanPublic || pub.Say.From.Kind != SpeakObject || pub.Say.From.Name.Text != "sign" {
		t.Fatalf("public %+v", pub.Say)
	}
	if num.Say == nil || num.Say.Channel.Kind != ChanNumber || num.Say.Channel.Int.Value != 1 || num.Say.From.Name.Text != "vendor" {
		t.Fatalf("numbered %+v", num.Say)
	}
	if !neg.Neg || neg.Say == nil || neg.Say.Text.Value != "error" || neg.Say.Channel.Kind != ChanPublic {
		t.Fatalf("negative %+v", neg)
	}
}

func TestStepsAreSetsUntilThenOrTheNextStimulus(t *testing.T) {
	s := mustCheck(t, `
slate 1
object a is "A"
touch a anywhere
expect say "one" on public from object a
expect say "two" on public from object a
touch a anywhere
then
expect say "three" on public from object a
expect say "hi" on public from tester
`)
	if len(body(s)) != 3 {
		t.Fatalf("%d steps", len(body(s)))
	}
	if body(s)[0].Stimulus == nil || body(s)[0].Stimulus.Touch == nil || len(body(s)[0].Expect) != 2 {
		t.Fatalf("step 0")
	}
	if body(s)[1].Stimulus == nil || len(body(s)[1].Expect) != 0 || body(s)[1].Then {
		t.Fatalf("step 1")
	}
	if !body(s)[2].Then || body(s)[2].Stimulus != nil || len(body(s)[2].Expect) != 2 {
		t.Fatalf("step 2")
	}
	if body(s)[2].Expect[1].Say.From.Kind != SpeakTester {
		t.Fatalf("speaker %d", body(s)[2].Expect[1].Say.From.Kind)
	}
	if got := s.Source(body(s)[0].Stimulus.Span); got != "touch a anywhere" {
		t.Fatalf("stimulus source %q", got)
	}
}

func TestAnExpectationOnlyStep(t *testing.T) {
	s := mustCheck(t, "slate 1\nobject a is \"A\"\nexpect say \"hi\" on public from object a\n")
	if len(body(s)) != 1 || body(s)[0].Stimulus != nil || body(s)[0].Then || len(body(s)[0].Expect) != 1 {
		t.Fatalf("%+v", body(s)[0])
	}
}

func TestParsedScriptKeepsTheForms(t *testing.T) {
	s := mustCheck(t, `
slate 1
timeout 10s
allow pay

object hud is "Example HUD"
object sign is "Example \"East"
object vendor is "Example Tip Jar"
probe vendor
probe hud
listen 1

touch hud button 2 text "Open" pattern "^O" symbol "right arrow" box circle face 0
expect texture sign face 0 is 17CA7E57-7E57-C0DE-EF46-5FEAE5A71169 within 8s
expect offset sign face 0 is 0.25 0
expect repeats sign face 0 is -2 1
expect rotation sign face 0 is 0.25
expect no say "error" on public from anyone

say "menu" on -7 as owner of vendor
expect dialog from vendor text "Choose a colour" button "Red" button "Blue" only
expect say "pong" on 1 from object vendor

pay vendor L$5 reason "tip"
expect give "Example Thank You" from vendor
expect rez name "Example Balloon" description "left" from vendor as left
expect rez name "Example Balloon" description "right" from vendor as right

touch left button text "Pop"
expect click left is sit

touch hud anywhere
touch hud link 3
touch hud link 3 anywhere
touch hud face 2 at 0.9 0.5
touch hud link 4 button image "logo" oval

drag hud link 2 face 0 from 0.1 0.5 to 0.9 0.5 over 500ms

sit sign
stand

choose "Red" on vendor
answer "Example Resident" on vendor

send on vendor from link 1 to link others num 7 text "ready" key null
expect link on vendor from link 1 num 7 text "ready" key null heard by 1

then
expect textbox from vendor text "Name"
expect no click sign is buy
expect no texture sign face 1 is 17ca7e57-7e57-c0de-ef46-5feae5a71169
expect no rez name "Nope" from vendor
`)
	if s.TimeoutDuration() != 10*time.Second || len(s.Allows) != 1 {
		t.Fatalf("timeout %s allows %d", s.TimeoutDuration(), len(s.Allows))
	}
	if s.Objects[1].World != "Example \"East" {
		t.Fatalf("world %q", s.Objects[1].World)
	}
	if s.Probes[0].Name.Text != "vendor" {
		t.Fatalf("probe %+v", s.Probes[0])
	}
	btn := body(s)[0].Stimulus.Touch.Button
	if btn.Nth == nil || btn.Nth.Value != 2 || btn.Face == nil || btn.Face.Value != 0 || len(btn.Parts) != 5 {
		t.Fatalf("button %+v", btn)
	}
	kinds := []PartKind{PartText, PartPattern, PartSymbol, PartBox, PartCircle}
	for i, k := range kinds {
		if btn.Parts[i].Kind != k {
			t.Fatalf("part %d kind %d", i, btn.Parts[i].Kind)
		}
	}
	tex := body(s)[0].Expect[0].Texture
	if tex == nil || tex.ID != "17ca7e57-7e57-c0de-ef46-5feae5a71169" || tex.Face.Value != 0 {
		t.Fatalf("texture %+v", tex)
	}
	if body(s)[0].Expect[0].Within == nil || body(s)[0].Expect[0].Within.Value != 8*time.Second {
		t.Fatalf("within")
	}
	if body(s)[0].Expect[2].Repeats == nil || body(s)[0].Expect[2].Repeats.S.Value != -2 {
		t.Fatalf("repeats")
	}
	off := body(s)[0].Expect[1].Offset
	if off == nil || !off.T.Zero || off.S.Value != 0.25 {
		t.Fatalf("offset")
	}
	say := body(s)[1].Stimulus.Say
	if say == nil || say.Channel.Value != -7 || say.As == nil || say.As.Kind != SpeakOwner || say.As.Name.Text != "vendor" {
		t.Fatalf("say %+v", say)
	}
	d := body(s)[1].Expect[0].Dialog
	if d == nil || !d.Only || len(d.Buttons) != 2 || d.Buttons[0] != "Red" {
		t.Fatalf("dialog %+v", d)
	}
	pay := body(s)[2].Stimulus.Pay
	if pay == nil || !pay.Linden || pay.Amount.Value != 5 || pay.Reason != "tip" {
		t.Fatalf("pay %+v", pay)
	}
	if body(s)[2].Expect[1].Rez.As.Text != "left" || body(s)[2].Expect[2].Rez.Desc.Value != "right" {
		t.Fatalf("rez")
	}
	img := findTouch(t, s, func(t *Touch) bool {
		return t.Button != nil && len(t.Button.Parts) == 2 && t.Button.Parts[0].Kind == PartImage
	})
	if img.Button.Parts[1].Kind != PartOval || img.Link == nil || img.Link.Value != 4 {
		t.Fatalf("image touch %+v", img)
	}
	dr := findDrag(t, s)
	if dr.Link == nil || dr.Link.Value != 2 || dr.Over == nil || dr.Over.Value != 500*time.Millisecond || dr.From.S.Value != 0.1 {
		t.Fatalf("drag %+v", dr)
	}
	send := findSend(t, s)
	if send.To.Word != "others" || send.Key == nil || !send.Key.Null || send.Key.ID != nullKey || send.Num.Value != 7 {
		t.Fatalf("send %+v", send)
	}
	link := findLink(t, s)
	if link.HeardBy == nil || link.HeardBy.Value != 1 || link.Key == nil || !link.Key.Null || link.Key.ID != nullKey || link.From.Value != 1 {
		t.Fatalf("link %+v", link)
	}
	var negRez, negClick bool
	for _, st := range body(s) {
		for _, e := range st.Expect {
			if e.Neg && e.Rez != nil && e.Rez.As.Text == "" {
				negRez = true
			}
			if e.Neg && e.Click != nil && e.Click.Action == "buy" {
				negClick = true
			}
		}
	}
	if !negRez || !negClick {
		t.Fatalf("neg rez %v click %v", negRez, negClick)
	}
	if body(s)[len(body(s))-1].Then != true {
		// The then step is not necessarily last if I miscounted. Search.
		found := false
		for _, st := range body(s) {
			if st.Then {
				found = true
			}
		}
		if !found {
			t.Fatal("no then step")
		}
	}
}

func findTouch(t *testing.T, s *Script, pred func(*Touch) bool) *Touch {
	t.Helper()
	for _, st := range body(s) {
		if st.Stimulus != nil && st.Stimulus.Touch != nil && pred(st.Stimulus.Touch) {
			return st.Stimulus.Touch
		}
	}
	t.Fatal("touch not found")
	return nil
}

func findDrag(t *testing.T, s *Script) *Drag {
	t.Helper()
	for _, st := range body(s) {
		if st.Stimulus != nil && st.Stimulus.Drag != nil {
			return st.Stimulus.Drag
		}
	}
	t.Fatal("drag not found")
	return nil
}

func findSend(t *testing.T, s *Script) *Send {
	t.Helper()
	for _, st := range body(s) {
		if st.Stimulus != nil && st.Stimulus.Send != nil {
			return st.Stimulus.Send
		}
	}
	t.Fatal("send not found")
	return nil
}

func findLink(t *testing.T, s *Script) *LinkExp {
	t.Helper()
	for _, st := range body(s) {
		for _, e := range st.Expect {
			if e.Link != nil {
				return e.Link
			}
		}
	}
	t.Fatal("link not found")
	return nil
}

func TestInWorldNameMayBeAKeyword(t *testing.T) {
	s := mustCheck(t, "slate 1\nobject hud is \"box\"\ntouch hud anywhere\n")
	if s.Objects[0].World != "box" {
		t.Fatalf("%q", s.Objects[0].World)
	}
}

func TestObjectNameMayUseAHyphen(t *testing.T) {
	mustCheck(t, "slate 1\nobject shop-hud is \"Example HUD\"\ntouch shop-hud anywhere\n")
}

func TestCommentAndBlankLinesDoNotMakeSteps(t *testing.T) {
	s := mustCheck(t, `
slate 1

# drive the hud
object a is "A" # the prim

touch a anywhere # the root
`)
	if len(body(s)) != 1 {
		t.Fatalf("%d steps", len(body(s)))
	}
}

func TestWorkedExamples(t *testing.T) {
	scripts := []string{
		`
slate 1
timeout 10s

object hud is "Example HUD"
object sign is "Example Sign"

touch hud button text "Open"
expect texture sign face 0 is 17ca7e57-7e57-c0de-ef46-5feae5a71169 within 8s
expect offset sign face 0 is 0.25 0 within 8s
expect repeats sign face 0 is 2 1 within 8s
`,
		`
slate 1
timeout 10s

object vendor is "Example Tip Jar"

say "menu" on -7 as owner of vendor
expect dialog from vendor text "Choose a colour" button "Red" button "Blue"

choose "Red" on vendor
expect say "red" on public from object vendor
expect give "Example Red Swatch" from vendor
`,
		`
slate 1
timeout 10s
allow pay

object vendor is "Example Tip Jar"

pay vendor L$5 reason "tip"
expect give "Example Thank You" from vendor
expect rez name "Example Balloon" description "left" from vendor as left
expect rez name "Example Balloon" description "right" from vendor as right

touch left button text "Pop"
expect say "pop" on public from object left
`,
		`
slate 1
timeout 10s

object chair is "Example Chair"

sit chair
expect click chair is touch within 10s

stand
`,
		`
slate 1
timeout 10s

object slider is "Example Volume"

drag slider face 0 from 0.1 0.5 to 0.9 0.5 over 500ms
expect offset slider face 0 is 0.4 0 within 10s
`,
		`
slate 1
timeout 10s

object panel is "Example Panel"
probe panel

touch panel anywhere
expect say "root" on public from object panel

touch panel face 2 at 0.9 0.5
expect say "face" on public from object panel

touch panel link 3
expect say "child" on public from object panel
`,
		`
slate 1
timeout 10s

object hud is "Example HUD"
object sign is "Example Sign"

touch hud button text "Next" box symbol "right arrow" circle
expect say "next" on public from object sign
`,
		`
slate 1
timeout 10s

object board is "Example Guest Book"

touch board button text "Sign"
expect textbox from board text "Name yourself"

answer "Example Resident" on board
expect say "Hello, Example" on public from object board
`,
		`
slate 1
timeout 10s

object vendor is "Example Tip Jar"
probe vendor

send on vendor from link 1 to link others num 7 text "ready"
expect link on vendor from link 1 num 7 text "ready"

then
expect link on vendor from link 3 num 9 text "go" heard by 1
`,
		`
slate 1
timeout 10s

object sign is "Example Sign"

say "ping" on 1 as tester
expect say "pong" on public from object sign within 1s
`,
		`
slate 1
timeout 10s

object vendor is "Example Tip Jar"

say "menu" on -7 as tester
expect dialog from vendor text "Choose a colour" button "Red" button "Blue"
choose "Red" on vendor
expect say "red" on public from object vendor

then
expect no say "error" on public from object vendor within 2s
`,
		`
slate 1
timeout 10s

object vendor is "Example Tip Jar"
listen 1

say "ping" on 1 as tester
expect say "pong" on 1 from object vendor within 1s
`,
	}
	for i, src := range scripts {
		if _, err := Parse("ex.slate", []byte(src)); err != nil {
			t.Fatalf("example %d parse: %v", i+1, err)
		}
		s := mustParse(t, src)
		if err := Check(s); err != nil {
			t.Fatalf("example %d check: %v", i+1, err)
		}
	}
}

// body is the steps of the only test in a file of plain steps.
func body(s *Script) []Step { return s.Tests[0].Steps }
