package agent

import (
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// A region is one place to the protocol and several places to anybody
// standing in it, and the parcel is the half that decides what may be
// done: building, flying, scripts, sound, and whether the avatar may
// stay at all.
//
// Two things arrive unasked and are kept here.  ParcelProperties comes
// on the event queue at every arrival, naming the parcel landed on and
// no other, with everything a person would want to know about it.
// ParcelOverlay comes on the circuit at the same moment, four packets
// that together say who owns each 4 metre square of the whole region --
// no names, but the shape of every parcel at once.
//
// Both describe the region the avatar is in, so both are dropped when
// it changes, beside the terrain and the appearances.

// ParcelProperties also answers a request, and a reply is not a push:
// it describes whatever parcel was asked about, which is usually not
// the one underfoot.  The sequence id is the only thing that tells them
// apart -- a reply carries back the id the request asked with, and a
// push carries a small rising counter of the simulator's own -- so a
// request asks with a negative id and anything negative is left for
// whoever asked.  Measured on Agni 2026-08-18: pushes numbered 0, then
// 2 to 6 over five moves, while replies came back as the -10000 they
// were asked with.
//
// PushSequence is the highest sequence id treated as a push.
const PushSequence = 0

// Parcel is what the simulator said about one piece of land.
//
// The fields are the event's own, renamed only where the protocol's
// name would mislead.  What is not decoded here is not lost: nothing
// reads Bitmap, the parcel's own map of which squares it covers, but it
// is kept because it is the only per-parcel shape the grid ever sends.
type Parcel struct {
	// LocalID names the parcel within this region and nowhere else; it
	// is what ParcelPropertiesRequestByID and ParcelDwellRequest take.
	// The grid-wide name is a uuid, which this event does not carry --
	// ParcelDwellReply and the RemoteParcelRequest capability do.
	LocalID int32

	Name string
	Desc string

	Owner        msg.UUID
	Group        msg.UUID
	IsGroupOwned bool

	// Area is in square metres, and AABBMin/AABBMax the box the parcel
	// occupies.  A parcel need not be a rectangle, so the box bounds it
	// rather than describing it; Bitmap is the exact shape.
	Area             int32
	AABBMin, AABBMax msg.Vector3
	Bitmap           []byte

	// Prims are the reason a build stops working, so all of the counts
	// are kept.  SimWide are the region's, for a parcel whose allowance
	// is pooled across it.
	MaxPrims          int32
	TotalPrims        int32
	OwnerPrims        int32
	GroupPrims        int32
	OtherPrims        int32
	SelectedPrims     int32
	SimWideMaxPrims   int32
	SimWideTotalPrims int32
	PrimBonus         float32

	// Who is standing here, under the protocol's own names: Self is
	// this avatar's own, Other everybody else's, Public the rest.
	SelfCount   int32
	OtherCount  int32
	PublicCount int32

	SalePrice  int32
	ClaimPrice int32
	RentPrice  int32
	PassPrice  int32
	PassHours  float32
	AuthBuyer  msg.UUID
	Claimed    time.Time

	// Category is the "type of land" a search would file it under, and
	// Status says whether it is owned, for sale or abandoned.
	Category int32
	Status   int32

	MediaURL string
	MediaID  msg.UUID
	MusicURL string
	Snapshot msg.UUID

	// LandingType says whether a teleport is sent to UserLocation or
	// left where it asked for, and UserLookAt which way it faces.
	LandingType  int32
	UserLocation msg.Vector3
	UserLookAt   msg.Vector3

	// Flags is the ParcelFlags word: what may be done here.  See the
	// Parcel* constants.
	Flags uint32

	// SeeAVs, AnyAVSounds and GroupAVSounds are the privacy settings
	// that a viewer shows on the same panel as the flags but that the
	// protocol keeps apart from them.
	SeeAVs        bool
	AnyAVSounds   bool
	GroupAVSounds bool

	// Sequence is the event's own id: negative for a reply to a
	// request that asked with that id, and rising from zero for a push.
	Sequence int32
}

// The ParcelFlags bits, from the viewer's llparcel.h.  Only the ones a
// person would ask about are named; the word is kept whole either way.
const (
	ParcelAllowFly           = 1 << 0
	ParcelAllowOtherScripts  = 1 << 1
	ParcelForSale            = 1 << 2
	ParcelAllowLandmark      = 1 << 3
	ParcelAllowTerraform     = 1 << 4
	ParcelAllowDamage        = 1 << 5
	ParcelCreateObjects      = 1 << 6
	ParcelForSaleObjects     = 1 << 7
	ParcelUseAccessGroup     = 1 << 8
	ParcelUseAccessList      = 1 << 9
	ParcelUseBanList         = 1 << 10
	ParcelUsePassList        = 1 << 11
	ParcelShowDirectory      = 1 << 12
	ParcelAllowDeedToGroup   = 1 << 13
	ParcelContributeWithDeed = 1 << 14
	ParcelSoundLocal         = 1 << 15
	ParcelSellParcelObjects  = 1 << 16
	ParcelAllowPublish       = 1 << 17
	ParcelMaturePublish      = 1 << 18
	ParcelURLWebPage         = 1 << 19
	ParcelURLRawHTML         = 1 << 20
	ParcelRestrictPushObject = 1 << 21
	ParcelDenyAnonymous      = 1 << 22
	ParcelAllowGroupScripts  = 1 << 25
	ParcelCreateGroupObjects = 1 << 26

	// Object entry is not building: it is whether an object may be
	// moved or rezzed ACROSS the boundary onto this land, which is the
	// setting that stops a vehicle at a property line.
	ParcelAllowAllObjectEntry   = 1 << 27
	ParcelAllowGroupObjectEntry = 1 << 28

	ParcelAllowVoiceChat    = 1 << 29
	ParcelUseEstateVoice    = 1 << 30
	ParcelDenyAgeUnverified = 1 << 31
)

// Checked against a viewer's own About Land panel for Pelmar Reach's
// Thrushmoor, whose flags word is 0x56a4800b, on 2026-08-18: fly, other
// scripts, landmarks, group scripts, group build and group object entry
// set, and terraform, damage, building by everyone, object entry by
// everyone and the search listing clear.  Every checkbox agreed, which
// is what says the word is read most significant byte first -- read the
// other way it is a parcel nobody may fly over.
//
// It also settled two bits this file had wrong: 29 is voice and 30 is
// the estate's voice channel, where they had been 28 and 29, and there
// is no "group fly" flag at all.  The panel is the only place those
// could have been caught.

// Overlay dimensions.  A region is 256 metres and the overlay describes
// it in 4 metre squares, one byte each, so it is a 64 by 64 grid that
// arrives as four packets of 1024 bytes.
const (
	OverlayStep    = 4
	OverlayEdge    = 64
	OverlaySquares = OverlayEdge * OverlayEdge
	OverlayPackets = 4
	overlayPacket  = OverlaySquares / OverlayPackets
)

// What a square's byte means, from llparcel.h.  The low three bits say
// whose it is, and the high bits are drawn rather than owned.
const (
	OverlayOwnerMask = 0x07
	OverlayPublic    = 0x00
	OverlayOwned     = 0x01
	OverlayGroup     = 0x02
	OverlaySelf      = 0x03
	OverlayForSale   = 0x04
	OverlayAuction   = 0x05

	OverlayHiddenAVs  = 0x10
	OverlaySoundLocal = 0x20
	OverlayWestLine   = 0x40
	OverlaySouthLine  = 0x80
)

// Overlay is a region's parcel layout, as far as it has arrived.
//
// It is worth having even where a parcel has been described, because it
// is complete where the descriptions are not: four packets say who owns
// every square of the region, where naming each parcel would be a
// request apiece.
type Overlay struct {
	squares [OverlaySquares]byte
	arrived [OverlayPackets]bool
	packets int
}

// note takes one of the four packets.
//
// A packet of the wrong length is ignored rather than trusted to be
// short at the end: the grid sends exactly a quarter each time, and
// anything else is a message this does not understand.
func (o *Overlay) note(m *msg.ParcelOverlay) {
	seq := m.ParcelData.SequenceID
	if seq < 0 || int(seq) >= OverlayPackets {
		return
	}
	if len(m.ParcelData.Data) != overlayPacket {
		return
	}
	copy(o.squares[int(seq)*overlayPacket:], m.ParcelData.Data)
	if !o.arrived[seq] {
		o.arrived[seq] = true
		o.packets++
	}
}

// Complete says whether all four packets have arrived.  A partial
// overlay is still worth reading -- the squares that arrived are this
// region's -- so this is asked rather than enforced.
func (o *Overlay) Complete() bool { return o.packets == OverlayPackets }

// Packets is how many of the four have arrived.
func (o *Overlay) Packets() int { return o.packets }

// Quarters is a bitmask of which packets arrived, bit n for packet n.
//
// It is what has to travel to a client rather than the count: the
// squares of a packet that never came are zero, which reads as public
// land, so which quarter is missing is what says where that reading is
// not an answer.
func (o *Overlay) Quarters() uint8 {
	var q uint8
	for i, ok := range o.arrived {
		if ok {
			q |= 1 << uint(i)
		}
	}
	return q
}

// OverlayFrom rebuilds an overlay from the squares and the quarter mask
// that crossed a connection, for a client reading another process's.
func OverlayFrom(squares []byte, quarters uint8) *Overlay {
	o := &Overlay{}
	copy(o.squares[:], squares)
	for i := range o.arrived {
		if quarters&(1<<uint(i)) != 0 {
			o.arrived[i] = true
			o.packets++
		}
	}
	return o
}

// At is the square covering a point in region metres, and whether that
// part of the overlay has arrived.
func (o *Overlay) At(x, y float32) (byte, bool) {
	if x < 0 || y < 0 {
		return 0, false
	}
	cx, cy := int(x)/OverlayStep, int(y)/OverlayStep
	if cx >= OverlayEdge || cy >= OverlayEdge {
		return 0, false
	}
	i := cy*OverlayEdge + cx
	if !o.arrived[i/overlayPacket] {
		return 0, false
	}
	return o.squares[i], true
}

// Squares is the whole grid, row by row from the south west corner.
// The bytes of a quarter that has not arrived are zero, which reads as
// public land, so Complete is worth asking first.
func (o *Overlay) Squares() []byte {
	out := make([]byte, OverlaySquares)
	copy(out, o.squares[:])
	return out
}

// parcels is what this session was told about the land it is on.
type parcels struct {
	mu      sync.Mutex
	current *Parcel
	overlay Overlay
}

// note keeps a push and steps over a reply.
//
// A reply belongs to whoever asked -- it describes the parcel they
// named, which is usually not the one underfoot -- and storing it here
// would have Parcel answer about somewhere else.
func (p *parcels) note(v *Parcel) {
	if v == nil || v.Sequence < PushSequence {
		return
	}
	p.mu.Lock()
	p.current = v
	p.mu.Unlock()
}

func (p *parcels) noteOverlay(m *msg.ParcelOverlay) {
	p.mu.Lock()
	p.overlay.note(m)
	p.mu.Unlock()
}

// forget drops both, for a region crossing: the parcel and the layout
// of the region just left describe somewhere else entirely.
func (p *parcels) forget() {
	p.mu.Lock()
	p.current = nil
	p.overlay = Overlay{}
	p.mu.Unlock()
}

// Parcel is what the simulator last said about the land under this
// avatar, or nil if this region has said nothing yet.
//
// Nil is the ordinary answer for a session that has been standing still
// since before it attached, since the push comes on arrival.  Asking is
// how that is filled in, and it is the caller's to do.
func (a *Agent) Parcel() *Parcel {
	a.parcels.mu.Lock()
	defer a.parcels.mu.Unlock()
	return a.parcels.current
}

// Overlay is the parcel layout of the region this agent is in, as far
// as it has arrived.  It is a copy: four kilobytes, taken under the
// lock, so that a caller reading it square by square cannot be halfway
// through when a region change empties it.
func (a *Agent) Overlay() *Overlay {
	a.parcels.mu.Lock()
	defer a.parcels.mu.Unlock()
	o := a.parcels.overlay
	return &o
}

// noteParcel decodes a ParcelProperties event.
func (a *Agent) noteParcel(body any) {
	if v := DecodeParcel(body); v != nil {
		a.parcels.note(v)
	}
}

// DecodeParcel reads the event's ParcelData block.
//
// Everything is read defensively: this is the grid's LLSD and a field
// that changed shape must cost that field and not the parcel.
func DecodeParcel(body any) *Parcel {
	m := llsd.Map(body)
	if m == nil {
		return nil
	}
	rows, _ := m["ParcelData"].([]any)
	if len(rows) == 0 {
		return nil
	}
	b := llsd.Map(rows[0])
	if b == nil {
		return nil
	}

	v := &Parcel{
		LocalID:  int32(llsd.Int(b, "LocalID")),
		Name:     llsd.String(b, "Name"),
		Desc:     llsd.String(b, "Desc"),
		Sequence: int32(llsd.Int(b, "SequenceID")),

		IsGroupOwned: llsd.Bool(b, "IsGroupOwned"),
		Area:         int32(llsd.Int(b, "Area")),
		Bitmap:       llsd.Bytes(b, "Bitmap"),

		MaxPrims:          int32(llsd.Int(b, "MaxPrims")),
		TotalPrims:        int32(llsd.Int(b, "TotalPrims")),
		OwnerPrims:        int32(llsd.Int(b, "OwnerPrims")),
		GroupPrims:        int32(llsd.Int(b, "GroupPrims")),
		OtherPrims:        int32(llsd.Int(b, "OtherPrims")),
		SelectedPrims:     int32(llsd.Int(b, "SelectedPrims")),
		SimWideMaxPrims:   int32(llsd.Int(b, "SimWideMaxPrims")),
		SimWideTotalPrims: int32(llsd.Int(b, "SimWideTotalPrims")),

		SelfCount:   int32(llsd.Int(b, "SelfCount")),
		OtherCount:  int32(llsd.Int(b, "OtherCount")),
		PublicCount: int32(llsd.Int(b, "PublicCount")),

		SalePrice:  int32(llsd.Int(b, "SalePrice")),
		ClaimPrice: int32(llsd.Int(b, "ClaimPrice")),
		RentPrice:  int32(llsd.Int(b, "RentPrice")),
		PassPrice:  int32(llsd.Int(b, "PassPrice")),

		Category:    int32(llsd.Int(b, "Category")),
		Status:      int32(llsd.Int(b, "Status")),
		LandingType: int32(llsd.Int(b, "LandingType")),

		MediaURL: llsd.String(b, "MediaURL"),
		MusicURL: llsd.String(b, "MusicURL"),

		SeeAVs:        llsd.Bool(b, "SeeAVs"),
		AnyAVSounds:   llsd.Bool(b, "AnyAVSounds"),
		GroupAVSounds: llsd.Bool(b, "GroupAVSounds"),
	}

	v.PrimBonus = parcelReal(b["ParcelPrimBonus"])
	v.PassHours = parcelReal(b["PassHours"])

	// The uuids arrive as text, the way llsd renders <uuid>, so asking
	// for bytes would quietly match nothing.
	v.Owner = parseUUID(llsd.String(b, "OwnerID"))
	v.Group = parseUUID(llsd.String(b, "GroupID"))
	v.AuthBuyer = parseUUID(llsd.String(b, "AuthBuyerID"))
	v.MediaID = parseUUID(llsd.String(b, "MediaID"))
	v.Snapshot = parseUUID(llsd.String(b, "SnapshotID"))

	if secs := llsd.Int(b, "ClaimDate"); secs > 0 {
		v.Claimed = time.Unix(secs, 0).UTC()
	}

	v.AABBMin = parcelVector(b["AABBMin"])
	v.AABBMax = parcelVector(b["AABBMax"])
	v.UserLocation = parcelVector(b["UserLocation"])
	v.UserLookAt = parcelVector(b["UserLookAt"])

	// ParcelFlags is a U32 the simulator writes as four raw bytes,
	// most significant first -- which is what llsd.Int already makes of
	// a binary field, and is the reading that makes sense of what was
	// measured: 56 a4 80 0b on a residential parcel is fly, scripts
	// and landmarks allowed, where the other way round is a parcel
	// nobody may fly over.  A viewer's own panel agreed; see the
	// ParcelFlags bits above.
	v.Flags = uint32(llsd.Int(b, "ParcelFlags"))

	return v
}

// parcelReal reads a number that may have arrived as either LLSD type.
func parcelReal(v any) float32 {
	switch n := v.(type) {
	case float64:
		return float32(n)
	case int64:
		return float32(n)
	}
	return 0
}

// parcelVector reads a three element LLSD array as a position.
func parcelVector(v any) msg.Vector3 {
	a, _ := v.([]any)
	if len(a) != 3 {
		return msg.Vector3{}
	}
	f := func(i int) float32 {
		switch n := a[i].(type) {
		case float64:
			return float32(n)
		case int64:
			return float32(n)
		}
		return 0
	}
	return msg.Vector3{X: f(0), Y: f(1), Z: f(2)}
}

// parseUUID is msg.ParseUUID for a field that may be absent or empty,
// where a zero uuid is the right answer and an error is not.
func parseUUID(s string) msg.UUID {
	id, err := msg.ParseUUID(s)
	if err != nil {
		return msg.UUID{}
	}
	return id
}
