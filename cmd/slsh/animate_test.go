package main

// animate, over a grid that is not there: what goes on the wire for a
// start and a stop, and what a word is taken to mean.

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var (
	shuffleItem  = msg.MustParseUUID("788a7e57-7e57-c0de-c175-010fd6ff85c7")
	shuffleAsset = msg.MustParseUUID("be387e57-7e57-c0de-fe86-db8f00c2cb34")
	twiceItemA   = msg.MustParseUUID("78c37e57-7e57-c0de-6790-5f597284ed8f")
	twiceItemB   = msg.MustParseUUID("78e47e57-7e57-c0de-2692-4190495563bb")
	twiceAsset   = msg.MustParseUUID("be557e57-7e57-c0de-f579-b2e90f754ee0")
	ownHello     = msg.MustParseUUID("bf687e57-7e57-c0de-9d4b-11a5c0a1b5a5")
	linkToDance  = msg.MustParseUUID("7b0b7e57-7e57-c0de-a88a-2b34adf65cdb")
	clapItemA    = msg.MustParseUUID("7cbb7e57-7e57-c0de-44c9-20c037615525")
	clapItemB    = msg.MustParseUUID("7e0b7e57-7e57-c0de-0556-7cbff436df31")
)

// animatingShell holds animations in the inventory root: one plain, two
// of one name, one that has a built-in's name, a notecard with another
// built-in's name, and a link to the plain one.
func animatingShell(t *testing.T) *testShell {
	t.Helper()
	x := newTestShell(t)
	anim := int(sl.AssetAnimation)
	x.grid.inv.Items = append(x.grid.inv.Items,
		&invItem{ID: shuffleItem, Name: "shuffle", Type: anim, Asset: shuffleAsset},
		&invItem{ID: twiceItemA, Name: "twice", Type: anim, Asset: twiceAsset},
		&invItem{ID: twiceItemB, Name: "twice", Type: anim, Asset: twiceAsset},
		&invItem{ID: testLamp, Name: "hello", Type: anim, Asset: ownHello},
		&invItem{ID: testProbe, Name: "bow", Type: int(sl.AssetNotecard)},
		&invItem{ID: clapItemA, Name: "clap", Type: anim, Asset: twiceAsset},
		&invItem{ID: clapItemB, Name: "clap", Type: anim, Asset: shuffleAsset},
		&invItem{ID: linkToDance, Name: "dance link", Type: anim, IsLink: true, Asset: shuffleItem},
	)
	return x
}

// theRequest is the one AgentAnimation the fake was sent.
func theRequest(t *testing.T, x *testShell) *msg.AgentAnimation {
	t.Helper()
	sent := x.grid.Sent()
	if len(sent) != 1 {
		t.Fatalf("%d messages went out, want the one request: %v", len(sent), sent)
	}
	m, ok := sent[0].(*msg.AgentAnimation)
	if !ok {
		t.Fatalf("the request went out as %T", sent[0])
	}
	return m
}

func wantPlayed(t *testing.T, m *msg.AgentAnimation, id msg.UUID, start bool) {
	t.Helper()
	if len(m.AnimationList) != 1 || m.AnimationList[0].AnimID != id || m.AnimationList[0].StartAnim != start {
		t.Errorf("the request is %+v, want %s with StartAnim %v", m.AnimationList, id, start)
	}
}

// TestAnimateStartsABuiltInByItsName.
func TestAnimateStartsABuiltInByItsName(t *testing.T) {
	x := animatingShell(t)
	got := x.do(t, "animate sit_ground")
	if want := "asked for sit_ground (" + agent.AnimSitGround.String() + ")\n"; got != want {
		t.Errorf("animate printed %q, want %q", got, want)
	}
	wantPlayed(t, theRequest(t, x), agent.AnimSitGround, true)
}

// TestAnimateStopStopsAnInventoryAnimationByItsAssetAndNotItsItemId.
func TestAnimateStopStopsAnInventoryAnimationByItsAssetAndNotItsItemId(t *testing.T) {
	x := animatingShell(t)
	got := x.do(t, "animate --stop shuffle")
	if want := "asked shuffle (" + shuffleAsset.String() + ") to stop\n"; got != want {
		t.Errorf("animate --stop printed %q, want %q", got, want)
	}
	wantPlayed(t, theRequest(t, x), shuffleAsset, false)
}

// TestAnimateFollowsALinkToTheAnimationItPointsAt.
func TestAnimateFollowsALinkToTheAnimationItPointsAt(t *testing.T) {
	x := animatingShell(t)
	x.do(t, `animate "dance link"`)
	wantPlayed(t, theRequest(t, x), shuffleAsset, true)
}

// TestAnimateTakesAUuidAsTheAssetId.
func TestAnimateTakesAUuidAsTheAssetId(t *testing.T) {
	x := animatingShell(t)
	x.do(t, "animate "+twiceAsset.String())
	wantPlayed(t, theRequest(t, x), twiceAsset, true)
}

// TestAnimateListNamesTheBuiltInsAndSendsNothing.
func TestAnimateListNamesTheBuiltInsAndSendsNothing(t *testing.T) {
	x := animatingShell(t)
	got := strings.Split(strings.TrimSpace(x.do(t, "animate --list")), "\n")
	if len(got) != len(agent.BuiltinAnimations) || got[0] != agent.BuiltinAnimations[0].Name {
		t.Errorf("--list printed %d lines starting %q, want %d", len(got), got[0], len(agent.BuiltinAnimations))
	}
	found := false
	for _, n := range got {
		found = found || n == "hello"
	}
	if !found {
		t.Errorf("--list has no hello: %v", got)
	}
	if sent := x.grid.Sent(); len(sent) != 0 {
		t.Errorf("--list put %d messages on the wire", len(sent))
	}
}

// TestAnimateRefusesANameSeveralAnimationsShare: refused with both ids
// and nothing sent, as every other name is.
func TestAnimateRefusesANameSeveralAnimationsShare(t *testing.T) {
	x := animatingShell(t)
	got := x.do(t, "animate twice")
	for _, want := range []string{"2 things", `"twice"`, twiceItemA.String(), twiceItemB.String()} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, got)
		}
	}
	if sent := x.grid.Sent(); len(sent) != 0 {
		t.Errorf("%d messages went out for a name that was never resolved", len(sent))
	}
}

// TestAnimateRefusesANameSeveralAnimationsShareEvenWhenABuiltInHasIt:
// the built-in does not settle which of them was meant.
func TestAnimateRefusesANameSeveralAnimationsShareEvenWhenABuiltInHasIt(t *testing.T) {
	x := animatingShell(t)
	got := x.do(t, "animate clap")
	for _, want := range []string{clapItemA.String(), clapItemB.String()} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal should mention %q:\n%s", want, got)
		}
	}
	if sent := x.grid.Sent(); len(sent) != 0 {
		t.Errorf("%d messages went out for a name that was never resolved", len(sent))
	}
}

// TestAnimateRefusesAnUnknownNameSayingItLookedInBothPlaces.
func TestAnimateRefusesAnUnknownNameSayingItLookedInBothPlaces(t *testing.T) {
	x := animatingShell(t)
	got := x.do(t, "animate no_such_animation")
	if !strings.Contains(got, `nothing called "no_such_animation"`) ||
		!strings.Contains(got, `no built-in animation "no_such_animation"`) {
		t.Errorf("the refusal should say neither place has it:\n%s", got)
	}
	if sent := x.grid.Sent(); len(sent) != 0 {
		t.Errorf("%d messages went out for an unknown name", len(sent))
	}
}

// TestAnAnimationInInventoryWinsOverABuiltInOfTheSameNameAndSaysSo,
// and --builtin reaches the built-in.
func TestAnAnimationInInventoryWinsOverABuiltInOfTheSameNameAndSaysSo(t *testing.T) {
	x := animatingShell(t)
	got := x.do(t, "animate hello")
	if !strings.Contains(got, "a built-in has that name too") || !strings.Contains(got, "animate --builtin hello") {
		t.Errorf("animate should say the name is a built-in's too:\n%s", got)
	}
	wantPlayed(t, theRequest(t, x), ownHello, true)

	y := animatingShell(t)
	y.do(t, "animate --builtin hello")
	hello, err := sl.BuiltinAnimation("hello")
	if err != nil {
		t.Fatal(err)
	}
	wantPlayed(t, theRequest(t, y), hello.ID, true)
}

// TestAnItemThatIsNotAnAnimationDoesNotHideABuiltIn: a notecard called
// bow is not something to play, and bow is a built-in.
func TestAnItemThatIsNotAnAnimationDoesNotHideABuiltIn(t *testing.T) {
	x := animatingShell(t)
	x.do(t, "animate bow")
	bow, err := sl.BuiltinAnimation("bow")
	if err != nil {
		t.Fatal(err)
	}
	wantPlayed(t, theRequest(t, x), bow.ID, true)
}

// TestAnimateRefusesANotecardAndAMissingName.
func TestAnimateRefusesANotecardAndAMissingName(t *testing.T) {
	x := animatingShell(t)
	if got := x.do(t, "animate readme"); !strings.Contains(got, "not an animation") {
		t.Errorf("a notecard gave %q, want it refused as not an animation", got)
	}
	if got := x.do(t, "animate"); !strings.Contains(got, "usage: animate") {
		t.Errorf("no name gave %q, want the usage line", got)
	}
	if sent := x.grid.Sent(); len(sent) != 0 {
		t.Errorf("%d messages went out", len(sent))
	}
}
