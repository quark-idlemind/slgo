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
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

	// log is the transcript, or nil when there is none: nothing is
	// kept when "log" is off, and a transcript that could not be
	// opened is a shell that carries on without one.  logErr is what
	// stopped it, said once in the banner rather than at every line.
	log    *transcript
	logErr error

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

	// history is what has been typed at a command prompt, and said is
	// what has been typed at a chat one.  Two rings rather than one,
	// because neither is any use where the other belongs: a command
	// recalled in chat would be said out loud, and a remark recalled
	// at a command prompt would be run.  See ring, and recall.
	history ring
	said    ring

	talk  *Conversations
	quit  chan struct{}
	once  sync.Once
	depth int // how deep the sourcing goes, to stop a file sourcing itself

	// promptGen numbers each prompt as it is worked out, and
	// promptDrawn is the last one drawn, under promptMu.  See prompt.
	promptGen   atomic.Uint64
	promptMu    sync.Mutex
	promptDrawn uint64
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
	if cfg.Log {
		// The avatar's name is what the file is called, so this waits
		// until there is a session to ask.  A failure here is not a
		// reason to refuse to start a shell: the session is up and
		// working, and losing the transcript is worth saying and
		// carrying on from.
		sh.log, sh.logErr = openTranscript(cfg.LogDir, s.Info().AvatarName, cfg.Agent)
	}
	return sh
}

// Close gives up whatever the shell holds that the process does not.
// Safe more than once.
func (sh *Shell) Close() error { return sh.log.Close() }

// printf writes a line above the prompt and into the transcript.
//
// Everything a person sees that is not a command's own output comes
// through here -- what was heard, what was said, every notice and every
// error -- which is what makes one funnel enough to keep a transcript
// with.  A command's output goes to its writer instead, because that
// writer may be a file the command was redirected into.
func (sh *Shell) printf(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	sh.term.Print(line)
	sh.log.line(line)
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
			return sessionEnded(sh.s.Err())
		case r, ok := <-keys:
			if !ok {
				return nil
			}
			sh.key(ctx, r)
		}
	}
}

// sessionEnded says plainly that the daemon ended this shell's session,
// and why.  slgod ends a stream with FailedPrecondition and a sentence
// for a person when the session under it is stopped for good or no
// longer hosted, and the sentence is shown without the rpc wrapping.
// Any other ending is passed on as it came.
func sessionEnded(err error) error {
	if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
		return fmt.Errorf("the session ended: %s", st.Message())
	}
	return err
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
		sh.recall(r == keyUp)
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
		answer := sh.term.Take()
		sh.log.line("| " + answer)
		sh.typed(ctx, answer)
		return
	}
	if sh.chatting() {
		// Chat does not echo the line here: send prints it in its own
		// marked form, so that what was said and what was heard can be
		// told apart.
		line := sh.term.Take()
		if strings.TrimSpace(line) != "" {
			// Remembered before it goes, and whether or not it goes.
			// The command ring keeps a command that failed for the
			// same reason: a line the circuit would not take is
			// exactly the line somebody wants back.
			sh.remember(line)
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

// ring is one history: the lines that were entered, oldest first, and
// where the arrows have got to.  at == len(lines) is the fresh line
// below the newest entry, which is where typing starts.
type ring struct {
	lines []string
	at    int
}

// add records a line that was entered.
func (r *ring) add(line string) {
	// The same line twice running is one entry, as in a shell.
	if n := len(r.lines); n == 0 || r.lines[n-1] != line {
		r.lines = append(r.lines, line)
	}
	r.at = len(r.lines)
}

// walk moves one step and answers with what to put on the line, and
// whether there was anything to move through at all: an empty history
// leaves what is half typed alone rather than clearing it.
func (r *ring) walk(back bool) (string, bool) {
	if len(r.lines) == 0 {
		return "", false
	}
	if back {
		if r.at > 0 {
			r.at--
		}
	} else if r.at < len(r.lines) {
		r.at++
	}
	if r.at >= len(r.lines) {
		// Past the newest: a fresh line, not the last entry again.
		return "", true
	}
	return r.lines[r.at], true
}

// recall walks the history of the mode the keyboard is in.
//
// What comes back lands on the line to be edited and is not sent or
// run by the recall, in either mode.  That is the whole of the case
// this was asked for: a line said in the wrong conversation is got
// back by tabbing to the right one and pressing up, and the ring
// belongs to the SHELL rather than to the conversation, so tabbing
// does not change what is on offer.
func (sh *Shell) recall(back bool) {
	sh.mu.Lock()
	line, ok := sh.ringLocked().walk(back)
	sh.mu.Unlock()
	if ok {
		sh.term.SetLine(line)
	}
}

func (sh *Shell) remember(line string) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.ringLocked().add(line)
}

// ringLocked is the history the mode in force walks.  A multi-line
// answer walks the command ring, as it always has: it is not chat, and
// the line before it was a command.
func (sh *Shell) ringLocked() *ring {
	if sh.mode == modeChat {
		return &sh.said
	}
	return &sh.history
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
//
// It is redrawn from the printer's goroutine and the session's as well
// as this one, when what is waiting changes, so two can be under way at
// once.  Each takes a number as it starts, and one that finishes after a
// later one has drawn is dropped: a prompt read before a change of mode
// must not land on top of the one read after it.  Nothing is held while
// it is worked out, since counting what is waiting can call back into
// prompt through OnHandled.
func (sh *Shell) prompt() {
	gen := sh.promptGen.Add(1)
	p := sh.promptText()
	sh.promptMu.Lock()
	defer sh.promptMu.Unlock()
	if gen < sh.promptDrawn {
		return
	}
	sh.promptDrawn = gen
	sh.term.SetPrompt(p)
}

// promptText is what prompt draws.
func (sh *Shell) promptText() string {
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
		return fmt.Sprintf("%d text> ", n+1)
	}
	if sh.chatting() {
		return mark + sh.talk.Current().Label() + "> "
	}
	return mark + sh.Pwd() + "$ "
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
	return sh.do(ctx, line, func() ([]string, string, bool, error) { return parse(line) })
}

// DoWords runs a command that is already split into words, as slsh's
// own arguments are by the shell that started it.  Nothing in a word
// is looked at again: the one redirection is an argument that is
// exactly > or >>, and the file is the argument after it.
func (sh *Shell) DoWords(ctx context.Context, args []string) error {
	return sh.do(ctx, quoteWords(args), func() ([]string, string, bool, error) { return splitRedirect(args) })
}

// do is the rest of Do and DoWords: line is what the transcript says
// was run, and split finds the words and the redirection in it.
func (sh *Shell) do(ctx context.Context, line string, split func() ([]string, string, bool, error)) error {
	// Every command run goes through here -- one typed at the prompt,
	// one given to -c or left on slsh's command line, and every line of
	// a file being sourced -- which is why the transcript takes it here
	// and not at the keyboard.  Written before it runs, so that a
	// command that hung or took the shell down with it is still in the
	// file that says what happened.
	sh.log.line("$ " + line)

	words, redirect, appending, err := split()
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
// prompt, a line at a time, and into the transcript.
//
// A redirection hands the command an *os.File instead, and that is the
// whole of why nothing redirected is logged: the transcript is what
// somebody saw, and "ls > listing" is a listing they did not see.  The
// same rule keeps man pages out of it, since a page on a terminal goes
// through the pager rather than through here.
func (sh *Shell) stdout() io.Writer { return &termWriter{t: sh.term, log: sh.log} }

// colour reports whether a command writing to out may put escape
// sequences in what it writes.
//
// Two things have to be true and neither of them is enough on its own.
//
// The writer has to be the shell's own, which is a question about the
// type and not about the pointer: stdout() hands out a fresh
// termWriter every time it is called, so there is nothing to compare
// with, and a redirection -- "map > listing" -- hands the command an
// *os.File instead.  Colour in a file is not colour, it is rubbish in
// the middle of the text, and the file is usually on its way back
// through ". listing".
//
// And the terminal has to be one.  "slsh -c" and "slsh -f" run with
// the terminal reading /dev/null, and a session driven down a pipe
// reads a pipe; all three are Plain, and all three are output on its
// way to whatever ran slsh rather than to somebody looking at it.
//
// NO_COLOR is honoured as the convention has it, which has exactly one
// rule: any value at all, "0" included, means no colour.
func (sh *Shell) colour(out io.Writer) bool {
	if _, ok := out.(*termWriter); !ok {
		return false
	}
	if sh.term.Plain() {
		return false
	}
	return os.Getenv("NO_COLOR") == ""
}

// termWriter turns writes into whole lines above the prompt.
type termWriter struct {
	t   *Term
	log *transcript
	buf []byte
}

func (w *termWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		// Without the CR of a CRLF, which would otherwise arrive in
		// Print cut off from its newline and be shown as ^M; see
		// visible, which takes the pair as one newline.
		w.say(strings.TrimSuffix(string(w.buf[:i]), "\r"))
		w.buf = w.buf[i+1:]
	}
	// A write with no newline is held until one arrives; anything
	// left over is flushed when the command ends.
	if len(w.buf) > 0 {
		w.say(string(w.buf))
		w.buf = nil
	}
	return len(p), nil
}

// say puts one line on the screen and the same line in the transcript.
// Indented there, so that a command's output can be told from the
// command, from what was heard and from what was said.
//
// Through Print, which makes it visible: a command's output is where
// the names of things are -- objects, avatars, parcels, groups, an
// inventory -- and every one of those was chosen by somebody on the
// grid.  The transcript makes the same line visible the same way, so
// the file says what the screen said.
func (w *termWriter) say(line string) {
	w.t.Print(line)
	w.log.line("  " + line)
}

// writeOwn is the way a command writes a line with the shell's own
// escape sequences in it -- a colour -- without Print showing them as
// ^[[32m.
//
// s is taken to be safe as it stands.  Whatever in it came from the
// grid has to have been through visible already, where s was composed,
// because nothing here can tell that part from the colour round it.
// That is why this is a function of its own, to be called by name,
// rather than a Write that knows better: every call is a place that
// has to be read to be trusted, and there are few of them.
//
// A writer that is not the terminal -- a file, a test's buffer -- is
// written to as it would have been anyway.  Colour only reaches one
// when somebody asked for it, which is Shell.colour's business.
func writeOwn(out io.Writer, s string) {
	w, ok := out.(*termWriter)
	if !ok {
		io.WriteString(out, s)
		return
	}
	for _, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		w.t.printOwn(line)
		w.log.line("  " + stripANSI(line))
	}
}

func (sh *Shell) errorf(format string, args ...any) {
	sh.printf("slsh: "+format, args...)
}

func (sh *Shell) noticef(format string, args ...any) {
	sh.printf("%s * %s", stamp(), fmt.Sprintf(format, args...))
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
	sh.printf("slsh: %s in %s, %s", info.AvatarName, where, how)
	sh.printf("      help for commands, chat to talk, %s to come back", KeyName(sh.cfg.Prefix))
	// What slgod kept from before this shell attached, which is the
	// thing somebody starting a shell after a while away most wants to
	// hear and the one thing the prompt's count cannot tell them apart.
	if n := sh.fromBefore(); n > 0 {
		sh.printf("      %d %s from before this shell attached; waiting lists %s",
			n, plural(n, "thing is waiting", "things are waiting"), plural(n, "it", "them"))
	}
	if sh.logErr != nil {
		sh.printf("slsh: no transcript: %v", sh.logErr)
	}
}

// ---------------------------------------------------------------- parsing

// parse splits a command line into words and finds a redirection.
//
// Quotes group, and nothing else is touched -- in particular a
// backslash is left alone, because an inventory path uses it to escape
// a separator and a shell that ate it would make those paths
// untypeable.
func parse(line string) (words []string, redirect string, appending bool, err error) {
	words, _, redirect, appending, err = parseQuoted(line)
	return words, redirect, appending, err
}

// parseQuoted is parse, and also, for each word, whether any of it was
// in quotes.  Nothing a command runs needs to know; the checks on a
// line a model wrote do (askcheck.go), because a quoted word is a value
// somebody chose to write exactly as it stands.
func parseQuoted(line string) (words []string, quotedWords []bool, redirect string, appending bool, err error) {
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
			quotedWords = append(quotedWords, quoted)
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
				return nil, nil, "", false, fmt.Errorf("only one redirection per line")
			}
			wantFile, want2 = true, appendMode
		default:
			started = true
			cur = append(cur, c)
		}
	}
	if quote != 0 {
		return nil, nil, "", false, fmt.Errorf("unclosed %c quote", quote)
	}
	flush()
	if wantFile && redirect == "" {
		return nil, nil, "", false, fmt.Errorf("no file after >")
	}
	return words, quotedWords, redirect, appending, nil
}

// splitRedirect is parse for words that are already split: an argument
// that is exactly > or >> is a redirection and the next is its file,
// under parse's rules and with its errors.  A > inside a word is part
// of the word.
func splitRedirect(args []string) (words []string, redirect string, appending bool, err error) {
	isRedirect := func(a string) bool { return a == ">" || a == ">>" }
	for i := 0; i < len(args); i++ {
		if !isRedirect(args[i]) {
			words = append(words, args[i])
			continue
		}
		if redirect != "" {
			return nil, "", false, fmt.Errorf("only one redirection per line")
		}
		if i+1 == len(args) {
			return nil, "", false, fmt.Errorf("no file after >")
		}
		if isRedirect(args[i+1]) {
			return nil, "", false, fmt.Errorf("only one redirection per line")
		}
		redirect, appending = args[i+1], args[i] == ">>"
		i++
	}
	return words, redirect, appending, nil
}

// quoteWords writes words back as a line that parse splits into the
// same words and the same redirection, for the transcript.
func quoteWords(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		switch {
		case a == ">" || a == ">>":
			q[i] = a
		case a == "":
			q[i] = `""`
		case strings.Contains(a, `"`) && strings.Contains(a, "'"):
			// quoteWord leaves this one bare.  parse joins quoted
			// pieces, so each " goes in single quotes of its own.
			q[i] = `"` + strings.ReplaceAll(a, `"`, `"'"'"`) + `"`
		default:
			q[i] = quoteWord(a)
		}
	}
	return strings.Join(q, " ")
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

	// keywords are the words somebody would use for this command who
	// does not know its name: "home" for landmark, "nearby" for who,
	// "attachment" for worn.  ask searches them (askcorpus.go) beside
	// the brief and the page, which are written by somebody who does
	// know the name and so say "landmark --set-home" where a question
	// says "make this my home".  Space-separated, lower case, and
	// honest: a word here that the command does not answer to sends
	// somebody to the wrong command with the index's authority behind
	// it.  Every command has some; a test says so.
	keywords string

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
	for _, set := range []map[string]*command{inventoryCommands, textureCommands, objectFileCommands, carryCommands, wearCommands, linkCommands, insideCommands, waitingCommands, worldCommands, groupCommands, maturityCommands, socialCommands, objectCommands, postureCommands, walkCommands, sessionCommands, viewerCommands, setCommands, manCommands, askCommandTable} {
		for n, c := range set {
			commands[n] = c
		}
	}

	commands["help"] = &command{
		params:   "[GROUP|all]",
		flags:    func() any { return new(helpOnly) },
		brief:    "the command groups; \"help GROUP\" for one, \"help all\" for everything",
		keywords: "commands list groups overview what can do index usage",
		man:      "help",
		run:      cmdHelp,
	}
	commands["quit"] = &command{
		flags:    func() any { return new(helpOnly) },
		brief:    "leave slsh",
		keywords: "exit leave close end shell",
		man:      "quit",
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
		params:   "FILE",
		flags:    func() any { return new(helpOnly) },
		brief:    "run the commands in a file",
		keywords: "run execute batch file of commands commands from file",
		// The page is source.md: "." is not a filename anybody wants,
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
		params:   "[text ...]",
		brief:    "print the arguments, which is how to write a note into a file",
		keywords: "print write text line note into file comment",
		man:      "echo",
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
