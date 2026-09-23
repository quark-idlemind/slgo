package agent

import (
	"context"
	"testing"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// aParcel is a ParcelProperties event body, with the fields the grid
// really sends and the LLSD types it really sends them as: uuids as
// text, ParcelFlags as four raw bytes, the box as arrays of reals.
func aParcel(name string, local, seq int, over ...func(map[string]any)) map[string]any {
	b := map[string]any{
		"Name":       name,
		"Desc":       "a description",
		"LocalID":    int64(local),
		"SequenceID": int64(seq),
		"Area":       int64(2048),

		"OwnerID":      "fcf97e57-7e57-c0de-31c3-89b609d7e31a",
		"GroupID":      "4e037e57-7e57-c0de-a18a-339d29bb11c3",
		"IsGroupOwned": false,

		"AABBMin": []any{12.0, 48.0, 0.0},
		"AABBMax": []any{44.0, 112.0, 50.0},
		"Bitmap":  make([]byte, 512),

		"MaxPrims":          int64(937),
		"TotalPrims":        int64(486),
		"OwnerPrims":        int64(485),
		"GroupPrims":        int64(1),
		"SimWideMaxPrims":   int64(937),
		"SimWideTotalPrims": int64(486),
		"ParcelPrimBonus":   1.0,

		"OtherCount":  int64(128),
		"PublicCount": int64(0),
		"SelfCount":   int64(0),

		"PassPrice": int64(10),
		"PassHours": 1.0,
		"ClaimDate": int64(1714701211),

		"LandingType":  int64(1),
		"UserLocation": []any{0.0, 0.0, 0.0},

		// 0x56a4800b: fly, other scripts and landmarks allowed.
		"ParcelFlags": []byte{0x56, 0xa4, 0x80, 0x0b},

		"SeeAVs":      false,
		"AnyAVSounds": true,
	}
	for _, f := range over {
		f(b)
	}
	return map[string]any{"ParcelData": []any{b}}
}

// event wraps bodies the way a poll's reply carries them.
func parcelEvent(t *testing.T, bodies ...map[string]any) []byte {
	t.Helper()
	events := make([]any, 0, len(bodies))
	for _, b := range bodies {
		events = append(events, map[string]any{"message": "ParcelProperties", "body": b})
	}
	out, err := llsd.Encode(map[string]any{"id": int64(1), "events": events})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestTheParcelUnderfootIsKept: the push arrives on the queue at every
// arrival and is the only description of this land the session gets
// without asking, so it has to be read before any client sees it.
func TestTheParcelUnderfootIsKept(t *testing.T) {
	a := eqAgent("")

	if a.Parcel() != nil {
		t.Fatal("a session that has heard nothing claims to know a parcel")
	}
	a.deliver(parcelEvent(t, aParcel("Thrushmoor", 5, 3)), nil)

	p := a.Parcel()
	if p == nil {
		t.Fatal("the push was not kept")
	}
	if p.Name != "Thrushmoor" || p.LocalID != 5 {
		t.Errorf("parcel = %q local %d", p.Name, p.LocalID)
	}
}

// TestAReplyIsLeftForWhoeverAsked: a reply describes the parcel that
// was asked about, which is usually not the one underfoot, so keeping
// it here would have Parcel answer about somewhere else entirely.  The
// sequence id is the only thing that tells the two apart.
func TestAReplyIsLeftForWhoeverAsked(t *testing.T) {
	a := eqAgent("")
	a.deliver(parcelEvent(t, aParcel("Thrushmoor", 5, 3)), nil)

	var seen int
	a.deliver(parcelEvent(t, aParcel("Quill Lodge", 9, -10000)),
		func(string, []byte) { seen++ })

	if p := a.Parcel(); p == nil || p.Name != "Thrushmoor" {
		t.Errorf("the reply overwrote the parcel underfoot: %+v", p)
	}
	if seen != 1 {
		t.Errorf("the reply reached %d handlers, want one: it belongs to whoever asked", seen)
	}
}

// TestTheParcelIsDecodedAsTheGridSendsIt: every field here arrived in a
// type that would decode to zero if it were read as the obvious one --
// uuids as text, flags as raw bytes, the box as reals.
func TestTheParcelIsDecodedAsTheGridSendsIt(t *testing.T) {
	a := eqAgent("")
	a.deliver(parcelEvent(t, aParcel("Thrushmoor", 5, 3)), nil)
	p := a.Parcel()

	if want := msg.MustParseUUID("fcf97e57-7e57-c0de-31c3-89b609d7e31a"); p.Owner != want {
		t.Errorf("owner = %v, want %v", p.Owner, want)
	}
	if p.Group.IsZero() {
		t.Error("group is zero: a uuid arrives as text and was read as bytes")
	}
	if p.AABBMin != (msg.Vector3{X: 12, Y: 48}) || p.AABBMax != (msg.Vector3{X: 44, Y: 112, Z: 50}) {
		t.Errorf("box = %v to %v", p.AABBMin, p.AABBMax)
	}
	if p.Area != 2048 || p.MaxPrims != 937 || p.TotalPrims != 486 || p.OwnerPrims != 485 {
		t.Errorf("area %d, prims %d/%d owner %d", p.Area, p.TotalPrims, p.MaxPrims, p.OwnerPrims)
	}
	if p.PrimBonus != 1 || p.PassHours != 1 || p.PassPrice != 10 {
		t.Errorf("bonus %v, pass %v hours at %d", p.PrimBonus, p.PassHours, p.PassPrice)
	}
	// Epoch seconds, kept as UTC.  A viewer shows this parcel as
	// claimed on 2 May 2024, which is the same moment in SLT: the
	// grid's clock is Pacific and nothing here converts to it.
	if got := p.Claimed.Format("2006-01-02 15:04:05"); got != "2024-05-03 01:53:31" {
		t.Errorf("claimed %s, want the moment the epoch seconds name", got)
	}
	if len(p.Bitmap) != 512 {
		t.Errorf("bitmap is %d bytes, want the 512 that say which squares are this parcel's", len(p.Bitmap))
	}

	// The flags word is four raw bytes, most significant first.  Read
	// the other way round this parcel forbids flying, which is the
	// failure this asserts against.
	if p.Flags&ParcelAllowFly == 0 || p.Flags&ParcelAllowOtherScripts == 0 ||
		p.Flags&ParcelAllowLandmark == 0 {
		t.Errorf("flags = %#08x, want fly, scripts and landmarks allowed", p.Flags)
	}
	if !p.AnyAVSounds || p.SeeAVs {
		t.Errorf("privacy settings = seeAVs %v anyAVSounds %v", p.SeeAVs, p.AnyAVSounds)
	}
}

// TestNonsenseCostsTheParcelAndNotTheQueue: this is the grid's LLSD and
// a poll carries other events beside it.
func TestNonsenseCostsTheParcelAndNotTheQueue(t *testing.T) {
	a := eqAgent("")
	for _, body := range []map[string]any{
		{},
		{"ParcelData": []any{}},
		{"ParcelData": "not a block at all"},
		{"ParcelData": []any{map[string]any{"Name": "no local id and no sequence"}}},
	} {
		a.deliver(parcelEvent(t, body), nil)
	}
	// The last of those is a push with sequence zero, which is a real
	// push: what it must not do is arrive as something other than a
	// parcel with an empty name.
	if p := a.Parcel(); p != nil && p.Name != "no local id and no sequence" {
		t.Errorf("parcel = %+v", p)
	}
	if st := a.EventStats(); st.Errors != 0 {
		t.Errorf("a parcel this package could not read was counted against the queue: %+v", st)
	}
}

// overlayPackets is the four quarters of a region where every square
// says the same thing, so that which quarter is which is visible.
func overlayPacket4(seq int, fill byte) *msg.ParcelOverlay {
	m := &msg.ParcelOverlay{}
	m.ParcelData.SequenceID = int32(seq)
	m.ParcelData.Data = make([]byte, overlayPacket)
	for i := range m.ParcelData.Data {
		m.ParcelData.Data[i] = fill
	}
	return m
}

// TestTheOverlayAssembles: four packets of 1024 bytes are a 64 by 64
// grid of 4 metre squares, and the whole point of it is that it
// describes the region's layout without a request per parcel.
func TestTheOverlayAssembles(t *testing.T) {
	var p parcels
	for i, fill := range []byte{OverlayPublic, OverlayOwned, OverlayGroup, OverlaySelf | OverlaySouthLine} {
		p.noteOverlay(overlayPacket4(i, fill))
	}
	o := &p.overlay

	if !o.Complete() || o.Packets() != 4 {
		t.Fatalf("complete %v after %d packets", o.Complete(), o.Packets())
	}

	// A square is 4 metres, the grid runs from the south west corner,
	// and each packet is sixteen rows: y=0 is the first packet and
	// y=255 the last.
	for _, c := range []struct {
		x, y float32
		want byte
	}{
		{0, 0, OverlayPublic},
		{255, 63, OverlayPublic},
		{4, 64, OverlayOwned},
		{128, 128, OverlayGroup},
		{200, 250, OverlaySelf | OverlaySouthLine},
	} {
		got, ok := o.At(c.x, c.y)
		if !ok || got != c.want {
			t.Errorf("at %v,%v = %#02x ok %v, want %#02x", c.x, c.y, got, ok, c.want)
		}
	}

	if _, ok := o.At(-1, 10); ok {
		t.Error("a point outside the region answered")
	}
	if _, ok := o.At(10, 256); ok {
		t.Error("a point past the far edge answered")
	}
}

// TestAPartialOverlaySaysSo: the four packets can be split by a region
// change, and squares that never arrived read as public land -- which
// is a real answer about somebody's land, so it must be askable.
func TestAPartialOverlaySaysSo(t *testing.T) {
	var p parcels
	p.noteOverlay(overlayPacket4(0, OverlayOwned))
	p.noteOverlay(overlayPacket4(2, OverlayGroup))

	// Neither of these is one of the four quarters.
	p.noteOverlay(overlayPacket4(9, OverlayOwned))
	short := &msg.ParcelOverlay{}
	short.ParcelData.Data = []byte{1, 2, 3}
	p.noteOverlay(short)

	o := &p.overlay
	if o.Complete() || o.Packets() != 2 {
		t.Errorf("complete %v after %d packets, want two", o.Complete(), o.Packets())
	}
	if _, ok := o.At(10, 10); !ok {
		t.Error("a square from a quarter that did arrive says it has not")
	}
	if _, ok := o.At(10, 70); ok {
		t.Error("a square from a quarter that never arrived answered anyway")
	}
	if len(o.Squares()) != OverlaySquares {
		t.Errorf("the grid is %d squares", len(o.Squares()))
	}
}

// TestACrossingDropsTheLand: the parcel and the layout describe the
// region the avatar was in, and after a move they describe somewhere
// else.  Answering with them would be worse than answering with
// nothing, because nothing is visibly nothing.
func TestACrossingDropsTheLand(t *testing.T) {
	a, _, to := twoRegions(t, Options{SkipCaps: true})

	a.deliver(parcelEvent(t, aParcel("Thrushmoor", 5, 3)), nil)
	a.parcels.noteOverlay(overlayPacket4(0, OverlayOwned))
	if a.Parcel() == nil || a.Overlay().Packets() == 0 {
		t.Fatal("nothing was held before the move")
	}

	if err := a.moveTo(context.Background(), to.sim.addr(), to.seed()); err != nil {
		t.Fatalf("moveTo: %v", err)
	}
	waitFor(t, "the land of the region left to be dropped", func() bool {
		return a.Parcel() == nil && a.Overlay().Packets() == 0
	})
}

// TestTheOverlayIsKeptFromTheCircuit: it arrives four packets to an
// arrival and never again, so a session that did not register for it
// before arriving has no way to ask.
func TestTheOverlayIsKeptFromTheCircuit(t *testing.T) {
	a, from, _ := twoRegions(t, Options{SkipCaps: true})

	from.sim.send(overlayPacket4(1, OverlayForSale), msg.FlagReliable)
	waitFor(t, "the overlay packet to arrive", func() bool {
		return a.Overlay().Packets() == 1
	})
	if got, ok := a.Overlay().At(0, 64); !ok || got != OverlayForSale {
		t.Errorf("square = %#02x ok %v", got, ok)
	}
}
