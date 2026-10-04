package sl

import (
	"context"
	"fmt"
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

	// Channel is which channel it was said on.  Only an echo carries
	// one: what the simulator sends inward has already been filtered to
	// what this avatar can hear, and the number is not in the message.
	Channel int32

	// Mine says this avatar said it, from another client of the same
	// session, and Via names that client.  See IM.Mine, which is the
	// same arrangement for the same reason: the grid does not echo what
	// an avatar says, so without this a second client watching sees
	// every reply and none of the questions.
	Mine bool
	Via  string
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

// OutOfMemory reports whether the script ran out of room rather than
// doing something wrong.
//
// It is the one kind of fault worth telling apart, because it is the
// only one a caller can do something about: a benchmark searching for a
// size limit has just found it, and halves its script rather than
// giving up.  Mistaking a division by zero for it halves for ever, and
// mistaking it for a division by zero ends a run that had its answer.
//
// The rule lives here because the words are Second Life's -- "Stack-Heap
// Collision" is its phrase, not ours -- and this package is where the
// grid's vocabulary is already read.  It was a string match inside
// slbench before, which meant a program about measuring LSL had to
// know how a simulator words a crash.
func (f *Fault) OutOfMemory() bool {
	return strings.Contains(f.Reason, "Stack-Heap")
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
func (w *Session) chat(raw *client.Message, m *msg.ChatFromSimulator) {
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

func (w *Session) startCollector(c *collector) {
	c.found = make(chan struct{})
	c.faulted = make(chan struct{})
	c.reasoned = make(chan struct{})
	w.mu.Lock()
	w.collectors = append(w.collectors, c)
	w.mu.Unlock()
}

func (w *Session) stopCollector(c *collector) {
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
//
// The value is not promised and may change in any release: refer to it by name.
const DefaultChatDepth = 64

// chatSub is one subscription.
type chatSub struct {
	ch      chan Line
	filter  ChatFilter
	dropped atomic.Uint64
}

// chatCmd is work to run on the reader goroutine.
//
// Every subscription -- chat, permissions, instant messages, region
// changes, money -- is added and removed this way, so that the reader stays the
// only thing that ever touches a subscription's channel: the only
// writer and the only closer.  Closing from anywhere else races with a
// send no matter how it is locked.
//
// A closure rather than a field per kind, because there are five kinds
// now and the next one should cost nothing.
type chatCmd struct {
	apply func()
	done  chan struct{}
}

// permSub is a subscription to permission requests.
type permSub struct {
	ch      chan *Permission
	dropped atomic.Uint64
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
func (w *Session) Chat(filter ChatFilter, depth int) <-chan Line {
	if depth <= 0 {
		depth = DefaultChatDepth
	}
	sub := &chatSub{ch: make(chan Line, depth), filter: filter}
	if !w.addSub(func() { w.chatSubs[sub.ch] = sub }) {
		close(sub.ch)
	}
	return sub.ch
}

// addSub registers a subscription, and says whether it took.
//
// It answers false when the session has already ended, and the caller
// closes the channel it was about to hand out: nothing will ever be
// delivered to it, and a caller ranging over one is owed an end to
// range to rather than a wait with no end.
//
// The decision is made inside onReader, which holds mu -- the same lock
// closeChat takes.  Testing readDone from out here instead leaves a
// window: the reader can stop, closeChat can run, and the registration
// can arrive after it and sit in a map nobody will read again.
func (w *Session) addSub(fn func()) bool {
	added := false
	w.onReader(func() {
		if w.subsClosed {
			return
		}
		fn()
		added = true
	})
	return added
}

// StopChat closes a subscription.
//
// It returns once the channel has been closed, so a caller ranging
// over it will see the range end.  Stopping something already stopped
// does nothing.
func (w *Session) StopChat(ch <-chan Line) {
	w.onReader(func() {
		if s := w.chatSubs[ch]; s != nil {
			delete(w.chatSubs, ch)
			close(s.ch)
		}
	})
}

// ChatDropped is how many lines a subscription missed because its
// buffer was full.
func (w *Session) ChatDropped(ch <-chan Line) uint64 {
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
func (w *Session) deliver(l Line) {
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
func (w *Session) applyChat(c chatCmd) {
	if c.apply != nil {
		w.mu.Lock()
		c.apply()
		w.mu.Unlock()
	}
	if c.done != nil {
		close(c.done)
	}
}

// onReader runs fn on the reader goroutine, with the lock held, and
// waits for it.  This is how a subscription is added or removed.
//
// A session whose reader has already stopped runs fn here instead: the
// caller is owed the same effect, and there is no longer anything to
// race with.
func (w *Session) onReader(fn func()) {
	done := make(chan struct{})
	select {
	case w.chatCtl <- chatCmd{apply: fn, done: done}:
		<-done
	case <-w.readDone:
		w.mu.Lock()
		fn()
		w.mu.Unlock()
	}
}

// closeChat shuts every subscription down, which is what tells a
// caller ranging over one that there will be no more.
//
// Every kind: chat, permissions, instant messages, region changes, money,
// friends and group chat alike.  Missing one of them is not a smaller version of the same bug:
// a caller ranging over the one that was missed waits for ever, which is
// the worst way for a session to end.
func (w *Session) closeChat() {
	w.mu.Lock()
	w.subsClosed = true
	for ch, s := range w.chatSubs {
		delete(w.chatSubs, ch)
		close(s.ch)
	}
	for ch, s := range w.permSubs {
		delete(w.permSubs, ch)
		close(s.ch)
	}
	for ch, s := range w.imSubs {
		delete(w.imSubs, ch)
		close(s.ch)
	}
	for ch, s := range w.regionSubs {
		delete(w.regionSubs, ch)
		close(s.ch)
	}
	for ch, s := range w.moneySubs {
		delete(w.moneySubs, ch)
		close(s.ch)
	}
	for ch, s := range w.gchatSubs {
		delete(w.gchatSubs, ch)
		close(s.ch)
	}
	for ch, s := range w.friendSubs {
		delete(w.friendSubs, ch)
		close(s.ch)
	}
	w.mu.Unlock()
}

// onProperties is a small internal subscription used by Properties.
func (w *Session) onProperties(fn func(*Properties)) (stop func()) {
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
func (w *Session) Say(ctx context.Context, text string, channel int32) error {
	return w.SayAs(ctx, text, channel, ChatSay)
}

// SayAs is Say with the chat type chosen: whisper, normal or shout.
//
// A negative channel goes a different way; see sayNegative.  The volume
// cannot be carried on that path, so asking to whisper or shout on one
// is refused rather than quietly sent at ordinary range.
func (w *Session) SayAs(ctx context.Context, text string, channel int32, chatType uint8) error {
	if channel < 0 {
		if chatType != ChatSay {
			return fmt.Errorf("sl: %s cannot be carried on channel %d; "+
				"a negative channel goes as a script dialog reply, which has no volume",
				ChatTypeName(chatType), channel)
		}
		return w.sayNegative(ctx, text, channel)
	}
	m := &msg.ChatFromViewer{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ChatData.Message = append([]byte(text), 0)
	m.ChatData.Type = chatType
	m.ChatData.Channel = channel
	return w.Send(ctx, m)
}

// MaxDialogReply is how much text a script dialog reply can carry.  The
// template gives ButtonLabel a one byte length prefix, so 255 bytes
// including the terminator.
//
// It bounds a text box answer and a say on a negative channel alike,
// since both travel in that one field.
const MaxDialogReply = 254

// sayNegative speaks on a negative channel, which ChatFromViewer does
// not carry.
//
// The message that does is ScriptDialogReply, which needs no dialog to
// have been opened: the simulator checks only that the object id names
// something real, and delivers the text to whatever is listening.  It
// is what the viewer sends for chat on a negative channel, with its own
// id as the object -- "Hack: ChatFromViewer doesn't allow negative
// channels" (llfloaterimnearbychat.cpp:939-965; Firestorm's own path is
// fsnearbychathub.cpp:77-99) -- and Firestorm reports collisions to
// scripts the same way, on a channel from its settings.
//
// Measured against a script listening on -4242: the text arrives
// verbatim, the script sees this avatar as the speaker exactly as it
// would for chat, and the reach is chat's reach -- heard at two metres,
// not heard with the listener a hundred metres up, heard again when it
// came back.  So this is a say, not a shout and not a region-wide
// backdoor.
//
// It costs the length limit of a button label, MaxDialogReply, which
// ChatFromViewer does not have.
func (w *Session) sayNegative(ctx context.Context, text string, channel int32) error {
	if len(text) > MaxDialogReply {
		return fmt.Errorf("sl: %d bytes is too long for channel %d; "+
			"a negative channel carries at most %d", len(text), channel, MaxDialogReply)
	}
	m := &msg.ScriptDialogReply{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	// Any id that names something real will do, and this avatar is
	// the thing most certainly there.
	m.Data.ObjectID = w.me
	m.Data.ChatChannel = channel
	m.Data.ButtonIndex = 0
	m.Data.ButtonLabel = append([]byte(text), 0)
	return w.Send(ctx, m)
}

// saidElsewhere is this avatar speaking through another client.
//
// ChatFromViewer is an OUTBOUND message -- it is how a client says
// something -- so one arriving here never came from the grid.  It is
// slgod relaying what another client of this session sent, and the
// only reason the subscription exists.
//
// It is delivered as an ordinary line with Mine set, so that anything
// showing chat shows the whole of it.  The fields a simulator would
// have filled in are filled in from what is known -- the speaker is
// this avatar, by name -- and the rest are left at zero rather than
// invented.  Audible is not claimed because nothing here heard
// anything, and Position is not asked for: this runs on the reader
// goroutine, asking would be a round trip to the backend for every
// line, and it would hold up every other message arriving while it
// waited.  A caller that wants to know where the avatar is standing can
// ask, from a goroutine where the answer costs only the caller.
func (w *Session) saidElsewhere(raw *client.Message, m *msg.ChatFromViewer) {
	if raw == nil || raw.FromClient == "" {
		// Not an echo, which should not happen: the grid does not send
		// this message inward.  Dropped rather than guessed at.
		return
	}
	at := time.Now()
	if !raw.At.IsZero() {
		at = raw.At
	}
	l := Line{
		At:         at,
		Source:     w.me,
		From:       w.Info().AvatarName,
		SourceType: SourceAgent,
		Type:       m.ChatData.Type,
		Channel:    m.ChatData.Channel,
		Text:       trimNul(m.ChatData.Message),
		Mine:       true,
		Via:        raw.FromClient,
	}
	w.deliver(l)
}
