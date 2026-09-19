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
// Measured (see llm.go for the conditions): restoring a 1457 token
// conversation took 5.4 ms and saved 5.0 s of prompt processing.  That
// is the whole argument for doing this at all.
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
// it costs the five milliseconds above.

import (
	"context"
	"encoding/json"
	"fmt"
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
const Medium = "You are speaking through instant messages in a virtual world. " +
	"Reply in a few sentences at most, in character, as speech rather than prose."

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

	// State is the kv cache file the server last wrote for this
	// conversation, and By is the fingerprint it was written under.
	// Both, because the file alone says nothing: a state whose
	// fingerprint has moved describes a conversation with a different
	// model and must not be loaded. See Props.Fingerprint.
	State string `json:"state,omitempty"`
	By    string `json:"by,omitempty"`

	// Tokens is what the server counted the last prompt at.  Measured
	// rather than estimated, and it is what drives the trimming: a
	// budget checked against a guess at the tokeniser is a budget that
	// is wrong in whichever direction the guess leans.
	Tokens int `json:"tokens,omitempty"`
}

// Key names a conversation, and names its files.
func (c *Conversation) Key() string { return c.Avatar + "-" + c.With }

// stateName is what the server is asked to call this conversation's kv
// cache.  A flat name because the server's slot directory is flat.
func (c *Conversation) stateName() string { return c.Key() + ".bin" }

// Prompt is the conversation as the model is given it: the backstory,
// then everything said, then the new remark.
func (c *Conversation) Prompt(backstory, said string) []Message {
	system := strings.TrimSpace(backstory)
	if system == "" {
		system = Medium
	} else {
		system += "\n\n" + Medium
	}

	out := make([]Message, 0, len(c.Turns)+2)
	out = append(out, Message{Role: "system", Content: system})
	for _, t := range c.Turns {
		out = append(out, Message{Role: t.Role, Content: t.Text})
	}
	out = append(out, Message{Role: "user", Content: said})
	return out
}

// Add records something said.
func (c *Conversation) Add(role, text string, at time.Time) {
	c.Turns = append(c.Turns, Turn{Role: role, Text: text, At: at})
	c.Spoke = at
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
// A file that will not parse is not an error and not a stop: it is one
// conversation's memory, the daemon has several, and refusing to speak
// to somebody because the record of the last time is unreadable is
// worse than starting again.  It is logged by the caller and replaced
// on the next save.
func (s *Store) Load(avatar string, with msg.UUID, name string) *Conversation {
	c := &Conversation{
		Avatar: avatar, With: with.String(), WithName: name,
		Started: time.Now(),
	}
	b, err := os.ReadFile(s.path(avatar, with.String()))
	if err != nil {
		return c
	}
	var got Conversation
	if err := json.Unmarshal(b, &got); err != nil {
		return c
	}
	if name != "" {
		got.WithName = name
	}
	return &got
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

	// props is what the server said about itself, asked once and kept.
	// It is asked lazily because the model may well be started after
	// this daemon: a slbotd that refused to run until llama-server was
	// up would be a slbotd that has to be started in an order.
	mu    sync.Mutex
	props *Props
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
			c.slots = newSlotPool(p.Slots)
		}
		c.logf("model %s (%s), %d slots of %d tokens, build %s",
			p.Alias, p.Ftype, p.Slots, p.Settings.Ctx, p.Build)
	}
	return c.props, nil
}

// Backstory is what an avatar was given to be.
//
// Read every time rather than held in memory, so that working on a
// character is an edit and not a restart.  It is safe to change under
// a running daemon because its text is part of the fingerprint: change
// the words and every saved kv cache for that avatar stops matching
// and is prefilled again from the conversation's text.
//
// A backstory that cannot be read is not a refusal to speak.  The
// avatar answers with no character at all, which is visible in one line
// of log and in the first thing it says, where a silent daemon would be
// neither.
func (c *Chatter) Backstory(avatar string) string {
	path := c.cfg.Backstory[avatar]
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		c.logf("%s has no backstory: %v", avatar, err)
		return ""
	}
	return strings.TrimSpace(string(b))
}

// Reply is one turn: what this avatar says back.
func (c *Chatter) Reply(ctx context.Context, conv *Conversation, said string) (string, error) {
	props, err := c.ready(ctx)
	if err != nil {
		return "", err
	}
	backstory := c.Backstory(conv.Avatar)
	fingerprint := props.Fingerprint(backstory)

	slot, mine, err := c.slots.take(ctx, conv.Key())
	if err != nil {
		return "", err
	}
	defer c.slots.give(slot)

	if !mine {
		c.place(ctx, conv, slot, fingerprint)
	}

	answer, err := c.llm.Chat(ctx, Ask{
		Messages:    conv.Prompt(backstory, said),
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

	// Saved before trimming.  The cache holds the prompt that was just
	// processed, and that is the one worth keeping: llama-server
	// matches whatever prefix the next prompt shares with it and
	// reprocesses the rest, so a trim costs the tail of the cache
	// rather than making the file wrong.
	if n, err := c.llm.SaveSlot(ctx, slot, conv.stateName()); err != nil {
		// Not fatal, and not even unusual: a server started without
		// --slot-save-path refuses every one of these.  The
		// conversation goes on; it is only slower to pick up.
		c.logf("%s: could not keep the context for %s: %v",
			conv.Avatar, conv.WithName, err)
		conv.State, conv.By = "", ""
	} else {
		conv.State, conv.By = conv.stateName(), fingerprint
		_ = n
	}

	if conv.Trim(c.cfg.ChatContext) {
		c.logf("%s: trimmed the conversation with %s to about %d tokens",
			conv.Avatar, conv.WithName, conv.Tokens)
	}
	if err := c.store.Save(conv); err != nil {
		c.logf("%s: could not write the conversation with %s: %v",
			conv.Avatar, conv.WithName, err)
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
		c.logf("%s: could not clear slot %d: %v", conv.Avatar, slot, err)
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
