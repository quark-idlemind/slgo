package sl

// A place kept, and gone back to.
//
// The asset below is the one stage 0 read off Agni on 2026-08-18, kept
// byte for byte, so that what this package parses is checked against a
// measurement rather than against something written to match it.
//
// Everything here answers from inside onSend, on the caller's
// goroutine, for parcel_test.go's reason: Relay fails the test when
// nothing reads it, and a failure raised from a goroutine that is not
// the test's is a panic rather than a failure.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// agniLandmark is the whole of one landmark asset, 96 bytes of it, as
// Agni served it.  The trailing newline is the grid's and is kept.
const agniLandmark = "Landmark version 2\n" +
	"region_id 5cf27e57-7e57-c0de-e8ce-271cf9bf3385\n" +
	"local_pos 32.00 70.00 1000.09\n"

// pelmarReach is the region id in the asset above.
var pelmarReach = msg.MustParseUUID("5cf27e57-7e57-c0de-e8ce-271cf9bf3385")

// aLandmarkAsset is an asset id to fetch by.  It is not an item id, and
// the difference is the whole of what this file is careful about.
var aLandmarkAsset = msg.MustParseUUID("97747e57-7e57-c0de-6eb9-326758b2e7f4")

// TestALandmarkIsAPointInARegion: 96 bytes naming a region and a place
// in it, and nothing else.  Not a handle, not a name, nothing about the
// parcel.
func TestALandmarkIsAPointInARegion(t *testing.T) {
	t.Parallel()
	if n := len(agniLandmark); n != 96 {
		t.Errorf("the measured asset is %d bytes here and was 96 on Agni", n)
	}

	lm, err := ParseLandmark([]byte(agniLandmark))
	if err != nil {
		t.Fatalf("ParseLandmark: %v", err)
	}
	if lm.Region != pelmarReach {
		t.Errorf("region = %v, want %v", lm.Region, pelmarReach)
	}
	if want := (msg.Vector3{X: 32, Y: 70, Z: 1000.09}); lm.Position != want {
		t.Errorf("position = %v, want %v", lm.Position, want)
	}
	if lm.Version != LandmarkVersion {
		t.Errorf("version = %d, want %d", lm.Version, LandmarkVersion)
	}
}

// TestAVersionNobodyHasSeenIsRefused: the version is what would change
// if the meaning of these fields ever did, so reading a version 3
// landmark with version 2's rules is guessing about the field that
// decides where an avatar ends up.
func TestAVersionNobodyHasSeenIsRefused(t *testing.T) {
	t.Parallel()
	for _, text := range []string{
		"Landmark version 3\nregion_id 5cf27e57-7e57-c0de-e8ce-271cf9bf3385\n" +
			"local_pos 32.00 70.00 1000.09\n",
		"Landmark version 1\nregion_id 5cf27e57-7e57-c0de-e8ce-271cf9bf3385\n" +
			"local_pos 32.00 70.00 1000.09\n",
	} {
		lm, err := ParseLandmark([]byte(text))
		if err == nil {
			t.Fatalf("ParseLandmark read %q as %+v", strings.SplitN(text, "\n", 2)[0], lm)
		}
		if !strings.Contains(err.Error(), "version") {
			t.Errorf("the refusal %q does not say it is about the version", err)
		}
	}

	// And something that is not a landmark at all.  `cat` sends
	// notecards through a different path, so a notecard's bytes
	// arriving here means somebody asked for the wrong asset type.
	if _, err := ParseLandmark([]byte("Linden text version 2\n{\n}\n")); err == nil {
		t.Error("a notecard was read as a landmark")
	}
	if _, err := ParseLandmark(nil); err == nil {
		t.Error("nothing at all was read as a landmark")
	}
}

// TestARegionIdThatWillNotParseIsRefusedRatherThanBecomingHome: this is
// the refusal the whole parse exists for.  The null region id is what
// the grid reads as HOME and obeys, so a parser that shrugged at a
// region id it could not read would hand back a landmark that teleports
// the avatar home while looking exactly like one that works.
func TestARegionIdThatWillNotParseIsRefusedRatherThanBecomingHome(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{
		"Landmark version 2\nregion_id not-a-uuid\nlocal_pos 32.00 70.00 1000.09\n",
		"Landmark version 2\nregion_id 5cf27e577e57c0dee8ce271cf9bf3385\n" +
			"local_pos 32.00 70.00 1000.09\n",
		"Landmark version 2\nregion_id \nlocal_pos 32.00 70.00 1000.09\n",
		"Landmark version 2\nlocal_pos 32.00 70.00 1000.09\n",
	} {
		lm, err := ParseLandmark([]byte(bad))
		if err == nil {
			t.Errorf("ParseLandmark read %q as region %v -- and the null region "+
				"id is home", bad, lm.Region)
			continue
		}
		if !strings.Contains(err.Error(), "HOME") && !strings.Contains(err.Error(), "home") {
			t.Errorf("the refusal %q does not say what a zero region id would have done", err)
		}
	}

	// A position that will not read is refused too, though for the
	// smaller reason: a zero position is a corner of the region rather
	// than another region entirely.
	for _, bad := range []string{
		"Landmark version 2\nregion_id 5cf27e57-7e57-c0de-e8ce-271cf9bf3385\n" +
			"local_pos 32.00 70.00\n",
		"Landmark version 2\nregion_id 5cf27e57-7e57-c0de-e8ce-271cf9bf3385\n" +
			"local_pos here there everywhere\n",
		"Landmark version 2\nregion_id 5cf27e57-7e57-c0de-e8ce-271cf9bf3385\n",
	} {
		if lm, err := ParseLandmark([]byte(bad)); err == nil {
			t.Errorf("ParseLandmark read %q as position %v", bad, lm.Position)
		}
	}
}

// TestAnAddedLineDoesNotTakeOutEveryLandmarkOnTheGrid: the one piece of
// slack in the parse.  An added key cannot make region_id wrong, and
// the version is what would say the format had changed, so a key this
// does not know inside a version it does is ignored rather than fatal.
func TestAnAddedLineDoesNotTakeOutEveryLandmarkOnTheGrid(t *testing.T) {
	t.Parallel()
	lm, err := ParseLandmark([]byte("Landmark version 2\n" +
		"region_id 5cf27e57-7e57-c0de-e8ce-271cf9bf3385\n" +
		"look_at 1.00 0.00 0.00\n" +
		"local_pos 32.00 70.00 1000.09\n"))
	if err != nil {
		t.Fatalf("ParseLandmark: %v", err)
	}
	if lm.Region != pelmarReach || lm.Position.Z != 1000.09 {
		t.Errorf("landmark = %+v", lm)
	}
}

// TestReadingALandmarkFetchesItByAssetId: the asset id is the second of
// an item's two uuids, and the item id fetches nothing.  The parameter
// is named for the half that works and this checks the request carries
// it.
func TestReadingALandmarkFetchesItByAssetId(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	var asked string
	f.ServeCap(t, AssetCap, func(rw http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		rw.Write([]byte(agniLandmark))
	})

	lm, err := w.Landmark(context.Background(), aLandmarkAsset)
	if err != nil {
		t.Fatalf("Landmark: %v", err)
	}
	if lm.Region != pelmarReach {
		t.Errorf("region = %v", lm.Region)
	}
	// The landmark carries the id it was read from, so that going to
	// one that has just been read does not mean carrying its id
	// alongside it.
	if lm.Asset != aLandmarkAsset {
		t.Errorf("the landmark came back naming asset %v", lm.Asset)
	}
	if !strings.Contains(asked, "landmark_id") || !strings.Contains(asked, aLandmarkAsset.String()) {
		t.Errorf("the fetch asked %q", asked)
	}

	// A zero id is refused before anything is fetched: it is what a
	// missing asset id looks like, and asking the network about it
	// would only waste the round trip.
	if _, err := w.Landmark(context.Background(), msg.UUID{}); err == nil {
		t.Error("Landmark accepted the null asset id")
	}
}

// TestALandmarkThatWillNotParseSaysWhichAssetItWas: a landmark read out
// of a listing of forty is no use as "this landmark's region id will
// not parse" alone.
func TestALandmarkThatWillNotParseSaysWhichAssetItWas(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.ServeCap(t, AssetCap, func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte("Landmark version 2\nregion_id nonsense\nlocal_pos 1 2 3\n"))
	})

	_, err := w.Landmark(context.Background(), aLandmarkAsset)
	if err == nil {
		t.Fatal("a landmark with an unreadable region id was accepted")
	}
	if !strings.Contains(err.Error(), aLandmarkAsset.String()) {
		t.Errorf("the refusal %q does not say which asset it was about", err)
	}
}

// TestMakingALandmarkSendsBothTypeNumbersAndNoFolder: the simulator
// writes the asset out of where the avatar is standing and files the
// item under Landmarks itself.  Naming a folder would only be a way of
// getting it wrong, and the two type numbers are what say this is a
// landmark rather than an empty notecard.
func TestMakingALandmarkSendsBothTypeNumbersAndNoFolder(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	wait := aside(t, func() (*Item, error) {
		return w.MakeLandmark(context.Background(), "Thrushmoor", "where the probe started")
	})

	m := waitSent[*msg.CreateInventoryItem](t, f)
	b := &m.InventoryBlock
	if b.Type != 3 || b.InvType != 3 {
		t.Errorf("asked for type %d/%d, want 3/3 -- AT_LANDMARK and IT_LANDMARK",
			b.Type, b.InvType)
	}
	if !b.FolderID.IsZero() {
		t.Errorf("a folder was named (%v); the simulator files a landmark under "+
			"Landmarks without being asked", b.FolderID)
	}
	if got := trimNul(b.Name); got != "Thrushmoor" {
		t.Errorf("asked for a landmark called %q", got)
	}
	if got := trimNul(b.Description); got != "where the probe started" {
		t.Errorf("described it as %q", got)
	}

	f.Relay(t, &msg.UpdateCreateInventoryItem{
		InventoryData: []msg.UpdateCreateInventoryItem_InventoryData{{
			CallbackID: b.CallbackID,
			ItemID:     msg.MustParseUUID("7d0c7e57-7e57-c0de-251f-1c6a0825436f"),
			FolderID:   msg.MustParseUUID("45937e57-7e57-c0de-f351-8711ac387fe4"),
			AssetID:    aLandmarkAsset,
			Name:       []byte("Thrushmoor\x00"),
			Type:       3, InvType: 3,
		}},
	})

	it, err := wait()
	if err != nil {
		t.Fatalf("MakeLandmark: %v", err)
	}
	// The item's own id and its asset id are different uuids, and only
	// the second is any use for going there.
	if it.AssetID != aLandmarkAsset {
		t.Errorf("the item came back naming asset %v", it.AssetID)
	}
	if it.ID == it.AssetID {
		t.Error("the item id and the asset id came back the same")
	}
}

// TestGoToWaitsForTheTeleportAnswer: the same answer Teleport waits on.
// A TeleportFinish says the simulator being left has let go, which is
// the middle of a teleport; the end of one is this session answering
// with the region the finish named.
func TestGoToWaitsForTheTeleportAnswer(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLandmarkRequest); !ok {
			return
		}
		f.RelayEvent(t, "TeleportFinish", agniFinish)
		f.mu.Lock()
		f.presence.RegionHandle = goguen
		f.presence.Region = "Sandbox Goguen"
		f.mu.Unlock()
	}
	f.mu.Unlock()

	if err := w.GoTo(context.Background(), aLandmarkAsset, 5*time.Second); err != nil {
		t.Fatalf("GoTo: %v", err)
	}
	m := onlySent[*msg.TeleportLandmarkRequest](t, f)
	if m.Info.LandmarkID != aLandmarkAsset {
		t.Errorf("asked to be taken to landmark %v", m.Info.LandmarkID)
	}
	if m.Info.AgentID != testAgentID || m.Info.SessionID != testSessionID {
		t.Error("the request did not carry this session's ids")
	}
	// Nothing turned the region id into a handle, because nothing here
	// can: FindRegions goes by name and a landmark carries none.
	if got := sentOf[*msg.TeleportLocationRequest](f); len(got) != 0 {
		t.Error("a landmark teleport went out as a location teleport")
	}
}

// TestGoToCountsALocalMoveAsArrival: a landmark may point into the
// region the avatar is already in, which the simulator does itself and
// announces with a TeleportLocal and no finish at all.  The caller
// cannot know that in advance -- the destination is inside an asset it
// has not fetched -- so refusing to count one would turn the commonest
// short trip into a timeout.
func TestGoToCountsALocalMoveAsArrival(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLandmarkRequest); ok {
			f.Relay(t, &msg.TeleportLocal{})
		}
	}
	f.mu.Unlock()

	// The fake never moves and never sends a finish, so this can only
	// return by having counted the local move.
	if err := w.GoTo(context.Background(), aLandmarkAsset, 5*time.Second); err != nil {
		t.Fatalf("GoTo: %v", err)
	}
}

// TestGoToAnsweredWithSilenceBlamesTheItemId: measured on Agni, the
// item id and a uuid that is nothing at all are both answered with
// perfect silence out to twelve seconds.  There is no error to report
// and nothing to catch, so the timeout is the only place this can ever
// be said.
func TestGoToAnsweredWithSilenceBlamesTheItemId(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.mu.Unlock()

	err := w.GoTo(context.Background(), aLandmarkAsset, 300*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("GoTo = %v, want a timeout", err)
	}
	if !strings.Contains(err.Error(), "ITEM id") {
		t.Errorf("the timeout %q does not name the likeliest cause", err)
	}
}

// TestGoToRefusesTheNullId: the one wrong id the grid is not silent
// about.  It reads the null id as HOME and obeys in about a second, so
// a zero arriving through an unset field or an item whose asset the
// simulator never named would move the avatar somewhere nobody asked
// for and report success.
func TestGoToRefusesTheNullId(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.mu.Unlock()

	err := w.GoTo(context.Background(), msg.UUID{}, time.Second)
	if err == nil {
		t.Fatal("GoTo accepted the null id, which the grid obeys as a trip home")
	}
	if !strings.Contains(err.Error(), "GoHome") {
		t.Errorf("the refusal %q does not say what to call if home was the point", err)
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("an accidental trip home went out anyway: %s", f.describe())
	}
}

// TestGoHomeSendsTheNullId: home is the one landmark nobody has to own,
// and it costs no inventory lookup -- the same message with the null id
// in it.
func TestGoHomeSendsTheNullId(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLandmarkRequest); ok {
			f.Relay(t, &msg.TeleportLocal{})
		}
	}
	f.mu.Unlock()

	if err := w.GoHome(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("GoHome: %v", err)
	}
	m := onlySent[*msg.TeleportLandmarkRequest](t, f)
	if !m.Info.LandmarkID.IsZero() {
		t.Errorf("GoHome sent landmark %v rather than the null id", m.Info.LandmarkID)
	}
	if m.Info.AgentID != testAgentID || m.Info.SessionID != testSessionID {
		t.Error("the request did not carry this session's ids")
	}
}

// TestGoHomeReportsARefusalAsARefusal: the errors are Teleport's and
// mean what they mean there, so a caller that has one already knows how
// to read this one.
func TestGoHomeReportsARefusalAsARefusal(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.TeleportLandmarkRequest); ok {
			f.RelayEvent(t, "TeleportFailed", agniNoSuchRegion)
		}
	}
	f.mu.Unlock()

	err := w.GoHome(context.Background(), 5*time.Second)
	if !errors.Is(err, ErrTeleportRefused) {
		t.Fatalf("GoHome = %v, want a refusal", err)
	}
	if !strings.Contains(err.Error(), "no_host") {
		t.Errorf("the refusal %q does not carry what the grid said", err)
	}
}

// TestGoToThatCouldNotBeSentIsNotATeleport: the request goes over the
// wire, and a circuit that has gone is not a landmark the grid could
// not find.
func TestGoToThatCouldNotBeSentIsNotATeleport(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.mu.Lock()
	f.presence.RegionHandle = 1099511628032
	f.mu.Unlock()
	f.FailSends(errors.New("the circuit is gone"))

	err := w.GoTo(context.Background(), aLandmarkAsset, time.Second)
	if err == nil || errors.Is(err, ErrTimeout) {
		t.Errorf("GoTo = %v, want the send's own failure", err)
	}
}
