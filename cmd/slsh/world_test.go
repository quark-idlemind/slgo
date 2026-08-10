package main

// What the world commands print.
//
// These are the commands with the least of their own logic and the most
// formatting, which is exactly the kind of code that is never wrong
// until it is: a column that has moved, a filter that matches nothing,
// an empty listing that says "nothing" where it used to say nothing at
// all.  All of it is reachable over a fake grid, so there is no reason
// for any of it to be checked by eye against a live avatar.

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// pcodeAvatar is what marks one of the objects in a region as somebody
// rather than something; the sl package has its own copy and does not
// export it.
const pcodeAvatar = 47

// TestWhereIsTheRegionAndThePosition, and says so in metres rather than
// in whatever the simulator counts in.
func TestWhereIsTheRegionAndThePosition(t *testing.T) {
	x := newTestShell(t)

	if got, want := x.do(t, "where"), "Test Region at 128, 128, 25\n"; got != want {
		t.Errorf("where printed %q, want %q", got, want)
	}
	if strings.Contains(x.out.String(), "acting as group") {
		t.Error("a session with no active group should not claim one")
	}

	// A group is worth saying, because building fails without one and
	// the land blames itself rather than the avatar.
	group := msg.MustParseUUID("93fc7e57-7e57-c0de-5bb7-3940f4894ffa")
	x.grid.presence.ActiveGroup = group
	if got := x.do(t, "where"); !strings.Contains(got, "acting as group "+group.String()) {
		t.Errorf("where should name the active group, got %q", got)
	}
}

// TestWhereSaysWhyItCannotAnswer rather than printing a position of
// zeroes, which reads as a real place.
func TestWhereSaysWhyItCannotAnswer(t *testing.T) {
	x := newTestShell(t)
	x.grid.presenceErr = errors.New("the circuit is down")

	if got := x.do(t, "where"); !strings.Contains(got, "the circuit is down") {
		t.Errorf("where should report the failure, got %q", got)
	}
}

// TestWhoNumbersThePeopleItFound, because the number is what the other
// commands take: "give 2 a lamp" means the second line of this.
func TestWhoNumbersThePeopleItFound(t *testing.T) {
	x := newTestShell(t)
	near := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000002")
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testSomebody, Local: 2}, PCode: pcodeAvatar,
			Position: msg.Vector3{X: 138, Y: 128, Z: 25}},
		{Object: sl.Object{ID: near, Local: 3}, PCode: pcodeAvatar,
			Position: msg.Vector3{X: 130, Y: 128, Z: 25}},
		// A prim, which is not somebody.
		{Object: sl.Object{ID: testLamp, Local: 4, Name: "a lamp"}, PCode: 9},
	}
	x.grid.AnswerNames(t, map[msg.UUID]string{
		testSomebody: "Far Resident", near: "Near Resident",
	})

	got := x.do(t, "who")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("who listed %d lines, want the two avatars:\n%s", len(lines), got)
	}
	// Nearest first, numbered from one.
	if !strings.HasPrefix(lines[0], " 1  Near Resident") {
		t.Errorf("the nearest should be first and numbered 1: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], " 2  Far Resident") {
		t.Errorf("the second line should be the far one: %q", lines[1])
	}

	// And the listing is what a number afterwards means.
	id, name, err := x.who(context.Background(), "1")
	if err != nil || id != near || name != "Near Resident" {
		t.Errorf(`who(1) = %v %q %v, want the first line`, id, name, err)
	}
}

// TestWhoSaysSoWhenTheRegionIsEmpty rather than printing nothing, which
// is indistinguishable from a command that failed quietly.
func TestWhoSaysSoWhenTheRegionIsEmpty(t *testing.T) {
	x := newTestShell(t)
	if got, want := x.do(t, "who"), "nobody else is in range\n"; got != want {
		t.Errorf("who printed %q, want %q", got, want)
	}

	x.grid.objectsErr = errors.New("nothing is holding this session")
	if got := x.do(t, "who"); !strings.Contains(got, "nothing is holding this session") {
		t.Errorf("who should report the failure, got %q", got)
	}
}

// TestLookIsWhatTheSimulatorSaidAboutItself.
func TestLookIsWhatTheSimulatorSaidAboutItself(t *testing.T) {
	x := newTestShell(t)
	x.grid.objects = []*sl.Seen{{Object: sl.Object{ID: testLamp, Local: 1}, PCode: 9}}

	got := x.do(t, "look")
	for _, want := range []string{
		"Test Region", "  access   13", "  water    20.0m",
		"Estate / Full Region", "1 described so far",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("look should mention %q:\n%s", want, got)
		}
	}

	x.grid.regionErr = errors.New("the handshake never arrived")
	if got := x.do(t, "look"); !strings.Contains(got, "the handshake never arrived") {
		t.Errorf("look should report the failure, got %q", got)
	}
}

// TestCapsAreSortedAndFiltered: the list is what the simulator granted,
// and a word narrows it, since a session has forty of them.
func TestCapsAreSortedAndFiltered(t *testing.T) {
	x := newTestShell(t)

	got := x.do(t, "caps")
	want := "LSLSyntax\nSimulatorFeatures\nViewerAsset\n"
	if got != want {
		t.Errorf("caps printed %q, want %q", got, want)
	}
	// The match ignores case, because nobody types a capability name
	// the way Linden Lab spells it.
	if got, want := x.do(t, "caps syntax"), "LSLSyntax\n"; got != want {
		t.Errorf("caps syntax printed %q, want %q", got, want)
	}
	if got := x.do(t, "caps nothing-of-the-sort"); got != "" {
		t.Errorf("a word that matches nothing should print nothing, got %q", got)
	}
}

// serveFeatures answers the SimulatorFeatures capability, which is one
// http GET and the only way to learn any of this.
func serveFeatures(t *testing.T, x *testShell, body string) {
	t.Helper()
	x.grid.ServeCap(t, "SimulatorFeatures", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, body)
	})
}

// TestFeaturesPrintsAMapAsItsKeys.
//
// A feature whose value is a map -- GridServices is one -- would print
// as a Go map literal, whose order changes between runs.  The keys,
// sorted, are both stable and the useful half.
func TestFeaturesPrintsAMapAsItsKeys(t *testing.T) {
	x := newTestShell(t)
	serveFeatures(t, x, `<?xml version="1.0" ?><llsd><map>
	  <key>MeshRezEnabled</key><boolean>1</boolean>
	  <key>MaxAgentAttachments</key><integer>38</integer>
	  <key>GridServices</key><map>
	    <key>search</key><string>https://example/search</string>
	    <key>map</key><string>https://example/map</string>
	  </map>
	</map></llsd>`)

	got := x.do(t, "features")
	if !strings.Contains(got, "GridServices                   [map search]") {
		t.Errorf("a map should print as its sorted keys:\n%s", got)
	}
	if !strings.Contains(got, "MaxAgentAttachments            38") {
		t.Errorf("features should print the plain values too:\n%s", got)
	}

	// A word narrows it, as with caps.
	got = x.do(t, "features mesh")
	if !strings.Contains(got, "MeshRezEnabled") || strings.Contains(got, "GridServices") {
		t.Errorf("features mesh should be the one line:\n%s", got)
	}
}

// TestFeaturesReportsACapabilityThatIsNotThere: an avatar attached to a
// simulator that never offered SimulatorFeatures has to be told that,
// not shown an empty list.
func TestFeaturesReportsACapabilityThatIsNotThere(t *testing.T) {
	x := newTestShell(t)
	if got := x.do(t, "features"); !strings.Contains(got, "SimulatorFeatures") {
		t.Errorf("features should say which capability is missing, got %q", got)
	}
}

// wearThings puts objects on the fake avatar, since an attachment is
// only an object that says which item it was worn from.
//
// It hangs them off the avatar, and describes the avatar if nothing has
// yet, because that is what says whose attachments these are: the store
// is the region's and may hold everyone's.
func wearThings(x *testShell, worn ...*sl.Seen) {
	const avatar = uint32(1)
	x.grid.mu.Lock()
	described := false
	for _, o := range x.grid.objects {
		described = described || o.ID == testMe
	}
	if !described {
		x.grid.objects = append(x.grid.objects,
			&sl.Seen{Object: sl.Object{ID: testMe, Local: avatar}, PCode: 47})
	}
	for _, o := range worn {
		if o.Parent == 0 {
			o.Parent = avatar
		}
	}
	x.grid.objects = append(x.grid.objects, worn...)
	x.grid.mu.Unlock()
}

// TestWornNamesAttachmentsFromInventory.
//
// The names come from inventory rather than from the objects, because a
// worn object will not answer a request for its properties -- and the
// inventory name is the one that does not change when it is taken off
// and put on again.
func TestWornNamesAttachmentsFromInventory(t *testing.T) {
	x := newTestShell(t)
	stranger := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-00000000000f")
	wearThings(x,
		&sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
			AttachItem: testLamp, AttachPoint: 1},
		&sl.Seen{Object: sl.Object{ID: testSomebody, Local: 11}, PCode: 9,
			AttachItem: stranger, AttachPoint: sl.HUDTop},
		// A second thing on the same point, which is allowed and which
		// the ordering has to settle by name rather than leave to the
		// order the region happened to describe them in.
		&sl.Seen{Object: sl.Object{ID: testSomebody, Local: 12}, PCode: 9,
			AttachItem: testNote, AttachPoint: 1},
	)

	got := x.do(t, "worn")
	if !strings.Contains(got, "chest              a lamp") {
		t.Errorf("worn should name the attachment from inventory:\n%s", got)
	}
	// An item the listing did not reach cannot be named, and the id is
	// then the only handle there is; a word claiming to know more would
	// be worse than the number.
	if !strings.Contains(got, stranger.String()) {
		t.Errorf("an unnamed attachment should print its item id:\n%s", got)
	}
	// Sorted by attachment point, so the HUDs group together.
	if strings.Index(got, "chest") > strings.Index(got, "HUD top") {
		t.Errorf("worn should be in attachment point order:\n%s", got)
	}

	// -l adds the ids, item first: the item is the one that survives
	// taking the thing off.
	got = x.do(t, "worn -l")
	if !strings.Contains(got, testLamp.String()+" "+testSomebody.String()) {
		t.Errorf("worn -l should print the item id and then the object id:\n%s", got)
	}
}

// TestWornTellsAnEmptyListFromAnEmptyMatch, since "nothing worn" when
// something is worn would be a lie that reads like an answer.
func TestWornTellsAnEmptyListFromAnEmptyMatch(t *testing.T) {
	x := newTestShell(t)
	if got, want := x.do(t, "worn"), "nothing worn\n"; got != want {
		t.Errorf("worn printed %q, want %q", got, want)
	}

	wearThings(x, &sl.Seen{Object: sl.Object{ID: testSomebody, Local: 10}, PCode: 9,
		AttachItem: testLamp, AttachPoint: 1})
	if got, want := x.do(t, "worn a lamp"), "chest              a lamp\n"; got != want {
		t.Errorf("worn with a word printed %q, want %q", got, want)
	}
	if got, want := x.do(t, "worn hat"), "nothing worn matched\n"; got != want {
		t.Errorf("worn with a word that matches nothing printed %q, want %q", got, want)
	}
}

// TestWornSaysWhyItCannotAnswer.
func TestWornSaysWhyItCannotAnswer(t *testing.T) {
	x := newTestShell(t)
	x.grid.objectsErr = errors.New("nobody is holding this session")
	if got := x.do(t, "worn"); !strings.Contains(got, "nobody is holding this session") {
		t.Errorf("worn should report the failure, got %q", got)
	}
}

// TestItemNamesGivesUpQuietly: naming things is a convenience on top of
// a listing, so an inventory that cannot be read should cost the
// listing its names and not the command.
func TestItemNamesGivesUpQuietly(t *testing.T) {
	x := newTestShell(t)
	x.grid.mu.Lock()
	x.grid.caps = map[string]string{} // no inventory capability at all
	x.grid.mu.Unlock()

	if got := x.itemNames(context.Background()); len(got) != 0 {
		t.Errorf("an unreadable inventory should name nothing, got %v", got)
	}
}

// TestObjectsSkipsAvatars: who is for people, and an avatar in the
// object listing would be counted twice by anything reading both.
func TestObjectsSkipsAvatars(t *testing.T) {
	x := newTestShell(t)
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "a lamp"}, PCode: 9,
			Position: msg.Vector3{X: 10, Y: 20, Z: 30}},
		{Object: sl.Object{ID: testSomebody, Local: 2, Name: "Somebody"}, PCode: pcodeAvatar},
	}

	got := x.do(t, "objects")
	if !strings.Contains(got, "a lamp") {
		t.Errorf("objects should list the prim:\n%s", got)
	}
	if strings.Contains(got, "Somebody") {
		t.Errorf("objects should leave the avatars to who:\n%s", got)
	}
	if !strings.Contains(got, "10, 20, 30") {
		t.Errorf("objects should print where each one is:\n%s", got)
	}

	if got, want := x.do(t, "objects nothing-like-it"), "nothing matched\n"; got != want {
		t.Errorf("objects with a word that matches nothing printed %q, want %q", got, want)
	}
	if got := x.do(t, "objects lamp"); !strings.Contains(got, "a lamp") {
		t.Errorf("objects should match part of a name:\n%s", got)
	}

	x.grid.objectsErr = errors.New("nobody is holding this session")
	if got := x.do(t, "objects"); !strings.Contains(got, "nobody is holding this session") {
		t.Errorf("objects should report the failure, got %q", got)
	}
}

// TestWorldCommandsAllAnswerForHelp.
//
// --help is the one flag every command has, and it has to stop the
// command rather than run it with no arguments: "worn --help" that went
// on to list the attachments would be a command ignoring what it was
// asked.
func TestWorldCommandsAllAnswerForHelp(t *testing.T) {
	x := newTestShell(t)
	x.grid.objectsErr = errors.New("this must not be reached")
	x.grid.presenceErr = x.grid.objectsErr
	x.grid.regionErr = x.grid.objectsErr

	for _, name := range []string{"where", "who", "look", "caps", "features", "worn", "objects"} {
		got := x.do(t, name+" --help")
		if !strings.Contains(got, name) {
			t.Errorf("%s --help should print its own usage, got %q", name, got)
		}
		if strings.Contains(got, "this must not be reached") {
			t.Errorf("%s --help ran the command anyway:\n%s", name, got)
		}
	}
}

// TestObjectsListsTheObjectsAndThenTheirPrims.
//
// An object is a linkset, and what a person means by one is its root.
// Listing every prim turns a hundred things into a thousand lines, most
// of them called "Object" and placed at an offset from something the
// listing does not name -- so the roots are the listing, and -c opens
// them up.
func TestObjectsListsTheObjectsAndThenTheirPrims(t *testing.T) {
	x := newTestShell(t)
	child := msg.MustParseUUID("f3a97e57-7e57-c0de-02ff-92b46275fd1b")
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "TrioBox"}, PCode: 9,
			Position: msg.Vector3{X: 30, Y: 76, Z: 1000}},
		{Object: sl.Object{ID: child, Local: 2, Name: "TrioChild"}, PCode: 9,
			Parent: 1, Position: msg.Vector3{X: 3}},
	}

	got := x.do(t, "objects")
	if !strings.Contains(got, "TrioBox") {
		t.Errorf("the object itself should be listed:\n%s", got)
	}
	if strings.Contains(got, "TrioChild") {
		t.Errorf("a prim inside an object should not be a line of its own:\n%s", got)
	}
	// What was left out is said, rather than a listing that quietly
	// claims the region holds less than it does.
	if !strings.Contains(got, "1 more prim, not shown") {
		t.Errorf("the prims left out should be counted:\n%s", got)
	}

	got = x.do(t, "objects -c")
	if !strings.Contains(got, "  "+child.String()) {
		t.Errorf("-c should list the prims, indented under the object:\n%s", got)
	}
	if !strings.Contains(got, "offset 3, 0, 0") {
		t.Errorf("a prim's position is an offset from its root:\n%s", got)
	}
	if strings.Contains(got, "not shown") {
		t.Errorf("nothing was left out, so nothing should be counted:\n%s", got)
	}
}

// TestObjectsSearchesTheNamesInsideToo: browsing shows the objects,
// searching shows what was searched for.  The name a person remembers
// is often on a prim inside, and being told nothing is here -- when it
// is standing in front of them -- would be a lie by omission.
func TestObjectsSearchesTheNamesInsideToo(t *testing.T) {
	x := newTestShell(t)
	child := msg.MustParseUUID("f3a97e57-7e57-c0de-02ff-92b46275fd1b")
	other := msg.MustParseUUID("95647e57-7e57-c0de-4d18-16ea29724ad6")
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "TrioBox"}, PCode: 9},
		{Object: sl.Object{ID: child, Local: 2, Name: "HearthEmbers"}, PCode: 9, Parent: 1},
		{Object: sl.Object{ID: other, Local: 3, Name: "Flames"}, PCode: 9, Parent: 1},
	}

	got := x.do(t, "objects HearthEmbers")
	if !strings.Contains(got, "TrioBox") {
		t.Errorf("the object holding the match should be named:\n%s", got)
	}
	if !strings.Contains(got, "HearthEmbers") {
		t.Errorf("the prim that was searched for should be shown without -c:\n%s", got)
	}
	if strings.Contains(got, "Flames") {
		t.Errorf("a prim nobody asked about should stay inside:\n%s", got)
	}
}

// TestObjectsShowsAPrimWhoseRootIsNotThere: a child whose root has not
// been described has nothing to sit under, and the region really does
// hold it.
func TestObjectsShowsAPrimWhoseRootIsNotThere(t *testing.T) {
	x := newTestShell(t)
	orphan := msg.MustParseUUID("d7987e57-7e57-c0de-3e1f-bedbf023d953")
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: orphan, Local: 9, Name: "HearthEmbers"}, PCode: 9,
			Parent: 4242, Position: msg.Vector3{X: -29.2}},
	}

	if got := x.do(t, "objects"); !strings.Contains(got, "1 more prim, not shown") {
		t.Errorf("an orphan prim should be counted, not dropped:\n%s", got)
	}
	got := x.do(t, "objects -c")
	if !strings.Contains(got, "HearthEmbers") {
		t.Errorf("-c should list it:\n%s", got)
	}
	if !strings.Contains(got, "from a root nothing has described") {
		t.Errorf("and should say why it is not under anything:\n%s", got)
	}
}

// TestObjectsNamesTheOwner: "whose is that" is the question a listing
// of a region full of other people's things is usually being asked.
func TestObjectsNamesTheOwner(t *testing.T) {
	x := newTestShell(t)
	owner := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-00000000000a")
	stranger := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-00000000000b")
	child := msg.MustParseUUID("f3a97e57-7e57-c0de-02ff-92b46275fd1b")
	unowned := msg.MustParseUUID("95647e57-7e57-c0de-4d18-16ea29724ad6")
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testSomebody, Local: 1, Name: "Wearer"}, PCode: pcodeAvatar, Owner: owner},
		{Object: sl.Object{ID: testLamp, Local: 2, Name: "a lamp"}, PCode: 9, Owner: owner},
		{Object: sl.Object{ID: child, Local: 3, Name: "the shade"}, PCode: 9, Parent: 2, Owner: owner},
		// Nobody has said who owns this one, which is not the same as
		// saying nobody owns it.
		{Object: sl.Object{ID: unowned, Local: 4, Name: "a mystery"}, PCode: 9},
	}
	x.grid.AnswerNames(t, map[msg.UUID]string{
		owner: "Kerra Yule", stranger: "Somebody Else", testSomebody: "Wearer Resident",
	})

	got := x.do(t, "objects -c")
	if !strings.Contains(got, "a lamp                       Kerra Yule") {
		t.Errorf("the owner should be named beside the object:\n%s", got)
	}
	// A prim inside says so too, so a line copied out on its own still
	// says whose it is.
	if !strings.Contains(got, "the shade                    Kerra Yule") {
		t.Errorf("a prim inside should name its owner too:\n%s", got)
	}
	// An owner nobody has answered for is left blank rather than shown
	// as an id nobody can read.
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "a mystery") && strings.Contains(line, "00000000") {
			t.Errorf("an unknown owner should be blank, not an id: %q", line)
		}
	}
}

// TestObjectsSaysWhoIsWearingSomethingOnlyWhenItIsNotTheirs: an avatar
// wears its own things, so naming the wearer beside the owner is the
// same word twice.  The case worth a word is the other one.
func TestObjectsSaysWhoIsWearingSomethingOnlyWhenItIsNotTheirs(t *testing.T) {
	x := newTestShell(t)
	wearer := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-00000000000c")
	other := msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-00000000000d")
	borrowed := msg.MustParseUUID("95647e57-7e57-c0de-4d18-16ea29724ad6")
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: wearer, Local: 1}, PCode: pcodeAvatar},
		{Object: sl.Object{ID: testLamp, Local: 2, Name: "own hat"}, PCode: 9,
			Parent: 1, AttachPoint: 4, Owner: wearer},
		{Object: sl.Object{ID: borrowed, Local: 3, Name: "borrowed hat"}, PCode: 9,
			Parent: 1, AttachPoint: 5, Owner: other},
	}
	x.grid.AnswerNames(t, map[msg.UUID]string{
		wearer: "Wearer Resident", other: "Somebody Else",
	})

	got := x.do(t, "objects")
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "own hat") && strings.Contains(line, ", by ") {
			t.Errorf("an avatar wearing its own thing needs no second name: %q", line)
		}
		if strings.Contains(line, "borrowed hat") && !strings.Contains(line, "by Wearer Resident") {
			t.Errorf("somebody wearing another's thing is worth saying: %q", line)
		}
	}
}
