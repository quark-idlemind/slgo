package world

import (
	"context"
	"strings"
	"sync"
	"time"

	"slgo/client"
	"slgo/msg"
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

// Line is something heard.
type Line struct {
	At       time.Time
	Sequence uint32 // the packet it arrived in
	Source   msg.UUID
	From     string
	Type     uint8
	Text     string
}

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
		At:       raw.At,
		Sequence: raw.Sequence,
		Source:   d.SourceID,
		From:     trimNul(d.FromName),
		Type:     d.ChatType,
		Text:     trimNul(d.Message),
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

// Listen gathers chat until the returned function is called, which
// returns what was heard.
//
// Source narrows it to one object; a zero id hears everything.  Start
// listening before doing the thing that causes the talking: a script
// says what it has to say the moment it is compiled, and a listener
// started afterwards has already missed it.
func (w *World) Listen(source msg.UUID, onLine func(Line)) (stop func() []Line) {
	c := &collector{source: source, fn: onLine}
	w.startCollector(c)
	return func() []Line {
		w.stopCollector(c)
		return c.collected()
	}
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
	m := &msg.ChatFromViewer{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.ChatData.Message = append([]byte(text), 0)
	m.ChatData.Type = ChatSay
	m.ChatData.Channel = channel
	return w.Send(ctx, m)
}
