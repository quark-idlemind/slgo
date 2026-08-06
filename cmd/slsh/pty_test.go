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
	os.Exit(m.Run())
}

var (
	harnessMe    = msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	harnessOther = msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	harnessRoot  = msg.MustParseUUID("12b57e57-7e57-c0de-efe3-b327af5dfe62")
)

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

func (b *fakeBackend) Send(ctx context.Context, m msg.Message, reliable bool) error { return nil }
func (b *fakeBackend) Messages() <-chan *sl.Message                                 { return b.messages }
func (b *fakeBackend) Done() <-chan struct{}                                        { return b.done }
func (b *fakeBackend) Err() error                                                   { return nil }
func (b *fakeBackend) Close() error                                                 { return nil }
func (b *fakeBackend) HasCap(name string) bool                                      { return name == "InventoryAPIv3" }

func (b *fakeBackend) Presence(ctx context.Context, d float32) (*sl.Presence, error) {
	return &sl.Presence{Region: "Nowhere"}, nil
}
func (b *fakeBackend) Objects(ctx context.Context, named, id string) ([]*sl.Seen, error) {
	return nil, nil
}
func (b *fakeBackend) Region(ctx context.Context) (*sl.Region, bool, error) {
	return &sl.Region{Name: "Nowhere"}, true, nil
}
func (b *fakeBackend) Flush(ctx context.Context) (int, error) { return 0, nil }
func (b *fakeBackend) Friends(ctx context.Context) ([]sl.Friend, error) {
	return []sl.Friend{{ID: harnessOther, Online: true}}, nil
}
func (b *fakeBackend) NoteFriend(ctx context.Context, id msg.UUID, online bool) error { return nil }

// DoCap answers an inventory listing for the root and nothing else, so
// ls and cd have something to walk.
func (b *fakeBackend) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	if !strings.HasPrefix(r.Path, "/category/") {
		return &agent.CapResponse{Status: 404}, nil
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
	s.send("help quit\r")
	s.waitText("leave slsh")
	s.waitPrompt("/$")
}

// TestPTYUnknownCommandSaysSo: and does not leave the shell.
func TestPTYUnknownCommandSaysSo(t *testing.T) {
	s := start(t, 24, 80)
	s.send("nosuchthing\r")
	s.waitText("no such command")
	s.waitPrompt("/$")
}
