package main

// The terminal, driven the way a person drives it.
//
// goterm gives us a real pseudo-terminal and a screen to read back, so
// these are the only tests that exercise what slchat actually is: raw
// mode, the key decoder against real terminal bytes, and the rule that
// everything the display rests on -- a message arriving while a
// sentence is half typed must land above it and leave it alone.
//
// The grid behind it is a fake.  Nothing here needs a region to stand
// in or two avatars to talk to; what is being tested is the screen.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goexvi-ctrl/goterm"
	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// The harness runs slchat's real terminal and app against a fake grid.
// It is this test binary, re-executed on the pseudo-terminal: that way
// what runs under goterm is the code being tested rather than a copy of
// it.
const harnessEnv = "SLCHAT_PTY_HARNESS"

func TestMain(m *testing.M) {
	if os.Getenv(harnessEnv) == "1" {
		harness()
		return
	}
	os.Exit(m.Run())
}

var (
	harnessMe    = msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	harnessOther = msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
)

// harness is the program goterm launches.
func harness() {
	g := &fakeGrid{messages: make(chan *client.Message, 16), done: make(chan struct{})}

	t, err := NewTerm(os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer t.Close()

	app, err := NewApp(
		Config{Addr: "fake", Prefix: 27}, t, g,
		&pb.AgentInfo{
			Name:       "harness",
			AgentId:    harnessMe.String(),
			SessionId:  msg.MustParseUUID("c9137e57-7e57-c0de-418e-43edaf0e2dbb").String(),
			AvatarName: "Harness Resident",
			Region:     "Nowhere",
		},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	app.Run(context.Background())
	t.Close()
}

// fakeGrid answers for the grid, and talks back.
//
// Anything said on open chat is answered, after a delay, by somebody
// else saying something in the region.  The delay is the point: it puts
// an arriving message in the middle of the next thing being typed,
// which is the case the display exists to get right.
type fakeGrid struct {
	messages chan *client.Message
	done     chan struct{}

	mu   sync.Mutex
	sent []msg.Message
}

const fakeReplyDelay = 400 * time.Millisecond

func (g *fakeGrid) Send(ctx context.Context, m msg.Message, reliable bool) error {
	g.mu.Lock()
	g.sent = append(g.sent, m)
	g.mu.Unlock()

	// Names are asked for and answered, the way the simulator does
	// it: an avatar in a listing is a uuid until this round trip has
	// happened.
	if r, ok := m.(*msg.UUIDNameRequest); ok {
		reply := &msg.UUIDNameReply{}
		for _, b := range r.UUIDNameBlock {
			reply.UUIDNameBlock = append(reply.UUIDNameBlock, msg.UUIDNameReply_UUIDNameBlock{
				ID: b.ID, FirstName: nulTerm("Someone"), LastName: nulTerm("Else"),
			})
		}
		go g.deliver(reply)
		return nil
	}

	if c, ok := m.(*msg.ChatFromViewer); ok {
		said := trimNul(c.ChatData.Message)
		go func() {
			time.Sleep(fakeReplyDelay)
			reply := &msg.ChatFromSimulator{}
			reply.ChatData.FromName = nulTerm("Someone Else")
			reply.ChatData.SourceID = harnessOther
			reply.ChatData.SourceType = 1 // an agent
			reply.ChatData.ChatType = 1
			reply.ChatData.Message = nulTerm("you said " + said)
			g.deliver(reply)
		}()
	}
	return nil
}

// deliver puts a message on the relay, the way slgod would.
func (g *fakeGrid) deliver(m msg.Message) {
	b, err := m.Encode()
	if err != nil {
		return
	}
	info := m.MsgInfo()
	select {
	case g.messages <- &client.Message{ID: info.ID, Name: info.Name, Body: b, At: time.Now()}:
	case <-g.done:
	}
}

func (g *fakeGrid) Messages() <-chan *client.Message { return g.messages }
func (g *fakeGrid) Done() <-chan struct{}            { return g.done }
func (g *fakeGrid) Err() error                       { return nil }

func (g *fakeGrid) Objects(ctx context.Context, named, id string) (*pb.ObjectsResponse, error) {
	return &pb.ObjectsResponse{Objects: []*pb.ObjectInfo{{
		Id: harnessOther.String(), Pcode: pcodeAvatar,
		Position: &pb.Vector3{X: 10, Y: 0, Z: 0},
	}}}, nil
}

func (g *fakeGrid) Presence(ctx context.Context, drawDistance float32) (*pb.PresenceResponse, error) {
	return &pb.PresenceResponse{Region: "Nowhere", Position: &pb.Vector3{X: 1, Y: 2, Z: 3}}, nil
}

func (g *fakeGrid) Region(ctx context.Context) (*pb.RegionInfo, error) {
	return &pb.RegionInfo{Name: "Nowhere", Known: true,
		Id: "e0627e57-7e57-c0de-f645-2204f669d7f1"}, nil
}

func (g *fakeGrid) Friends(ctx context.Context) ([]*pb.Friend, error) {
	return []*pb.Friend{{Id: harnessOther.String(), Online: true}}, nil
}

func (g *fakeGrid) NoteFriend(ctx context.Context, id msg.UUID, online bool) error { return nil }

// The search capability, answering with as many people as were asked
// for: "lookup 30" produces thirty, which is what a pager needs.
func (g *fakeGrid) HasCap(name string) bool { return name == capAvatarPicker }

func (g *fakeGrid) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	u, err := url.Parse(r.Path)
	if err != nil {
		return nil, err
	}
	want := u.Query().Get("names")

	n := 3
	if k, err := strconv.Atoi(strings.TrimSpace(want)); err == nil {
		n = k
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" ?><llsd><map><key>agents</key><array>`)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, `<map>`+
			`<key>id</key><uuid>%s</uuid>`+
			`<key>legacy_first_name</key><string>Match%02d</string>`+
			`<key>legacy_last_name</key><string>Resident</string>`+
			`<key>display_name</key><string>Match%02d</string>`+
			`<key>username</key><string>match%02d</string>`+
			`</map>`, searchID(i), i, i, i)
	}
	b.WriteString(`</array></map></llsd>`)
	return &agent.CapResponse{Status: 200, Body: []byte(b.String())}, nil
}

// searchID makes a distinct uuid per result.
func searchID(i int) msg.UUID {
	var u msg.UUID
	u[0], u[15] = 0xAA, byte(i)
	return u
}

// ------------------------------------------------------------- the tests

// screen is a running slchat on a pseudo-terminal.
type screen struct {
	t  *testing.T
	tm *goterm.Term
}

func start(t *testing.T, rows, cols int) *screen {
	t.Helper()
	t.Setenv(harnessEnv, "1")

	tm := goterm.New(rows, cols)
	if err := tm.Start(os.Args[0]); err != nil {
		t.Fatalf("could not start the harness on a pty: %v", err)
	}
	t.Cleanup(func() { tm.Close() })

	s := &screen{t: t, tm: tm}
	s.waitLine(0, "slchat: Harness Resident in Nowhere")
	s.waitPrompt("Local> ")
	return s
}

// lines is the screen, with the trailing blank rows dropped.
func (s *screen) lines() []string {
	d := s.tm.Dump()
	for len(d) > 0 && strings.TrimSpace(d[len(d)-1]) == "" {
		d = d[:len(d)-1]
	}
	return d
}

func (s *screen) text() string { return strings.Join(s.lines(), "\n") }

// prompt is the bottom line, which is where the prompt lives.
func (s *screen) prompt() string {
	d := s.lines()
	if len(d) == 0 {
		return ""
	}
	return d[len(d)-1]
}

func (s *screen) send(keys string) {
	s.t.Helper()
	s.tm.Send([]byte(keys))
}

// waitPrompt waits for the bottom line to start with want.  The screen
// has no trailing spaces -- a terminal that has drawn "Local> " and put
// the cursor after it holds only "Local>" in that row -- so the
// comparison ignores them.
func (s *screen) waitPrompt(want string) {
	s.t.Helper()
	want = strings.TrimRight(want, " ")
	if !s.tm.WaitFor(5*time.Second, func(d []string) bool {
		for i := len(d) - 1; i >= 0; i-- {
			if strings.TrimSpace(d[i]) != "" {
				return strings.HasPrefix(d[i], want)
			}
		}
		return false
	}) {
		s.t.Fatalf("prompt never became %q; screen:\n%s", want, s.text())
	}
}

func (s *screen) waitLine(row int, want string) {
	s.t.Helper()
	if !s.tm.WaitFor(5*time.Second, func(d []string) bool {
		return row < len(d) && strings.Contains(d[row], want)
	}) {
		s.t.Fatalf("row %d never contained %q; screen:\n%s", row, want, s.text())
	}
}

// waitFlat is waitText for a narrow screen, where a long message is
// wrapped by the terminal and the text being looked for may straddle a
// row break.
func (s *screen) waitFlat(want string) {
	s.t.Helper()
	if !s.tm.WaitFor(5*time.Second, func(d []string) bool {
		return strings.Contains(strings.Join(d, ""), want)
	}) {
		s.t.Fatalf("never saw %q; screen:\n%s", want, s.text())
	}
}

func (s *screen) waitText(want string) {
	s.t.Helper()
	if !s.tm.WaitFor(5*time.Second, func(d []string) bool {
		return strings.Contains(strings.Join(d, "\n"), want)
	}) {
		s.t.Fatalf("never saw %q; screen:\n%s", want, s.text())
	}
}

// TestPTYTypingShowsOnThePrompt: the plumbing, and the cursor.
func TestPTYTypingShowsOnThePrompt(t *testing.T) {
	s := start(t, 24, 80)

	s.send("hello there")
	s.waitPrompt("Local> hello there")

	// The cursor sits after what was typed, which is what makes the
	// next keystroke go where it looks like it will.
	row, col := s.tm.Cursor()
	if want := len("Local> hello there"); col != want {
		t.Errorf("cursor at column %d, want %d", col, want)
	}
	if got := len(s.lines()) - 1; row != got {
		t.Errorf("cursor on row %d, want the prompt row %d", row, got)
	}
}

// TestPTYMessageLandsAboveTheTypedLine is the rule the whole display
// rests on.  A message arrives while a sentence is half typed: it must
// go above, and the half typed sentence must still be there, still
// editable, with the cursor where it was.
func TestPTYMessageLandsAboveTheTypedLine(t *testing.T) {
	s := start(t, 24, 80)

	// Say something, which the fake grid answers after a delay.
	s.send("ping\r")
	s.waitText("> [Local] ping")

	// Start typing the next thing before the answer arrives.
	s.send("half a sentence")
	s.waitPrompt("Local> half a sentence")

	// The answer lands.
	s.waitText("< [Local] Someone Else: you said ping")

	if got := s.prompt(); got != "Local> half a sentence" {
		t.Errorf("the typed line did not survive the message: %q", got)
	}
	// The message is above the prompt, not on it.
	lines := s.lines()
	for i, l := range lines[:len(lines)-1] {
		if strings.Contains(l, "you said ping") {
			t.Logf("message on row %d, prompt on row %d: correct", i, len(lines)-1)
		}
	}
	if strings.Contains(s.prompt(), "you said ping") {
		t.Error("the message was written onto the prompt line")
	}

	// And editing carries on from where it was.
	s.send(" more")
	s.waitPrompt("Local> half a sentence more")
}

// TestPTYPrefixKeyRewritesThePrompt: ESC, on a real terminal, where it
// is also the first byte of every arrow key.
func TestPTYPrefixKeyRewritesThePrompt(t *testing.T) {
	s := start(t, 24, 80)

	s.send("half typed")
	s.waitPrompt("Local> half typed")

	s.send("\x1b") // the prefix key, alone
	s.waitPrompt("command> ")
	if got := s.prompt(); got != "command>" {
		t.Errorf("command prompt = %q", got)
	}

	// A command runs, and the half typed line comes back afterwards.
	s.send("sessions\r")
	s.waitText("sessions (tab cycles):")
	s.waitPrompt("Local> half typed")
}

// TestPTYArrowKeysAreNotThePrefix: the same first byte, a different
// meaning.  An arrow must edit the line rather than open a command.
func TestPTYArrowKeysAreNotThePrefix(t *testing.T) {
	s := start(t, 24, 80)

	s.send("hello world")
	s.waitPrompt("Local> hello world")

	// Five lefts and an insert: "hello world" becomes "hello XXXworld".
	s.send("\x1b[D\x1b[D\x1b[D\x1b[D\x1b[D")
	s.send("XXX")
	s.waitPrompt("Local> hello XXXworld")

	if strings.HasPrefix(s.prompt(), "command>") {
		t.Error("an arrow key was taken for the prefix key")
	}
}

// TestPTYTabCyclesSessions: an instant message opens a session, and tab
// moves between it and open chat.  The prompt is the only thing that
// says which, so the prompt is what is checked.
func TestPTYTabCyclesSessions(t *testing.T) {
	s := start(t, 24, 80)

	// Open a session by asking for one, which needs a name: the
	// nearby listing supplies it, having asked the simulator who the
	// uuid belongs to.
	s.send("\x1bwho\r")
	s.waitText("Someone Else")
	s.send("\x1bim 1\r")
	s.waitPrompt("Someone Else> ")

	s.send("\t")
	s.waitPrompt("Local> ")
	s.send("\t")
	s.waitPrompt("Someone Else> ")

	// With something typed, tab must not move: a half typed line
	// going to the wrong person is worse than not cycling.
	s.send("mid sentence")
	s.send("\t")
	time.Sleep(200 * time.Millisecond)
	if got := s.prompt(); !strings.HasPrefix(got, "Someone Else> mid sentence") {
		t.Errorf("tab moved with a line in progress: %q", got)
	}
}

// TestPTYEditingKeys drives the line editor through a terminal rather
// than by calling it.
func TestPTYEditingKeys(t *testing.T) {
	s := start(t, 24, 80)

	s.send("hello world")
	s.send("\x17") // Ctrl-W
	s.waitPrompt("Local> hello ")

	s.send("\x01")  // Ctrl-A, to the start
	s.send("well ") // inserted there
	s.waitPrompt("Local> well hello ")

	s.send("\x05")   // Ctrl-E, to the end
	s.send("\x7ffs") // backspace, then two letters
	s.waitPrompt("Local> well hellofs")

	s.send("\x15") // Ctrl-U, kill to the start
	s.waitPrompt("Local> ")
}

// TestPTYQuitLeavesTheTerminal: the command exits, and the screen is
// not left with the prompt half drawn.
func TestPTYQuitLeavesTheTerminal(t *testing.T) {
	s := start(t, 24, 80)
	s.send("\x1bquit\r")
	if !s.tm.WaitFor(5*time.Second, func(d []string) bool {
		for _, l := range d {
			if strings.HasPrefix(l, "Local>") {
				return false
			}
		}
		return true
	}) {
		t.Errorf("the prompt was still on screen after quit:\n%s", s.text())
	}
}

// TestPTYNarrowTerminalScrolls: a line longer than the terminal is wide
// must scroll sideways rather than wrap.  A wrapped line takes two rows,
// and the next message printed above it lands in the middle of itself.
func TestPTYNarrowTerminalScrolls(t *testing.T) {
	s := start(t, 12, 40)

	before := len(s.lines())
	s.send(strings.Repeat("abcdefghij", 6)) // 60 characters into 40 columns
	s.waitText("abcdefghij")

	// The prompt still occupies exactly one row: nothing wrapped.
	if got := len(s.lines()); got != before {
		t.Errorf("the screen grew from %d rows to %d: the line wrapped", before, got)
	}
	// What is shown is the end of what was typed, since that is where
	// the cursor is.
	p := s.prompt()
	if len([]rune(p)) > 40 {
		t.Errorf("the prompt row is %d columns wide in a 40 column terminal: %q", len([]rune(p)), p)
	}
	if !strings.HasSuffix(p, "abcdefghij") {
		t.Errorf("the end of the typed line is not visible: %q", p)
	}

	// And a message still lands above it, with the typing intact.
	s.send("\r")
	s.waitFlat("you said")
	if got := s.prompt(); got != "Local>" {
		t.Errorf("prompt after sending = %q", got)
	}
}

// TestPTYLookupPages: a listing longer than the screen stops at the
// bottom and waits, space shows the next screenful, and the listing
// ends by giving the prompt back.
func TestPTYLookupPages(t *testing.T) {
	s := start(t, 12, 80)

	// The fake answers "30" with thirty people, which is more than a
	// twelve row terminal can hold.
	s.send("\x1blookup 30\r")
	s.waitText("30 matching")
	s.waitPrompt("--more--")

	// The first page is on screen and the last name is not.
	if !strings.Contains(s.text(), "Match00") {
		t.Errorf("the first result is missing:\n%s", s.text())
	}
	if strings.Contains(s.text(), "Match29") {
		t.Errorf("the whole listing was shown at once:\n%s", s.text())
	}

	// Space shows more.
	s.send(" ")
	s.waitText("Match11")
	s.waitPrompt("--more--")

	// Keep going to the end; the pager then hands the prompt back on
	// its own.
	for i := 0; i < 4 && strings.HasPrefix(s.prompt(), "--more--"); i++ {
		s.send(" ")
		time.Sleep(150 * time.Millisecond)
	}
	s.waitPrompt("Local> ")
	if !strings.Contains(s.text(), "Match29") {
		t.Errorf("the last result never appeared:\n%s", s.text())
	}
}

// TestPTYPagerQuits: q stops a long listing, and says how much was
// dropped rather than pretending that was all of it.
func TestPTYPagerQuits(t *testing.T) {
	s := start(t, 12, 80)

	s.send("\x1blookup 40\r")
	s.waitPrompt("--more--")

	s.send("q")
	s.waitPrompt("Local> ")
	s.waitText("more not shown")

	// And the keyboard is back: typing goes to the session again
	// rather than to the pager.
	s.send("back to normal")
	s.waitPrompt("Local> back to normal")
}

// TestPTYPagerKeepsTheTypedLine: paging is a pause, not a prompt.  What
// was half typed when a listing started must come back afterwards.
func TestPTYPagerKeepsTheTypedLine(t *testing.T) {
	s := start(t, 12, 80)

	s.send("half typed")
	s.waitPrompt("Local> half typed")

	s.send("\x1blookup 30\r")
	s.waitPrompt("--more--")
	s.send("q")

	s.waitPrompt("Local> half typed")
}

// TestPTYShortListingDoesNotPage: three results fit, so nothing waits
// for a keystroke.
func TestPTYShortListingDoesNotPage(t *testing.T) {
	s := start(t, 24, 80)

	s.send("\x1blookup someone\r")
	s.waitText("3 matching")
	s.waitPrompt("Local> ")
	if strings.Contains(s.text(), "--more--") {
		t.Errorf("a short listing paged:\n%s", s.text())
	}
}
