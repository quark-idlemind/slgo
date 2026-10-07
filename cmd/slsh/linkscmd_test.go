package main

// links: the object's own numbering beside the store's, and what is told
// to the store.  The far end is a root that answers its selection with
// the masks a test gives, takes the drop of the script and says the lines
// a test gives, and holds nothing afterwards.
// Why: doc/scripts.md#the-links-of-an-object-from-its-own-script

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids of an object of three prims.
var (
	aCrate  = msg.MustParseUUID("2edd7e57-7e57-c0de-8032-83a67ef75c9b")
	aLid    = msg.MustParseUUID("b5e97e57-7e57-c0de-3e9c-d0ea654a06fd")
	aHinge  = msg.MustParseUUID("cb047e57-7e57-c0de-2592-a39bfde0b20a")
	aLinkIt = msg.MustParseUUID("e5ac7e57-7e57-c0de-cb5c-2be0cdf8df50")
)

func (f *fakeGrid) ConfirmLinkOrder(ctx context.Context, root msg.UUID, keys []msg.UUID) (*sl.LinkConfirmation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirms = append(f.confirms, append([]msg.UUID(nil), keys...))
	if f.confirmErr != nil {
		return nil, f.confirmErr
	}
	if f.confirmWith != nil {
		return f.confirmWith, nil
	}
	return &sl.LinkConfirmation{}, nil
}

// AnswerLinkMap makes the root at local 31 answer a selection with these
// permissions, take the drop of the link map script and say its lines,
// and hold nothing when its contents are read.  The script is in
// inventory at the version the command expects.
func (f *fakeGrid) AnswerLinkMap(t *testing.T, ownerMask uint32, lines ...string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, d := range f.inv.Dirs {
		if d.ID == testScripts {
			d.Items = append(d.Items, &invItem{ID: aLinkIt, Name: "slgo linkmap", Type: int(sl.AssetLSLText),
				InvType: int(sl.AssetLSLText), Desc: "slgo linkmap v2", Created: 1754000300})
		}
	}
	f.onSend = func(m msg.Message) {
		switch m.(type) {
		case *msg.ObjectSelect:
			f.Relay(t, &msg.ObjectProperties{ObjectData: []msg.ObjectProperties_ObjectData{{
				ObjectID: aCrate, OwnerID: testMe, CreatorID: testMe, OwnerMask: ownerMask,
				Name: append([]byte("crate"), 0),
			}}})
		case *msg.RezScript:
			for _, l := range lines {
				c := &msg.ChatFromSimulator{}
				c.ChatData.FromName = append([]byte("crate"), 0)
				c.ChatData.SourceID, c.ChatData.OwnerID = aCrate, testMe
				c.ChatData.SourceType, c.ChatData.ChatType = sl.SourceObject, sl.ChatOwner
				c.ChatData.Audible = 1
				c.ChatData.Message = append([]byte(l), 0)
				f.Relay(t, c)
			}
		case *msg.RequestTaskInventory:
			f.Relay(t, replyTaskInventory(aCrate, ""))
		}
	}
}

// aCrateOfThree is the set a test starts from: the root and two prims,
// numbered 1, 2 and 3 by the store, which is sure of it.
func aCrateOfThree(t *testing.T, known bool) *testShell {
	t.Helper()
	x := newTestShell(t)
	root := aPrim(aCrate, 31, "crate", 0)
	lid := aPrim(aLid, 32, "lid", 31)
	hinge := aPrim(aHinge, 33, "left hinge", 31)
	for i, p := range []*sl.Seen{root, lid, hinge} {
		p.LinkNumber, p.LinkKnown = i+1, known
	}
	standing(x, root, lid, hinge)
	return x
}

func linesOf(order ...msg.UUID) []string {
	names := map[msg.UUID]string{aCrate: "crate", aLid: "lid", aHinge: "left hinge"}
	var out []string
	for i, k := range order {
		out = append(out, fmt.Sprintf("LINKMAP %d %s %s", i+1, k, names[k]))
	}
	return append(out, fmt.Sprintf("LINKMAP done %d 0", len(order)))
}

func TestLinksSetsTheObjectsOwnNumberingBesideTheStoresAndSaysTheyAgree(t *testing.T) {
	x := aCrateOfThree(t, true)
	x.grid.AnswerLinkMap(t, sl.PermAll, linesOf(aCrate, aLid, aHinge)...)
	got := x.do(t, "links crate")
	for _, want := range []string{
		"crate: 3 links, as the object's own script numbered them",
		aLid.String() + "  lid", "left hinge",
		"verdict: the store's order agrees with the object's own, and the store had it as known",
		"the store now has this order confirmed by the object's own script",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "differs") {
		t.Errorf("a column says they differ:\n%s", got)
	}
	if len(x.grid.confirms) != 1 || len(x.grid.confirms[0]) != 3 || x.grid.confirms[0][1] != aLid {
		t.Errorf("the store was told %v", x.grid.confirms)
	}
}

func TestLinksSaysWhereTheStoreDiffersAndTellsItTheObjectsOrder(t *testing.T) {
	x := aCrateOfThree(t, true)
	x.grid.confirmWith = &sl.LinkConfirmation{Corrected: true, Moved: []int{2, 3}}
	x.grid.AnswerLinkMap(t, sl.PermAll, linesOf(aCrate, aHinge, aLid)...)
	got := x.do(t, "links crate")
	for _, want := range []string{
		"2  differs", "3  differs",
		"verdict: the store's order differs from the object's own at links 2 and 3; the packets said otherwise",
		"the store now takes the object's order, which it had other prims at links 2 and 3",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if k := x.grid.confirms; len(k) != 1 || k[0][1] != aHinge || k[0][2] != aLid {
		t.Errorf("the store was told %v", k)
	}
}

func TestLinksSaysWhenTheStoreAgreedWithoutKnowing(t *testing.T) {
	x := aCrateOfThree(t, false)
	x.grid.AnswerLinkMap(t, sl.PermAll, linesOf(aCrate, aLid, aHinge)...)
	got := x.do(t, "links crate")
	if !strings.Contains(got, "agrees with the object's own, though the store had it as not known") {
		t.Errorf("printed\n%s", got)
	}
}

func TestLinksRefusesAnObjectItMayNotModifyBeforeDroppingAnything(t *testing.T) {
	x := aCrateOfThree(t, true)
	x.grid.AnswerLinkMap(t, sl.PermAll&^sl.PermModify, linesOf(aCrate, aLid, aHinge)...)
	got := x.do(t, "links crate")
	for _, want := range []string{"crate cannot be asked for its own numbering", "may not modify", "best reading from the packets"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if n := len(sentOfShell[*msg.RezScript](x)); n != 0 {
		t.Errorf("%d scripts were dropped", n)
	}
	if len(x.grid.confirms) != 0 {
		t.Error("the store was told something")
	}
}

func TestLinksRefusesAPrimThatIsPartOfAnotherObject(t *testing.T) {
	x := aCrateOfThree(t, true)
	x.grid.AnswerLinkMap(t, sl.PermAll, linesOf(aCrate, aLid, aHinge)...)
	got := x.do(t, "links lid")
	if !strings.Contains(got, "is a prim of another object, linked under local id 31; name the root") {
		t.Errorf("printed\n%s", got)
	}
	if n := len(sentOfShell[*msg.RezScript](x)); n != 0 {
		t.Errorf("%d scripts were dropped", n)
	}
}

func TestLinksOfALonePrimIsLinkZero(t *testing.T) {
	x := newTestShell(t)
	root := aPrim(aCrate, 31, "crate", 0)
	standing(x, root)
	x.grid.AnswerLinkMap(t, sl.PermAll, fmt.Sprintf("LINKMAP 0 %s crate", aCrate), "LINKMAP done 1 0")
	got := x.do(t, "links crate")
	if !strings.Contains(got, "crate: 1 link, as") || !strings.Contains(got, "agrees with the object's own") {
		t.Errorf("printed\n%s", got)
	}
}

func TestLinksSaysWhenThePrimsAreNotAllInTheStore(t *testing.T) {
	x := aCrateOfThree(t, true)
	standing(x, x.grid.objects[0], x.grid.objects[1])
	x.grid.AnswerLinkMap(t, sl.PermAll, linesOf(aCrate, aLid, aHinge)...)
	got := x.do(t, "links crate")
	for _, want := range []string{"not seen by the store", "the store has not seen 1 link of this object's prims yet"} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in\n%s", want, got)
		}
	}
	if len(x.grid.confirms) != 0 {
		t.Error("the store was told a set it does not have")
	}
}

func TestLinksSaysWhenTheStoreCannotBeTold(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"an older daemon": {sl.ErrNotSupported, "the store was not told: this daemon is older than the call"},
		"a refusal":       {fmt.Errorf("%w: a prim is missing", sl.ErrLinkSetDiffers), "the store was not told: "},
	} {
		x := aCrateOfThree(t, true)
		x.grid.confirmErr = tc.err
		x.grid.AnswerLinkMap(t, sl.PermAll, linesOf(aCrate, aLid, aHinge)...)
		if got := x.do(t, "links crate"); !strings.Contains(got, tc.want) {
			t.Errorf("%s: printed\n%s", name, got)
		}
	}
}

func TestLinksWantsOneObject(t *testing.T) {
	x := newTestShell(t)
	for _, line := range []string{"links", "links a b"} {
		if got := x.do(t, line); !strings.Contains(got, "usage: links [-w SECONDS] OBJECT") {
			t.Errorf("%q printed %q", line, got)
		}
	}
}
