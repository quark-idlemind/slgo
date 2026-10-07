package agent

// How each object was described: the evidence the store holds for the
// order it numbers a linkset in.  The store keeps, per object, the last
// few ways the region (or this session) spoke of it, bounded and dropped
// with the object, so that a link order that proves wrong can be read
// off what arrived instead of guessed at.
// Why: doc/objects.md#how-an-object-was-described

import (
	"sync/atomic"
	"time"
)

// DescKind is the way an object was described.
type DescKind uint8

const (
	// DescFull is an ObjectUpdate block.
	DescFull DescKind = iota + 1
	// DescCompressed is an ObjectUpdateCompressed block.
	DescCompressed
	// DescTerse is an ImprovedTerseObjectUpdate block, which only moves
	// what is known.  Consecutive ones are folded into one entry.
	DescTerse
	// DescCached is an ObjectUpdateCached block: the region says it
	// believes this session holds the object, and sends no content.
	DescCached
	// DescRequested is a RequestMultipleObjects block this session sent
	// for the object, which the region answers with a full or
	// compressed update.
	DescRequested
	// DescKilled is a KillObject for the local id, seen before the
	// object that now has it was described.
	DescKilled
)

func (k DescKind) String() string {
	switch k {
	case DescFull:
		return "full"
	case DescCompressed:
		return "compressed"
	case DescTerse:
		return "terse"
	case DescCached:
		return "cached"
	case DescRequested:
		return "requested"
	case DescKilled:
		return "killed"
	}
	return "unknown"
}

// Description is one way an object was described.
type Description struct {
	Kind DescKind

	// Parent is the parent the update carried, zero for none.  A cached,
	// requested or killed entry carries none.
	Parent uint32

	// Seq is the sequence number of the packet, Message which message
	// this store heard it in (the blocks of one packet share one), and
	// Block and Blocks the place of the block in it and how many it held.
	// Message counts from the store's start; it is not on the wire.
	Seq     uint32
	Message uint32
	Block   int
	Blocks  int

	// Count is how many terse updates the entry stands for.
	Count int

	// Refill says a full or compressed update followed a cached or
	// requested entry for the same local id: the region was answering a
	// request, whose order the region does not keep to the link order.
	Refill bool

	// Listed says this description put the object in its parent's list
	// of children, which fixed its place in the link order.
	Listed bool

	// Circuit names the circuit the update came on: a number the store
	// gives each agent's circuit.  Sequence numbers compare only within
	// one.
	Circuit uint32

	At time.Time
}

// ringSize is how many descriptions an object keeps, beside the one that
// listed it.
const ringSize = 4

// desc is a Description in the compact form an object stores.
type desc struct {
	at             int64
	seq, msg, par  uint32
	circ           uint32
	index, of, cnt uint16
	kind           DescKind
	flags          uint8
}

const (
	dfRefill = 1 << iota
	dfListed
)

func (d desc) export() Description {
	return Description{
		Kind: d.kind, Parent: d.par, Seq: d.seq, Message: d.msg,
		Block: int(d.index), Blocks: int(d.of), Count: int(d.cnt),
		Refill: d.flags&dfRefill != 0, Listed: d.flags&dfListed != 0, Circuit: d.circ,
		At: time.Unix(0, d.at),
	}
}

// push records a description in the ring, a fixed array: the oldest goes
// when it is full.  A terse update after a terse update is folded in.
func (v *Object) push(d desc) {
	if d.kind == DescTerse && v.ringN > 0 {
		last := &v.ring[(int(v.ringHead)+int(v.ringN)-1)%ringSize]
		if last.kind == DescTerse {
			last.cnt++
			last.at, last.seq, last.msg = d.at, d.seq, d.msg
			return
		}
	}
	if d.kind == DescTerse {
		d.cnt = 1
	}
	if v.ringN < ringSize {
		v.ring[(int(v.ringHead)+int(v.ringN))%ringSize] = d
		v.ringN++
		return
	}
	v.ring[v.ringHead] = d
	v.ringHead = (v.ringHead + 1) % ringSize
}

// Descriptions is the last few ways the object was described, oldest
// first, and the description that listed it under its parent first of
// all when it has since gone out of the last few.  At most ringSize + 1.
func (v *Object) Descriptions() []Description {
	out := make([]Description, 0, ringSize+1)
	inRing := false
	for i := 0; i < int(v.ringN); i++ {
		d := v.ring[(int(v.ringHead)+i)%ringSize]
		if d.flags&dfListed != 0 {
			inRing = true
		}
		out = append(out, d.export())
	}
	if v.hasListedBy && !inRing {
		out = append([]Description{v.listedBy.export()}, out...)
	}
	return out
}

// inbound is a message as the store heard it: one per packet's worth of
// blocks, shared by them.
type inbound struct {
	kind DescKind
	seq  uint32
	msg  uint32 // zero for an update given to the store directly
	of   int
	at   time.Time

	// circ names the circuit the packet came on, which numbers its
	// packets by itself: a sequence number means something only against
	// another from the same circuit.  Zero for none.
	circ uint32
}

var messageCount atomic.Uint32

// circuitCount makes the circuit names: one per agent's handlers, which
// is one per circuit (a reconnect is a new agent).
var circuitCount atomic.Uint32

// arrive names a message the store is about to be told about, so that
// the blocks of one packet can be told from those of another.  The same
// packet heard by two agents sharing the store is two messages: the
// second finds nothing new to list.
func (o *Objects) arrive(circ uint32, kind DescKind, seq uint32, blocks int) inbound {
	return inbound{kind: kind, seq: seq, msg: messageCount.Add(1), of: blocks, at: o.now(), circ: circ}
}

// direct is the inbound of an update that came by no packet, as in a
// store a test or a tool fills by hand: it counts as one message however
// many updates there are.
func (o *Objects) direct(kind DescKind) inbound {
	return inbound{kind: kind, at: o.now()}
}

// evidence is what one block says about how it arrived.
type evidence struct {
	inbound
	index  int
	refill bool
}

func (e evidence) desc(parent uint32) desc {
	d := desc{
		at: e.at.UnixNano(), seq: e.seq, msg: e.msg, par: parent, circ: e.circ,
		index: uint16(e.index), of: uint16(e.of), kind: e.kind,
	}
	if e.refill {
		d.flags |= dfRefill
	}
	return d
}

// noteTTL is how long a cached or requested notice for a local id makes
// the next update for it a refill.
const noteTTL = time.Minute

// noteCap is how many local ids with a notice are kept.  Past it the
// notices are not kept and every description inside noteTTL counts as a
// refill, which only ever errs towards unknown.
const noteCap = 1 << 16

// note is what was said about a local id before an update described it.
type note struct {
	descs  [3]desc
	n      int
	refill bool  // a cached or requested notice, within noteTTL of at
	at     int64 // when the refill notice was last made
	last   int64 // when anything was last noted
	pushed bool  // the descs are already in an object's ring
}

// noted records that these local ids were named by the region as cached
// (DescCached), or asked for by this session (DescRequested), or killed
// (DescKilled).  A later full or compressed update for one is a refill,
// and the notice goes to the ring of the object it describes.
func (o *Objects) noted(kind DescKind, seq uint32, locals ...uint32) {
	if len(locals) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.notedLocked(kind, seq, locals)
}

func (o *Objects) notedLocked(kind DescKind, seq uint32, locals []uint32) {
	now := o.now()
	at := now.UnixNano()
	var have map[uint32]*Object
	if kind != DescKilled {
		have = make(map[uint32]*Object, len(o.byID))
		for _, v := range o.byID {
			have[v.Local] = v
		}
	}
	for _, l := range locals {
		d := desc{at: at, seq: seq, kind: kind}
		if v := have[l]; v != nil {
			v.push(d)
		}
		n := o.notes[l]
		if n == nil {
			if len(o.notes) >= noteCap {
				o.sweepNotesLocked(now)
			}
			if len(o.notes) >= noteCap {
				o.notesLost = now
				continue
			}
			n = &note{}
			o.notes[l] = n
		}
		if kind == DescKilled {
			// A kill ends the note's earlier story: the id is another
			// object's from here.
			*n = note{}
		}
		n.pushed = have[l] != nil
		n.last = at
		if n.n < len(n.descs) {
			n.descs[n.n] = d
			n.n++
		} else {
			copy(n.descs[:], n.descs[1:])
			n.descs[len(n.descs)-1] = d
		}
		if kind == DescCached || kind == DescRequested {
			n.refill, n.at = true, at
		}
	}
}

func (o *Objects) sweepNotesLocked(now time.Time) {
	for l, n := range o.notes {
		if now.Sub(time.Unix(0, n.last)) > noteTTL {
			delete(o.notes, l)
		}
	}
}

// takeNotesLocked is called when an update describes v: it moves what
// was said about the local id into v's ring, spends the notice, and says
// whether the update is a refill.
func (o *Objects) takeNotesLocked(v *Object, local uint32, now time.Time) bool {
	refill := !o.notesLost.IsZero() && now.Sub(o.notesLost) <= noteTTL
	n := o.notes[local]
	if n == nil {
		return refill
	}
	delete(o.notes, local)
	if !n.pushed {
		for i := 0; i < n.n; i++ {
			v.push(n.descs[i])
		}
	}
	if n.refill && now.Sub(time.Unix(0, n.at)) <= noteTTL {
		refill = true
	}
	return refill
}

// seqSpace is how many sequence numbers a circuit has before they wrap:
// 0x01000000, the viewer's LL_MAX_OUT_PACKET_ID (llcircuit.h:52), which
// it takes a difference of more than half of as a wrap.  The header is
// four bytes wide; the region's numbers stay in the low three.
// Why: doc/objects.md#ordering-by-the-packet
const seqSpace = 1 << 24

// seqBefore says whether packet number a was sent before b, modulo the
// circuit's sequence space.  Numbers within half the space of each other
// compare as they would without the wrap; a is not before itself.
func seqBefore(a, b uint32) bool {
	d := (b - a) & (seqSpace - 1)
	return d != 0 && d < seqSpace/2
}

// listedBefore says whether the block of packet seqA at indexA was sent
// before the block of packet seqB at indexB.
func listedBefore(seqA uint32, indexA int, seqB uint32, indexB int) bool {
	if seqA != seqB {
		return seqBefore(seqA, seqB)
	}
	return indexA < indexB
}
