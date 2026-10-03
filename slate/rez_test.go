package slate

import (
	"fmt"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

func TestARezClaimIsMatchedAndItsNameIsUsableFromTheNextStep(t *testing.T) {
	f := newGrid(t)
	root := balloonAt(idRootA, 201, 130)
	root.TextureEntry = teOf(idTexA)
	f.whenSaid("go", func() { f.appear(root) })
	res := play(t, f, hdr+rezBalloon+fmt.Sprintf("then\nexpect texture balloon face 0 is %s within 300ms\n", idTexA))
	wantExit(t, res, 0)
	mustHave(t, res,
		`rez `+idRootA.String()+` name "Example Balloon" at 130.0 133.0 25.0`,
		"slate: pass step 1", "slate: pass step 2")

	// In the step that binds it the name is refused by the static check.
	s := mustParse(t, hdr+rezBalloon+fmt.Sprintf("expect texture balloon face 0 is %s within 300ms\n", idTexA))
	if err := Check(s); err == nil {
		t.Error("a name bound by a rez was usable in the step that binds it")
	}
}

func TestADescriptionDistinguishesTwinsAndClaimsTakeRootsInOrder(t *testing.T) {
	f := newGrid(t)
	left, right := balloonAt(idRootA, 201, 130), balloonAt(idRootB, 202, 131)
	left.TextureEntry, right.TextureEntry = teOf(idTexA), teOf(idTexB)
	f.setProps(idRootA, "Example Balloon", "left", idStranger)
	f.setProps(idRootB, "Example Balloon", "right", idStranger)
	f.whenSaid("go", func() { f.appear(left, right) })
	// The claims are written right first; the roots are seen left first.
	res := play(t, f, hdr+fmt.Sprintf(`say "go" on 0
expect rez name "Example Balloon" description "right" from vendor as right within 1s
expect rez name "Example Balloon" description "left" from vendor as left within 1s
then
expect texture left face 0 is %s within 300ms
expect texture right face 0 is %s within 300ms
`, idTexA, idTexB))
	wantExit(t, res, 0)

	// With nothing to tell them apart, roots go to claims in observation
	// order, and in local id order within one poll.
	f = newGrid(t)
	first, second := balloonAt(idRootA, 201, 130), balloonAt(idRootB, 202, 131)
	first.TextureEntry, second.TextureEntry = teOf(idTexA), teOf(idTexB)
	f.whenSaid("go", func() { f.appear(second, first) })
	res = play(t, f, hdr+fmt.Sprintf(`say "go" on 0
expect rez name "Example Balloon" from vendor as one within 1s
expect rez name "Example Balloon" from vendor as two within 1s
then
expect texture one face 0 is %s within 300ms
expect texture two face 0 is %s within 300ms
`, idTexA, idTexB))
	wantExit(t, res, 0)
}

func TestASecondRootThatMatchesAClaimFailsAtOnce(t *testing.T) {
	f := newGrid(t)
	f.whenSaid("go", func() { f.appear(balloonAt(idRootA, 201, 130), balloonAt(idRootB, 202, 131)) })
	t0 := time.Now()
	res := play(t, f, hdr+rezBalloon)
	wantExit(t, res, 1)
	if took := time.Since(t0); took > 700*time.Millisecond {
		t.Errorf("failed after %s, not at once", took)
	}
	mustHave(t, res, "a second root matches the rez claim", idRootA.String(), idRootB.String())
	mustNotHave(t, res, "slate: pass step 1")

	// The binding is not made: a later step does not get it. (The test
	// failed, so nothing after the step ran.)
	mustNotHave(t, res, "step 2")
}

func TestATwinThatShowsInTheSettleFailsTheStep(t *testing.T) {
	cfg := testCfg()
	cfg.settle = 300 * time.Millisecond
	f := newGrid(t)
	f.whenSaid("go", func() {
		f.appear(balloonAt(idRootA, 201, 130))
		f.appearAfter(t, 120*time.Millisecond, balloonAt(idRootB, 202, 131))
	})
	res := playWith(t, f, hdr+rezBalloon, Options{}, cfg)
	wantExit(t, res, 1)
	mustHave(t, res, "a second root matches the rez claim", idRootB.String())

	// Without the twin the settle is waited out, and the step passes.
	f = newGrid(t)
	f.whenSaid("go", func() { f.appear(balloonAt(idRootA, 201, 130)) })
	t0 := time.Now()
	res = playWith(t, f, hdr+rezBalloon, Options{}, cfg)
	wantExit(t, res, 0)
	if took := time.Since(t0); took < 300*time.Millisecond {
		t.Errorf("passed after %s, before the settle was over", took)
	}
}

func TestARootOutsideTenMetresOrWithAnotherOwnerIsIgnored(t *testing.T) {
	f := newGrid(t)
	far := balloonAt(idRootB, 202, 160) // 32 m from the vendor
	foreign := balloonAt(idRootC, 203, 131)
	foreign.Owner = idOwnerOther
	f.whenSaid("go", func() { f.appear(far, foreign, balloonAt(idRootA, 201, 130)) })
	res := play(t, f, hdr+rezBalloon)
	wantExit(t, res, 0)
	mustHave(t, res, `rez `+idRootA.String(), `rez `+idRootB.String())

	// An owner the grid has not said is not a mismatch.
	f = newGrid(t)
	unknown := balloonAt(idRootC, 203, 131)
	unknown.Owner = msg.UUID{}
	f.whenSaid("go", func() { f.appear(unknown) })
	wantExit(t, play(t, f, hdr+rezBalloon), 0)
}

func TestZeroMatchesListsEveryRejection(t *testing.T) {
	f := newGrid(t)
	far := balloonAt(idRootB, 202, 160)
	foreign := balloonAt(idRootC, 203, 131)
	foreign.Owner = idOwnerOther
	f.whenSaid("go", func() { f.appear(far, foreign) })
	res := play(t, f, hdr+`say "go" on 0
expect rez name "Example Balloon" from vendor as balloon within 300ms
`)
	wantExit(t, res, 1)
	mustHave(t, res,
		"unmatched rez name \"Example Balloon\" from vendor as balloon within 300ms; new roots rejected:",
		idRootB.String(), "distance 32.0 m from vendor",
		idRootC.String(), "is not the owner of vendor")

	f = newGrid(t)
	res = play(t, f, hdr+`say "go" on 0
expect rez name "Example Balloon" from vendor as balloon within 150ms
`)
	wantExit(t, res, 1)
	mustHave(t, res, "; no new root was seen")
}

func TestARootNearTheObjectThatMatchesNoClaimFailsTheStep(t *testing.T) {
	f := newGrid(t)
	other := balloonAt(idRootB, 202, 131)
	other.Name = "Example Panel"
	f.whenSaid("go", func() { f.appear(other) })
	t0 := time.Now()
	res := play(t, f, hdr+rezBalloon)
	wantExit(t, res, 1)
	if took := time.Since(t0); took > 700*time.Millisecond {
		t.Errorf("failed after %s, not at once", took)
	}
	mustHave(t, res, "matches no rez claim", `name "Example Panel"`)
}

func TestANegativeRez(t *testing.T) {
	src := hdr + "say \"go\" on 0\nexpect no rez name \"Example Balloon\" from vendor within 150ms\n"
	wantExit(t, play(t, newGrid(t), src), 0)

	f := newGrid(t)
	f.whenSaid("go", func() { f.appear(balloonAt(idRootA, 201, 130)) })
	res := play(t, f, src)
	wantExit(t, res, 1)
	mustHave(t, res, "forbidden no rez name")

	// Another name, or one far away, is silence.
	f = newGrid(t)
	other := balloonAt(idRootB, 202, 131)
	other.Name = "Example Panel"
	f.whenSaid("go", func() { f.appear(other, balloonAt(idRootC, 203, 170)) })
	wantExit(t, play(t, f, src), 0)
}

func TestAPropertiesReadThatFailsIsTriedAgainOnTheNextPoll(t *testing.T) {
	f := newGrid(t)
	root := balloonAt(idRootA, 201, 130)
	f.whenSaid("go", func() {
		f.appear(root)
		time.AfterFunc(100*time.Millisecond, func() { f.setProps(idRootA, "Example Balloon", "left", idStranger) })
	})
	cfg := testCfg()
	cfg.props = 40 * time.Millisecond
	res := playWith(t, f, hdr+`say "go" on 0
expect rez name "Example Balloon" description "left" from vendor as balloon within 2s
`, Options{}, cfg)
	wantExit(t, res, 0)
}

func TestTheRezRadiusIsMeasuredAtTheRootOfTheObjectsLinkset(t *testing.T) {
	// The vendor is a child prim: its Position is an offset from its root,
	// and a rez beside the root is beside the vendor.
	f := newGrid(t)
	stand := at(prim(idRootD, 301, "Example Stand", idStranger), 128, 133, 25)
	f.objects[1].Parent = 301
	f.objects[1].Position = msg.Vector3{X: 0.5, Y: 0, Z: 0.5}
	f.objects = append(f.objects, stand)
	f.whenSaid("go", func() { f.appear(balloonAt(idRootA, 201, 135)) })
	res := play(t, f, hdr+rezBalloon)
	wantExit(t, res, 0)
}

func TestAHUDWornBySomebodySeatedIsMeasuredAtTheSeat(t *testing.T) {
	// The avatar sits: its Parent is the seat, and the HUD's is the avatar.
	// The walk goes through the avatar to the seat.
	f := newGrid(t)
	seat := at(prim(idRootD, 400, "Example Chair", idStranger), 100, 100, 25)
	hud := prim(idChildOfA, 401, "Example HUD", testMe)
	hud.Parent = 1
	f.objects[2].Parent = 400
	f.objects = append(f.objects, seat, hud)
	src := `slate 1
object sign is "Example Sign"
object hud is "Example HUD"
say "go" on 0
expect rez name "Example Balloon" from hud as balloon within 300ms
`
	// Beside the seat: taken. Beside the place the avatar's own offset
	// names, which is not a place in the region: not.
	balloon := balloonAt(idRootA, 201, 103)
	balloon.Position = msg.Vector3{X: 103, Y: 100, Z: 25}
	balloon.Owner = testMe
	f.whenSaid("go", func() { f.appear(balloon) })
	wantExit(t, play(t, f, src), 0)

	f = newGrid(t)
	f.objects[2].Parent = 400
	f.objects = append(f.objects, seat, hud)
	other := balloonAt(idRootA, 201, 128)
	other.Position = msg.Vector3{X: 128, Y: 128, Z: 22}
	other.Owner = testMe
	f.whenSaid("go", func() { f.appear(other) })
	res := play(t, f, src)
	wantExit(t, res, 1)
	mustHave(t, res, "distance")
}

func TestASecondRootWhileTheStepIsHeldForAClickDescribeFailsItAsExitOne(t *testing.T) {
	src := hdr + rezBalloon + "then\nexpect click balloon is touch within 300ms\n"
	f := newGrid(t)
	f.whenSaid("go", func() {
		f.appear(balloonAt(idRootA, 201, 130))
		f.appearAfter(t, 100*time.Millisecond, balloonAt(idRootB, 202, 131))
	})
	cfg := testCfg()
	cfg.click = 2 * time.Second
	res := playWith(t, f, src, Options{}, cfg)
	wantExit(t, res, 1)
	mustHave(t, res, "a second root matches the rez claim")
	mustNotHave(t, res, "click action was not")
}
