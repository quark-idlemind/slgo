# Where a worn HUD is on the screen

A HUD is drawn over the viewer's world view, and a person uses one by
clicking and dragging on it. A test that drives a HUD as a person
does needs to know which prim and face a point on the screen is, what a
click there sends, and where a face's S,T is on the screen. `sl` works
that out from what the region says about the worn linkset, plus two
things only the viewer knows: the world view's size and the HUD zoom.

```go
ls, _ := s.Linkset(ctx, &worn.Object)
view := sl.HUDView{Width: 3840, Height: 2050}
faces, _ := view.Faces(ls)               // what is shown, in front first
hit, ok, _ := view.Pick(ls, sl.ScreenPoint{X: 2278, Y: 922})
at, _ := view.PointOf(ls, 2, 0, msg.Vector3{X: 0.85, Y: 0.7})
err := s.DragOnScreen(ctx, &worn.Object, sl.ScreenDrag{
	View: view, Points: []sl.ScreenPoint{at, {X: at.X - 300, Y: at.Y - 200}},
	Move: time.Second, Settle: true,
})
```

Below, **measured** means seen in Firestorm, or on the grid, and
**source** means read from Firestorm's source at revision 885631b93a
and not yet seen. Measurements were taken with Firestorm 7.2.4 on a
screen at 2x, so pixels are backing pixels.

## The world view

**Measured.** The world view is the window's content area less the
viewer's menu bar. The navigation, favourites and chat bars are drawn
over it and do not shrink it, and neither did a side toolbar. A window
of 1920 by 1080 points gave a world view of 3840 by 2050 pixels. Its
height is one metre of HUD at any width: a 0.5 m box was 1024 by 1026
pixels there, 666 square in a 2560 by 1332 world view, and 944 square
in a 2000 by 1888 one.

`HUDView` takes the world view's size in whatever pixels the caller
counts in. Nothing the region says gives it.

## Where a prim is

**Source, then measured.** The eight HUD points hang off a screen joint
scaled `(1, aspect, 1)`, with aspect the world view's width over its
height (llvoavatarself.cpp:384-393). Their positions are in
avatar_lad.xml (lines 341-432), and none of them is turned:

| point | Y, Z |
|---|---|
| Center 2 (31), Center (35) | 0, 0 |
| Top Right (32) | -0.5, 0.5 |
| Top (33) | 0, 0.5 |
| Top Left (34) | 0.5, 0.5 |
| Bottom Left (36) | 0.5, -0.5 |
| Bottom (37) | 0, -0.5 |
| Bottom Right (38) | -0.5, -0.5 |

A point's Y is stretched by the aspect; what is worn on it is not. The
view is orthographic and looks along +X (get_hud_matrices,
llviewerdisplay.cpp:1541-1583), so a point at Y, Z in the HUD's frame
is at

    x = W/2 - Y * H * zoom        y = H/2 - Z * H * zoom

from the world view's top left. +Y is to the left and +Z up. Moving a
prim along X changes nothing on the screen. The centres of a box worn on
all eight points, at three window shapes, were within 2.4 pixels of
this.

A child's position and rotation are its root's frame's: it is placed at
the root's position plus its own position turned by the root's
rotation, and turned by the root's rotation and then its own.
**Measured** with two 0.25 m boxes, the child at 0.25 m along the root's
+Y: 512 pixels to the root's left, at the same height; with the root
turned 90 degrees about X, 513 above it; with the child alone turned 30
degrees about Z, its faces 3 and 4 side by side, 256 and 444 pixels
wide, and its middle where it was.

The metre measured 2048 pixels in a world view 2050 tall, within 2
pixels of the rule; the measurements below use 2050.

## Which faces show

A face shows when its outward normal, turned with the prim, points
toward -X.

**Measured.** An unturned box shows face 4, its -X face. Turned 30
degrees about Z it shows faces 4 and 3 side by side, 888 and 512 pixels
wide in the 2050-pixel world view (cos 30 and sin 30 of 1025); at 45
degrees each is 724. Turned 30 degrees about Y it shows face 4 above
face 5, the bottom. Turned corner-on, Rz(45) then Ry(35.26), it shows
faces 3, 4 and 5 as a hexagon whose corners were within 2 pixels of
prediction. A cylinder turned -90 degrees about Y shows its top, face 0,
as a disc; turned +90 its bottom, face 2; turned -70, an ellipse of the
top above the side, face 1, with the ellipse's height and both edges
within a pixel of prediction.

`Faces` gives each face shown as an outline on the screen, with its
depth, in front first. A round edge is a polygon of short sides, and a
cylinder's side is a curved band, not a convex polygon.

## Which shapes

A plain box or a plain cylinder: no path cut, hollow, twist, taper,
top size or shear, and no sculpt or mesh. These are the shapes whose
faces were measured above. Most HUD panels and buttons are flattened
boxes, and a round button is a cylinder seen end on.

Any other prim's faces are not known here, since a cut or a hollow adds
faces and moves their numbers
([How many faces a prim has](objects.md#how-many-faces-a-prim-has)). It
is not left out either: it stands for the box it fits in, which is in
the way of whatever is behind it. So a click where such a prim is in
front is refused with `ErrShapeNotPlaced`, and so is asking where one of
its faces is or what a point on it is (`PointOf`, `PickOn`); a click on
the plain prims anywhere else is answered as before, and a drag held on
a plain prim goes on reading that prim whatever passes in front of it.
`Faces`, which promises every face, refuses a linkset with such a prim.
The box is larger than the prim inside it, so near a cut or a hollow the
refusal is cautious: it may refuse a point where the viewer would have
hit what is behind.

## Where S and T lie

S and T run 0 to 1 across a face, as `llDetectedTouchST` reports.

**Measured** on face 4 of an unturned box: S grows to the right, from
the +Y edge, and T grows up. Clicks at the pixels predicted for five S,T
came back within 0.001 in S, and with a constant 0.005 in T, about one
point of rounding. On the face turned 30 degrees, clicks spaced evenly
across it came back 0.0997, 0.4987 and 0.8978. A face's texture repeats,
offset and rotation do not enter S,T; they make UV.

**Measured** on every face, each turned to the viewer and clicked
right of its middle and above it. The sides of a box follow its profile,
counter-clockwise seen from above from -Y: face 1 (-Y), 2 (+X), 3 (+Y),
4 (-X), and on each S runs left to right seen from outside and T up.
The top's S runs along +X and T along +Y, and the bottom's S along +X
and T along -Y, as LLVolumeFace::createUnCutCubeCap lays them out
(llvolume.cpp:6022-6160). A cylinder's ends are laid out as a box's top
and bottom. Its side's S is 0 at +X and goes round counter-clockwise
seen from above, so it is 0.5 on the side facing the viewer when the
cylinder is not turned and grows to the right; T grows up. Every click
came back within 0.005 of this. The top was also checked on the grid: a
press at S,T 0.06, 0.12 on the top face of the ExampleHUD glass landed
in the corner its script resizes from ([A drag](#a-drag)).

## A click

`Pick` is the prim in front along the line of sight through a point:
the one a click there touches. It gives the face, S,T, the position and
the normal a viewer would send.

A left click picks transparent prims first, and its first choice is a
transparent attachment (lltoolpie.cpp:127-140). **Measured:** a box with
every face at alpha 0 was clicked as face 4 at its middle, though the
viewer does not draw it. So `Pick` ignores how a prim looks, and a
transparent prim in front takes the click.

The position is in the HUD's own frame, whose origin is the middle of
the world view, whatever point the prim is worn on: the viewer casts
the click from `LLViewerWindow::mousePointHUD`, which measures from the
world view's centre in units of its height (divided by the HUD zoom),
and sends where the ray meets the prim as the grab's
`SurfaceInfo.Position` (`LLViewerWindow::cursorIntersect`,
llviewerwindow.cpp:5535-5606; `LLPickInfo::getSurfaceInfo`; the grab in
lltoolgrab.cpp:909-914; Firestorm 885631b93a). The wiki says the same
of `llDetectedTouchPos`: on a HUD, "relative to the center of the screen
rather than the attachment point".

**Measured.** The middle of face 4 of a 0.5 m box half a metre in front
of Center 2 read -0.75, 0, 0. That reading was on Center 2, which is the
middle of the screen, so it could not tell the two frames apart, and
`Pick` sent the position from the attachment point until 2026-10-03.
Then a 0.1 x 0.4 x 0.2 box was clicked in Firestorm on both Center 2 and
Top Left, and through `DragOnScreen` on both. On Center 2 the two
agreed to a pixel. On Top Left, near the box's lower-right corner, the
viewer read -0.05, 0.74524, 0.40556 -- the point, half the aspect
across and half a metre up, plus the place on the box -- where `Pick`
sent -0.05, -0.192, -0.096. It sends the viewer's now.

A script that moves or resizes a HUD by the change in the position,
as the ExampleHUD does, sees the same change either way: the old
answer differed from the viewer's by the point's position, the same
for every touch of a drag. And on Center 2, where the ExampleHUD's move
and resize were measured, the two are the same answer. The region
passes the position a client sends to the script unchanged.

## A drag

While a touch is held the viewer keeps the prim it pressed
(LLToolGrabBase::handleHoverNonPhysical, lltoolgrab.cpp:787-800) and
casts each new mouse position against that prim alone
(LLPickInfo::getSurfaceInfo, llviewerwindow.cpp:7677-7710). Off it, the
update is still sent, with face -1, S,T and UV -1,-1 and a zero
position (lltoolgrab.cpp:880-917). **Measured:** pressed on a box and
dragged off it, the script's touch events were face 4 while the cursor
was on it and face -1 at -1,-1, position zero, once it was off, and so
was touch_end, released off it.

`PickOn` is that cast, and `DragOnScreen` the drag: it presses the prim
in front at the first point, and every later point, interpolated at the
touch rate, is cast against the prim pressed as the region last
described it. Off it the touch is face -1 at -1,-1, as the viewer's.

A HUD a person can move or resize with the mouse has to keep the cursor
on the prim pressed, and does it with a transparent prim it grows over
the whole screen when pressed:
[ExampleHUD](https://github.com/quark-idlemind/ExampleHUD) is the
public example. Its glass, in front of its background, grows to a 10 m
cube on `touch_start`, and its script moves or scales the HUD by the
change in `llDetectedTouchPos` since the press. `ScreenDrag.Settle`
waits, after the press, for the region to say the prim pressed has
changed before the cursor moves, so that the later points land on the
grown glass.

Not every HUD grows its glass at the press. **Measured** on a HUD made
that way by somebody else: its glass grew to a 10 m cube 220 ms after
the press, once the drag had begun to move, and went back 400 ms after
the release; `Settle` would wait for a change that comes only when the
cursor moves. So as it moves, `DragOnScreen` reads the HUD again every
100 ms and casts each step against the HUD as the region last described
it, as the viewer does, and a glass that grows mid-drag is under the
cursor from the step after the region says so.

**After the release.** The same HUD's glass went back 400 ms after the
release, and a drag placed on it before then is placed on the grown
glass: a second drag from a face of the glass, put on the screen from the
prim as it then was, started at 390,3745 in a 1920x1025 view, and the
product moved the HUD off the screen. So `DragOnScreen` lets go and then
reads the prim it pressed. If its scale is the one it had at the press,
the drag returns at once, and a prim that was never changed costs no
wait. Otherwise it waits until the scale has stayed the same for a quiet
period, one second, whatever size that is, and fails with `ErrTimeout`,
"the HUD was still changing", only if the prim is still changing when
`Options.HUDChangeTimeout` runs out. The quiet period is the measured
400 ms plus margin. It does not ask for the size from the press: a
resize leaves the glass at a new size for good (measured live: after a
resize drag the glass kept the HUD's new size), and waiting for the old
one failed that drag. A
glass that stays grown is therefore taken as settled, and the next
drag placed on it is refused if its point is off the view. This was
chosen over waiting where a face point is placed, because a point placed
from a face is placed from whatever the test last saw, and a HUD left
grown is wrong for anything that follows. The wait is inside the drag's
time budget, and a timeout shorter than the quiet period cannot see a
prim settle. A grow that the region reports only after the release is
not waited for, since nothing is read for it. **Measured** with the
quiet-period rule (slgo-dev #59's build, 5 October 2026), on the HUD
above worn on Center 1 in the default 1920x1025 view: a move drag of 200
by 100 pixels and the drag back, then a resize drag of the corner by
-100 by 50 and the drag back, each straight after the one before with no
wait between, all started on the view, and the HUD moved and resized as
it does under a mouse; and a drag `by 2000 0` was refused as ending at
3077,354, off the view, with nothing sent.

**A point off the view.** A mouse held down cannot leave the window: the
viewer clips it while a button is down, for any tool that does not say
otherwise (`LLViewerWindow::handleAnyMouseClick`,
llviewerwindow.cpp:1180-1183, `LLTool::clipMouseWhenDown`, lltool.h:80,
which the grab tool does not override), and a drag on a window's
menu bar is not a drag on the world view. So `DragOnScreen` refuses a
drag with a point outside 0 to Width-1 by 0 to Height-1, with `ErrOffView`
and before anything is sent, and Slate refuses a start or an end off the
view with `the drag would start at 390,3745, off the 1920x1025 view;
nothing was sent`. The end is refused rather than clamped: a clamped end
is a drag the test did not write, and a test written for a window that
does not fit its numbers should be told. A straight line between two
points on the view is on it, so the points between need no check.

**Measured.** A box whose script set its own scale on `touch_start` was
reported changed 95 to 197 ms after the press, median 146, in 20
presses. `Options.HUDChangeTimeout` defaults to 5 s.

**Measured** end to end, with ExampleHUD (f174c9e) built and worn on
Center 2 by a test avatar: a drag on the screen from its glass, 300
pixels left and 200 up in a 2050-pixel world view, moved the HUD by
0.1463 and 0.0976 m, which is 300/2050 and 200/2050; a drag from the
resize corner, 150 left and 100 down, scaled it by 1.3146, the factor
its script computes for that distance. A press in the middle of the
screen found the HUD's button, in front of the glass, and Settle timed
out on it, as it should for a prim that does not change.

## Zoom

The viewer draws HUDs scaled about the middle of the world view by its
HUDScaleFactor setting (default 1, settings.xml), times its HUD edit
zoom while a HUD is selected for editing
(LLAgentCamera::getAgentHUDTargetZoom, llagentcamera.cpp:891-896;
get_hud_matrices). The edit zoom is held between 0.1 and 1
(llviewerdisplay.cpp:1414). `HUDView.Zoom` is that product; 0 is taken
as 1. Nothing the region says gives it.

**Measured.** A 0.5 m box 0.2 m left of and 0.1 m above Center 2 was
1024 pixels square at HUDScaleFactor 1, 514 at 0.5 and 768 at 0.75, and
its middle was 410 pixels left of the HUD's middle and 205 above it at
1, 205 and 103 at 0.5, and 308 and 154 at 0.75 -- the rule to within 2
pixels. The setting was
kept through a log out and in. Below 1 the viewer also draws a thin
rectangle, the scaled edge of the HUD. The edit zoom was not measured.

## Not known

- A HUD turned so a face is seen exactly edge on: which of two faces a
  point on their shared edge picks.
- Shapes other than a plain box and cylinder.
