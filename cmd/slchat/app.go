package main

// The loop, and everything the person at the keyboard sees.
//
// There are two modes and one line editor.  In chat mode what is typed
// goes to the current session when Enter is pressed; the prefix key
// switches to command mode, which rewrites the prompt, puts the
// half-typed line aside, and reads one command.  Either way the line
// being edited stays on the bottom line of the terminal and everything
// that arrives is printed above it.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

const (
	modeChat = iota
	modeCommand
)

// Grid is what slchat needs from the connection to slgod.
//
// It is an interface rather than the connection itself so that the
// terminal can be driven without a grid behind it: the display rules --
// a message landing above the line being typed, the prompt naming who
// is being talked to -- are what this program is, and they are worth
// testing without two live avatars and a region to stand in.
// *client.Conn is the real one.
type Grid interface {
	Send(ctx context.Context, m msg.Message, reliable bool) error
	Messages() <-chan *client.Message
	Done() <-chan struct{}
	Err() error

	Objects(ctx context.Context, named, id string) (*pb.ObjectsResponse, error)
	Presence(ctx context.Context, drawDistance float32) (*pb.PresenceResponse, error)
	Region(ctx context.Context) (*pb.RegionInfo, error)
	Friends(ctx context.Context) ([]*pb.Friend, error)
	NoteFriend(ctx context.Context, id msg.UUID, online bool) error
}

// App is one running slchat.
type App struct {
	cfg  Config
	term *Term
	conn Grid
	info *pb.AgentInfo

	me     msg.UUID
	sess   msg.UUID
	myName string

	roster   *Roster
	sessions *Sessions

	mu       sync.Mutex
	mode     int
	held     string // the chat line put aside while a command is typed
	offers   map[msg.UUID]offer
	listed   []person // the last listing, so "im 2" means what it showed
	regionID msg.UUID
	position msg.Vector3

	quit chan struct{}
	once sync.Once
}

// NewApp wires a connection and a terminal together.
func NewApp(cfg Config, t *Term, c Grid, info *pb.AgentInfo) (*App, error) {
	me, err := msg.ParseUUID(info.AgentId)
	if err != nil {
		return nil, fmt.Errorf("slchat: the server gave a bad agent id: %w", err)
	}
	sess, err := msg.ParseUUID(info.SessionId)
	if err != nil {
		return nil, fmt.Errorf("slchat: the server gave a bad session id: %w", err)
	}
	return &App{
		cfg: cfg, term: t, conn: c, info: info,
		me: me, sess: sess, myName: info.AvatarName,
		roster:   NewRoster(),
		sessions: NewSessions(),
		offers:   map[msg.UUID]offer{},
		quit:     make(chan struct{}),
	}, nil
}

// Run reads the keyboard until it runs out or something says to stop.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.roster.Learn(a.me, a.myName)
	go a.track(ctx)
	go a.relay(ctx)

	a.banner()
	a.refreshPrompt()

	keys := a.term.Keys()
	for {
		select {
		case <-a.quit:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-a.conn.Done():
			a.term.Print("")
			return a.conn.Err()
		case r, ok := <-keys:
			if !ok {
				return nil
			}
			a.key(ctx, r)
		}
	}
}

// relay prints what arrives from the grid.
func (a *App) relay(ctx context.Context) {
	msgs := a.conn.Messages()
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-msgs:
			if !ok {
				return
			}
			a.handle(m)
		}
	}
}

// key is one keystroke.
func (a *App) key(ctx context.Context, r rune) {
	switch r {
	case '\r', '\n':
		a.enter(ctx)
		return
	case '\t':
		a.tab()
		return
	case 3: // Ctrl-C
		if a.commandMode() {
			a.leaveCommand()
			return
		}
		if a.term.Line() != "" {
			a.term.Take()
			return
		}
		a.Quit()
		return
	}

	if r == a.cfg.Prefix {
		a.enterCommand()
		return
	}

	// Ctrl-D on an empty line is the end of input, as it is
	// everywhere else.
	if !a.term.Key(r) && r == 4 && a.term.Line() == "" {
		a.Quit()
	}
}

// enter acts on the line, in whichever mode it was typed.
func (a *App) enter(ctx context.Context) {
	line := a.term.Take()
	if a.commandMode() {
		a.leaveCommand()
		if s := strings.TrimSpace(line); s != "" {
			a.command(ctx, s)
		}
		return
	}
	if strings.TrimSpace(line) == "" {
		return
	}
	a.send(ctx, line)
}

// tab cycles sessions when the line is empty, which is the only time it
// is unambiguous: with something typed it would be a completion, and
// getting a message to the wrong person is worse than not completing.
func (a *App) tab() {
	if a.term.Line() != "" {
		return
	}
	if a.commandMode() {
		return
	}
	a.sessions.Next()
	a.refreshPrompt()
}

// send puts a line to the current session.
func (a *App) send(ctx context.Context, text string) {
	s := a.sessions.Current()
	if s.Local() {
		if err := a.Say(ctx, text); err != nil {
			a.notice("could not say that: %v", err)
			return
		}
		a.outgoing("Local", text)
		return
	}
	if err := a.SendIM(ctx, s.Target, text); err != nil {
		a.notice("could not send that: %v", err)
		return
	}
	a.outgoing("IM "+s.Label(), text)
}

// ---------------------------------------------------------------- modes

func (a *App) commandMode() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.mode == modeCommand
}

// enterCommand rewrites the prompt and keeps whatever was half typed,
// so that reaching for a command in the middle of a sentence does not
// cost the sentence.
func (a *App) enterCommand() {
	a.mu.Lock()
	if a.mode == modeCommand {
		a.mu.Unlock()
		return
	}
	a.mode = modeCommand
	a.held = a.term.Line()
	a.mu.Unlock()

	a.term.SetLine("")
	a.term.SetPrompt("command> ")
}

func (a *App) leaveCommand() {
	a.mu.Lock()
	held := a.held
	a.mode, a.held = modeChat, ""
	a.mu.Unlock()

	a.term.SetLine(held)
	a.refreshPrompt()
}

// refreshPrompt names where the next thing typed will go.
func (a *App) refreshPrompt() {
	if a.commandMode() {
		a.term.SetPrompt("command> ")
		return
	}
	s := a.sessions.Current()
	if s.Local() {
		a.term.SetPrompt("Local> ")
		return
	}
	a.term.SetPrompt(s.Label() + "> ")
}

// Quit stops the loop.  Safe to call from anywhere and more than once.
func (a *App) Quit() { a.once.Do(func() { close(a.quit) }) }

// ---------------------------------------------------------------- output

// stamp is the time column every line starts with.
func stamp() string { return time.Now().Format("15:04:05") }

// incoming prints something somebody else said.  Where it came from is
// in brackets: the session it belongs to, which is the same label the
// prompt uses, so it is obvious which tab stop would answer it.
//
// The speaker is named only where the session does not already name
// them.  Open chat has many voices and needs it; an instant message
// session has exactly one, and "[IM Quark Idlemind] Quark Idlemind:"
// says it twice.
func (a *App) incoming(where, who, text string) {
	if who == "" {
		a.term.Printf("%s < [%s] %s", stamp(), where, text)
		return
	}
	a.term.Printf("%s < [%s] %s: %s", stamp(), where, who, text)
}

// outgoing prints something we said, next to the rest.  The direction
// marker is the whole difference: same line, opposite arrow.
func (a *App) outgoing(where, text string) {
	a.term.Printf("%s > [%s] %s", stamp(), where, text)
}

// notice is slchat talking, rather than anybody in the world.
func (a *App) notice(format string, args ...any) {
	a.term.Printf("%s * %s", stamp(), fmt.Sprintf(format, args...))
}

func (a *App) banner() {
	where := a.info.Region
	if where == "" {
		where = "an unnamed region"
	}
	a.term.Printf("slchat: %s in %s, through %s", a.myName, where, a.cfg.Addr)
	a.term.Printf("        %s for a command, %s help for the list, tab to change who you are talking to",
		KeyName(a.cfg.Prefix), KeyName(a.cfg.Prefix))
}
