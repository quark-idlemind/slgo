package sl

// The land under the avatar.
//
// Three ways of finding out, and the difference between them is the
// whole of why this file is not one function:
//
//   - The push.  ParcelProperties arrives on the event queue at every
//     arrival, naming the parcel landed on.  Whoever held the session
//     then has it; a client that attached afterwards does not, and Land
//     is how it asks that session what it was told.
//   - The ask.  ParcelPropertiesRequest is answered in about a tenth of
//     a second, for any parcel in the region and not merely the one
//     underfoot.  The answer comes back on the event queue like the
//     push, and the sequence id is the only thing that says which
//     request it belongs to.
//   - The capability.  RemoteParcelRequest turns a point into the
//     parcel's grid-wide uuid, which the properties never carry.
//
// The doc is doc/history/parcel.md, including the measurements behind
// the sequence-id rule and the 404 that the capability answers to a
// request carrying one field too many.

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// DefaultParcelTimeout is how long an ask waits.
//
// Measured on Agni: every kind of parcel question was answered between
// 90 and 130 milliseconds, so this is generous by a factor of fifty and
// still short enough that a person typing "parcel" does not sit and
// wonder.
const DefaultParcelTimeout = 5 * time.Second

// parcelSeqBase is where this package's sequence ids start, counting
// down.
//
// Far away from the viewer's own -50000 for a hover and -10000 for a
// selection, because a viewer may be attached to the same session and
// its replies arrive on the same queue: a band of our own means one of
// its answers can never be mistaken for one of ours.
const parcelSeqBase = -1 << 20

// parcelSeq hands out the sequence ids.  Package-wide rather than
// per-session because it costs nothing and two sessions in one process
// sharing a band is one less thing to think about.
var parcelSeq atomic.Int32

func nextParcelSeq() int32 {
	n := parcelSeq.Add(-1) + parcelSeqBase
	// Not zero and not positive, whatever happens after four million
	// asks: a non-negative id is a push and would be taken as one.
	if n >= 0 {
		parcelSeq.Store(0)
		n = parcelSeqBase
	}
	return n
}

// Told is what a session was told it is standing on, without asking.
//
// Only the name and the local id, because everything else can be asked
// for and this cannot: it is the answer to "what does the session
// believe" rather than "what is true now".
type Told struct {
	Name    string
	LocalID int32
}

// Land is what a session was told about the ground it is on.
type Land struct {
	// Told is the parcel pushed on arrival, or nil where this region
	// has pushed none.
	Told *Told

	// Overlay is the region's parcel layout, never nil, and empty where
	// none of the four packets has arrived.
	Overlay *agent.Overlay
}

// Land is what the session was told about the ground under the avatar:
// the parcel pushed on arrival and the region's overlay.
//
// The overlay is the reason to call it.  It arrives four packets to an
// arrival and never again, so a session that has been up for hours is
// the only thing that still has it -- there is no request that would
// bring it back.
func (w *Session) Land(ctx context.Context) (*Land, error) {
	return w.b.Land(ctx)
}

// Parcel is the parcel the avatar is standing on, asked for rather than
// remembered.
//
// It asks because the answer is cheap and the remembered one is only as
// new as the last arrival: an avatar that has stood still while the
// land was sold, divided or renamed is told what was true when it got
// there.  Where the ask goes unanswered, Land says what the session
// believes.
func (w *Session) Parcel(ctx context.Context, timeout time.Duration) (*agent.Parcel, error) {
	p, err := w.Where(ctx)
	if err != nil {
		return nil, err
	}
	return w.ParcelAt(ctx, p.Position.X, p.Position.Y, timeout)
}

// ParcelAt is the parcel covering a point in this region, in metres
// from its south west corner.
//
// The point may be anywhere in the region; the avatar does not have to
// be near it, and asking does not move anything.  It does move the
// simulator's idea of which parcel this agent has SELECTED, which a
// viewer attached to the same session will notice: its About Land
// redraws to show the parcel asked about.  See doc/history/parcel.md.
func (w *Session) ParcelAt(ctx context.Context, x, y float32, timeout time.Duration) (*agent.Parcel, error) {
	seq := nextParcelSeq()

	// The viewer snaps a hover to the 4 metre grid the overlay is
	// drawn on, and so does this: a rectangle inside one square asks
	// about one parcel, where a wider one would ask about whatever
	// several of them have in common.
	west := float32(int(x/agent.OverlayStep) * agent.OverlayStep)
	south := float32(int(y/agent.OverlayStep) * agent.OverlayStep)

	m := &msg.ParcelPropertiesRequest{}
	m.AgentData.AgentID = w.me
	m.AgentData.SessionID = w.Session()
	m.ParcelData.SequenceID = seq
	m.ParcelData.West, m.ParcelData.South = west, south
	m.ParcelData.East, m.ParcelData.North = west+agent.OverlayStep, south+agent.OverlayStep
	m.ParcelData.SnapSelection = true

	return w.askParcel(ctx, seq, m, timeout, fmt.Sprintf("the parcel at %.0f,%.0f", x, y))
}

// ParcelByID is the parcel with a local id, which is what a push and an
// overlay both name a parcel by.
//
// A local id belongs to the region that issued it and to no other, so
// one kept across a teleport names something -- and something else.
func (w *Session) ParcelByID(ctx context.Context, local int32, timeout time.Duration) (*agent.Parcel, error) {
	seq := nextParcelSeq()

	m := &msg.ParcelPropertiesRequestByID{}
	m.AgentData.AgentID = w.me
	m.AgentData.SessionID = w.Session()
	m.ParcelData.SequenceID = seq
	m.ParcelData.LocalID = local

	return w.askParcel(ctx, seq, m, timeout, fmt.Sprintf("parcel %d", local))
}

// askParcel sends one request and waits for the answer with its own
// sequence id on it.
//
// Listening before asking, because the answer is an event like any
// other and a listener opened after the request has already missed it.
func (w *Session) askParcel(ctx context.Context, seq int32, m msg.Message,
	timeout time.Duration, what string) (*agent.Parcel, error) {

	if timeout <= 0 {
		timeout = DefaultParcelTimeout
	}

	got := make(chan *agent.Parcel, 1)
	stop := w.onParcel(func(p *agent.Parcel) {
		if p.Sequence != seq {
			return
		}
		select {
		case got <- p:
		default:
		}
	})
	defer stop()

	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	select {
	case p := <-got:
		return p, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("sl: %s: no answer in %s", what, timeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.b.Done():
		return nil, fmt.Errorf("sl: %s: the session ended", what)
	}
}

// ParcelID is the grid-wide uuid of the parcel covering a point, from
// the RemoteParcelRequest capability.
//
// The properties never carry it: a parcel names itself by a local id
// that belongs to one region, and this is the name that does not.
//
// The request carries the location and the region's id and nothing
// else.  Adding region_handle turns the answer into a 404 -- the viewer
// computes that handle from a global position and ours is the local
// one, and the capability would rather refuse than reconcile them.
// That cost half an hour to find once; see doc/history/parcel.md.
func (w *Session) ParcelID(ctx context.Context, x, y, z float32) (msg.UUID, error) {
	r, err := w.Region(ctx)
	if err != nil {
		return msg.UUID{}, err
	}
	if r == nil || r.ID.IsZero() {
		return msg.UUID{}, fmt.Errorf("sl: ParcelID: the region has not said what it is")
	}
	return w.ParcelIDIn(ctx, r.ID, msg.Vector3{X: x, Y: y, Z: z})
}

// ParcelIDIn is ParcelID for a point in any region, asked through the
// capability of the region the avatar is in.
//
// A landmark names its region by uuid and the capability takes that as
// region_id; measured, it answers for a region the avatar is not in,
// with no region_handle.
// Why: doc/history/parcel.md#a-parcel-anywhere-from-a-landmark
func (w *Session) ParcelIDIn(ctx context.Context, region msg.UUID, pos msg.Vector3) (msg.UUID, error) {
	if region.IsZero() {
		return msg.UUID{}, fmt.Errorf("sl: ParcelIDIn: no region given")
	}
	body, err := llsd.Encode(map[string]any{
		"location":  []any{float64(pos.X), float64(pos.Y), float64(pos.Z)},
		"region_id": llsd.UUID(region.String()),
	})
	if err != nil {
		return msg.UUID{}, err
	}

	resp, err := w.b.DoCap(ctx, agent.CapRequest{
		Cap: "RemoteParcelRequest", Method: "POST", Body: body,
		Type: "application/llsd+xml",
	})
	if err != nil {
		return msg.UUID{}, err
	}
	if resp.Status != 200 {
		return msg.UUID{}, fmt.Errorf("sl: ParcelID: the capability answered %d", resp.Status)
	}

	v, err := llsd.Decode(bytes.NewReader(resp.Body))
	if err != nil {
		return msg.UUID{}, err
	}
	m := llsd.Map(v)
	if m == nil {
		return msg.UUID{}, fmt.Errorf("sl: ParcelID: the capability answered %T", v)
	}
	id, err := msg.ParseUUID(llsd.String(m, "parcel_id"))
	if err != nil {
		return msg.UUID{}, fmt.Errorf("sl: ParcelID: %w", err)
	}
	return id, nil
}

// Dwell is the "traffic" a viewer shows for a parcel: how much time
// avatars have spent on it.
//
// It comes back on the circuit rather than the queue, and it carries
// the parcel's grid-wide uuid beside the local id asked with -- which
// makes it the cheapest way to learn both namings of one parcel at
// once.
func (w *Session) Dwell(ctx context.Context, local int32, timeout time.Duration) (float32, msg.UUID, error) {
	if timeout <= 0 {
		timeout = DefaultParcelTimeout
	}

	type answer struct {
		dwell float32
		id    msg.UUID
	}
	got := make(chan answer, 1)
	stop := w.onDwell(func(l int32, id msg.UUID, dwell float32) {
		if l != local {
			return
		}
		select {
		case got <- answer{dwell, id}:
		default:
		}
	})
	defer stop()

	m := &msg.ParcelDwellRequest{}
	m.AgentData.AgentID = w.me
	m.AgentData.SessionID = w.Session()
	m.Data.LocalID = local
	if err := w.Send(ctx, m); err != nil {
		return 0, msg.UUID{}, err
	}

	select {
	case a := <-got:
		return a.dwell, a.id, nil
	case <-time.After(timeout):
		return 0, msg.UUID{}, fmt.Errorf("sl: parcel %d: no dwell in %s", local, timeout)
	case <-ctx.Done():
		return 0, msg.UUID{}, ctx.Err()
	case <-w.b.Done():
		return 0, msg.UUID{}, fmt.Errorf("sl: parcel %d: the session ended", local)
	}
}

// onParcel and onDwell register a waiter, and are the same shape as
// onScriptRunning: a slot in a slice, blanked rather than removed, so
// that an index handed out earlier still means what it meant.
func (w *Session) onParcel(fn func(*agent.Parcel)) (stop func()) {
	w.mu.Lock()
	w.parcelFns = append(w.parcelFns, fn)
	i := len(w.parcelFns) - 1
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if i < len(w.parcelFns) {
			w.parcelFns[i] = nil
		}
		w.mu.Unlock()
	}
}

func (w *Session) onDwell(fn func(local int32, id msg.UUID, dwell float32)) (stop func()) {
	w.mu.Lock()
	w.dwellFns = append(w.dwellFns, fn)
	i := len(w.dwellFns) - 1
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if i < len(w.dwellFns) {
			w.dwellFns[i] = nil
		}
		w.mu.Unlock()
	}
}

// parcelEvent is a ParcelProperties off the event queue, push or reply
// alike: the waiters sort them out by sequence id and a push matches
// none of them.
func (w *Session) parcelEvent(m map[string]any) {
	p := agent.DecodeParcel(m)
	if p == nil {
		return
	}
	w.mu.Lock()
	fns := make([]func(*agent.Parcel), len(w.parcelFns))
	copy(fns, w.parcelFns)
	w.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(p)
		}
	}
}

// dwellReply hands one ParcelDwellReply to whoever asked.
func (w *Session) dwellReply(local int32, id msg.UUID, dwell float32) {
	w.mu.Lock()
	fns := make([]func(int32, msg.UUID, float32), len(w.dwellFns))
	copy(fns, w.dwellFns)
	w.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(local, id, dwell)
		}
	}
}
