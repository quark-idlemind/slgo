// Package viewer is slgod's side of handing a live session to a real
// viewer.
//
// slgod is already logged in and holding a circuit to a simulator.  A
// viewer is then pointed at slgod, speaks the ordinary login protocol,
// and is handed that session's agent id, session id and circuit code
// with slgod's own address in place of the simulator's.  From then on
// slgod is a simulator to the viewer and a client to the simulator, and
// moves messages between them.
//
// This file is the record of what crossed, and it is here because of how
// the far end fails.  A viewer that is missing something does not say
// so: it renders an empty grey world and waits, looking exactly the same
// whether the fault is a message never sent, a message sent in the wrong
// direction, or a message the relay ate.  With no record of what
// crossed, every one of those is the same symptom and the only tool is
// guessing.
//
// So there are two records, and they answer different questions.
//
// The Trace is the transcript: every packet, both directions, in the
// order it happened.  It answers "what actually crossed, and in what
// order" -- which is the question worth asking once something has gone
// wrong and there is a suspect.
//
// The Census is the tally: how many of each message went each way, and
// what was dropped and why.  It answers "what is missing", and that is
// the question that actually finds these faults.  A viewer stuck at a
// grey screen is nearly always waiting on one particular message, and a
// census showing that message with a count of zero -- or, worse, under a
// disposition other than forwarded -- names the bug outright, where the
// transcript would have to be read to notice the absence of something.
package viewer

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Direction is which way a packet was going, named from slgod's point of
// view because slgod is the only party that sees both circuits.
type Direction uint8

const (
	// FromSim is a packet the simulator sent to slgod, and ToSim one
	// slgod sent to the simulator: the circuit that already exists
	// and stays up whether a viewer is attached or not.
	FromSim Direction = iota
	ToSim

	// FromViewer and ToViewer are the viewer's circuit, on which slgod
	// is the simulator.  It outlasts any one viewer, so it can be up
	// with none attached.
	FromViewer
	ToViewer
)

func (d Direction) String() string {
	switch d {
	case FromSim:
		return "sim>slgod"
	case ToSim:
		return "slgod>sim"
	case FromViewer:
		return "viewer>slgod"
	case ToViewer:
		return "slgod>viewer"
	}
	return fmt.Sprintf("direction(%d)", uint8(d))
}

// directions is every Direction, in the order a report should list them.
var directions = []Direction{FromSim, ToSim, FromViewer, ToViewer}

// Disposition is what became of a message the relay was offered, or,
// for a retransmission, why it was not offered one.
type Disposition uint8

const (
	// Forwarded is the ordinary case: it went to the other side.
	Forwarded Disposition = iota

	// Absorbed means the relay understood it and deliberately did not
	// pass it on -- a viewer's LogoutRequest, which would end the real
	// session, or a ping this side answers itself.
	Absorbed

	// NoViewer means there was nowhere to forward it: no viewer had
	// joined, which is ordinary while slgod runs on its own, or, for
	// a message from the viewer, there was no session to give it to.
	NoViewer

	// Dropped means there was somewhere to send it and no room to
	// queue it -- a viewer not keeping up with the simulator.  It is
	// counted rather than waited on, because waiting would be the
	// grid session waiting.
	Dropped

	// Retransmission means the packet repeated one already handled, so
	// the relay was not offered it again: whatever became of the first
	// is what became of the message.
	Retransmission
)

func (d Disposition) String() string {
	switch d {
	case Forwarded:
		return "forwarded"
	case Absorbed:
		return "absorbed"
	case NoViewer:
		return "no viewer attached"
	case Dropped:
		return "dropped, viewer behind"
	case Retransmission:
		return "retransmission, not relayed again"
	}
	return fmt.Sprintf("disposition(%d)", uint8(d))
}

// dispositions is every Disposition, in report order.
var dispositions = []Disposition{Forwarded, Absorbed, NoViewer, Dropped, Retransmission}

// Count is what the census knows about one message in one direction.
type Count struct {
	Name string
	Dir  Direction

	// Packets is how many arrived and Last is when the most recent
	// one did.  A message that has never been seen has no Count at
	// all, which is the case worth noticing.
	Packets uint64
	Last    time.Time

	// By counts what became of them.  The total matches Packets.
	By map[Disposition]uint64
}

// key identifies one row of the census.
type key struct {
	name string
	dir  Direction
}

// Census tallies what crossed, by message and direction.
//
// It is safe for concurrent use, which it has to be: each circuit's
// dispatch and send goroutines write here, and so does the one that
// passes the simulator's messages to the viewer.
type Census struct {
	mu     sync.Mutex
	rows   map[key]*Count
	total  uint64
	perDir map[Direction]uint64
}

// NewCensus prepares an empty Census.
func NewCensus() *Census {
	return &Census{
		rows:   make(map[key]*Count),
		perDir: make(map[Direction]uint64),
	}
}

// Record notes one packet and what became of it.
func (c *Census) Record(name string, dir Direction, at time.Time, what Disposition) {
	c.mu.Lock()
	defer c.mu.Unlock()

	k := key{name, dir}
	row := c.rows[k]
	if row == nil {
		row = &Count{Name: name, Dir: dir, By: make(map[Disposition]uint64)}
		c.rows[k] = row
	}
	row.Packets++
	row.By[what]++
	if at.After(row.Last) {
		row.Last = at
	}
	c.total++
	c.perDir[dir]++
}

// Counts returns every row, sorted by message name and then direction,
// so two runs of the same thing produce comparable output.
func (c *Census) Counts() []Count {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := make([]Count, 0, len(c.rows))
	for _, row := range c.rows {
		cp := *row
		cp.By = make(map[Disposition]uint64, len(row.By))
		for k, v := range row.By {
			cp.By[k] = v
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Dir < out[j].Dir
	})
	return out
}

// Total is how many packets have been recorded, in all directions.
func (c *Census) Total() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

// Seen reports whether this message has ever gone this way.  It is the
// question a stuck viewer poses: not how many, but whether at all.
func (c *Census) Seen(name string, dir Direction) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rows[key{name, dir}] != nil
}

// Report renders the census as a table, one line per message and
// direction, with a last column saying what became of its packets:
// each disposition that happened, and how many times.
//
// A message that appears nowhere at all is the one a stuck viewer is
// probably waiting for, and one that was not forwarded says why.
func (c *Census) Report() string {
	rows := c.Counts()

	var b strings.Builder
	fmt.Fprintf(&b, "%-40s %-14s %8s  %s\n", "message", "direction", "packets", "disposition")
	for _, r := range rows {
		var what []string
		for _, d := range dispositions {
			if n := r.By[d]; n > 0 {
				what = append(what, fmt.Sprintf("%s %d", d, n))
			}
		}
		fmt.Fprintf(&b, "%-40s %-14s %8d  %s\n",
			r.Name, r.Dir, r.Packets, strings.Join(what, ", "))
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	fmt.Fprintf(&b, "\n%d packets in %d rows:", c.total, len(rows))
	for _, d := range directions {
		fmt.Fprintf(&b, " %s %d", d, c.perDir[d])
	}
	b.WriteByte('\n')
	return b.String()
}

// Trace writes both circuits to one stream in arrival order.
//
// One stream rather than two, because the useful questions are about
// order across the two circuits -- whether slgod forwarded a thing
// before or after the viewer asked for it -- and two files with two
// clocks cannot answer that.
//
// A nil Trace is usable and does nothing, so a caller can hold one
// whether or not tracing was asked for.
type Trace struct {
	mu   sync.Mutex
	w    io.Writer
	only map[string]bool

	// bodies says whether to write each message out in full.  Off, an
	// entry is one line naming the message, which is enough to see
	// order and is affordable on a busy region; on, it is the whole
	// decoded message as YAML, which is what is wanted once there is
	// a suspect.
	bodies bool

	written uint64
	skipped uint64
}

// NewTrace writes to w.  Only, if non-empty, limits the trace to the
// named messages, and bodies asks for the whole decoded message rather
// than a single line.
//
// A busy region is thousands of packets a second and most of them are
// object updates, so writing every body by default would bury the
// handful of messages a handover actually turns on.  Name the messages
// under suspicion instead.
func NewTrace(w io.Writer, only []string, bodies bool) *Trace {
	t := &Trace{w: w, bodies: bodies}
	if len(only) > 0 {
		t.only = make(map[string]bool, len(only))
		for _, n := range only {
			t.only[n] = true
		}
	}
	return t
}

// Write records one packet travelling in one direction.
//
// It is safe to call from any goroutine, and it serialises them: the
// file's order is the order the entries reached the lock, which is as
// close to the order things happened as a relay can know.
func (t *Trace) Write(dir Direction, p *msg.Packet, what Disposition) {
	if t == nil || t.w == nil {
		return
	}
	name := MessageName(p)
	if t.only != nil && !t.only[name] {
		t.mu.Lock()
		t.skipped++
		t.mu.Unlock()
		return
	}

	var buf []byte
	if t.bodies {
		buf = append(buf, "---\n"...)
		buf = append(buf, "direction: "...)
		buf = append(buf, dir.String()...)
		buf = append(buf, '\n')
		buf = append(buf, "disposition: "...)
		buf = append(buf, what.String()...)
		buf = append(buf, '\n')
		buf = msg.AppendPacketYAML(buf, p)
	} else {
		buf = fmt.Appendf(buf, "%s %-14s %-40s seq=%d %s\n",
			stamp(p.At), dir, name, p.Header.Sequence, what)
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.w.Write(buf); err != nil {
		// A trace that cannot be written must not take the session
		// with it: this is a diagnostic, and the session it is
		// diagnosing is worth more than the record of it.
		t.w = nil
		return
	}
	t.written++
}

// Stats is how many entries were written and how many the filter
// dropped.  A trace that looks empty is usually a filter naming a
// message that never arrived, and the skipped count says so.
func (t *Trace) Stats() (written, skipped uint64) {
	if t == nil {
		return 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.written, t.skipped
}

// MessageName is what to call a packet in a record.
//
// A packet that would not decode still has a number, and a number that
// is not in the template still has a frequency and an index, so there is
// always something to write down.  Naming it "unknown" and moving on
// would throw away the one detail that says which message a build is
// missing.
func MessageName(p *msg.Packet) string {
	if p.Message != nil {
		if info := p.Message.MsgInfo(); info != nil && info.Name != "" {
			return info.Name
		}
	}
	if p.ID != 0 {
		return p.ID.String()
	}
	if len(p.Acks) > 0 {
		return "(acks only)"
	}
	return "(empty)"
}

func stamp(at time.Time) string {
	if at.IsZero() {
		return "                       "
	}
	return at.UTC().Format("15:04:05.000000")
}
