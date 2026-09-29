package sl

import (
	"context"
	"errors"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	theDance  = msg.MustParseUUID("788a7e57-7e57-c0de-c175-010fd6ff85c7")
	theDance2 = msg.MustParseUUID("78c37e57-7e57-c0de-6790-5f597284ed8f")
	theAsset  = msg.MustParseUUID("be387e57-7e57-c0de-fe86-db8f00c2cb34")
	theAsset2 = msg.MustParseUUID("be557e57-7e57-c0de-f579-b2e90f754ee0")
)

// sentAnimation is the one AgentAnimation a call put on the wire.
func sentAnimation(t *testing.T, f *fakeBackend) *msg.AgentAnimation {
	t.Helper()
	sent := f.Sent()
	if len(sent) != 1 {
		t.Fatalf("%d messages went out, want the one request: %v", len(sent), sent)
	}
	m, ok := sent[0].Msg.(*msg.AgentAnimation)
	if !ok {
		t.Fatalf("the request went out as %T", sent[0].Msg)
	}
	if !sent[0].Reliable {
		t.Errorf("the request went out unreliably; the viewer sends it reliably")
	}
	return m
}

// TestStartingAnAnimationSendsItsIdWithTheStartFlagAndTheAgentBlock.
func TestStartingAnAnimationSendsItsIdWithTheStartFlagAndTheAgentBlock(t *testing.T) {
	w, f := newFakeSession(t)
	if err := w.StartAnimation(context.Background(), theAsset); err != nil {
		t.Fatal(err)
	}
	m := sentAnimation(t, f)
	if m.AgentData.AgentID != testAgentID || m.AgentData.SessionID != testSessionID {
		t.Errorf("the agent block is %v, want this avatar and this session", m.AgentData)
	}
	if len(m.AnimationList) != 1 || m.AnimationList[0].AnimID != theAsset || !m.AnimationList[0].StartAnim {
		t.Errorf("the animation list is %+v, want %s started", m.AnimationList, theAsset)
	}
	if len(m.PhysicalAvatarEventList) != 1 || len(m.PhysicalAvatarEventList[0].TypeData) != 0 {
		t.Errorf("the physical avatar event list is %+v, want the viewer's one empty block",
			m.PhysicalAvatarEventList)
	}
}

// TestStoppingAnAnimationSendsTheSameMessageWithTheFlagClear.
func TestStoppingAnAnimationSendsTheSameMessageWithTheFlagClear(t *testing.T) {
	w, f := newFakeSession(t)
	if err := w.StopAnimation(context.Background(), theAsset); err != nil {
		t.Fatal(err)
	}
	m := sentAnimation(t, f)
	if len(m.AnimationList) != 1 || m.AnimationList[0].AnimID != theAsset || m.AnimationList[0].StartAnim {
		t.Errorf("the animation list is %+v, want %s stopped", m.AnimationList, theAsset)
	}
}

// TestNoAnimationIsAskedForByTheNullId: the viewer skips one, and it
// would ask the simulator for nothing.
func TestNoAnimationIsAskedForByTheNullId(t *testing.T) {
	w, f := newFakeSession(t)
	if err := w.StartAnimation(context.Background(), msg.UUID{}); err == nil {
		t.Error("a null id was accepted")
	}
	if sent := f.Sent(); len(sent) != 0 {
		t.Errorf("%d messages went out for a null id", len(sent))
	}
}

// TestABuiltInIsFoundByItsNameAsLindenSpellsIt.
func TestABuiltInIsFoundByItsNameAsLindenSpellsIt(t *testing.T) {
	a, err := BuiltinAnimation("sit_ground")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != agent.AnimSitGround {
		t.Errorf("sit_ground is %s, want %s", a.ID, agent.AnimSitGround)
	}

	// Exactly, in the case it has: a name in another case is refused
	// and the near miss is offered.
	_, err = BuiltinAnimation("Sit_Ground")
	var ne *NameError
	if !errors.As(err, &ne) || len(ne.Near) != 1 || ne.Near[0] != "sit_ground" {
		t.Errorf("Sit_Ground gave %v, want a NameError offering sit_ground", err)
	}

	// And a name nothing has.
	_, err = BuiltinAnimation("no_such_animation")
	if !errors.As(err, &ne) || len(ne.IDs) != 0 {
		t.Errorf("an unknown name gave %v, want a NameError with no ids", err)
	}
}

// TestTheBuiltInTableIsSortedUniqueAndAgreesWithThePostureIds: the
// names are matched exactly and each has to mean one animation.
func TestTheBuiltInTableIsSortedUniqueAndAgreesWithThePostureIds(t *testing.T) {
	names, ids := map[string]bool{}, map[msg.UUID]bool{}
	prev := ""
	for _, a := range agent.BuiltinAnimations {
		if a.Name <= prev {
			t.Errorf("%q is out of order or repeated after %q", a.Name, prev)
		}
		prev = a.Name
		if names[a.Name] || ids[a.ID] {
			t.Errorf("%s (%s) is in the table twice", a.Name, a.ID)
		}
		names[a.Name], ids[a.ID] = true, true
	}
	for name, want := range map[string]msg.UUID{
		"sit": agent.AnimSit, "stand": agent.AnimStand, "sit_to_stand": agent.AnimSitToStand,
		"sit_female": agent.AnimSitFemale, "sit_generic": agent.AnimSitGeneric,
		"sit_ground": agent.AnimSitGround, "sit_ground_constrained": agent.AnimSitGroundConstrained,
	} {
		a, err := BuiltinAnimation(name)
		if err != nil || a.ID != want {
			t.Errorf("%s is %v (%v), want %s", name, a.ID, err, want)
		}
	}
}

// animationTree serves a root holding a folder of animations, one of
// which is named twice, and a notecard.
func animationTree(t *testing.T, f *fakeBackend) {
	t.Helper()
	f.ServeInventoryTree(t, func(id msg.UUID) ([]*Folder, []*Item) {
		switch id {
		case testInvRoot:
			return []*Folder{{ID: aFolder, ParentID: testInvRoot, Name: "Animations", Type: -1}}, nil
		case aFolder:
			return nil, []*Item{
				{ID: theDance, ParentID: aFolder, Name: "shuffle", Type: int(AssetAnimation), AssetID: theAsset},
				{ID: theDance2, ParentID: aFolder, Name: "twice", Type: int(AssetAnimation), AssetID: theAsset},
				{ID: theChild, ParentID: aFolder, Name: "twice", Type: int(AssetAnimation), AssetID: theAsset2},
				{ID: theOther, ParentID: aFolder, Name: "notes", Type: int(AssetNotecard), AssetID: theAsset2},
			}
		}
		return nil, nil
	})
}

// TestAnInventoryAnimationIsPlayedByItsAssetAndNotItsItemId.
func TestAnInventoryAnimationIsPlayedByItsAssetAndNotItsItemId(t *testing.T) {
	w, f := newFakeSession(t)
	animationTree(t, f)

	e, err := w.InventoryAnimation(context.Background(), "/Animations/shuffle")
	if err != nil {
		t.Fatal(err)
	}
	id, err := AnimationAsset(e)
	if err != nil || id != theAsset || id == e.ID {
		t.Errorf("the id to play is %s (%v), want the asset %s and not the item %s", id, err, theAsset, e.ID)
	}
}

// TestAnInventoryNameSeveralItemsShareIsRefusedWithTheirIds.
func TestAnInventoryNameSeveralItemsShareIsRefusedWithTheirIds(t *testing.T) {
	w, f := newFakeSession(t)
	animationTree(t, f)

	_, err := w.InventoryAnimation(context.Background(), "/Animations/twice")
	var ne *NameError
	if !errors.As(err, &ne) || len(ne.IDs) != 2 {
		t.Fatalf("a name two items share gave %v, want a NameError listing both", err)
	}
}

// TestAnInventoryNameNothingHasOrThatIsNotAnAnimationIsRefused.
func TestAnInventoryNameNothingHasOrThatIsNotAnAnimationIsRefused(t *testing.T) {
	w, f := newFakeSession(t)
	animationTree(t, f)

	_, err := w.InventoryAnimation(context.Background(), "/Animations/Shuffle")
	var ne *NameError
	if !errors.As(err, &ne) || len(ne.Near) != 1 || ne.Near[0] != "shuffle" {
		t.Errorf("a name in the wrong case gave %v, want the near miss offered", err)
	}
	if _, err := w.InventoryAnimation(context.Background(), "/Animations/absent"); !errors.As(err, &ne) {
		t.Errorf("an unknown name gave %v, want a NameError", err)
	}
	if _, err := w.InventoryAnimation(context.Background(), "/Animations/notes"); err == nil {
		t.Error("a notecard was accepted as an animation")
	}
}
