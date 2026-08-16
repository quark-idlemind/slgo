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
	"errors"
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

	// modeText collects a multi-line answer for a text box.  See
	// entry.go.
	modeText
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

	// ignoring is what has been set aside: things still waiting for an
	// answer that the prompt has stopped counting.  Keyed by what
	// waiter.key builds, and the shell's own -- another client attached
	// to this avatar has its own idea of what it has dealt with.
	ignoring map[string]bool

	// numbers is what each waiting thing is called, so that the number
	// on the screen still means the same thing after something else
	// has been answered.  nextNum is the next to hand out, and both
	// reset when nothing is left waiting.
	numbers map[string]int
	nextNum int

	// entry is the multi-line answer being typed, if one is.
	entry *entry

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
		if sh.typing() {
			sh.abandon()
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

	// The escape key leaves chat, and starts a typed answer again:
	// the line editor reaches the line being typed and no further, so
	// this is the only way back from a line already entered.
	if r == sh.cfg.Prefix {
		if sh.typing() {
			sh.startOver()
			return
		}
		if chat {
			sh.setMode(modeCommand)
			return
		}
	}

	// Ctrl-D deletes forward while there is anything to delete, and
	// otherwise means end of input: of the answer being typed, or of
	// the shell.
	if !sh.term.Key(r) && r == 4 {
		if sh.typing() {
			sh.finish(ctx)
			return
		}
		if sh.term.Line() == "" {
			sh.Quit()
		}
	}
}

func (sh *Shell) enter(ctx context.Context) {
	if sh.typing() {
		// Echoed like a command rather than swallowed like chat: what
		// was typed is the answer, and a person needs to see it to
		// know whether to type a full stop yet.
		sh.term.Echo()
		sh.typed(ctx, sh.term.Take())
		return
	}
	if sh.chatting() {
		// Chat does not echo the line here: send prints it in its own
		// marked form, so that what was said and what was heard can be
		// told apart.
		line := sh.term.Take()
		if strings.TrimSpace(line) != "" {
			sh.send(ctx, line)
		}
		return
	}

	// Leave the command on the screen before running it, so that the
	// output underneath says what it is the output of.
	sh.term.Echo()

	// No prompt until the command has returned.  Drawing one first said
	// the shell was ready when it was not: a command that takes ten
	// seconds -- removing six hundred items, walking the whole tree --
	// left a prompt sitting there that would not take a keystroke, and
	// the only way to tell that from a finished command was to try.
	//
	// Deferred so that it comes back however the command leaves, panic
	// included; a shell with no prompt is a shell that looks hung.
	sh.term.SetBusy(true)
	line := sh.term.Take()
	defer func() {
		sh.term.SetBusy(false)
		sh.prompt()
	}()

	if strings.TrimSpace(line) == "" {
		return
	}
	sh.remember(line)
	sh.Do(ctx, line)
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
	// What is waiting goes in front of everything, in both modes: a
	// teleport offered while somebody is mid-conversation is exactly
	// when it is easiest to miss, and a dialog nobody answers expires.
	mark := ""
	if n := sh.waitingCount(); n > 0 {
		mark = fmt.Sprintf("(%d) ", n)
	}
	if sh.typing() {
		sh.mu.Lock()
		n := 0
		if sh.entry != nil {
			n = len(sh.entry.lines)
		}
		sh.mu.Unlock()
		sh.term.SetPrompt(fmt.Sprintf("%d text> ", n+1))
		return
	}
	if sh.chatting() {
		sh.term.SetPrompt(mark + sh.talk.Current().Label() + "> ")
		return
	}
	sh.term.SetPrompt(mark + sh.Pwd() + "$ ")
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

// errStopped says a file of commands gave up because one of them
// failed.  What failed has already been reported where it happened, so
// whatever sees this must not report it a second time; it only says not
// to carry on.
var errStopped = errors.New("stopped")

// Do runs one command line: tokens, redirection, and the command.
//
// The error is for a caller that has to decide whether to go on -- a
// file of commands does -- and is reported here either way, since the
// person watching wants to know at the moment it happens.
func (sh *Shell) Do(ctx context.Context, line string) error {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return nil
	}

	words, redirect, appending, err := parse(line)
	if err != nil {
		sh.errorf("%v", err)
		return err
	}
	if len(words) == 0 {
		return nil
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
			return err
		}
		defer f.Close()
		out = f
	}

	return sh.run(ctx, out, words)
}

// run dispatches one already-parsed command.
func (sh *Shell) run(ctx context.Context, out io.Writer, words []string) error {
	name, args := words[0], words[1:]
	c, ok := commands[name]
	if !ok {
		sh.errorf("%s: no such command; try help", name)
		return fmt.Errorf("no such command: %s", name)
	}
	err := c.run(ctx, sh, out, args)
	switch {
	case err == nil:
	case errors.Is(err, errStopped):
		// A sourced file that stopped.  Where and why is already on
		// the screen; saying "stopped" again adds nothing.
	default:
		sh.errorf("%s: %v", name, err)
	}
	return err
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
		if err := sh.Do(ctx, line); err != nil {
			// Stop here rather than running the rest against a state
			// nobody intended.  A file of moves usually begins by
			// changing folder, and carrying on after that failed runs
			// every remaining line somewhere else -- which, when the
			// lines are removals, is not a thing to find out about
			// afterwards.
			sh.errorf("%s:%d: %s", path, n, line)
			left := remaining(sc)
			sh.errorf("stopped; %d %s not run", left, plural(left, "line was", "lines were"))
			return errStopped
		}
	}
	return sc.Err()
}

// plural picks the wording, because "1 lines were not run" reads like a
// bug in the thing reporting the bug.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// remaining counts what is left in a file that has stopped, so the
// report can say how much did not happen.
func remaining(sc *bufio.Scanner) int {
	n := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line != "" && !strings.HasPrefix(line, "#") {
			n++
		}
	}
	return n
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
	// params is what the command takes after its flags, written the way
	// a usage line writes it: "PATH DEST", "N [BUTTON|TEXT|L$FEE]".
	// Empty for a command that takes nothing but flags.  This is the
	// only part of a usage line anybody writes by hand -- see options.go
	// for the rest of it, and for why.
	params string

	// flags makes a fresh option struct for this command.  A function
	// rather than a value because parsing fills the struct in and every
	// run wants an empty one, and because the usage line is read off a
	// throwaway copy that nobody parses into.
	//
	// nil is a command with no flags at all, which is echo and nothing
	// else: it has to be able to print the word "--help".
	flags func() any

	// brief is the one line help listings print beside the usage line.
	brief string

	// man is the long description, printed by "man NAME", or empty for
	// a command that has not been written up yet.  It lives beside the
	// command it describes rather than in one file of its own, so that
	// changing what a command does and changing what is said about it
	// are the same edit.  See man.go for how it is laid out.
	man string

	run func(ctx context.Context, sh *Shell, out io.Writer, args []string) error
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
	for _, set := range []map[string]*command{inventoryCommands, textureCommands, objectFileCommands, carryCommands, wearCommands, linkCommands, insideCommands, waitingCommands, worldCommands, groupCommands, socialCommands, objectCommands, sessionCommands, viewerCommands, manCommands} {
		for n, c := range set {
			commands[n] = c
		}
	}

	commands["help"] = &command{
		params: "[GROUP|all]",
		flags:  func() any { return new(helpOnly) },
		brief:  "the command groups; \"help GROUP\" for one, \"help all\" for everything",
		man:    "help",
		run:    cmdHelp,
	}
	commands["quit"] = &command{
		flags: func() any { return new(helpOnly) },
		brief: "leave slsh",
		man:   "quit",
		run: func(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
			var flags helpOnly
			if _, done, err := subOptions("quit", &flags, out, args); err != nil || done {
				return err
			}
			sh.Quit()
			return nil
		},
	}
	commands["exit"] = commands["quit"]
	commands["."] = &command{
		params: "FILE",
		flags:  func() any { return new(helpOnly) },
		brief:  "run the commands in a file",
		// The page is source.txt: "." is not a filename anybody wants,
		// and the field naming the page is what allows the difference.
		man: "source",
		run: func(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
			var flags helpOnly
			args, done, err := subOptions(".", &flags, out, args)
			if err != nil || done {
				return err
			}
			if len(args) != 1 {
				return usageError(".")
			}
			return sh.Source(ctx, args[0])
		},
	}
	commands["source"] = commands["."]
	commands["echo"] = &command{
		params: "[text ...]",
		brief:  "print the arguments, which is how to write a note into a file",
		man:    "echo",
		// No flags at all, deliberately, which is why this is the one
		// command with no option struct.  echo exists to put a line into
		// a file, so it has to be able to print the word "--help" like
		// any other; a usage message there would be a command refusing
		// to do the one thing it is for.  Unix echo makes the same
		// choice for the same reason.
		run: func(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
			fmt.Fprintln(out, strings.Join(args, " "))
			return nil
		},
	}
}
