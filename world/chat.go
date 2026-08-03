package world

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
)

// Chat types, from the viewer's LLChatType.  A script's run-time
// errors arrive on Debug.
const (
	ChatWhisper = 0
	ChatSay     = 1
	ChatShout   = 2
	ChatDebug   = 6
	ChatRegion  = 7
	ChatOwner   = 8
	ChatDirect  = 9
)

// Chat source types, from the viewer's LLChatSourceType.
const (
	SourceSystem = 0
	SourceAgent  = 1
	SourceObject = 2
)

// Line is something heard.
//
// It carries everything that distinguishes one utterance from another,
// because a subscription may be hearing several sources at once and
// the channel is the only thing they arrive on.  Source says who,
// SourceType says what kind of thing that is, and Type says which
// channel it came in on -- open chat and the debug channel are the
// same message with a different Type.
type Line struct {
	At       time.Time
	Sequence uint32 // the packet it arrived in

	// Source is the object or avatar that spoke; Owner is who owns it
	// when it is an object, and From is the name shown.
	Source msg.UUID
	Owner  msg.UUID
	From   string

	SourceType uint8
	Type       uint8

	// Audible is whether the simulator thought we could hear it.
	Audible uint8

	// Position is where it was said.
	Position msg.Vector3

	Text string
}

// FromObject and FromAgent say what kind of thing spoke.
func (l Line) FromObject() bool { return l.SourceType == SourceObject }
func (l Line) FromAgent() bool  { return l.SourceType == SourceAgent }

// Debug reports whether the line is on the channel the simulator puts
// script errors on.
func (l Line) Debug() bool { return l.Type == ChatDebug }

func (l Line) String() string {
	return l.From + ": " + l.Text
}

// ChatTypeName names a chat type.
func ChatTypeName(t uint8) string {
	switch t {
	case ChatWhisper:
		return "whisper"
	case ChatSay:
		return "say"
	case ChatShout:
		return "shout"
	case ChatDebug:
		return "debug"
	case ChatRegion:
		return "region"
	case ChatOwner:
		return "owner"
	case ChatDirect:
		return "direct"
	}
	return "type " + itoa(int(t))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// Fault is a run-time error the simulator blamed on a named script.
//
// This is the fatal kind, and the only kind that can be recognised.
// The simulator reports it as two messages: a header naming the object
// and the script, then the reason on its own.  A script that hits one
// stops, so there is no point waiting for anything more from it.
//
// The other kind of run-time complaint -- "Could not find texture" and
// its relatives -- is a single line naming nothing, and the script
// carries on afterwards.  Those are not faults and must not end a run.
type Fault struct {
	Script string // the script the simulator named
	Reason string // "Math Error", "Stack-Heap Collision"
	Line   Line   // the header line it was recognised from
}

func (f *Fault) String() string {
	if f.Reason == "" {
		return f.Script + ": run-time error"
	}
	return f.Script + ": " + f.Reason
}

// faultScript recognises the header of a script fault and returns the
// script it names:
//
//	Test HUD [script:slgo try divzero] Script run-time error
func faultScript(text string) (string, bool) {
	const open = "[script:"
	i := strings.Index(text, open)
	if i < 0 {
		return "", false
	}
	rest := text[i+len(open):]
	j := strings.Index(rest, "]")
	if j < 0 || !strings.Contains(rest[j:], "Script run-time error") {
		return "", false
	}
	return rest[:j], true
}

// collector gathers chat, optionally from one object, and fires when a
// sentinel appears or the named script faults.
type collector struct {
	source   msg.UUID // zero means anything
	sentinel string
	faultFor string // script name to watch for faults in; empty disables

	mu    sync.Mutex
	lines []Line
	fn    func(Line)
	fault *Fault

	found    chan struct{}
	faulted  chan struct{}
	reasoned chan struct{}
	once     sync.Once
	onceF    sync.Once
	onceR    sync.Once
}

func (c *collector) add(l Line) {
	c.mu.Lock()
	c.lines = append(c.lines, l)
	fn := c.fn
	hit := c.sentinel != "" && strings.Contains(l.Text, c.sentinel)

	// A fault is two messages.  The header names the script, and
	// exactly one debug line follows it with the reason, so the run
	// can end the moment that arrives.
	fired := false
	gotReason := false
	if c.faultFor != "" && c.fault == nil {
		if name, ok := faultScript(l.Text); ok && name == c.faultFor {
			c.fault = &Fault{Script: name, Line: l}
			fired = true
		}
	} else if c.fault != nil && c.fault.Reason == "" && l.Debug() {
		if _, isHeader := faultScript(l.Text); !isHeader {
			c.fault.Reason = l.Text
			gotReason = true
		}
	}
	c.mu.Unlock()

	if fn != nil {
		fn(l)
	}
	if hit {
		c.once.Do(func() { close(c.found) })
	}
	if fired {
		c.onceF.Do(func() { close(c.faulted) })
	}
	if gotReason {
		c.onceR.Do(func() { close(c.reasoned) })
	}
}

// faultSeen returns the fault, if one was recognised.
func (c *collector) faultSeen() *Fault {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fault == nil {
		return nil
	}
	f := *c.fault
	return &f
}

func (c *collector) collected() []Line {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Line(nil), c.lines...)
}

// chat routes one heard line to whoever is listening.
func (w *World) chat(raw *client.Message, m *msg.ChatFromSimulator) {
	d := &m.ChatData
	l := Line{
		At:         raw.At,
		Sequence:   raw.Sequence,
		Source:     d.SourceID,
		Owner:      d.OwnerID,
		From:       trimNul(d.FromName),
		SourceType: d.SourceType,
		Type:       d.ChatType,
		Audible:    d.Audible,
		Position:   d.Position,
		Text:       trimNul(d.Message),
	}
	w.mu.Lock()
	cs := make([]*collector, len(w.collectors))
	copy(cs, w.collectors)
	w.mu.Unlock()

	for _, c := range cs {
		if !c.source.IsZero() && c.source != l.Source {
			continue
		}
		c.add(l)
	}
	w.deliver(l)
}

func (w *World) startCollector(c *collector) {
	c.found = make(chan struct{})
	c.faulted = make(chan struct{})
	c.reasoned = make(chan struct{})
	w.mu.Lock()
	w.collectors = append(w.collectors, c)
	w.mu.Unlock()
}

func (w *World) stopCollector(c *collector) {
	w.mu.Lock()
	for i, x := range w.collectors {
		if x == c {
			w.collectors = append(w.collectors[:i], w.collectors[i+1:]...)
			break
		}
	}
	w.mu.Unlock()
}

// ChatFilter narrows what a subscription hears.  The zero value hears
// everything.
type ChatFilter struct {
	// Source, if set, hears only this object or avatar.
	Source msg.UUID

	// SourceTypes, if non-empty, hears only these kinds of speaker:
	// SourceObject, SourceAgent, SourceSystem.
	SourceTypes []uint8

	// Types, if non-empty, hears only these chat types.  This is how
	// to ask for the debug channel and nothing else.
	Types []uint8
}

func (f *ChatFilter) match(l Line) bool {
	if !f.Source.IsZero() && f.Source != l.Source {
		return false
	}
	if len(f.SourceTypes) > 0 && !hasU8(f.SourceTypes, l.SourceType) {
		return false
	}
	if len(f.Types) > 0 && !hasU8(f.Types, l.Type) {
		return false
	}
	return true
}

func hasU8(xs []uint8, x uint8) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// DefaultChatDepth is the buffer a subscription gets when none is
// asked for.
const DefaultChatDepth = 64

// chatSub is one subscription.
type chatSub struct {
	ch      chan Line
	filter  ChatFilter
	dropped atomic.Uint64
}

// chatCmd adds or removes a subscription.
//
// Both go through the goroutine that reads the relay, so that
// goroutine is the only one that ever touches a subscription's
// channel.  It is the only writer and the only closer, which is what
// makes closing safe: with any other arrangement a close can land
// between a sender deciding to send and sending.
type chatCmd struct {
	add    *chatSub
	remove <-chan Line
	done   chan struct{}
}

// Chat returns a channel of everything heard that matches the filter,
// and it is closed when StopChat is called or the connection ends.
//
// The channel is buffered to depth, and the reader never blocks on it:
// a subscription whose buffer is full has lines dropped rather than
// holding up the relay for everything else.  ChatDropped says how many
// went that way.
//
// A line matching several subscriptions is delivered to all of them.
func (w *World) Chat(filter ChatFilter, depth int) <-chan Line {
	if depth <= 0 {
		depth = DefaultChatDepth
	}
	sub := &chatSub{ch: make(chan Line, depth), filter: filter}
	done := make(chan struct{})
	select {
	case w.chatCtl <- chatCmd{add: sub, done: done}:
		<-done
	case <-w.readDone:
		// The reader has stopped, so nothing will ever be delivered.
		// Hand back a closed channel rather than one that stays empty
		// for ever.
		close(sub.ch)
	}
	return sub.ch
}

// StopChat closes a subscription.
//
// It returns once the channel has been closed, so a caller ranging
// over it will see the range end.  Stopping something already stopped
// does nothing.
func (w *World) StopChat(ch <-chan Line) {
	done := make(chan struct{})
	select {
	case w.chatCtl <- chatCmd{remove: ch, done: done}:
		<-done
	case <-w.readDone:
	}
}

// ChatDropped is how many lines a subscription missed because its
// buffer was full.
func (w *World) ChatDropped(ch <-chan Line) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.chatSubs[ch]; s != nil {
		return s.dropped.Load()
	}
	return 0
}

// deliver hands a line to every subscription that wants it.
//
// Called from the reader goroutine only.  The send is non-blocking:
// one slow consumer must not stop the relay, and the protocol has no
// way to ask for chat again, so the choice is between dropping for
// that subscriber and stalling for everyone.
func (w *World) deliver(l Line) {
	w.mu.Lock()
	subs := make([]*chatSub, 0, len(w.chatSubs))
	for _, s := range w.chatSubs {
		subs = append(subs, s)
	}
	w.mu.Unlock()

	for _, s := range subs {
		if !s.filter.match(l) {
			continue
		}
		select {
		case s.ch <- l:
		default:
			s.dropped.Add(1)
		}
	}
}

// applyChat runs one subscription command, in the reader goroutine.
func (w *World) applyChat(c chatCmd) {
	w.mu.Lock()
	switch {
	case c.add != nil:
		w.chatSubs[c.add.ch] = c.add
	case c.remove != nil:
		if s := w.chatSubs[c.remove]; s != nil {
			delete(w.chatSubs, c.remove)
			close(s.ch)
		}
	}
	w.mu.Unlock()
	if c.done != nil {
		close(c.done)
	}
}

// closeChat shuts every subscription down, which is what tells a
// caller ranging over one that there will be no more.
func (w *World) closeChat() {
	w.mu.Lock()
	for ch, s := range w.chatSubs {
		delete(w.chatSubs, ch)
		close(s.ch)
	}
	w.mu.Unlock()
}

// onProperties is a small internal subscription used by Properties.
func (w *World) onProperties(fn func(*Properties)) (stop func()) {
	w.mu.Lock()
	w.propsFns = append(w.propsFns, fn)
	i := len(w.propsFns) - 1
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if i < len(w.propsFns) {
			w.propsFns[i] = nil
		}
		w.mu.Unlock()
	}
}

// Say puts something in open chat, which everyone nearby hears.
//
// Prefer a script's llOwnerSay for test output: this is for when open
// chat is the thing being tested.
func (w *World) Say(ctx context.Context, text string, channel int32) error {
	return w.SayAs(ctx, text, channel, ChatSay)
}

// SayAs is Say with the chat type chosen: whisper, normal or shout.
func (w *World) SayAs(ctx context.Context, text string, channel int32, chatType uint8) error {
	m := &msg.ChatFromViewer{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ChatData.Message = append([]byte(text), 0)
	m.ChatData.Type = chatType
	m.ChatData.Channel = channel
	return w.Send(ctx, m)
}
