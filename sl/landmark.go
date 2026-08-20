package sl

// A place kept, and gone back to.
//
// A landmark is an ordinary inventory item whose asset is 96 bytes of
// text naming a region and a point in it.  That is the whole of it: not
// a handle, not a region name, and nothing about the parcel.  What a
// landmark remembers is a point, and everything a person would
// recognise about the place has to be asked for again on arrival --
// Parcel is the call that does that.
//
// Three things happen here and each is one message.  Reading one is a
// fetch of the asset and a parse.  Making one is CreateItem with two
// type numbers and no folder: the simulator writes the asset out of
// where the avatar is standing and files the item under Landmarks
// without being asked.  Going to one is TeleportLandmarkRequest, which
// the simulator turns into a handle itself -- which matters, because a
// handle is what Teleport needs and the only way this program has of
// getting one is a world map lookup by NAME, and a landmark carries no
// name.
//
// The doc is doc/landmark.md, including the measurements behind every
// claim above.
//
// # The two uuids, and why one of them is dangerous
//
// An inventory item has an ITEM id and an ASSET id, and everything in
// this file wants the asset one.  Measured on Agni 2026-08-18: the item
// id sent to the grid as a landmark is answered with perfect silence --
// no start, no progress, no refusal, waited out to twelve seconds --
// and so is a uuid that is nothing at all.  There is nothing to catch
// and no error to report, so the calls here name the parameter `asset`
// and say it again in every comment.
//
// The one wrong id that is not silent is the null one: the grid reads
// it as HOME and the avatar goes there in about a second.  So a zero id
// arriving at GoTo through a nil map, an unset field or an item whose
// asset the simulator did not name would move the avatar somewhere,
// convincingly, and call it success.  GoTo refuses it; GoHome is the
// deliberate way to ask.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// LandmarkVersion is the only version of the asset format anything here
// has read.
//
// Every landmark stage 0 looked at was version 2, including one made in
// 2026 and one a viewer made in July.  Nothing has ever seen a version
// 1, so there is nothing to say what one would look like and a parser
// that accepted it would be guessing about a file that decides where an
// avatar ends up.
const LandmarkVersion = 2

// Landmark is a place remembered: a region, and a point inside it.
//
// It is a point and not a platform.  The land under it may have been
// sold, the prim stood on taken away, and the region itself may be
// gone; none of that is in the asset and the grid says none of it on
// arrival.  A landmark into empty air is arrival in empty air followed
// by a fall -- measured, from 1000 metres to the ground, with nothing
// reported.
type Landmark struct {
	// Region is the region's grid-wide uuid, which is what the asset
	// carries and what the teleport is made from.
	//
	// It is NOT a handle.  Nothing local turns one into the other:
	// FindRegions goes by name, and a landmark has no name in it.
	Region msg.UUID

	// Position is where in that region, in metres from its south west
	// corner.  The simulator writes it from where the avatar stood, so
	// it is accurate to the couple of centimetres a standing avatar
	// drifts.
	Position msg.Vector3

	// Version is the asset format version, and is LandmarkVersion for
	// everything that parses at all.  Kept because a landmark that
	// stops working after a grid update is worth being able to see the
	// version of.
	Version int

	// Asset is the id this was read from, so that a landmark read and
	// then travelled to does not have to be carried alongside its own
	// id.  Zero for one that came from ParseLandmark, which is handed
	// bytes and never learns where they came from.
	Asset msg.UUID
}

// ParseLandmark reads the text of a landmark asset.
//
// The whole of one, as measured on Agni 2026-08-18 -- 96 bytes:
//
//	Landmark version 2
//	region_id 5cf27e57-7e57-c0de-e8ce-271cf9bf3385
//	local_pos 32.00 70.00 1000.09
//
// Strictness here is not fussiness.  The null region id is what the
// grid reads as HOME, so a parser that shrugged at a region id it could
// not read would hand back a landmark that teleports the avatar home
// while looking exactly like a landmark that works.  Quietly going
// somewhere else is the worst answer available, so a region id that
// will not parse is an error and never a zero.
//
// A version nobody has read is refused for the same reason at one
// remove: the version is the thing that would change if the meaning of
// these fields ever did, and reading version 3 with version 2's rules
// is guessing about the same field.
//
// A key this does not know, inside a version it does, is ignored.  That
// is the one piece of slack, and it is deliberate: an added field
// cannot make region_id wrong, and refusing the whole asset over one
// would take out every landmark on the grid the day Linden Lab adds a
// line.
func ParseLandmark(b []byte) (*Landmark, error) {
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return nil, fmt.Errorf("sl: this is not a landmark: it is empty")
	}

	// The version line first, and first: it is what says the rest of
	// the file means what this thinks it means.
	var version int
	if v, ok := strings.CutPrefix(lines[0], "Landmark version "); ok {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return nil, fmt.Errorf("sl: this is not a landmark: its version line is %q", lines[0])
		}
		version = n
	} else {
		return nil, fmt.Errorf("sl: this is not a landmark: it begins %q, "+
			"and every landmark begins %q", snippetLine(lines[0]), "Landmark version 2")
	}
	if version != LandmarkVersion {
		return nil, fmt.Errorf("sl: this landmark says version %d, and the only "+
			"version anything here has read is %d; what its fields mean is not "+
			"known and guessing would send the avatar somewhere", version, LandmarkVersion)
	}

	lm := &Landmark{Version: version}
	var haveRegion, havePos bool
	for _, line := range lines[1:] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, val, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("sl: this landmark has a line with no value on it: %q",
				snippetLine(line))
		}
		switch key {
		case "region_id":
			id, err := msg.ParseUUID(strings.TrimSpace(val))
			if err != nil {
				// Never a zero: see the doc comment.  A zero here is
				// home, and a broken landmark that silently goes home
				// is worse than one that will not go anywhere.
				return nil, fmt.Errorf("sl: this landmark's region id will not parse, "+
					"and the null region id is what the grid reads as HOME, so it is "+
					"refused rather than guessed at: %w", err)
			}
			lm.Region, haveRegion = id, true
		case "local_pos":
			p, err := parseLocalPos(val)
			if err != nil {
				return nil, err
			}
			lm.Position, havePos = p, true
		}
	}

	if !haveRegion {
		return nil, fmt.Errorf("sl: this landmark names no region_id, and a landmark " +
			"with no region is a teleport to the null region, which is home")
	}
	if !havePos {
		return nil, fmt.Errorf("sl: this landmark names no local_pos")
	}
	return lm, nil
}

// parseLocalPos reads the three numbers of a local_pos line.
//
// float32 rather than float64 because that is what the protocol carries
// and what every other position in this package is: parsing wider and
// narrowing later would leave a landmark that compares unequal to the
// position it was made from.
func parseLocalPos(val string) (msg.Vector3, error) {
	f := strings.Fields(val)
	if len(f) != 3 {
		return msg.Vector3{}, fmt.Errorf("sl: this landmark's local_pos has %d numbers "+
			"on it rather than three: %q", len(f), snippetLine(val))
	}
	var v [3]float32
	for i, s := range f {
		n, err := strconv.ParseFloat(s, 32)
		if err != nil {
			return msg.Vector3{}, fmt.Errorf("sl: this landmark's local_pos does not "+
				"read as a position: %w", err)
		}
		v[i] = float32(n)
	}
	return msg.Vector3{X: v[0], Y: v[1], Z: v[2]}, nil
}

// snippetLine keeps an error about a malformed file from printing the
// whole file.
func snippetLine(s string) string {
	const most = 60
	if len(s) > most {
		return s[:most] + "..."
	}
	return s
}

// Landmark fetches a landmark's asset and reads it.
//
// The id is the ASSET id, which is the second of an inventory item's
// two uuids: Entry.Asset in a listing, Item.AssetID on a created item.
// The ITEM id -- the one `ls -l` prints and the one a person is most
// likely to have to hand -- is not accepted anywhere the grid takes a
// landmark, and it is refused by silence rather than by an error, so
// passing it here fetches nothing and passing it to GoTo moves nothing.
// The parameter is named for the half that works.
func (w *Session) Landmark(ctx context.Context, asset msg.UUID) (*Landmark, error) {
	if asset.IsZero() {
		return nil, fmt.Errorf("sl: no landmark asset id; " +
			"Entry.Asset is where a listing carries one, and it is not the item id")
	}
	b, err := w.Asset(ctx, asset, AssetLandmark)
	if err != nil {
		return nil, err
	}
	lm, err := ParseLandmark(b)
	if err != nil {
		return nil, fmt.Errorf("%w (asset %s)", err, asset)
	}
	lm.Asset = asset
	return lm, nil
}

// The two numbers a landmark is created with.
//
// They are from two different tables of Linden Lab's -- LLAssetType's
// AT_LANDMARK and LLInventoryType's IT_LANDMARK -- which both happen to
// be 3.  Written separately because the agreement is a coincidence of
// two enumerations rather than one number used twice, and a future
// where they part company should break here and not somewhere subtler.
const (
	assetTypeLandmark = int8(AssetLandmark)
	invTypeLandmark   = int8(3)
)

// MakeLandmark makes a landmark of where the avatar is standing now,
// and hands back the inventory item.
//
// Nothing says where the landmark is of, and nothing can: the position
// is the simulator's to write, out of where it believes this avatar is.
// So this is a call with a moment attached -- it records the place the
// avatar occupies at the instant the message lands, which for a walking
// avatar is not quite the place it was asked from.
//
// No folder is named, on purpose.  The simulator files the item under
// the folder preferred for the type, which is Landmarks, without being
// asked; naming one would only be a way of getting it wrong.
//
// Measured on Agni 2026-08-18: the item came back in about 300ms and
// its asset was readable 200ms after that, so a caller that wants to
// read what it just made can call Landmark on Item.AssetID and expect
// it to be there.  There is nothing to wait for and nothing that says
// when it is ready, so a fetch that 404s is worth one retry.
func (w *Session) MakeLandmark(ctx context.Context, name, desc string) (*Item, error) {
	return w.CreateItem(ctx, name, desc, assetTypeLandmark, invTypeLandmark)
}

// GoTo takes the avatar to a landmark and waits until it is there.
//
// The id is the ASSET id, for Landmark's reason and with worse
// consequences: the item id is answered with nothing whatever, so a
// caller that passed the wrong one waits out the timeout with no idea
// why.  The timeout says so.
//
// A zero id is refused.  The grid reads the null id as home and goes
// there, in about a second, reporting success -- so a zero arriving
// through an unset field or an item whose asset the simulator never
// named would move the avatar to a place nobody asked for and look like
// it worked.  Going home is GoHome, which is the same message said on
// purpose.
//
// Nothing here has to turn the region id into a handle: the simulator
// does that, which is the whole reason this is not Teleport with a
// world map lookup in front of it.
//
// Waiting is Teleport's waiting and the errors are Teleport's errors:
// ErrTeleportRefused for a grid that said no, ErrTeleportLost for a
// grid that handed this avatar on and a daemon that did not follow, and
// ErrTimeout for silence.  A zero timeout is DefaultTeleportTimeout.
func (w *Session) GoTo(ctx context.Context, asset msg.UUID, timeout time.Duration) error {
	if asset.IsZero() {
		return fmt.Errorf("sl: GoTo was given the null landmark id, which the grid " +
			"reads as HOME and obeys; if that is what was wanted, GoHome says so, " +
			"and if it is not, the asset id has gone missing on the way here")
	}
	err := w.goToLandmark(ctx, asset, timeout, "the teleport to landmark "+asset.String())
	if errors.Is(err, ErrTimeout) {
		// Measured on Agni 2026-08-18: the ITEM id, and a uuid that is
		// nothing at all, are both answered with perfect silence out to
		// twelve seconds.  So the commonest cause of this timeout is
		// not a busy grid but the wrong one of an item's two uuids, and
		// nothing else will ever say so.
		return fmt.Errorf("%w; the grid says nothing at all to a landmark it cannot "+
			"find, and the usual reason is the ITEM id where the ASSET id was "+
			"wanted -- Entry.Asset and Item.AssetID are the one it takes", err)
	}
	return err
}

// GoHome takes the avatar to wherever this account's home is set.
//
// It is TeleportLandmarkRequest carrying the null id, which the grid
// reads as home rather than as an error: measured on Agni 2026-08-18,
// the avatar arrived at the home parcel in 1.0 seconds.  So home is the
// one landmark nobody has to own, and it costs no inventory lookup.
//
// It is a call of its own rather than something GoTo does with a zero
// id, because a zero id is what a missing id looks like: see GoTo.  The
// errors are Teleport's.
func (w *Session) GoHome(ctx context.Context, timeout time.Duration) error {
	return w.goToLandmark(ctx, msg.UUID{}, timeout, "the teleport home")
}

// goToLandmark sends the one message and waits for the answer the
// session already reads.
//
// A TeleportLocal counts as an answer here, which it does not for
// Teleport.  A landmark may well point into the region the avatar is
// already standing in -- stage 0 measured one that did -- and the
// caller cannot know that in advance, since the destination is inside
// an asset it has not fetched.  Refusing to count a local move would
// turn the commonest short trip into a timeout.
func (w *Session) goToLandmark(ctx context.Context, asset msg.UUID,
	timeout time.Duration, what string) error {

	if timeout == 0 {
		timeout = DefaultTeleportTimeout
	}

	where, err := w.Where(ctx)
	if err != nil {
		return err
	}

	m := &msg.TeleportLandmarkRequest{}
	m.Info.AgentID, m.Info.SessionID = w.agentBlock()
	m.Info.LandmarkID = asset

	// Watching before asking, for teleport.go's reason: the answer is
	// about a third of a second behind the request, which is a race a
	// watcher registered afterwards loses often enough to matter and
	// silently when it does.
	watch := w.watchTeleport(where.RegionHandle, true)
	defer watch.stop()

	if err := w.Send(ctx, m); err != nil {
		return err
	}
	return watch.arrive(ctx, what, timeout)
}
