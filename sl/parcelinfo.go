package sl

// A parcel anywhere on the grid, by its id.
//
// ParcelIDIn turns a point in any region into the parcel's uuid, and
// ParcelInfoRequest turns that into a description: name, owner, area and
// the NAME of the region it is in, which a landmark does not carry.  The
// viewer does the same chain for its place panel.
// Why: doc/history/parcel.md#a-parcel-anywhere-from-a-landmark

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// ParcelInfo is what the grid says about a parcel it was asked about by
// id, field for field the ParcelInfoReply.
type ParcelInfo struct {
	ID    msg.UUID
	Owner msg.UUID

	Name string
	Desc string

	// ActualArea and BillableArea are in square metres.
	ActualArea   int32
	BillableArea int32

	// Flags is the reply's byte; Mature and Adult read the two bits the
	// viewer reads, and it reads no others.
	Flags uint8

	// Global is the reply's GlobalX/Y/Z, in metres on the grid.  It is
	// the parcel's own point and not the point asked about.  Inferred,
	// not measured, to be its landing point or centre.
	Global msg.Vector3

	// RegionName is the reply's SimName.
	RegionName string

	Snapshot  msg.UUID
	Dwell     float32
	SalePrice int32
	AuctionID int32
}

// Mature is the region's maturity being Moderate: bit 0x1, as the
// viewer's place panel reads it.
func (p *ParcelInfo) Mature() bool { return p.Flags&0x1 != 0 && !p.Adult() }

// Adult is the region's maturity being Adult: bit 0x2.
func (p *ParcelInfo) Adult() bool { return p.Flags&0x2 != 0 }

func parcelInfoFrom(m *msg.ParcelInfoReply) *ParcelInfo {
	d := &m.Data
	return &ParcelInfo{
		ID: d.ParcelID, Owner: d.OwnerID,
		Name: trimNul(d.Name), Desc: trimNul(d.Desc),
		ActualArea: d.ActualArea, BillableArea: d.BillableArea,
		Flags:      d.Flags,
		Global:     msg.Vector3{X: d.GlobalX, Y: d.GlobalY, Z: d.GlobalZ},
		RegionName: trimNul(d.SimName),
		Snapshot:   d.SnapshotID, Dwell: d.Dwell,
		SalePrice: d.SalePrice, AuctionID: d.AuctionID,
	}
}

// ParcelInfo asks the grid about a parcel by its grid-wide id, wherever
// it is, and waits ParcelInfoTimeout for the reply with that id.
//
// Waiters rather than a store of replies: each call listens for its own
// parcel before asking, so concurrent calls cannot take one another's
// answers and nothing is kept once the answer is delivered.
func (w *Session) ParcelInfo(ctx context.Context, parcel msg.UUID) (*ParcelInfo, error) {
	timeout := w.parcelInfoWait()

	got := make(chan *ParcelInfo, 1)
	stop := w.onParcelInfo(func(p *ParcelInfo) {
		if p.ID != parcel {
			return
		}
		select {
		case got <- p:
		default:
		}
	})
	defer stop()

	m := &msg.ParcelInfoRequest{}
	m.AgentData.AgentID = w.me
	m.AgentData.SessionID = w.Session()
	m.Data.ParcelID = parcel
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	select {
	case p := <-got:
		return p, nil
	case <-time.After(timeout):
		return nil, fmt.Errorf("sl: parcel %v: no ParcelInfoReply in %s: %w", parcel, timeout, ErrTimeout)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.b.Done():
		return nil, fmt.Errorf("sl: parcel %v: the session ended", parcel)
	}
}

// LandmarkPlace is the parcel a landmark points into: ParcelIDIn for its
// region and position, then ParcelInfo.
func (w *Session) LandmarkPlace(ctx context.Context, lm *Landmark) (*ParcelInfo, error) {
	id, err := w.ParcelIDIn(ctx, lm.Region, lm.Position)
	if err != nil {
		return nil, err
	}
	return w.ParcelInfo(ctx, id)
}

// onParcelInfo is onParcel's shape, a slot blanked on stop, except that a
// blank slot is taken before the slice grows: calls without end must not
// make it longer without end.
func (w *Session) onParcelInfo(fn func(*ParcelInfo)) (stop func()) {
	w.mu.Lock()
	i := -1
	for j, f := range w.infoFns {
		if f == nil {
			i = j
			break
		}
	}
	if i < 0 {
		w.infoFns = append(w.infoFns, fn)
		i = len(w.infoFns) - 1
	} else {
		w.infoFns[i] = fn
	}
	w.mu.Unlock()
	return func() {
		w.mu.Lock()
		if i < len(w.infoFns) {
			w.infoFns[i] = nil
		}
		w.mu.Unlock()
	}
}

// parcelInfoReply hands one reply to whoever asked.
func (w *Session) parcelInfoReply(p *ParcelInfo) {
	w.mu.Lock()
	fns := make([]func(*ParcelInfo), len(w.infoFns))
	copy(fns, w.infoFns)
	w.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			fn(p)
		}
	}
}
