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

// collector gathers chat, optionally from one object, and fires when a
// sentinel appears.
type collector struct {
	source   msg.UUID // zero means anything
	sentinel string

	mu    sync.Mutex
	lines []Line
	fn    func(Line)

	found chan struct{}
	once  sync.Once
}

func (c *collector) add(l Line) {
	c.mu.Lock()
	c.lines = append(c.lines, l)
	fn := c.fn
	hit := c.sentinel != "" && strings.Contains(l.Text, c.sentinel)
	c.mu.Unlock()

	if fn != nil {
		fn(l)
	}
	if hit {
		c.once.Do(func() { close(c.found) })
	}
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
