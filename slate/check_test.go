package slate

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/sl"
)

func TestNilScript(t *testing.T) {
	if err := Check(nil); err == nil {
		t.Fatal("nil script was accepted")
	}
}

func TestDurationBounds(t *testing.T) {
	ok := []string{"100ms", "10s", "120s", "2m", "120000ms"}
	bad := []string{"99ms", "0s", "121s", "3m", "120001ms"}
	for _, d := range ok {
		mustCheck(t, "slate 1\ntimeout "+d+"\nobject a is \"A\"\ntouch a anywhere\n")
	}
	for _, d := range bad {
		checkErr(t, "slate 1\ntimeout "+d+"\nobject a is \"A\"\ntouch a anywhere\n", "outside 100ms to 120s")
	}
	checkErr(t, "slate 1\ntimeout 10s\ntimeout 10s\nobject a is \"A\"\ntouch a anywhere\n", "timeout is set twice")
	checkErr(t, "slate 1\nobject a is \"A\"\ndrag a face 0 from 0.1 0.5 to 0.9 0.5 over 50ms\n", "outside 100ms to 120s")
}

func TestOrigin(t *testing.T) {
	const want = "at 0 0 is the middle of the face; placeTouches treats a zero ST as not given (sl/touch.go)"
	bad := []string{
		"touch a face 0 at 0 0",
		"touch a face 0 at 0.0 0.0",
		"touch a face 0 at 0 0.0",
		"touch a face 0 at -0 -0",
		"drag a face 0 from 0 0 to 0.9 0.5",
		"drag a face 0 from 0.1 0.5 to 0 0",
		"drag a face 0 from 0.0 0 to 0.2 0.2",
	}
	for _, line := range bad {
		checkErr(t, "slate 1\nobject a is \"A\"\n"+line+"\n", want)
	}
	mustCheck(t, "slate 1\nobject a is \"A\"\ntouch a face 0 at 0 0.5\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\ntouch a face 0 at 0.0 0.5\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\nexpect offset a face 0 is 0 0\n")
}

func TestPayGate(t *testing.T) {
	checkErr(t, "slate 1\nobject a is \"A\"\npay a L$5\n", "pay needs allow pay")
	checkErr(t, "slate 1\nallow pay\nallow pay\nobject a is \"A\"\npay a L$1\n", "allow pay is set twice")
	mustCheck(t, "slate 1\nallow pay\nobject a is \"A\"\ntouch a anywhere\n")
	checkErr(t, "slate 1\nallow pay\nobject a is \"A\"\npay a L$0\n", "at least L$1")
	checkErr(t, "slate 1\nallow pay\nobject a is \"A\"\npay a -3\n", "at least L$1")
	checkErr(t, "slate 1\nallow pay\nobject a is \"A\"\npay a "+strings.Repeat("9", 40)+"\n", "does not fit in an integer")
	reason := strings.Repeat("r", sl.MaxPayReason)
	mustCheck(t, "slate 1\nallow pay\nobject a is \"A\"\npay a 5 reason \""+reason+"\"\n")
	checkErr(t, "slate 1\nallow pay\nobject a is \"A\"\npay a 5 reason \""+reason+"x\"\n", "payment carries")
	s := mustCheck(t, "slate 1\nallow pay\nobject a is \"A\"\npay a 5\n")
	if body(s)[0].Stimulus.Pay.Linden || body(s)[0].Stimulus.Pay.HasReason {
		t.Fatal("plain amount grew a symbol or a reason")
	}
}

func TestObjectsAndNames(t *testing.T) {
	checkErr(t, "slate 1\nobject a is \"A\"\nobject a is \"B\"\ntouch a anywhere\n", "a is already bound")
	checkErr(t, "slate 1\nobject a is \"A\"\nobject b is \"A\"\ntouch a anywhere\n", "already used")
	checkErr(t, "slate 1\nobject a is \"\"\ntouch a anywhere\n", "in-world name is empty")
	mustCheck(t, "slate 1\nobject a is \"Example Shop\"\nobject b is \"example shop\"\ntouch a anywhere\n")
	checkErr(t, "slate 1\nobject a is \"A\"\ntouch missing anywhere\n", "missing is not an object")
}

func TestProbeChannelsAndTheListenCap(t *testing.T) {
	mustCheck(t, "slate 1\nprobe a\nobject a is \"A\"\ntouch a link 1\n")
	checkErr(t, "slate 1\nobject a is \"A\"\nprobe missing\ntouch a anywhere\n", "missing is not an object")
	checkErr(t, "slate 1\nobject a is \"A\"\nprobe a\nprobe a\ntouch a anywhere\n", "already has a probe")
	checkErr(t, "slate 1\nobject a is \"A\"\nlisten 0\ntouch a anywhere\n", "public chat")
	checkErr(t, "slate 1\nobject a is \"A\"\nlisten 2147483647\ntouch a anywhere\n", "debug chat")
	checkErr(t, "slate 1\nobject a is \"A\"\nlisten 4\nlisten 4\ntouch a anywhere\n", "channel 4 is already used")
	checkErr(t, "slate 1\nobject a is \"A\"\nlisten 2147483648\ntouch a anywhere\n", "outside -2147483648 to 2147483647")
	checkErr(t, "slate 1\nobject a is \"A\"\nlisten -2147483649\ntouch a anywhere\n", "outside -2147483648 to 2147483647")

	var b strings.Builder
	b.WriteString("slate 1\nobject a is \"A\"\n")
	for i := 1; i <= 63; i++ {
		b.WriteString("listen ")
		b.WriteString(itoa(i))
		b.WriteByte('\n')
	}
	b.WriteString("touch a anywhere\n")
	mustCheck(t, b.String())
	var over strings.Builder
	over.WriteString("slate 1\nobject a is \"A\"\n")
	for i := 1; i <= 64; i++ {
		over.WriteString("listen ")
		over.WriteString(itoa(i))
		over.WriteByte('\n')
	}
	over.WriteString("touch a anywhere\n")
	checkErr(t, over.String(), "the bridge opens at most 63 listens")

	// Probes open no listens of their own.
	var probes strings.Builder
	probes.WriteString("slate 1\n")
	for i := 0; i < 70; i++ {
		probes.WriteString("object o" + itoa(i) + " is \"O" + itoa(i) + "\"\nprobe o" + itoa(i) + "\n")
	}
	probes.WriteString("touch o0 anywhere\n")
	mustCheck(t, probes.String())
}

func TestProbeHeader(t *testing.T) {
	mustCheck(t, "slate 1\nobject x is \"X\"\nprobe x\ntouch x anywhere\n")
	parseErr(t, "slate 1\nobject x is \"X\"\nprobe x command 1 report 2\n", "found command")
	checkErr(t, "slate 1\nobject x is \"X\"\nprobe x\nprobe x\ntouch x anywhere\n", "x already has a probe")
	checkErr(t, "slate 1\nprobe x\ntouch x anywhere\n", "x is not an object")
}

func TestSayAnswerAndChooseLengths(t *testing.T) {
	negOK := strings.Repeat("a", sl.MaxDialogReply)
	negBad := strings.Repeat("a", sl.MaxDialogReply+1)
	posOK := strings.Repeat("b", maxSay)
	posBad := strings.Repeat("b", maxSay+1)
	mustCheck(t, "slate 1\nobject a is \"A\"\nsay \""+negOK+"\" on -1\n")
	checkErr(t, "slate 1\nobject a is \"A\"\nsay \""+negBad+"\" on -1\n", "negative channel carries at most")
	mustCheck(t, "slate 1\nobject a is \"A\"\nsay \""+posOK+"\" on 1\n")
	checkErr(t, "slate 1\nobject a is \"A\"\nsay \""+posBad+"\" on 0\n", "a channel carries at most")
	mustCheck(t, "slate 1\nobject a is \"A\"\nanswer \""+negOK+"\" on a\n")
	checkErr(t, "slate 1\nobject a is \"A\"\nanswer \""+negBad+"\" on a\n", "text box carries at most")
	mustCheck(t, "slate 1\nobject a is \"A\"\nchoose \""+posBad+"\" on a\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\nsay \"café\" on 0\n")
	checkErr(t, "slate 1\nobject a is \"A\"\nsay \"hi\" on 2147483648\n", "outside -2147483648 to 2147483647")
	mustCheck(t, "slate 1\nobject a is \"A\"\nsay \"hi\" on -2147483648\n")
}

func TestChannelsThatNeedAListen(t *testing.T) {
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect say \"p\" on 1 from object a\n", "channel 1 needs a listen")
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect no say \"p\" on -7 from object a\n", "channel -7 needs a listen")
	mustCheck(t, "slate 1\nobject a is \"A\"\nlisten 1\nexpect say \"p\" on 1 from object a\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\nexpect say \"p\" on 0 from object a\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\nexpect say \"p\" on public from object a\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\nexpect say \"p\" on 2147483647 from object a\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\nexpect say \"p\" on debug from tester\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\nexpect say \"p\" on owner from avatar \"Example Resident\"\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\nexpect say \"p\" on direct from owner of a\n")
}

// A link N on a touch or a drag is the store's to number; only the
// link messages need the probe, which is the mechanism there.
func TestOnlyLinkMessagesNeedAProbeAndLinkTextIsASCII(t *testing.T) {
	mustCheck(t, "slate 1\nobject a is \"A\"\ntouch a link 1\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\ndrag a link 1 face 0 from 0.1 0.5 to 0.9 0.5\n")
	checkErr(t, "slate 1\nobject a is \"A\"\nsend on a from link 1 to link root num 1 text \"hi\"\n", "send on a needs a probe")
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect link on a from link 1 num 1 text \"hi\"\n", "expect link on a needs a probe")
	mustCheck(t, "slate 1\nobject a is \"A\"\ntouch a face 1\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\ndrag a face 0 from 0.1 0.5 to 0.9 0.5\n")
	checkErr(t, "slate 1\nobject a is \"A\"\nprobe a\nsend on a from link 1 to link all num 1 text \"café\"\n", "bytes 0x20-0x7E")
	checkErr(t, "slate 1\nobject a is \"A\"\nprobe a\nexpect link on a from link 1 num 1 text \"\x7f\"\n", "bytes 0x20-0x7E")
	mustCheck(t, "slate 1\nobject a is \"A\"\nprobe a\nsend on a from link 1 to link all num 1 text \"say \\\"hi\\\"\"\n")
	mustCheck(t, "slate 1\nobject a is \"A\"\nprobe a\nexpect link on a from link 1 num 1 text \"ready\"\n")
}

func TestRelayLineLength(t *testing.T) {
	got := relayLine(1, -2, 7, nullKey, "ready")
	want := `slprobe/2 0000000000000000 relay 00000000-0000-0000-0000-000000000000 -2147483648 ` +
		`slprobe/2 0000000000000000 send 1 -2 7 00000000-0000-0000-0000-000000000000 "ready"`
	if got != want {
		t.Fatalf("got %s", got)
	}
	got = relayLine(1, -2, 7, nullKey, "say \"hi\"")
	if !strings.HasSuffix(got, ` "say \"hi\""`) {
		t.Fatalf("got %s", got)
	}
	// The line is fixed text plus the quoted text; find the room for text.
	const pre = "slate 1\nobject a is \"A\"\nprobe a\nsend on a from link 1 to link others num 7 text \""
	room := maxSay - len(relayLine(1, -2, 7, nullKey, ""))
	// "link others" is -2, and the empty text quotes to two bytes.
	at := strings.Repeat("a", room)
	mustCheck(t, pre+at+"\"\n")
	if n := len(relayLine(1, -2, 7, nullKey, at)); n != maxSay {
		t.Fatalf("line is %d bytes, want %d", n, maxSay)
	}
	checkErr(t, pre+at+"a\"\n", "relayed command is 1024 bytes")
	// Quoting counts: a backslash-quote is two bytes on the wire.
	quotes := strings.Repeat("\\\"", room/2)
	mustCheck(t, pre+quotes+"\"\n")
	checkErr(t, pre+quotes+"\\\"\"\n", "relayed command is")
}

func TestRezNamesAreVisibleOnlyLater(t *testing.T) {
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect rez name \"Example Balloon\" from a\n", "a rez names the new object with as")
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect no rez name \"Example Balloon\" from a as left\n", "a negative rez has no as")
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect rez name \"Example Balloon\" from a as left\nexpect say \"x\" on public from object left\n", "bound in this step")
	checkErr(t, "slate 1\nobject left is \"L\"\nexpect rez name \"Example Balloon\" from left as left\n", "left is already bound")
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect rez name \"B\" from a as left\nexpect rez name \"C\" from a as left\n", "left is already bound")
	mustCheck(t, `
slate 1
object a is "A"
expect rez name "Example Balloon" description "left" from a as left
expect rez name "Example Balloon" description "right" from a as right
touch left anywhere
expect click right is touch
expect no rez name "Other" from a
`)
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect rez name \"B\" from left as left\n", "bound in this step")
}

func TestButtonsPatternsAndFloats(t *testing.T) {
	checkErr(t, "slate 1\nobject a is \"A\"\ntouch a button text \"   \"\n", "button text is empty")
	checkErr(t, "slate 1\nobject a is \"A\"\ntouch a button text \"\"\n", "button text is empty")
	mustCheck(t, "slate 1\nobject a is \"A\"\ntouch a button text \" Open \"\n")
	checkErr(t, "slate 1\nobject a is \"A\"\ntouch a button pattern \"[\"\n", "pattern")
	mustCheck(t, "slate 1\nobject a is \"A\"\ntouch a button pattern \"^Menu$\"\n")
	checkErr(t, "slate 1\nobject a is \"A\"\ntouch a button 0 text \"A\"\n", "not a match; the first is 1")
	checkErr(t, "slate 1\nobject a is \"A\"\ntouch a button -1 text \"A\"\n", "not a match; the first is 1")
	mustCheck(t, "slate 1\nobject a is \"A\"\ntouch a button 1 text \"A\" box circle image \"logo\" oval\n")
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect offset a face 0 is 1.1 0\n", "outside -1 to 1")
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect rotation a face 0 is -1.01\n", "outside -1 to 1")
	mustCheck(t, "slate 1\nobject a is \"A\"\nexpect offset a face 0 is -1 1\nexpect rotation a face 0 is 1\nexpect repeats a face 0 is 0 -2\n")
	checkErr(t, "slate 1\nobject a is \"A\"\nexpect repeats a face 0 is "+strings.Repeat("9", 40)+" 1\n", "not an exact float")
}

func TestClickAndTextureNegativesPassTheStaticCheck(t *testing.T) {
	// A missing reading is not a pass. That is a run-time rule. The file is legal.
	mustCheck(t, `
slate 1
object a is "A"
expect no click a is sit
expect no texture a face 0 is 17ca7e57-7e57-c0de-ef46-5feae5a71169
`)
}

func TestLinkWords(t *testing.T) {
	for word, n := range LinkWords {
		s := mustCheck(t, "slate 1\nobject a is \"A\"\nprobe a\nsend on a from link 1 to link "+word+" num 1 text \"x\"\n")
		if body(s)[0].Stimulus.Send.To.Word != word {
			t.Fatalf("%s stored as %s", word, body(s)[0].Stimulus.Send.To.Word)
		}
		if got := LinkWords[word]; got != n {
			t.Fatalf("%s = %d", word, got)
		}
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func TestADragLongerThanItsBudgetIsRefused(t *testing.T) {
	checkSource := func(t *testing.T, src string) error {
		s, err := Parse("t.slate", []byte(src))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		return Check(s)
	}
	head := "slate 1\nobject s is \"Example Slider\"\n"
	ok := head + "drag s face 0 from 0.1 0.5 to 0.9 0.5 over 2s\nexpect offset s face 0 is 0.4 0 within 3s\n"
	if err := checkSource(t, ok); err != nil {
		t.Fatalf("a drag inside its budget: %v", err)
	}
	bad := head + "drag s face 0 from 0.1 0.5 to 0.9 0.5 over 2s\nexpect offset s face 0 is 0.4 0 within 1s\n"
	if err := checkSource(t, bad); err == nil || !strings.Contains(err.Error(), "longer than the step's budget of 1s") {
		t.Fatalf("a drag longer than its budget: %v", err)
	}
	dflt := "slate 1\ntimeout 1s\nobject s is \"Example Slider\"\ndrag s face 0 from 0.1 0.5 to 0.9 0.5 over 2s\n"
	if err := checkSource(t, dflt); err == nil {
		t.Fatal("a drag longer than the timeout with no within was accepted")
	}
}

// TestADragsPressAndDwellCountAgainstTheBudget: a drag blocks for its
// press, move and dwell together, and the three must fit the step.
func TestADragsPressAndDwellCountAgainstTheBudget(t *testing.T) {
	mustCheck(t, "slate 1\nobject a is \"A\"\ndrag a face 0 from 0.1 0.5 to 0.9 0.5 over 1s press 2s dwell 2s\n")
	src := "slate 1\nobject a is \"A\"\ndrag a face 0 from 0.1 0.5 to 0.9 0.5 over 4s press 4s dwell 4s\n"
	s, err := Parse("t.slate", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if err := Check(s); err == nil || !strings.Contains(err.Error(), "can take 12s") {
		t.Errorf("a 12 s drag in a 10 s step: %v", err)
	}
}

// TestAWaitMustFitItsStep: a wait blocks, so it must fit the step's
// budget, and is a duration in the usual range.
func TestAWaitMustFitItsStep(t *testing.T) {
	mustCheck(t, "slate 1\nobject a is \"A\"\nwait 2s\n")
	for _, c := range []struct{ src, want string }{
		{"slate 1\nobject a is \"A\"\nwait 12s\n", "longer than the step's budget"},
		{"slate 1\nobject a is \"A\"\nwait 50ms\n", "outside 100ms to 120s"},
	} {
		s, err := Parse("t.slate", []byte(c.src))
		if err != nil {
			t.Fatal(err)
		}
		if err := Check(s); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: %v, want %q", c.src, err, c.want)
		}
	}
}
