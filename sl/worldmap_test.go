package sl

// What the map answers with, and the four shapes of it that are not a
// list of regions: a prefix that matches many, an end marker that is not
// a place, a name that matches nothing but still gets the marker, and an
// answer spread over more than one packet.  All four were measured on
// Agni and all four are reachable here.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// mapBlock is one region as the grid describes it, with the fields the
// grid leaves at zero left at zero -- Agents, RegionFlags and
// WaterHeight were empty for every region on every run.
func mapBlock(name string, x, y uint16, access uint8) msg.MapBlockReply_Data {
	return msg.MapBlockReply_Data{
		X: x, Y: y, Name: append([]byte(name), 0), Access: access,
	}
}

// endOfList is the block every reply finishes with: no coordinates, an
// access code that is not a rating, and the query lowercased with its
// last character taken off.  The mangled name is written out here
// because it is the thing this package deliberately does NOT match on.
func endOfList(query string) msg.MapBlockReply_Data {
	mangled := strings.ToLower(query)
	if mangled != "" {
		mangled = mangled[:len(mangled)-1]
	}
	return msg.MapBlockReply_Data{Name: append([]byte(mangled), 0), Access: 255}
}

// answerMap makes the fake reply to a MapNameRequest with these packets,
// in order, which is how a reply that arrives in more than one of them
// is reached.
func answerMap(t *testing.T, f *fakeBackend, packets ...[]msg.MapBlockReply_Data) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.MapNameRequest); !ok {
			return
		}
		for _, blocks := range packets {
			r := &msg.MapBlockReply{}
			r.AgentData.AgentID = testAgentID
			r.Data = blocks
			f.Relay(t, r)
		}
	}
}

// TestFindingARegionAsksTheMapAndReadsThePositionOutOfTheReply, which is
// the whole of the lookup: a name goes out and a place comes back, with
// the handle a teleport takes worked out from the coordinates.
func TestFindingARegionAsksTheMapAndReadsThePositionOutOfTheReply(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	answerMap(t, f, []msg.MapBlockReply_Data{
		mapBlock("Pelmar Reach", 43648, 43648, AccessModerate),
		endOfList("Pelmar Reach"),
	})

	got, err := w.FindRegions(context.Background(), "Pelmar Reach", 5*time.Second)
	if err != nil {
		t.Fatalf("FindRegions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("FindRegions gave %d regions, want the one: %+v", len(got), got)
	}
	r := got[0]
	if r.Name != "Pelmar Reach" || r.X != 1054 || r.Y != 992 || r.Access != AccessModerate {
		t.Errorf("the map answered %+v", r)
	}
	// The handle is what stage 0 read off the live session for this
	// region, so this is the arithmetic checked against the grid rather
	// than against itself.
	if r.Handle != 47991483540340736 {
		t.Errorf("handle = %d, want the 47991483540340736 the session reports", r.Handle)
	}

	m := onlySent[*msg.MapNameRequest](t, f)
	if m.AgentData.AgentID != testAgentID || m.AgentData.SessionID != testSessionID {
		t.Errorf("the request does not say who is asking: %+v", m.AgentData)
	}
	if m.AgentData.Flags != mapLayerFlag || m.AgentData.EstateID != 0 || m.AgentData.Godlike {
		t.Errorf("the request should carry what the viewer sends, got %+v", m.AgentData)
	}
	if trimNul(m.NameData.Name) != "Pelmar Reach" {
		t.Errorf("the request asked about %q", trimNul(m.NameData.Name))
	}
}

// TestAPrefixMatchesSeveralRegionsAndAllOfThemComeBack.
//
// The search is by prefix and ignores case -- "Sandbox" found
// thirty-three regions on Agni and no region is called Sandbox -- so a
// caller is handed the list.  Picking the exact name out of it here
// would be this package choosing on behalf of whoever typed the prefix.
func TestAPrefixMatchesSeveralRegionsAndAllOfThemComeBack(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	answerMap(t, f, []msg.MapBlockReply_Data{
		mapBlock("Pelmar Reach Two", 43649, 43648, AccessGeneral),
		mapBlock("Pelmar Reach", 43648, 43648, AccessModerate),
		mapBlock("Pelmarwood", 900, 1000, AccessAdult),
		endOfList("pelm"),
	})

	got, err := w.FindRegions(context.Background(), "pelm", 5*time.Second)
	if err != nil {
		t.Fatalf("FindRegions: %v", err)
	}
	// In name order, because the grid's order is the grid's business and
	// a listing that will not sit still cannot be read twice.
	var names []string
	for _, r := range got {
		names = append(names, r.Name)
	}
	want := "Pelmar Reach, Pelmar Reach Two, Pelmarwood"
	if strings.Join(names, ", ") != want {
		t.Errorf("FindRegions gave %q, want %q", strings.Join(names, ", "), want)
	}
}

// TestTheEndOfTheListIsNotARegion: it has a name, and a caller handed it
// would be handed a region at (0, 0) that nobody can go to.
func TestTheEndOfTheListIsNotARegion(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	answerMap(t, f, []msg.MapBlockReply_Data{
		mapBlock("Sandbox Goguen", 995, 997, AccessGeneral),
		endOfList("Sandbox Goguen"),
	})

	got, err := w.FindRegions(context.Background(), "Sandbox Goguen", 5*time.Second)
	if err != nil {
		t.Fatalf("FindRegions: %v", err)
	}
	for _, r := range got {
		if r.X == 0 && r.Y == 0 {
			t.Errorf("the end of the list was passed on as a region: %+v", r)
		}
	}
	if len(got) != 1 {
		t.Fatalf("FindRegions gave %d regions, want the one: %+v", len(got), got)
	}
}

// TestAnAnswerThatArrivesInSeveralPacketsIsOneList.
//
// "Sandbox" came back as 26 blocks and then 8, with the end marker in
// the second, so a call that returned on the first reply would answer
// with three quarters of the grid's sandboxes and no sign that it had.
func TestAnAnswerThatArrivesInSeveralPacketsIsOneList(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	var first, second []msg.MapBlockReply_Data
	for i := 0; i < 26; i++ {
		first = append(first, mapBlock("Sandbox "+string(rune('A'+i)), uint16(1000+i), 1000, AccessGeneral))
	}
	for i := 0; i < 8; i++ {
		second = append(second, mapBlock("Sandbox "+string(rune('a'+i)), uint16(1100+i), 1000, AccessGeneral))
	}
	second = append(second, endOfList("Sandbox"))
	answerMap(t, f, first, second)

	got, err := w.FindRegions(context.Background(), "Sandbox", 5*time.Second)
	if err != nil {
		t.Fatalf("FindRegions: %v", err)
	}
	if len(got) != 34 {
		t.Errorf("FindRegions gave %d regions, want the 26 and the 8 together", len(got))
	}
}

// TestANameThatMatchesNothingIsAnErrorSayingWhichName.
//
// The grid answers with the end marker and nothing before it, which is
// the only way it says "no such region" -- and is not an empty reply, so
// a caller cannot tell it from a lost one without being told.
func TestANameThatMatchesNothingIsAnErrorSayingWhichName(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	answerMap(t, f, []msg.MapBlockReply_Data{endOfList("Nowhere At All")})

	got, err := w.FindRegions(context.Background(), "Nowhere At All", 5*time.Second)
	if err == nil {
		t.Fatalf("FindRegions found %+v where the grid knows nothing", got)
	}
	if errors.Is(err, ErrTimeout) {
		t.Errorf("FindRegions = %v, want an answer rather than a wait that ran out", err)
	}
	if !strings.Contains(err.Error(), "Nowhere At All") {
		t.Errorf("FindRegions = %v, want it to name the region asked for", err)
	}
}

// TestAMapThatNeverAnswersIsATimeoutAndNotAnEmptyGrid, since the two are
// opposite facts and the difference is whether to ask again.
func TestAMapThatNeverAnswersIsATimeoutAndNotAnEmptyGrid(t *testing.T) {
	t.Parallel()
	w, _ := newFakeSession(t)

	_, err := w.FindRegions(context.Background(), "Pelmar Reach", 300*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("FindRegions = %v, want a timeout", err)
	}
}

// TestAnAnswerWithNoEndToItIsNotAnAnswer: the marker is what says the
// list is whole, so blocks without it are a fragment, and a fragment
// returned as the answer is a shorter grid than the real one.
func TestAnAnswerWithNoEndToItIsNotAnAnswer(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	answerMap(t, f, []msg.MapBlockReply_Data{
		mapBlock("Sandbox Goguen", 995, 997, AccessGeneral),
	})

	got, err := w.FindRegions(context.Background(), "Sandbox", 300*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("FindRegions = %+v, %v; want a timeout", got, err)
	}
	// And it says how much did arrive, since "nothing came back" and
	// "most of it came back" are different things to do next about.
	if !strings.Contains(err.Error(), "so far: 1") {
		t.Errorf("FindRegions = %v, want it to say what had arrived", err)
	}
}

// TestAnotherClientsAnswerIsNotThisOnes.
//
// The daemon relays a MapBlockReply to every client subscribed to one,
// and the reply says nothing about which question provoked it: there is
// no id in it to sift by, the way Profile and ScriptRunning sift theirs.
// So two shells looking regions up at the same time see each other's
// blocks, and the only thing that tells them apart is the search's own
// rule -- a name that does not begin with what was asked about is not an
// answer to it.
//
// The end marker is the half that cannot be told apart, and is not
// tested here because there is nothing to test: it is recognised by its
// shape, which is the same shape whoever asked.  See the head of
// worldmap.go.
func TestAnotherClientsAnswerIsNotThisOnes(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	answerMap(t, f, []msg.MapBlockReply_Data{
		// The answer to somebody else's "Sandbox", arriving first.
		mapBlock("Sandbox Goguen", 995, 997, AccessGeneral),
		mapBlock("Pelmar Reach", 43648, 43648, AccessModerate),
		endOfList("Pelmar Reach"),
	})

	got, err := w.FindRegions(context.Background(), "Pelmar Reach", 5*time.Second)
	if err != nil {
		t.Fatalf("FindRegions: %v", err)
	}
	if len(got) != 1 || got[0].Name != "Pelmar Reach" {
		t.Fatalf("FindRegions gave %+v, want the one region asked about", got)
	}
}

// TestFindingARegionNeedsAName, rather than asking the map about the
// empty string and reporting whatever that provokes.
func TestFindingARegionNeedsAName(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	if _, err := w.FindRegions(context.Background(), "   ", time.Second); err == nil {
		t.Error("FindRegions looked for a region with no name")
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("a request for nothing went out anyway: %s", f.describe())
	}
}

// TestARequestThatCouldNotBeSentIsNotAnEmptyGrid: a circuit that has
// gone is not the map saying there is no such place.
func TestARequestThatCouldNotBeSentIsNotAnEmptyGrid(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.FailSends(errors.New("the circuit is gone"))

	_, err := w.FindRegions(context.Background(), "Pelmar Reach", time.Second)
	if err == nil || errors.Is(err, ErrTimeout) {
		t.Errorf("FindRegions = %v, want the send's own failure", err)
	}
}

// TestAMaturityRatingIsPrintedInWordsWhereThereAreWords, and as its
// number where there are not: 254 is a region that is down, and calling
// it "general" would be a comfortable lie.
func TestAMaturityRatingIsPrintedInWordsWhereThereAreWords(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		access uint8
		want   string
	}{
		{AccessGeneral, "general"},
		{AccessModerate, "moderate"},
		{AccessAdult, "adult"},
		{254, "access 254"},
	} {
		if got := AccessName(c.access); got != c.want {
			t.Errorf("AccessName(%d) = %q, want %q", c.access, got, c.want)
		}
	}
}
