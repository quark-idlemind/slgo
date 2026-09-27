package main

// Conversation: what an avatar says back when what arrived was not a
// command.
//
// # What is kept, and what is only cached
//
// The conversation is TEXT, on disk, and that is the record.  The kv
// cache is an accelerator and nothing else: it is welded to one model,
// one quantisation, one context size and one server build, and the
// server checks none of that before loading one -- so a state file is
// something to be grateful for when it works and to throw away without
// ceremony when it does not.  Everything here is arranged around that
// asymmetry.  Losing the text loses the conversation; losing the cache
// costs a few seconds of prefill.
//
// Restoring a kept cache was measured at milliseconds, where prefilling
// the same conversation took seconds, and that is the whole argument for
// doing this at all.
// Why: doc/slbotd.md#keeping-the-kv-cache
//
// # Why a slot is pinned
//
// llama-server has a fixed number of parallel slots, each with its own
// kv cache, and hands them out itself unless told which to use.  Left
// to it, a conversation lands somewhere different each turn and its
// cache is always somewhere else.  So slbotd keeps the mapping: a
// conversation holds a slot while it speaks, slots go to whoever spoke
// least recently when there are more conversations than slots, and the
// displaced conversation is picked up again from its state file.  That
// is the "swap the context per conversation" this was built for, and
// it costs those milliseconds.
//
// # Remembering more than fits
//
// A context window holds a few thousand tokens and a conversation that
// goes on for weeks does not.  Dropping the oldest exchanges keeps it
// inside the window and gives an avatar no memory at all: it forgets
// your name between Tuesday and Thursday, and it forgets it silently.
//
// So at the budget the old turns are COMPACTED rather than dropped --
// handed to the model, which writes a short note, and replaced by that
// note.  The recent turns stay word for word, because they carry the
// thread of what is being said now; everything older becomes three
// lines that carry what it was about.  Compacting again folds the note
// in with whatever has accumulated since, so one note always stands for
// the whole of the conversation before the last few exchanges.
//
// It is lossy and it drifts, and it is meant to.  What it buys is not a
// transcript, it is the perception of having been talked to before --
// that the avatar knows who you are, what you told it and what it
// agreed to, which is what somebody means when they say it remembers.
//
// The backstory is not part of any of this and cannot be lost to it.
// It is read from its file on every single turn and is always the first
// message; only Turns are ever summarised, and Summarise is never given
// the character.  That is structural rather than careful: there is no
// path through this file on which the two meet.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Medium is what the model is told about where it is talking.
//
// Appended to the backstory rather than written into it, because it is
// about the CHANNEL and not about the character: every avatar this
// daemon holds is talking through instant messages however different
// they are otherwise, and an operator writing a character should not
// have to remember to explain the plumbing to it.
//
// Short on purpose.  A long instruction here competes with the
// backstory for the model's attention, and the backstory is the part
// somebody wrote.
//
// The third sentence is the price of the gap hint working at all:
// without it the model was measured never to mention the elapsed time,
// and with it about half the time.  It is conditional by its own
// wording, so it costs nothing on the turns where no gap is offered --
// which is most of them, since nothing is sent below ChatGap.
// Why: doc/slbotd.md#the-sentence-that-makes-a-pause-show
const Medium = "You are speaking through instant messages in a virtual world. " +
	"Reply in a few sentences at most, in character, as speech rather than prose. " +
	"If you are told how long it has been since they last wrote, remark on it."

// Turn is one thing said.
type Turn struct {
	Role string    `json:"role"` // "user" or "assistant"
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Conversation is one avatar talking with one person.
type Conversation struct {
	Avatar   string `json:"avatar"`
	With     string `json:"with"`
	WithName string `json:"with_name"`

	Started time.Time `json:"started"`
	Spoke   time.Time `json:"spoke"`

	Turns []Turn `json:"turns"`

	// Times is when the last few things were said, compaction or not.
	// The turns that are folded into the summary lose their times with
	// them, and a bound on how much two models say in one sitting has
	// to count across a compaction: with a short context, eight
	// exchanges can be enough to set one off.
	Times []time.Time `json:"times,omitempty"`

	// State is the kv cache file the server last wrote for this
	// conversation, and By is the fingerprint it was written under.
	// Both, because the file alone says nothing: a state whose
	// fingerprint has moved describes a conversation with a different
	// model and must not be loaded. See Props.Fingerprint.
	State string `json:"state,omitempty"`
	By    string `json:"by,omitempty"`

	// Summary is what was said before the turns that are still kept
	// word for word, as the model wrote it down.  This is the whole of
	// an avatar's memory of a conversation beyond the last few
	// exchanges.
	Summary string `json:"summary,omitempty"`

	// Compacted is how many turns have been folded into that summary,
	// which is the only measure of how much of the conversation the
	// avatar is no longer holding exactly.
	Compacted int `json:"compacted,omitempty"`

	// Tokens is what the server counted the last prompt at.  Measured
	// rather than estimated, and it is what decides when to compact: a
	// budget checked against a guess at the tokeniser is a budget that
	// is wrong in whichever direction the guess leans.
	Tokens int `json:"tokens,omitempty"`
}

// Key names a conversation, and names its files.
func (c *Conversation) Key() string { return c.Avatar + "-" + c.With }

// stateName is what the server is asked to call this conversation's kv
// cache.  A flat name because the server's slot directory is flat.
func (c *Conversation) stateName() string { return c.Key() + ".bin" }

// Remembered introduces the summary to the avatar as its own memory
// rather than as a document.  An avatar told "here is a summary" talks
// about the summary; one told it remembers something talks about the
// thing.
const Remembered = "What you remember of talking with this person before: "

// Prompt is the conversation as the model is given it: what the avatar
// is, what it remembers, what was said lately, and the new remark.
//
// The memory is a message of its own when the template will carry one,
// and joined onto the backstory when it will not.  Two is better where
// it works: the backstory is then byte for byte the same message on
// every turn, so it stays a prefix the kv cache can match even across a
// compaction, which is the one moment everything after it changes.
func (c *Conversation) Prompt(backstory, said string, separate bool) []Message {
	character := strings.TrimSpace(backstory)
	if character == "" {
		character = Medium
	} else {
		character += "\n\n" + Medium
	}

	memory := ""
	if c.Summary != "" {
		memory = Remembered + c.Summary
	}

	out := make([]Message, 0, len(c.Turns)+3)
	switch {
	case memory == "":
		out = append(out, Message{Role: "system", Content: character})
	case separate:
		out = append(out, Message{Role: "system", Content: character})
		out = append(out, Message{Role: "system", Content: memory})
	default:
		out = append(out, Message{Role: "system", Content: character + "\n\n" + memory})
	}

	for _, t := range c.Turns {
		out = append(out, Message{Role: t.Role, Content: t.Text})
	}
	out = append(out, Message{Role: "user", Content: said})
	return out
}

// Paused is how long this conversation has been quiet, when that is
// long enough to be worth telling the avatar about.
//
// A conversation nobody has said anything in yet has not paused; it
// has not started.  And the threshold is a floor and not a memory: the
// turns, the summary and the kept context are all exactly as they
// were, whatever this says.  The avatar is being told that time
// passed, in the conversation it was already having.
func (c *Conversation) Paused(floor time.Duration, now time.Time) (time.Duration, bool) {
	if c.Spoke.IsZero() || len(c.Turns) == 0 || floor <= 0 {
		return 0, false
	}
	d := now.Sub(c.Spoke)
	if d < floor {
		return 0, false
	}
	return d, true
}

// Gap is how the elapsed time is put to the model.
//
// On the new remark and nowhere else.  Everything before the last
// message is the cached prefix -- the backstory, the memory, the turns
// -- and a sentence up there that changes every turn would invalidate
// the whole conversation's kv cache on every single reply, which is
// the one expensive mistake available here.  On the tail it costs
// nothing that was not already being processed, and lands where a
// model weighs hardest.
//
// It is also sent and not STORED: Prompt is given this and Add is
// given what the person actually wrote, so the hint never enters the
// record, never reaches the summariser, and cannot come back later as
// something they said.
//
// "It has been X" rather than "X have passed", because X is sometimes
// "an hour" and sometimes "3 days" and only one of those phrasings
// survives both.
func Gap(d time.Duration) string {
	return fmt.Sprintf("(It has been %s since they last wrote to you.)", ago(d))
}

// Add records something said.
func (c *Conversation) Add(role, text string, at time.Time) {
	c.Turns = append(c.Turns, Turn{Role: role, Text: text, At: at})
	c.Spoke = at
	c.Times = append(c.Times, at)
	if len(c.Times) > keptTimes {
		c.Times = append([]time.Time(nil), c.Times[len(c.Times)-keptTimes:]...)
	}
}

// keptTimes is how many times Add remembers: more than any sensible
// chat-own, which is what they are for.
const keptTimes = 64

// Recent is how many things have been said since the two last went
// rest without saying anything -- counting back from the latest, and
// none at all if that was itself rest ago.  Zero rest counts everything
// ever said, compacted or not, as if no rest were ever long enough.
//
// A conversation written before the times were kept has only its kept
// turns to go by, which is right for everything since its last
// compaction and misses what was folded away before.
func (c *Conversation) Recent(now time.Time, rest time.Duration) int {
	if rest <= 0 {
		return c.Compacted + len(c.Turns)
	}
	times := c.Times
	if len(times) == 0 {
		for _, t := range c.Turns {
			times = append(times, t.At)
		}
	}
	n, next := 0, now
	for i := len(times) - 1; i >= 0; i-- {
		if next.Sub(times[i]) >= rest {
			break
		}
		n++
		next = times[i]
	}
	return n
}

// Trim drops the oldest turns until the conversation should fit the
// budget.
//
// "Should", because the only exact count available is of the prompt
// that was just sent, and that one is already over.  The tokens per
// character of THAT prompt is what the estimate is scaled by, so it is
// this model's own tokeniser on this conversation's own text rather
// than a rule of thumb about English -- which is as close as anything
// can get without another round trip.
//
// Turns go in pairs, oldest first, because half an exchange left at the
// front of a conversation is a question nobody answered or an answer to
// nothing.
func (c *Conversation) Trim(budget int) bool {
	if c.Tokens <= budget || len(c.Turns) < 2 {
		return false
	}
	chars := 0
	for _, t := range c.Turns {
		chars += len(t.Text)
	}
	if chars == 0 {
		return false
	}
	perChar := float64(c.Tokens) / float64(chars)

	// Aim below the budget rather than at it, so that a conversation
	// does not trim one pair on every single turn for the rest of its
	// life.
	want := float64(budget) * 0.8
	trimmed := false
	for len(c.Turns) >= 2 && float64(c.Tokens) > want {
		gone := float64(len(c.Turns[0].Text)+len(c.Turns[1].Text)) * perChar
		c.Turns = c.Turns[2:]
		c.Tokens -= int(gone)
		trimmed = true
	}
	return trimmed
}

// ------------------------------------------------------------- the store

// Store is where conversations live between runs.
type Store struct{ dir string }

// NewStore makes the directory if it is not there.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(avatar, with string) string {
	return filepath.Join(s.dir, avatar, with+".json")
}

// Load reads a conversation, or hands back an empty one.
//
// The conversation is usable whatever the error says.  A file that will
// not read or parse is not a stop: it is one conversation's memory, the
// daemon has several, and refusing to speak to somebody because the
// record of the last time is unreadable is worse than starting again.
// So it is renamed aside for somebody to look at later -- see setAside
// -- an empty conversation comes back, and the error says what happened
// and where the file went, for the caller to log.
func (s *Store) Load(avatar string, with msg.UUID, name string) (*Conversation, error) {
	c, what, err := s.read(avatar, with, name)
	if err == nil {
		return c, nil
	}
	aside, moved := setAside(s.path(avatar, with.String()), time.Now())
	if moved != nil {
		return c, fmt.Errorf("the conversation with %s %s (%v), and could not be set aside: %v; begun again",
			with, what, err, moved)
	}
	return c, fmt.Errorf("the conversation with %s %s (%v); kept as %s, and begun again",
		with, what, err, aside)
}

// Peek is Load for a look that writes nothing back.  A file that will
// not read comes back as an empty conversation and is left where it is,
// for the next Load to set aside and report.
func (s *Store) Peek(avatar string, with msg.UUID, name string) *Conversation {
	c, _, _ := s.read(avatar, with, name)
	return c
}

// read is a conversation off the disk, a new one when there is no file,
// and a new one with the error and what it means -- "would not be read"
// or "would not parse" -- when there is a file and it is no good.
func (s *Store) read(avatar string, with msg.UUID, name string) (*Conversation, string, error) {
	fresh := &Conversation{
		Avatar: avatar, With: with.String(), WithName: name,
		Started: time.Now(),
	}
	b, err := os.ReadFile(s.path(avatar, with.String()))
	if errors.Is(err, fs.ErrNotExist) {
		return fresh, "", nil
	}
	if err != nil {
		return fresh, "would not be read", err
	}
	var got Conversation
	if err := json.Unmarshal(b, &got); err != nil {
		return fresh, "would not parse", err
	}
	if name != "" {
		got.WithName = name
	}
	return &got, "", nil
}

// setAside renames an unreadable conversation to
// <name>.unreadable-<UTC time> beside it, mode and all, and never over
// another file: a second one set aside in the same second gets a
// number on the end.  It says what the file is called now.
func setAside(path string, now time.Time) (string, error) {
	base := path + ".unreadable-" + now.UTC().Format("20060102T150405Z")
	aside := base
	for n := 2; ; n++ {
		_, err := os.Lstat(aside)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		aside = fmt.Sprintf("%s-%d", base, n)
	}
	return aside, os.Rename(path, aside)
}

// Save writes it back, through a temporary file so that a crash cannot
// leave half a conversation behind.
func (s *Store) Save(c *Conversation) error {
	dir := filepath.Join(s.dir, c.Avatar)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+c.With+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.path(c.Avatar, c.With))
}

// ------------------------------------------------------------- the slots

// slotPool hands the server's parallel slots to conversations.
//
// Affinity first: a conversation that still holds the slot it spoke in
// finds its kv cache already there and pays nothing at all.  Failing
// that, an empty slot; failing that, whichever slot spoke least
// recently, whose conversation is on disk and will be picked up again
// when it next says something.
type slotPool struct {
	mu   sync.Mutex
	held []string // the conversation in each slot, empty for none
	busy []bool
	used []int64
	tick int64

	// freed is closed and replaced whenever a slot is given back, which
	// is how a waiter is woken.  A channel rather than a sync.Cond so
	// that waiting can be given up on when the daemon is stopping;
	// sync.Cond has no way to be cancelled.
	freed chan struct{}
}

func newSlotPool(n int) *slotPool {
	if n < 1 {
		n = 1
	}
	return &slotPool{
		held:  make([]string, n),
		busy:  make([]bool, n),
		used:  make([]int64, n),
		freed: make(chan struct{}),
	}
}

// N is how many slots there are.
func (p *slotPool) N() int { return len(p.held) }

// take gives a slot to a conversation, waiting for one if they are all
// speaking.  mine says the slot already held this conversation, which
// is the case where nothing has to be restored.
func (p *slotPool) take(ctx context.Context, key string) (slot int, mine bool, err error) {
	for {
		p.mu.Lock()
		if s, ok := p.pick(key); ok {
			was := p.held[s]
			p.held[s], p.busy[s] = key, true
			p.tick++
			p.used[s] = p.tick
			p.mu.Unlock()
			return s, was == key, nil
		}
		wait := p.freed
		p.mu.Unlock()

		select {
		case <-wait:
		case <-ctx.Done():
			return 0, false, ctx.Err()
		}
	}
}

// pick chooses a slot under the lock: ours, then an empty one, then the
// one that spoke longest ago.
func (p *slotPool) pick(key string) (int, bool) {
	for i, h := range p.held {
		if h == key && !p.busy[i] {
			return i, true
		}
	}
	for i, h := range p.held {
		if h == "" && !p.busy[i] {
			return i, true
		}
	}
	best, found := 0, false
	for i := range p.held {
		if p.busy[i] {
			continue
		}
		if !found || p.used[i] < p.used[best] {
			best, found = i, true
		}
	}
	return best, found
}

// give hands a slot back and wakes whoever is waiting.
func (p *slotPool) give(slot int) {
	p.mu.Lock()
	p.busy[slot] = false
	close(p.freed)
	p.freed = make(chan struct{})
	p.mu.Unlock()
}

// ------------------------------------------------------------- the talker

// Chatter is the whole of the conversation machinery: one model, one
// store, one set of slots, shared by every avatar.
//
// Shared rather than one per avatar because the slots are the server's
// and there is one server.  Two avatars talking at once are two
// conversations competing for the same slots, which is exactly what the
// pool is for.
type Chatter struct {
	cfg   Config
	llm   *LLM
	store *Store
	slots *slotPool
	logf  func(string, ...any)

	// troubled is told about a failure that belongs to one avatar, so
	// that whoever drives that avatar can ask it what has gone wrong.
	// Nil is a Chatter nobody is watching, which is what a test has.
	troubled func(avatar, text string)

	// props is what the server said about itself, asked once and kept.
	// It is asked lazily because the model may well be started after
	// this daemon: a slbotd that refused to run until llama-server was
	// up would be a slbotd that has to be started in an order.
	mu       sync.Mutex
	props    *Props
	separate *bool // whether a second system message survives the template

	// held serialises everything that writes one conversation, by key.
	//
	// There are two writers now and there did not used to be: a reply
	// being composed, and a line this avatar said through ANOTHER
	// client of the same session, which arrives whenever it arrives.
	// Both read the file, add to it and write it back, so without this
	// the later write silently drops whatever the earlier one added --
	// and what it drops is somebody's remark, which is the one thing a
	// conversation is made of.
	heldMu sync.Mutex
	held   map[string]*sync.Mutex
}

// hold takes the lock for one conversation and hands back the release.
func (c *Chatter) hold(key string) func() {
	c.heldMu.Lock()
	m := c.held[key]
	if m == nil {
		m = new(sync.Mutex)
		c.held[key] = m
	}
	c.heldMu.Unlock()
	m.Lock()
	return m.Unlock
}

// NewChatter prepares the machinery.  It talks to nothing yet.
func NewChatter(cfg Config, logf func(string, ...any)) (*Chatter, error) {
	dir, err := cfg.ChatStore()
	if err != nil {
		return nil, err
	}
	store, err := NewStore(dir)
	if err != nil {
		return nil, err
	}
	return &Chatter{
		held:  map[string]*sync.Mutex{},
		cfg:   cfg,
		llm:   NewLLM(cfg.LLMURL, cfg.LLMModel, cfg.LLMTimeout),
		store: store,
		logf:  logf,
		// Sized when the server is first asked how many it has.
		slots: newSlotPool(1),
	}, nil
}

// ready asks the server about itself, once, and sizes the pool to what
// it says.
func (c *Chatter) ready(ctx context.Context) (*Props, error) {
	c.mu.Lock()
	p := c.props
	c.mu.Unlock()
	if p != nil {
		return p, nil
	}

	p, err := c.llm.Props(ctx)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.props == nil {
		c.props = p
		if p.Slots > 0 {
			n := p.Slots
			if c.cfg.LLMSlots > 0 && c.cfg.LLMSlots < n {
				n = c.cfg.LLMSlots
				c.logf("using slots 0 to %d of the server's %d; the rest are left for other programs", n-1, p.Slots)
			}
			c.slots = newSlotPool(n)
		}
		c.logf("model %s (%s), %d slots of %d tokens, build %s",
			p.Alias, p.Ftype, p.Slots, p.Settings.Ctx, p.Build)
	}
	return c.props, nil
}

// errf logs a failure that belongs to one avatar and keeps it where
// that avatar's admin can ask for it.  See trouble.go.
func (c *Chatter) errf(avatar, format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	c.logf("%s: %s", avatar, text)
	if c.troubled != nil {
		c.troubled(avatar, text)
	}
}

// Backstory is what an avatar was given to be, talking to this person.
//
// Who is being spoken to is part of the question because the
// configuration may name a directory rather than a file, and a
// directory can hold a character for one person as well as the one
// everybody gets.  See character.go.
//
// Read every time rather than held in memory, so that working on a
// character is an edit and not a restart.  It is safe to change under
// a running daemon because its text is part of the fingerprint: change
// the words and every saved kv cache that was built on them stops
// matching and is prefilled again from the conversation's text.  With a
// directory that is per conversation rather than per avatar, since what
// a person is told is now part of what changed.
//
// A backstory that cannot be read is not a refusal to speak.  The
// avatar answers with no character at all, which is visible in one line
// of log and in the first thing it says, where a silent daemon would be
// neither.
func (c *Chatter) Backstory(avatar string, who msg.UUID, name string) string {
	path := c.cfg.Backstory[avatar]
	if path == "" {
		return ""
	}
	text, err := readCharacter(path, who, name)
	if err != nil {
		c.errf(avatar, "no backstory: %v", err)
		return ""
	}
	return text
}

// load is Store.Load, with a conversation set aside for being
// unreadable logged and kept among the avatar's troubles.  Called with
// the conversation held; see hold.
func (c *Chatter) load(avatar string, who msg.UUID, name string) *Conversation {
	conv, err := c.store.Load(avatar, who, name)
	if err != nil {
		c.errf(avatar, "%v", err)
	}
	return conv
}

// Reply is one turn: what this avatar says back.
//
// The conversation is loaded here rather than handed in, and held for
// the whole turn.  Another client of this session may be writing to the
// same one -- see hold -- and a turn that read it before that write and
// saved after would take the remark back out again.
func (c *Chatter) Reply(ctx context.Context, avatar string, who msg.UUID, name, said string) (string, error) {
	release := c.hold(avatar + "-" + who.String())
	defer release()
	conv := c.load(avatar, who, name)

	props, err := c.ready(ctx)
	if err != nil {
		return "", err
	}
	backstory := c.Backstory(conv.Avatar, who, name)
	fingerprint := props.Fingerprint(backstory)

	slot, mine, err := c.slots.take(ctx, conv.Key())
	if err != nil {
		return "", err
	}
	defer c.slots.give(slot)

	// Whether this turn is about to rewrite the conversation, which
	// decides whether restoring the old context is worth anything: it
	// would be thrown away by the compaction just below.
	folding := c.shouldCompact(conv)
	if !mine && !folding {
		c.place(ctx, conv, slot, fingerprint)
	}
	if folding {
		c.compact(ctx, conv, slot)
	}

	// What is SENT, which may carry the pause; what is stored is still
	// what the person wrote.  See Gap.
	asked := said
	if d, ok := conv.Paused(c.cfg.ChatGap, time.Now()); ok {
		asked = Gap(d) + "\n\n" + said
	}

	answer, err := c.llm.Chat(ctx, Ask{
		Messages:    conv.Prompt(backstory, asked, c.separateMemory(ctx)),
		Slot:        slot,
		MaxTokens:   c.cfg.ChatTokens,
		Temperature: c.cfg.ChatTemp,
	})
	if err != nil {
		return "", err
	}
	if answer.Text == "" {
		return "", fmt.Errorf("the model said nothing at all")
	}

	now := time.Now()
	conv.Add("user", said, now)
	conv.Add("assistant", answer.Text, now)
	conv.Tokens = answer.Prompt

	// The cache holds the prompt that was just processed, and that is
	// the one worth keeping: llama-server matches whatever prefix the
	// next prompt shares with it and reprocesses the rest.
	if n, err := c.llm.SaveSlot(ctx, slot, conv.stateName()); err != nil {
		// Not fatal, and not even unusual: a server started without
		// --slot-save-path refuses every one of these.  The
		// conversation goes on; it is only slower to pick up.
		c.errf(conv.Avatar, "could not keep the context for %s: %v", conv.WithName, err)
		conv.State, conv.By = "", ""
	} else {
		conv.State, conv.By = conv.stateName(), fingerprint
		_ = n
	}

	if err := c.store.Save(conv); err != nil {
		c.errf(conv.Avatar, "could not write the conversation with %s: %v", conv.WithName, err)
	}
	return answer.Text, nil
}

// place gets a conversation's context into the slot it is about to
// speak in.
//
// Restoring is tried only when the fingerprint still matches; anything
// else is erased instead.  Erasing rather than leaving it is the part
// worth being deliberate about: a slot still holding somebody else's
// conversation shares a prefix with this one -- they begin with the
// same backstory -- so the server would match that prefix and carry
// the wrong person's words into this reply.
func (c *Chatter) place(ctx context.Context, conv *Conversation, slot int, fingerprint string) {
	if conv.State != "" && conv.By == fingerprint {
		if n, err := c.llm.RestoreSlot(ctx, slot, conv.State); err == nil {
			c.logf("%s: picked up %d tokens of the conversation with %s",
				conv.Avatar, n, conv.WithName)
			return
		} else {
			// Every failure looks the same from here -- missing,
			// truncated, from another model, too big -- so there is
			// nothing to tell apart and nothing to do but prefill.
			c.logf("%s: the kept context for %s would not load (%v); starting it again",
				conv.Avatar, conv.WithName, err)
		}
	}
	if err := c.llm.EraseSlot(ctx, slot); err != nil {
		c.errf(conv.Avatar, "could not clear slot %d: %v", slot, err)
	}
}

// Store is where conversations are kept, for whoever is reporting.
func (c *Chatter) Store() *Store { return c.store }

// List is every conversation an avatar has kept, most recently spoken
// first.
//
// A conversation that will not parse is skipped rather than reported:
// the listing is for a person asking who this avatar has been talking
// to, and one unreadable file should not take the answer away.
func (s *Store) List(avatar string) []*Conversation {
	ents, err := os.ReadDir(filepath.Join(s.dir, avatar))
	if err != nil {
		return nil
	}
	var out []*Conversation
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(s.dir, avatar, e.Name()))
		if err != nil {
			continue
		}
		var c Conversation
		if err := json.Unmarshal(b, &c); err != nil {
			continue
		}
		out = append(out, &c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spoke.After(out[j].Spoke) })
	return out
}

// Forget removes a conversation.
//
// The kv cache goes with it where this process can reach it, and is
// left where it cannot: the slot directory belongs to the server, which
// may be on another machine, and a state file nobody deletes is wasted
// disk rather than a conversation that comes back.  It cannot come
// back -- the text is what a conversation is made of, and that is what
// this removes.
func (s *Store) Forget(avatar, with, slotDir string) error {
	if err := os.Remove(s.path(avatar, with)); err != nil {
		return err
	}
	if slotDir != "" {
		_ = os.Remove(filepath.Join(slotDir, avatar+"-"+with+".bin"))
	}
	return nil
}

// Conversations is the store's listing for an avatar.
func (c *Chatter) Conversations(avatar string) []*Conversation { return c.store.List(avatar) }

// Forget drops one.
func (c *Chatter) Forget(avatar, with string) error {
	return c.store.Forget(avatar, with, c.cfg.SlotDir)
}

// Model is what the server said about itself, if it has been asked.
func (c *Chatter) Model(ctx context.Context) (*Props, error) { return c.ready(ctx) }

// ------------------------------------------------------------ remembering

// Summarise is what the model is told when it is being asked to
// remember rather than to talk.
//
// Deliberately not in character.  This is the one call that is not the
// avatar speaking, and a dockhand asked to summarise a conversation
// writes a dockhand's remark about it rather than a note anybody can
// use.  So the backstory does not reach here at all -- which is also
// why an avatar's character cannot be lost to compaction: there is no
// path through this file on which the two meet.
//
// Three labelled lines, because a note asked for in prose summarised
// nothing and lost the person's name, and a labelled one keeps it and
// survives being folded again.
// Why: doc/slbotd.md#why-the-note-is-three-labelled-lines
//
// The fields are what somebody means when they say an avatar remembers
// them: who they are, what has been talked about, and what was agreed.
const Summarise = "You keep notes on the people somebody talks to. From the exchange " +
	"below, write exactly these three lines and nothing else:\n\n" +
	"THEM: the other person's name, and their trade, home or anything else they said " +
	"about themselves\n" +
	"TOPICS: what was talked about, a few words each\n" +
	"OWED: anything asked for, promised or agreed, with when\n\n" +
	"Write 'unknown' for a line nothing was said about. Keep every name, place, number " +
	"and date. Do not copy the exchange back."

// shouldCompact reports whether this conversation has outgrown its
// budget and has enough in it to be worth folding.
//
// The count is the one the server took of the LAST prompt, so the
// decision is made on a measurement rather than an estimate -- and it
// is one turn behind, which is the right way to be wrong: a
// conversation compacts just after it crosses the line rather than just
// before, and never on a guess that it might.
func (c *Chatter) shouldCompact(conv *Conversation) bool {
	return conv.Tokens > c.cfg.ChatContext && len(conv.Turns) > c.cfg.ChatKeep
}

// compact folds the older turns into the summary.
//
// It costs a second call to the model, on the turn that crosses the
// budget, and the person waiting for a reply waits for both.  That is
// the price of the avatar remembering them, it is paid once every
// several dozen exchanges, and the alternative -- doing it afterwards,
// in the background -- would have a second goroutine writing to a
// conversation while the next remark is being answered from it.
//
// A failure here is not a failure of the turn.  The conversation is
// trimmed instead, which is what this replaced: the avatar forgets the
// oldest exchanges rather than remembering them imperfectly, the reply
// still happens, and the log says which it was.
func (c *Chatter) compact(ctx context.Context, conv *Conversation, slot int) {
	keep := c.cfg.ChatKeep
	fold := conv.Turns[:len(conv.Turns)-keep]
	if len(fold) == 0 {
		return
	}

	said, err := c.llm.Chat(ctx, Ask{
		Messages:  summaryPrompt(conv.Summary, fold),
		Slot:      slot,
		MaxTokens: c.cfg.ChatSummary,
		// Low, because this is a note and not a performance.  The
		// character's temperature belongs to the character.
		Temperature: 0.2,
	})
	if err != nil || strings.TrimSpace(said.Text) == "" {
		if conv.Trim(c.cfg.ChatContext) {
			c.errf(conv.Avatar, "could not summarise the conversation with %s (%v); "+
				"dropped the oldest exchanges instead", conv.WithName, err)
		}
		return
	}

	conv.Summary = strings.TrimSpace(said.Text)
	conv.Compacted += len(fold)
	conv.Turns = conv.Turns[len(fold):]

	// The count described the conversation that has just been replaced,
	// and nothing here knows what the new one comes to.  The next real
	// call measures it; until then it must not be treated as known, or
	// the turn after this one would compact again on a stale number.
	conv.Tokens = 0

	// The kept context describes the history that was just rewritten.
	// llama-server would match what little the two still share and
	// reprocess the rest, which is correct but is not what the file
	// claims to be, so the claim goes.
	conv.State, conv.By = "", ""

	c.logf("%s: folded %d turns of the conversation with %s into %d characters of memory",
		conv.Avatar, len(fold), conv.WithName, len(conv.Summary))
}

// summaryPrompt is the conversation as something to be summarised
// rather than continued.
//
// The turns are rendered INTO one message with who said what spelt out,
// rather than handed over as turns.  Given real turns the model answers
// the last one -- it is a chat model and that is what a chat model does
// -- and what comes back is the next remark in the conversation instead
// of a note about it.
func summaryPrompt(previous string, turns []Turn) []Message {
	var b strings.Builder
	if previous != "" {
		b.WriteString("The note so far:\n")
		b.WriteString(previous)
		b.WriteString("\n\nWhat has been said since:\n")
	} else {
		b.WriteString("What has been said:\n")
	}
	for _, t := range turns {
		who := "They"
		if t.Role == "assistant" {
			who = "You"
		}
		fmt.Fprintf(&b, "%s: %s\n", who, t.Text)
	}
	b.WriteString("\nWrite the note now, folding in everything above.")

	return []Message{
		{Role: "system", Content: Summarise},
		{Role: "user", Content: b.String()},
	}
}

// separateMemory reports whether a second system message survives this
// model's chat template.
//
// Asked once, of the server, rather than assumed.  Templates differ and
// some keep the first system message and drop the rest -- and a memory
// dropped by a template is the worst of the failures available here:
// the avatar goes on answering fluently, having forgotten everything,
// and nothing anywhere says so.  So a marker is rendered through
// /apply-template and looked for in what comes back.
//
// A server that will not answer the question is assumed to be the
// stricter of the two.  Joining the memory onto the backstory works
// everywhere; it only costs the stable prefix.
func (c *Chatter) separateMemory(ctx context.Context) bool {
	c.mu.Lock()
	known := c.separate
	c.mu.Unlock()
	if known != nil {
		return *known
	}

	const marker = "REMEMBERED-MARKER-8f31"
	ok := false
	got, err := c.llm.Template(ctx, []Message{
		{Role: "system", Content: "CHARACTER"},
		{Role: "system", Content: marker},
		{Role: "user", Content: "hello"},
	})
	switch {
	case err != nil:
		c.logf("could not ask how the template handles a second system message (%v); "+
			"keeping memory in the first one", err)
	case strings.Contains(got, marker):
		ok = true
	default:
		c.logf("this model's template drops a second system message; " +
			"keeping memory in the first one")
	}

	c.mu.Lock()
	if c.separate == nil {
		c.separate = &ok
	}
	out := *c.separate
	c.mu.Unlock()
	return out
}

// Remember records something this avatar said through another client of
// the same session.
//
// A person answering through slsh, as an avatar slbotd is attending, is
// that avatar speaking: there is one avatar and there should be one
// memory of what it said, or the daemon will later contradict a promise
// the person made through the same mouth.  So the line is kept exactly
// as a reply of its own would be, and folds into the note when the
// conversation is next compacted.
//
// Recorded and never acted on.  Nothing here answers, and nothing here
// treats it as a remark that wants an answer -- an avatar that replied
// to its own speech would be talking to itself, and with "chat = *" it
// would do so for ever.  sl.IM.Conversation is false for these, which
// is the same rule said once further down.
//
// A conversation that does not exist yet is not started by one.  An
// avatar saying something to somebody it has never spoken to has said
// one thing to them; the memory of a conversation begins when there is
// a conversation, and a store full of one-line files nobody replied to
// is not worth the disk.
func (c *Chatter) Remember(avatar string, who msg.UUID, name, said string) bool {
	release := c.hold(avatar + "-" + who.String())
	defer release()

	conv := c.load(avatar, who, name)
	if len(conv.Turns) == 0 {
		return false
	}
	conv.Add("assistant", said, time.Now())
	if err := c.store.Save(conv); err != nil {
		c.errf(avatar, "could not record what was said to %s elsewhere: %v", name, err)
		return false
	}
	return true
}
