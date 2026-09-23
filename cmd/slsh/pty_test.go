package main

// The shell, driven the way a person drives it.
//
// goterm gives a real pseudo-terminal and a screen to read back, which
// is the only way to test what slsh actually is: two modes over one
// line editor, with everything heard arriving above the prompt
// whichever mode is in force.
//
// The grid behind it is a fake, so none of this needs a session.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/goexvi-ctrl/goterm"
	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

const harnessEnv = "SLSH_PTY_HARNESS"

func TestMain(m *testing.M) {
	if os.Getenv(harnessEnv) == "1" {
		harness()
		return
	}

	// No test writes a transcript into whoever is running it.
	//
	// Logging is on by default, which means every shape of test shell
	// built from DefaultConfig keeps one, and the default place for it
	// is under the real home directory.  That is how this was found:
	// one run of this package appended 274 lines of set_test's
	// settings to the transcript of a live avatar.  A temporary
	// XDG_DATA_HOME here covers every test in the package, including
	// the ones nobody has written yet.
	dir, err := os.MkdirTemp("", "slsh-test-data")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("XDG_DATA_HOME", dir)

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

var (
	harnessMe    = msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	harnessOther = msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	harnessRoot  = msg.MustParseUUID("12b57e57-7e57-c0de-efe3-b327af5dfe62")

	// harnessSlow is a folder the fake takes its time over, so that a
	// test can look at the screen WHILE a command is still running.
	harnessSlow = msg.MustParseUUID("47687e57-7e57-c0de-8baf-656f9f67e24b")
)

// slowListing is how long the fake dawdles over harnessSlow.  Long
// enough to look at the screen in the middle of it, short enough not to
// be felt in a test run.
const slowListing = 900 * time.Millisecond

// harness is the program goterm launches: the real terminal and the
// real shell, over a backend that answers from nothing.
func harness() {
	s, err := sl.New(&fakeBackend{
		messages: make(chan *sl.Message, 8),
		done:     make(chan struct{}),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	t, err := NewTerm(os.Stdin, os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer t.Close()

	sh := NewShell(Config{Addr: "fake", Prefix: 27}, t, s)
	sh.Run(context.Background())
	t.Close()
}

// fakeBackend answers the few things the shell asks at startup and
// refuses the rest, which is enough to exercise the modes.
type fakeBackend struct {
	messages chan *sl.Message
	done     chan struct{}
	mu       sync.Mutex
}

func (b *fakeBackend) Info() *sl.Info {
	return &sl.Info{
		Name: "harness", AgentID: harnessMe,
		SessionID:     msg.MustParseUUID("1c117e57-7e57-c0de-da64-e42aeda52a0a"),
		AvatarName:    "Harness Resident",
		Region:        "Nowhere",
		InventoryRoot: harnessRoot,
		Caps:          []string{"InventoryAPIv3"},
	}
}

// Refresh is Backend's; nothing here rebuilds a session underneath, so
// the identity it hands back is the one it has.
func (b *fakeBackend) Refresh(context.Context) (*sl.Info, error) { return b.Info(), nil }

// Control is nothing here.  Nothing this fake stands in for sits
// down or stands up; the method exists because sl.Backend has it,
// so that the one place an AgentUpdate is built stays the one place
// that owns the camera.
func (b *fakeBackend) Control(ctx context.Context, flags uint32) error { return nil }

func (b *fakeBackend) Send(ctx context.Context, m msg.Message, reliable bool) error { return nil }
func (b *fakeBackend) Messages() <-chan *sl.Message                                 { return b.messages }

// Events is nothing: this fake exists so a real terminal can be driven
// over a real pty, and nothing it drives reads the event queue.
func (b *fakeBackend) Events() <-chan *sl.QueueEvent { return nil }
func (b *fakeBackend) Land(ctx context.Context) (*sl.Land, error) {
	return &sl.Land{Overlay: agent.OverlayFrom(nil, 0)}, nil
}
func (b *fakeBackend) Done() <-chan struct{}   { return b.done }
func (b *fakeBackend) Err() error              { return nil }
func (b *fakeBackend) Close() error            { return nil }
func (b *fakeBackend) HasCap(name string) bool { return name == "InventoryAPIv3" }

// RegionChanges is never told of one: nothing typed at this shell
// leaves the region.
func (b *fakeBackend) RegionChanges() <-chan *sl.RegionChange { return nil }

// SimAttachments: this fake has never heard an appearance.
func (b *fakeBackend) SimAttachments(ctx context.Context, avatar msg.UUID) (*sl.SimAttachments, error) {
	return nil, nil
}

func (b *fakeBackend) Presence(ctx context.Context, d float32) (*sl.Presence, error) {
	return &sl.Presence{Region: "Nowhere"}, nil
}
func (b *fakeBackend) Objects(ctx context.Context, named, id string) ([]*sl.Seen, error) {
	return nil, nil
}
func (b *fakeBackend) Region(ctx context.Context) (*sl.Region, bool, error) {
	return &sl.Region{Name: "Nowhere"}, true, nil
}
func (b *fakeBackend) Neighbours(ctx context.Context, set *bool) (*sl.Neighbours, error) {
	return &sl.Neighbours{}, nil
}
func (b *fakeBackend) Flush(ctx context.Context) (int, error) { return 0, nil }
func (b *fakeBackend) Friends(ctx context.Context) ([]sl.Friend, error) {
	return []sl.Friend{{ID: harnessOther, Online: true}}, nil
}
func (b *fakeBackend) NoteFriend(ctx context.Context, id msg.UUID, online bool) error { return nil }
func (b *fakeBackend) Lock(ctx context.Context, name string) error                    { return nil }
func (b *fakeBackend) Unlock(name string) error                                       { return nil }
func (b *fakeBackend) TryLock(ctx context.Context, name string) (bool, string, error) {
	return true, "", nil
}

// DoCap answers an inventory listing for the root and nothing else, so
// ls and cd have something to walk.
func (b *fakeBackend) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	if !strings.HasPrefix(r.Path, "/category/") {
		return &agent.CapResponse{Status: 404}, nil
	}
	if strings.Contains(r.Path, harnessSlow.String()) {
		time.Sleep(slowListing)
	}
	body := `<?xml version="1.0" ?><llsd><map>
	  <key>category_id</key><uuid>` + harnessRoot.String() + `</uuid>
	  <key>_embedded</key><map>
	    <key>categories</key><map>
	      <key>35517e57-7e57-c0de-220b-d980670cf2d2</key><map>
	        <key>category_id</key><uuid>35517e57-7e57-c0de-220b-d980670cf2d2</uuid>
	        <key>parent_id</key><uuid>` + harnessRoot.String() + `</uuid>
	        <key>name</key><string>Objects</string>
	      </map>
	      <key>` + harnessSlow.String() + `</key><map>
	        <key>category_id</key><uuid>` + harnessSlow.String() + `</uuid>
	        <key>parent_id</key><uuid>` + harnessRoot.String() + `</uuid>
	        <key>name</key><string>slow</string>
	      </map>
	    </map>
	    <key>items</key><map>
	      <key>3f0d7e57-7e57-c0de-b8ae-792bb838fc97</key><map>
	        <key>item_id</key><uuid>3f0d7e57-7e57-c0de-b8ae-792bb838fc97</uuid>
	        <key>parent_id</key><uuid>` + harnessRoot.String() + `</uuid>
	        <key>name</key><string>a notecard</string>
	      </map>
	    </map>
	  </map>
	</map></llsd>`
	return &agent.CapResponse{Status: 200, Body: []byte(body)}, nil
}

var _ sl.Backend = (*fakeBackend)(nil)

// ------------------------------------------------------------- the tests

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
	s.waitText("slsh: Harness Resident in Nowhere")
	s.waitPrompt("/$")
	return s
}

func (s *screen) lines() []string {
	d := s.tm.Dump()
	for len(d) > 0 && strings.TrimSpace(d[len(d)-1]) == "" {
		d = d[:len(d)-1]
	}
	return d
}

func (s *screen) text() string { return strings.Join(s.lines(), "\n") }

func (s *screen) prompt() string {
	d := s.lines()
	if len(d) == 0 {
		return ""
	}
	return d[len(d)-1]
}

func (s *screen) send(keys string) { s.tm.Send([]byte(keys)) }

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

func (s *screen) waitText(want string) {
	s.t.Helper()
	if !s.tm.WaitFor(5*time.Second, func(d []string) bool {
		return strings.Contains(strings.Join(d, "\n"), want)
	}) {
		s.t.Fatalf("never saw %q; screen:\n%s", want, s.text())
	}
}

// TestPTYPromptIsTheWorkingDirectory: the prompt is what says where a
// command will land, and cd has to move it.
func TestPTYPromptIsTheWorkingDirectory(t *testing.T) {
	s := start(t, 24, 80)

	s.send("pwd\r")
	s.waitText("/")
	s.send("cd Objects\r")
	s.waitPrompt("/Objects$")
	s.send("cd ..\r")
	s.waitPrompt("/$")
}

// TestPTYModes is the shape of the whole program: commands outside,
// chat inside, and the escape key back out.
func TestPTYModes(t *testing.T) {
	s := start(t, 24, 80)

	s.send("chat\r")
	s.waitPrompt("Local>")

	// What is typed in chat mode is not a command.
	s.send("pwd")
	s.waitPrompt("Local> pwd")

	// The escape key comes back, and what was typed in chat stays
	// behind rather than being run as a command.
	s.send("\x1b")
	s.waitPrompt("/$")
	if strings.Contains(s.prompt(), "pwd") {
		t.Errorf("the chat line followed us into command mode: %q", s.prompt())
	}

	// And back again, where the half-typed line is waiting.
	s.send("chat\r")
	s.waitPrompt("Local> pwd")
}

// TestPTYHistory: up and down walk what was typed, in command mode.
func TestPTYHistory(t *testing.T) {
	s := start(t, 24, 80)

	s.send("pwd\r")
	s.send("cd Objects\r")
	s.waitPrompt("/Objects$")

	s.send("\x1b[A") // up
	s.waitPrompt("/Objects$ cd Objects")
	s.send("\x1b[A") // up again
	s.waitPrompt("/Objects$ pwd")
	s.send("\x1b[B") // down
	s.waitPrompt("/Objects$ cd Objects")
}

// TestPTYChatHistory: the arrows walk what was said, over a real
// terminal and with the arrows arriving as the three bytes a terminal
// sends.  They did nothing at all in chat mode before this.
func TestPTYChatHistory(t *testing.T) {
	s := start(t, 24, 80)

	// A command first, so there is something in the other ring.
	s.send("pwd\r")
	s.waitText("/$ pwd")

	s.send("chat\r")
	s.waitPrompt("Local>")
	s.send("hello there\r")
	s.waitText("> [Local] hello there")

	// Up brings it back to the line, and does not say it again.
	s.send("\x1b[A")
	s.waitPrompt("Local> hello there")
	if n := strings.Count(s.text(), "> [Local] hello there"); n != 1 {
		t.Errorf("the recall said the line again: %d of them\n%s", n, s.text())
	}

	// Up again stays where it is rather than reaching the commands.
	s.send("\x1b[A")
	time.Sleep(200 * time.Millisecond)
	if got := s.prompt(); got != "Local> hello there" {
		t.Errorf("up past the start of the chat history gave %q", got)
	}

	// What came back is editable, and goes only when Enter says so.
	s.send("\x7f\x7f\x7f\x7f\x7f") // backspace over "there"
	s.send("again")
	s.waitPrompt("Local> hello again")
	s.send("\r")
	s.waitText("> [Local] hello again")

	// And the command ring is untouched by any of it: the two commands
	// that were run, newest first, with nothing said in between them.
	s.send("\x1b")
	s.waitPrompt("/$")
	s.send("\x1b[A")
	s.waitPrompt("/$ chat")
	s.send("\x1b[A")
	s.waitPrompt("/$ pwd")
}

// TestPTYCompletion: tab finishes a command in command mode, and does
// not in chat mode, where it moves between conversations instead.
func TestPTYCompletion(t *testing.T) {
	s := start(t, 24, 80)

	s.send("featu\t")
	s.waitPrompt("/$ features")

	// A prefix shared by several commands completes as far as it can.
	s.send("\x15") // Ctrl-U, clear
	s.send("of\t")
	s.waitPrompt("/$ offer")

	// In chat mode the same key is for conversations, so the line is
	// left alone.
	s.send("\x15")
	s.send("chat\r")
	s.waitPrompt("Local>")
	s.send("featu\t")
	time.Sleep(200 * time.Millisecond)
	if got := s.prompt(); got != "Local> featu" {
		t.Errorf("tab completed in chat mode: %q", got)
	}
}

// TestPTYOutputLandsAboveThePrompt: a command's output goes above the
// line being typed, like everything else.
func TestPTYOutputLandsAboveThePrompt(t *testing.T) {
	s := start(t, 24, 80)

	s.send("half typed")
	s.waitPrompt("/$ half typed")

	// Ctrl-C clears it, then a command with output.
	s.send("\x03")
	s.send("help shell\r")
	s.waitText("leave slsh")
	s.waitPrompt("/$")
}

// TestPTYCommandsStayOnTheScreen: output is no use if what was asked
// has scrolled away unrecorded, so the prompt and the command stay
// above it, the way a shell leaves them.
func TestPTYCommandsStayOnTheScreen(t *testing.T) {
	s := start(t, 24, 80)

	s.send("pwd\r")
	s.waitText("/$ pwd")
	s.send("help shell\r")
	s.waitText("leave slsh")
	s.waitPrompt("/$")

	// In that order: the command, and then what it printed.
	txt := s.text()
	cmd, out := strings.Index(txt, "/$ help shell"), strings.Index(txt, "leave slsh")
	switch {
	case cmd < 0:
		t.Errorf("the command did not stay on the screen:\n%s", txt)
	case out < cmd:
		t.Errorf("the output came out above its command:\n%s", txt)
	}
}

// TestPTYChatDoesNotEchoTheRawLine: an outgoing line is printed in its
// own marked form, so echoing it as well would say it twice and lose
// which of the two was the one that went.
func TestPTYChatDoesNotEchoTheRawLine(t *testing.T) {
	s := start(t, 24, 80)

	s.send("chat\r")
	s.waitPrompt("Local>")
	s.send("hello everyone\r")
	s.waitText("> [Local] hello everyone")

	if strings.Contains(s.text(), "Local> hello everyone\n") {
		t.Errorf("chat echoed the line as well as marking it:\n%s", s.text())
	}
}

// TestPTYPromptWaitsForTheCommand.
//
// A prompt says the shell is ready for the next line.  It used to be
// drawn as soon as the line was taken, so a command that took ten
// seconds left a prompt sitting there that would not accept a
// keystroke, and the only way to tell it from a finished command was to
// try typing at it.
func TestPTYPromptWaitsForTheCommand(t *testing.T) {
	s := start(t, 24, 80)

	s.send("ls slow\r")

	// Partway through: the command is on the screen, and there is no
	// prompt under it, because there is nothing to type at yet.
	time.Sleep(slowListing / 3)
	if got := s.prompt(); got != "/$ ls slow" {
		t.Errorf("during the command the last line should still be the command, got %q\nscreen:\n%s",
			got, s.text())
	}

	// And it comes back when the command is done.
	s.waitPrompt("/$")
}

// TestPTYSourceStopsAtTheFirstFailure.
//
// A file of commands usually begins by changing folder, and carrying on
// after that failed would run every line that follows somewhere else --
// which, when the lines are removals, is not a thing to find out about
// afterwards.
func TestPTYSourceStopsAtTheFirstFailure(t *testing.T) {
	s := start(t, 24, 80)

	path := filepath.Join(t.TempDir(), "moves")
	script := "cd Objects\n" +
		"cd nowhere at all\n" + // fails: no such folder
		"echo THIS-MUST-NOT-RUN\n"
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}

	s.send(". " + path + "\r")
	s.waitText("stopped")
	s.waitPrompt("/Objects$")

	if strings.Contains(s.text(), "THIS-MUST-NOT-RUN") {
		t.Errorf("the file carried on after a failure:\n%s", s.text())
	}
	// It says where it gave up and how much did not happen.
	if !strings.Contains(s.text(), "moves:2") {
		t.Errorf("the report should name the line that failed:\n%s", s.text())
	}
	if !strings.Contains(s.text(), "1 line was not run") {
		t.Errorf("the report should say how much was skipped:\n%s", s.text())
	}
	// The first line did run: we are in Objects.
}

// TestPTYSourceRunsRightThroughWhenNothingFails
func TestPTYSourceRunsRightThroughWhenNothingFails(t *testing.T) {
	s := start(t, 24, 80)

	path := filepath.Join(t.TempDir(), "fine")
	if err := os.WriteFile(path, []byte("cd Objects\necho ALL-THE-WAY\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.send(". " + path + "\r")
	s.waitText("ALL-THE-WAY")
	s.waitPrompt("/Objects$")
}

// TestPTYUnknownCommandSaysSo: and does not leave the shell.
func TestPTYUnknownCommandSaysSo(t *testing.T) {
	s := start(t, 24, 80)
	s.send("nosuchthing\r")
	s.waitText("no such command")
	s.waitPrompt("/$")
}
