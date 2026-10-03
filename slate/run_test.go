package slate

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// on makes the far end answer when the tester says trigger.
func (f *fakeGrid) on(trigger string, replies ...msg.Message) {
	f.replyTo(func(m msg.Message) {
		if text, _, ok := says(m); ok && text == trigger {
			for _, r := range replies {
				f.relay(r)
			}
		}
	})
}

func signSays(text string) msg.Message { return chatMsg("Example Sign", idSign, sl.ChatSay, text) }

const hdr = "slate 1\nobject sign is \"Example Sign\"\nobject vendor is \"Example Tip Jar\"\n"

func TestSayThenSayPasses(t *testing.T) {
	f := newGrid(t)
	f.on("go", signSays("hello"))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect say \"hello\" on public from object sign within 500ms\n")
	wantExit(t, res, 0)
	if len(res.Tests) != 1 || !res.Tests[0].Passed || res.Tests[0].Name != "t" || res.Tests[0].Exit != 0 {
		t.Fatalf("tests = %+v", res.Tests)
	}
	mustHave(t, res, `slate: test "t"`, `chat public from sign: "hello"`, "slate: pass step 1", `slate: pass test "t"`, "slate: passed 1 tests")
	if got := f.said(); len(got) != 1 || got[0] != "go" {
		t.Errorf("said %q", got)
	}
}

func TestAnUnmatchedSayFailsAtItsWithin(t *testing.T) {
	f := newGrid(t)
	t0 := time.Now()
	res := play(t, f, hdr+"say \"go\" on 0\nexpect say \"x\" on public from object sign within 150ms\n")
	took := time.Since(t0)
	wantExit(t, res, 1)
	if took < 150*time.Millisecond || took > time.Second {
		t.Errorf("failed after %s, want about 150ms", took)
	}
	if res.Tests[0].Passed || res.Tests[0].Exit != 1 {
		t.Errorf("test = %+v", res.Tests[0])
	}
	mustHave(t, res,
		`slate: fail t.slate test "t" step 1 lines 4-5`,
		`stimulus: say "go" on 0`,
		`sent on channel 0 as the tester`,
		`unmatched say "x" on public from object sign within 150ms`,
		"waited: 150ms",
		"  seated: no", "  dialog left unanswered: no", "  payment: none",
		"slate: failed 1 of 1 tests")
	ls := lines(res)
	for i, l := range ls {
		if l == "  heard during the step:" && ls[i+1] != "    (none)" {
			t.Errorf("heard = %q", ls[i+1])
		}
	}
	mustNotHave(t, res, "slate: pass")
}

func TestTwoExpectationsForOneLineNeedTwoLines(t *testing.T) {
	f := newGrid(t)
	f.on("go", signSays("hello"))
	src := hdr + "say \"go\" on 0\nexpect say \"hello\" on public from object sign within 150ms\nexpect say \"hello\" on public from object sign within 150ms\n"
	res := play(t, f, src)
	wantExit(t, res, 1)
	mustHave(t, res, `matched say "hello" on public from object sign within 150ms at`, `unmatched say "hello" on public from object sign within 150ms`)

	f = newGrid(t)
	f.on("go", signSays("hello"), signSays("hello"))
	wantExit(t, play(t, f, src), 0)
}

func TestSetOrderDoesNotMatter(t *testing.T) {
	f := newGrid(t)
	f.on("go", signSays("one"), signSays("two"))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect say \"two\" on public from object sign within 300ms\nexpect say \"one\" on public from object sign within 300ms\n")
	wantExit(t, res, 0)
}

// D2: a reply that arrives while the previous step is still in a negative
// window is after the arm point of a then step, so it is eligible; one
// observed before the previous step's matched event is not.
func TestThenArmsFromThePreviousMatch(t *testing.T) {
	step1 := hdr + "say \"go\" on 0\nexpect say \"a\" on public from object sign within 300ms\nexpect no say \"zzz\" on public from object sign within 300ms\n\nthen\nexpect say \"b\" on public from object sign within 300ms\n"

	f := newGrid(t)
	f.on("go", signSays("a"))
	f.quietly(t, 100*time.Millisecond, signSays("b"))
	res := play(t, f, step1)
	wantExit(t, res, 0)
	mustHave(t, res, "slate: pass step 1", "slate: pass step 2")

	// The same reply, but before "a": not eligible for the later step.
	f = newGrid(t)
	f.on("go", signSays("b"))
	f.quietly(t, 60*time.Millisecond, signSays("a"))
	res = play(t, f, step1)
	wantExit(t, res, 1)
	mustHave(t, res, "slate: pass step 1", `unmatched say "b" on public from object sign within 300ms`, "step 2")
}

func TestEventsFromAnEarlierTestAreNotEligible(t *testing.T) {
	f := newGrid(t)
	f.on("go", signSays("a"))
	f.quietly(t, 50*time.Millisecond, signSays("stale"))
	src := hdr + `
test "one" {
  say "go" on 0
  expect say "a" on public from object sign within 300ms
  expect no say "zzz" on public from object sign within 200ms
}
test "two" {
  expect say "stale" on public from object sign within 150ms
}
`
	res := play(t, f, src)
	wantExit(t, res, 1)
	if len(res.Tests) != 2 || !res.Tests[0].Passed || res.Tests[1].Passed {
		t.Fatalf("tests = %+v\n%s", res.Tests, res.Transcript)
	}
	mustHave(t, res, `slate: pass test "one"`, `slate: fail t.slate test "two" step 1`, "stimulus: (none)", "slate: failed 1 of 2 tests")
}

func TestANegativeSayFailsAtOnceAndOtherwiseHoldsTheStep(t *testing.T) {
	src := hdr + "say \"go\" on 0\nexpect no say \"error\" on public from object sign within 400ms\n"

	f := newGrid(t)
	f.on("go", signSays("error"))
	t0 := time.Now()
	res := play(t, f, src)
	if took := time.Since(t0); took > 300*time.Millisecond {
		t.Errorf("a forbidden line failed the step after %s, want at once", took)
	}
	wantExit(t, res, 1)
	mustHave(t, res, `forbidden no say "error" on public from object sign within 400ms at`, `chat public from sign: "error"`)

	f = newGrid(t)
	f.on("go", signSays("fine"))
	t0 = time.Now()
	res = play(t, f, src)
	wantExit(t, res, 0)
	if took := time.Since(t0); took < 400*time.Millisecond {
		t.Errorf("a negative passed after %s, want its whole 400ms window", took)
	}

	// A positive match does not end a step that has a negative window.
	f = newGrid(t)
	f.on("go", signSays("ok"))
	t0 = time.Now()
	res = play(t, f, hdr+"say \"go\" on 0\nexpect say \"ok\" on public from object sign within 300ms\nexpect no say \"error\" on public from object sign within 300ms\n")
	wantExit(t, res, 0)
	if took := time.Since(t0); took < 300*time.Millisecond {
		t.Errorf("step passed after %s, before its negative window", took)
	}
}

func TestMatchingAndExact(t *testing.T) {
	for _, c := range []struct {
		clause, line string
		want         bool
	}{
		{`"hello"`, "hello", true},
		{`"hello"`, "hello there", false}, // exact is not a substring
		{`"hello"`, "Hello", false},       // and is case-sensitive
		{`matching "ell"`, "hello there", true},
		{`matching "^hello$"`, "hello there", false},
		{`matching "(?i)HELLO"`, "say hello", true},
		{`matching "^Thanks, .+!$"`, "Thanks, Example!", true},
	} {
		f := newGrid(t)
		f.on("go", signSays(c.line))
		res := play(t, f, hdr+"say \"go\" on 0\nexpect say "+c.clause+" on public from object sign within 100ms\n")
		if got := res.Exit == 0; got != c.want {
			t.Errorf("%s against %q: passed = %v, want %v", c.clause, c.line, got, c.want)
		}
	}
}

func TestRegionChatIsPrintedAndNeverMatched(t *testing.T) {
	f := newGrid(t)
	f.on("go", chatMsg("Example Sign", idSign, sl.ChatRegion, "hello"))
	res := play(t, f, hdr+"say \"go\" on 0\nexpect say \"hello\" on public from object sign within 100ms\n")
	wantExit(t, res, 1)
	mustHave(t, res, `chat region from sign: "hello"`)

	f = newGrid(t)
	f.on("go", chatMsg("Example Sign", idSign, sl.ChatRegion, "error"))
	res = play(t, f, hdr+"say \"go\" on 0\nexpect no say \"error\" on public from anyone within 100ms\n")
	wantExit(t, res, 0)
}

func TestSayChannels(t *testing.T) {
	for _, c := range []struct {
		on   string
		kind uint8
		want bool
	}{
		{"public", sl.ChatSay, true}, {"public", sl.ChatWhisper, true}, {"public", sl.ChatShout, true},
		{"0", sl.ChatSay, true}, {"public", sl.ChatOwner, false}, {"public", sl.ChatDebug, false},
		{"owner", sl.ChatOwner, true}, {"owner", sl.ChatSay, false},
		{"debug", sl.ChatDebug, true}, {"2147483647", sl.ChatDebug, true}, {"debug", sl.ChatDirect, false},
		{"direct", sl.ChatDirect, true}, {"direct", sl.ChatSay, false},
	} {
		f := newGrid(t)
		f.on("go", chatMsg("Example Sign", idSign, c.kind, "hi"))
		res := play(t, f, hdr+"say \"go\" on 0\nexpect say \"hi\" on "+c.on+" from object sign within 100ms\n")
		if got := res.Exit == 0; got != c.want {
			t.Errorf("on %s, line type %s: passed = %v, want %v", c.on, sl.ChatTypeName(c.kind), got, c.want)
		}
	}
	// A channel only the bridge can hear is matched in bridge_test.go.
}

func TestSpeakers(t *testing.T) {
	type line struct {
		source msg.UUID
		from   string
	}
	sign := line{idSign, "Example Sign"}
	tester := line{testMe, testAvatar}
	visitor := line{idVisitor, "Example Resident"}
	stranger := line{idStranger, "Example Stranger"}
	for _, c := range []struct {
		from string
		line line
		want bool
	}{
		{"tester", tester, true},
		{"tester", visitor, false},
		{"owner of sign", tester, true}, // the tester owns the sign
		{"owner of sign", stranger, false},
		{"owner of vendor", stranger, true},
		{"owner of vendor", tester, false},
		{`avatar "example resident"`, visitor, true}, // EqualFold of the whole name
		{`avatar "EXAMPLE RESIDENT"`, visitor, true},
		{`avatar "Example"`, visitor, false}, // a prefix is not the name
		{`avatar "Resident"`, visitor, false},
		{`avatar "Example Resident Jr"`, visitor, false},
		{"object sign", sign, true},
		{"object sign", visitor, false},
		{"object vendor", sign, false},
		{"anyone", visitor, true},
		{"anyone", sign, true},
	} {
		f := newGrid(t)
		f.on("go", chatMsg(c.line.from, c.line.source, sl.ChatSay, "hi"))
		res := play(t, f, hdr+"say \"go\" on 0\nexpect say \"hi\" on public from "+c.from+" within 100ms\n")
		if got := res.Exit == 0; got != c.want {
			t.Errorf("from %s, line from %q: passed = %v, want %v\n%s", c.from, c.line.from, got, c.want, res.Transcript)
		}
	}
}

func TestSayAs(t *testing.T) {
	// As the owner, which the tester is for the sign; as the avatar, by
	// the whole name ignoring case.
	f := newGrid(t)
	res := play(t, f, hdr+"say \"a\" on 0 as owner of sign\nsay \"b\" on 0 as avatar \"quark IDLEMIND\"\nsay \"c\" on -7 as tester\n")
	wantExit(t, res, 0)
	if got := strings.Join(f.said(), ","); got != "a,b,c" {
		t.Errorf("said %q", got)
	}
	if r := f.replies(); len(r) != 1 || r[0].Data.ChatChannel != -7 {
		t.Errorf("negative say went as %+v", r)
	}

	for _, c := range []struct{ as, want string }{
		{"owner of vendor", "cannot speak as the owner of vendor: the tester is not its owner"},
		{`avatar "Quark"`, "this runner drives only the tester"},
		{`avatar "Example Resident"`, "this runner drives only the tester"},
	} {
		f := newGrid(t)
		res := play(t, f, hdr+"say \"nope\" on 0 as "+c.as+"\n")
		wantExit(t, res, 1)
		mustHave(t, res, c.want, "not sent: "+c.want)
		if len(f.said()) != 0 {
			t.Errorf("as %s: said %q before refusing", c.as, f.said())
		}
	}
}

func TestTheOwnerOfAnObjectThatHasNoOwnerYet(t *testing.T) {
	// Seen.Owner is zero, so Properties is asked: unanswered, the step
	// fails with the sentence and nothing is said.
	f := newGrid(t)
	f.objects[0].Owner = msg.UUID{}
	res := play(t, f, hdr+"say \"x\" on 0 as owner of sign\n")
	wantExit(t, res, 1)
	mustHave(t, res, "the owner of sign is not known")
	if len(f.said()) != 0 {
		t.Errorf("said %q", f.said())
	}

	// Answered, the owner is read from it.
	f = newGrid(t)
	f.objects[0].Owner = msg.UUID{}
	f.replyTo(func(m msg.Message) {
		if sel, ok := m.(*msg.ObjectSelect); ok && len(sel.ObjectData) > 0 {
			p := &msg.ObjectProperties{}
			p.ObjectData = []msg.ObjectProperties_ObjectData{{ObjectID: idSign, OwnerID: testMe, Name: []byte("Example Sign\x00")}}
			f.relay(p)
		}
	})
	res = play(t, f, hdr+"say \"x\" on 0 as owner of sign\n")
	wantExit(t, res, 0)
}

func TestBeforeAndAfterEachOrderAndNumbering(t *testing.T) {
	f := newGrid(t)
	src := hdr + `
before each {
  say "before" on 0
}
after each {
  say "after" on 0
}
test "one" {
  say "one" on 0
}
test "two" {
  say "two" on 0
  expect say "never" on public from object sign within 100ms
  say "unreached" on 0
}
test "three" {
  say "three" on 0
}
`
	res := play(t, f, src)
	wantExit(t, res, 1)
	want := "before one after before two after before three after"
	if got := strings.Join(f.said(), " "); got != want {
		t.Errorf("said %q, want %q (after each runs after a failure, the next test runs, the rest of a failed test does not)", got, want)
	}
	if len(res.Tests) != 3 || !res.Tests[0].Passed || res.Tests[1].Passed || !res.Tests[2].Passed {
		t.Fatalf("tests = %+v", res.Tests)
	}
	ls := lines(res)
	var steps []string
	for _, l := range ls {
		if strings.HasPrefix(l, "slate: pass step") || strings.HasPrefix(l, "slate: test") || strings.HasPrefix(l, "slate: fail") {
			steps = append(steps, l)
		}
	}
	want2 := []string{
		`slate: test "one"`, "slate: pass step 1", "slate: pass step 2", "slate: pass step 3",
		`slate: test "two"`, "slate: pass step 1",
		// the failed body step is 2; after each is step 4 (the body has two more)
		"slate: pass step 4",
		`slate: fail t.slate test "two" step 2 lines 15-16`,
		`slate: test "three"`, "slate: pass step 1", "slate: pass step 2", "slate: pass step 3",
		"slate: failed 1 of 3 tests",
	}
	if strings.Join(steps, "\n") != strings.Join(want2, "\n") {
		t.Errorf("steps:\n%s\nwant:\n%s", strings.Join(steps, "\n"), strings.Join(want2, "\n"))
	}
}

func TestAFailureInAfterEachIsReportedAsOne(t *testing.T) {
	f := newGrid(t)
	src := hdr + `
after each {
  say "a1" on 0
  expect say "never" on public from object sign within 100ms
  say "a2" on 0
}
test "one" {
  say "one" on 0
  expect say "never" on public from object sign within 100ms
}
`
	res := play(t, f, src)
	wantExit(t, res, 1)
	if got := strings.Join(f.said(), " "); got != "one a1" {
		t.Errorf("said %q: the rest of after each is skipped", got)
	}
	ls := strings.Join(lines(res), "\n")
	first := strings.Index(ls, `test "one" step 1 `)
	second := strings.Index(ls, `test "one" after each step 2 `)
	if first < 0 || second < first {
		t.Errorf("the test's own block comes first, then the after each one:\n%s", ls)
	}
}

func TestRunSelectsTestsByName(t *testing.T) {
	f := newGrid(t)
	src := hdr + "test \"alpha\" {\n say \"alpha\" on 0\n}\ntest \"beta\" {\n say \"beta\" on 0\n}\n"
	res := playWith(t, f, src, Options{Run: regexp.MustCompile("^be")}, testCfg())
	wantExit(t, res, 0)
	if len(res.Tests) != 1 || res.Tests[0].Name != "beta" {
		t.Fatalf("tests = %+v", res.Tests)
	}
	if got := f.said(); len(got) != 1 || got[0] != "beta" {
		t.Errorf("said %q", got)
	}
	mustNotHave(t, res, "alpha")
	mustHave(t, res, "slate: passed 1 tests")
}

func TestAFailureInASequenceNamesTheCalls(t *testing.T) {
	f := newGrid(t)
	src := hdr + `
sequence inner {
  say "in" on 0
  expect say "never" on public from object sign within 100ms
}
sequence outer {
  do inner
}
test "calls" {
  say "first" on 0
  do outer
}
`
	res := play(t, f, src)
	wantExit(t, res, 1)
	mustHave(t, res, `slate: fail t.slate test "calls" step 2 lines 6-7 via do inner at line 10 via do outer at line 14`)
}

func TestSetupFailures(t *testing.T) {
	f := newGrid(t)
	res, err := tryPlay(t, f, "slate 1\nobject gone is \"Example Missing\"\nsay \"x\" on 0\n", Options{}, testCfg())
	if err == nil {
		t.Error("a setup failure returned no error")
	}
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: "Example Missing" is not in the region, or is beyond the draw distance (looked up for 200ms)`)
	if len(res.Tests) != 0 || len(f.said()) != 0 {
		t.Errorf("tests %+v said %q after a setup failure", res.Tests, f.said())
	}

	f = newGrid(t)
	f.objects = append(f.objects, prim(idStray, 103, "Example Sign", idStranger))
	res, err = tryPlay(t, f, hdr+"say \"x\" on 0\n", Options{}, testCfg())
	if err == nil {
		t.Error("a setup failure returned no error")
	}
	wantExit(t, res, 3)
	mustHave(t, res, `slate: setup: "Example Sign" names 2 objects`)

}

func TestADroppedChatSubscriptionStopsTheRun(t *testing.T) {
	f := newGrid(t)
	// While the tester's say is being sent the runner is not reading, so a
	// burst overflows the two-line buffer.
	f.replyTo(func(m msg.Message) {
		if text, _, ok := says(m); ok && text == "flood" {
			for range 10 {
				f.relay(signSays("noise"))
			}
		}
	})
	cfg := testCfg()
	cfg.chatDepth = 2
	src := hdr + "test \"one\" {\n say \"flood\" on 0\n}\ntest \"two\" {\n say \"two\" on 0\n}\n"
	res := playWith(t, f, src, Options{}, cfg)
	wantExit(t, res, 1)
	if len(res.Tests) != 1 || res.Tests[0].Passed {
		t.Fatalf("tests = %+v\n%s", res.Tests, res.Transcript)
	}
	mustHave(t, res, "slate: the chat subscription dropped 8 lines")
	mustNotHave(t, res, `slate: test "two"`)
	if got := f.said(); len(got) != 1 {
		t.Errorf("said %q: nothing after the drop may run", got)
	}
}

func TestACancelledContextEndsTheRun(t *testing.T) {
	f := newGrid(t)
	s := mustCheck(t, hdr+"say \"go\" on 0\nexpect say \"never\" on public from object sign within 30s\n")
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	t0 := time.Now()
	res, err := run(ctx, f.session(t), s, Options{}, testCfg())
	if err == nil || res == nil {
		t.Fatalf("res %v err %v", res, err)
	}
	if took := time.Since(t0); took > 2*time.Second {
		t.Errorf("a cancelled run took %s to stop", took)
	}
	wantExit(t, res, 1)
}

func TestARunThatTheSessionEndsUnderIsAnError(t *testing.T) {
	f := newGrid(t)
	s := mustCheck(t, hdr+"say \"go\" on 0\nexpect say \"never\" on public from object sign within 30s\n")
	sess := f.session(t)
	time.AfterFunc(100*time.Millisecond, func() { f.Close() })
	res, err := run(context.Background(), sess, s, Options{}, testCfg())
	if err == nil {
		t.Fatalf("no error:\n%s", res.Transcript)
	}
	wantExit(t, res, 1)
}

func TestStepBeginsWithAStimulusArmsJustBeforeIt(t *testing.T) {
	// A line heard before a stimulus step is not eligible for it, though
	// it is still in the buffer when the step begins.
	f := newGrid(t)
	f.quietly(t, 20*time.Millisecond, signSays("early"))
	f.on("go", signSays("late"))
	src := hdr + `
test "t1" {
  say "wait" on 0
  expect no say "zzz" on public from object sign within 100ms
  say "go" on 0
  expect say "early" on public from object sign within 100ms
}
`
	res := play(t, f, src)
	wantExit(t, res, 1)
	mustHave(t, res, "slate: pass step 1", `unmatched say "early"`)
}
