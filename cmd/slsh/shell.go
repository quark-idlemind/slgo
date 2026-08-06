package main

// The shell: two modes, one line editor.
//
// Commands are the outer mode, because that is what this is mostly
// used for -- chat output arrives whatever mode you are in, and the
// keyboard is wanted for cd and ls far more often than for talking.
// "chat" goes the other way and the escape key comes back.
//
// Everything a command prints goes through an io.Writer, so that
// redirecting it to a file is the same code path as printing it.  That
// is what makes "ls > listing", an editor, and ". listing" a workflow
// rather than a special case.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

const (
	modeCommand = iota
	modeChat
)

// Shell is one running slsh.
type Shell struct {
	cfg  Config
	term *Term
	s    *sl.Session

	mu   sync.Mutex
	mode int
	held string // the command line put aside while chatting

	// cwd is where in inventory we are, as names, and cwdID the
	// folder it resolves to.  Both are kept so that a rename
	// somewhere above does not silently move us.
	cwd   []string
	cwdID msg.UUID

	// listed is the last listing, so a command can say "3" and mean
	// what it just showed.
	listed []person

	// history is what has been typed, oldest first, and histAt is
	// where the arrows have got to.
	history []string
	histAt  int

	talk  *Conversations
	quit  chan struct{}
	once  sync.Once
	depth int // how deep the sourcing goes, to stop a file sourcing itself
}

// person is somebody a listing showed.
type person struct {
	ID   msg.UUID
	Name string
}

// NewShell wires a session to a terminal.
func NewShell(cfg Config, t *Term, s *sl.Session) *Shell {
	sh := &Shell{
		cfg: cfg, term: t, s: s,
		cwdID: s.InventoryRoot(),
		talk:  NewConversations(),
		quit:  make(chan struct{}),
	}
	if cfg.Chat {
		sh.mode = modeChat
	}
	return sh
}

// Run reads until the input runs out or something says to stop.
func (sh *Shell) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	go sh.watch(ctx)
	sh.banner()
	sh.prompt()

	keys := sh.term.Keys()
	for {
		select {
		case <-sh.quit:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-sh.s.Done():
			sh.term.Print("")
			return sh.s.Err()
		case r, ok := <-keys:
			if !ok {
				return nil
			}
			sh.key(ctx, r)
		}
	}
}

// Quit stops the shell.  Safe from anywhere, and more than once.
func (sh *Shell) Quit() { sh.once.Do(func() { close(sh.quit) }) }

// ---------------------------------------------------------------- keys

func (sh *Shell) key(ctx context.Context, r rune) {
	chat := sh.chatting()

	switch r {
	case '\r', '\n':
		sh.enter(ctx)
		return
	case '\t':
		if chat {
			sh.cycle()
		} else {
			sh.complete(ctx)
		}
		return
	case 3: // Ctrl-C
		if sh.term.Line() != "" {
			sh.term.Take()
			return
		}
		if chat {
			sh.setMode(modeCommand)
			return
		}
		sh.Quit()
		return
	case keyUp, keyDown:
		if !chat {
			sh.recall(r == keyUp)
		}
		return
	}

	// The escape key leaves chat, and does nothing outside it.
	if chat && r == sh.cfg.Prefix {
		sh.setMode(modeCommand)
		return
	}

	if !sh.term.Key(r) && r == 4 && sh.term.Line() == "" {
		sh.Quit()
	}
}

func (sh *Shell) enter(ctx context.Context) {
	line := sh.term.Take()
	if sh.chatting() {
		if strings.TrimSpace(line) != "" {
			sh.send(ctx, line)
		}
		return
	}
	if strings.TrimSpace(line) == "" {
		return
	}
	sh.remember(line)
	sh.Do(ctx, line)
	sh.prompt()
}

// recall walks the history.
func (sh *Shell) recall(back bool) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if len(sh.history) == 0 {
		return
	}
	if back {
		if sh.histAt > 0 {
			sh.histAt--
		}
	} else {
		if sh.histAt < len(sh.history) {
			sh.histAt++
		}
	}
	if sh.histAt >= len(sh.history) {
		sh.term.SetLine("")
		return
	}
	sh.term.SetLine(sh.history[sh.histAt])
}

func (sh *Shell) remember(line string) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	// The same command twice running is one entry, as in a shell.
	if n := len(sh.history); n == 0 || sh.history[n-1] != line {
		sh.history = append(sh.history, line)
	}
	sh.histAt = len(sh.history)
}

// ---------------------------------------------------------------- modes

func (sh *Shell) chatting() bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.mode == modeChat
}

// setMode swaps the mode, putting the half-typed line of the mode being
// left aside so it is there again on the way back.
func (sh *Shell) setMode(mode int) {
	sh.mu.Lock()
	if sh.mode == mode {
		sh.mu.Unlock()
		return
	}
	held := sh.held
	sh.held = sh.term.Line()
	sh.mode = mode
	sh.mu.Unlock()

	sh.term.SetLine(held)
	sh.prompt()
}

// prompt says where the next line will go, which is the only thing
// standing between a command and a remark said out loud.
func (sh *Shell) prompt() {
	if sh.chatting() {
		sh.term.SetPrompt(sh.talk.Current().Label() + "> ")
		return
	}
	sh.term.SetPrompt(sh.Pwd() + "$ ")
}

// Pwd is the working directory as a path.
func (sh *Shell) Pwd() string {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if len(sh.cwd) == 0 {
		return "/"
	}
	return "/" + sl.JoinPath(sh.cwd...)
}

// ---------------------------------------------------------------- running

// Do runs one command line: tokens, redirection, and the command.
func (sh *Shell) Do(ctx context.Context, line string) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return
	}

	words, redirect, appending, err := parse(line)
	if err != nil {
		sh.errorf("%v", err)
		return
	}
	if len(words) == 0 {
		return
	}

	out := io.Writer(sh.stdout())
	if redirect != "" {
		flags := os.O_CREATE | os.O_WRONLY
		if appending {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
		}
		f, err := os.OpenFile(redirect, flags, 0o644)
		if err != nil {
			sh.errorf("%v", err)
			return
		}
		defer f.Close()
		out = f
	}

	sh.run(ctx, out, words)
}

// run dispatches one already-parsed command.
func (sh *Shell) run(ctx context.Context, out io.Writer, words []string) {
	name, args := words[0], words[1:]
	c, ok := commands[name]
	if !ok {
		sh.errorf("%s: no such command; try help", name)
		return
	}
	if err := c.run(ctx, sh, out, args); err != nil {
		sh.errorf("%s: %v", name, err)
	}
}

// Source reads commands from a file, one per line.
//
// This is the other half of redirection: a listing written out, edited
// into a list of moves, and run.  Blank lines and lines starting with #
// are skipped, so an edited listing can keep its notes.
func (sh *Shell) Source(ctx context.Context, path string) error {
	if sh.depth > 8 {
		return fmt.Errorf("sourcing is nested too deep; is a file sourcing itself?")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sh.depth++
	defer func() { sh.depth-- }()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		select {
		case <-sh.quit:
			return nil
		default:
		}
		sh.Do(ctx, line)
	}
	return sc.Err()
}

// ---------------------------------------------------------------- output

// stdout is where a command prints when it is not redirected: above the
// prompt, a line at a time.
func (sh *Shell) stdout() io.Writer { return &termWriter{t: sh.term} }

// termWriter turns writes into whole lines above the prompt.
type termWriter struct {
	t   *Term
	buf []byte
}

func (w *termWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		w.t.Print(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	// A write with no newline is held until one arrives; anything
	// left over is flushed when the command ends.
	if len(w.buf) > 0 {
		w.t.Print(string(w.buf))
		w.buf = nil
	}
	return len(p), nil
}

func (sh *Shell) errorf(format string, args ...any) {
	sh.term.Printf("slsh: "+format, args...)
}

func (sh *Shell) noticef(format string, args ...any) {
	sh.term.Printf("%s * %s", stamp(), fmt.Sprintf(format, args...))
}

func stamp() string { return time.Now().Format("15:04:05") }

func (sh *Shell) banner() {
	info := sh.s.Info()
	where := info.Region
	if where == "" {
		where = "an unnamed region"
	}
	how := "through " + sh.cfg.Addr
	if sh.cfg.Direct {
		how = "logged in directly -- quitting logs out"
	}
	sh.term.Printf("slsh: %s in %s, %s", info.AvatarName, where, how)
	sh.term.Printf("      help for commands, chat to talk, %s to come back", KeyName(sh.cfg.Prefix))
}

// ---------------------------------------------------------------- parsing

// parse splits a command line into words and finds a redirection.
//
// Quotes group, and nothing else is touched -- in particular a
// backslash is left alone, because an inventory path uses it to escape
// a separator and a shell that ate it would make those paths
// untypeable.
func parse(line string) (words []string, redirect string, appending bool, err error) {
	var cur []rune
	var quoted, started bool
	var quote rune
	var wantFile, want2 bool

	flush := func() {
		if !started {
			return
		}
		word := string(cur)
		switch {
		case wantFile:
			redirect, appending = word, want2
			wantFile = false
		default:
			words = append(words, word)
		}
		cur, started, quoted = nil, false, false
	}

	rs := []rune(line)
	for i := 0; i < len(rs); i++ {
		c := rs[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
				continue
			}
			started = true
			cur = append(cur, c)
		case c == '"' || c == '\'':
			quote, started, quoted = c, true, true
		case c == ' ' || c == '\t':
			flush()
		case c == '>' && !quoted:
			flush()
			appendMode := false
			if i+1 < len(rs) && rs[i+1] == '>' {
				appendMode = true
				i++
			}
			if redirect != "" || wantFile {
				return nil, "", false, fmt.Errorf("only one redirection per line")
			}
			wantFile, want2 = true, appendMode
		default:
			started = true
			cur = append(cur, c)
		}
	}
	if quote != 0 {
		return nil, "", false, fmt.Errorf("unclosed %c quote", quote)
	}
	flush()
	if wantFile && redirect == "" {
		return nil, "", false, fmt.Errorf("no file after >")
	}
	return words, redirect, appending, nil
}

// ---------------------------------------------------------------- commands

// command is one thing slsh can do.
type command struct {
	usage string
	brief string
	run   func(ctx context.Context, sh *Shell, out io.Writer, args []string) error
}

var commands map[string]*command

// commandNames is every command, sorted, for help and completion.
func commandNames() []string {
	out := make([]string, 0, len(commands))
	for n := range commands {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func init() {
	commands = map[string]*command{}
	for _, set := range []map[string]*command{inventoryCommands, worldCommands, socialCommands} {
		for n, c := range set {
			commands[n] = c
		}
	}

	commands["help"] = &command{
		usage: "help [command]",
		brief: "what the commands are",
		run: func(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
			if len(args) > 0 {
				c, ok := commands[args[0]]
				if !ok {
					return fmt.Errorf("no command %q", args[0])
				}
				fmt.Fprintf(out, "%-28s %s\n", c.usage, c.brief)
				return nil
			}
			// Aliases share a command, and listing one twice
			// under two names says nothing extra.
			seen := map[*command]bool{}
			for _, n := range commandNames() {
				c := commands[n]
				if seen[c] {
					continue
				}
				seen[c] = true
				fmt.Fprintf(out, "%-28s %s\n", c.usage, c.brief)
			}
			fmt.Fprintf(out, "\nA path may be quoted, and \\/ is a slash inside a name.\n")
			fmt.Fprintf(out, "Output redirects with > and >>, and \". file\" runs a file of commands.\n")
			return nil
		},
	}
	commands["quit"] = &command{
		usage: "quit",
		brief: "leave slsh",
		run: func(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
			sh.Quit()
			return nil
		},
	}
	commands["exit"] = commands["quit"]
	commands["."] = &command{
		usage: ". FILE",
		brief: "run the commands in a file",
		run: func(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
			if len(args) != 1 {
				return fmt.Errorf("usage: . FILE")
			}
			return sh.Source(ctx, args[0])
		},
	}
	commands["source"] = commands["."]
	commands["echo"] = &command{
		usage: "echo [text ...]",
		brief: "print the arguments, which is how to write a note into a file",
		run: func(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
			fmt.Fprintln(out, strings.Join(args, " "))
			return nil
		},
	}
}
