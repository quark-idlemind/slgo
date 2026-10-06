package sl

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// ScreenDrag is a drag given on the screen rather than on a face: what a
// mouse does to a worn HUD.
type ScreenDrag struct {
	View HUDView

	// Points are where the cursor is, in order: pressed at the first,
	// released at the last.
	Points []ScreenPoint

	// Press, Move, Dwell and Rate are as in Drag.
	Press, Move, Dwell time.Duration
	Rate               int

	// Settle waits, after the press, for the region to say the prim
	// pressed has changed -- grown, moved or turned -- before the
	// cursor moves, for as long as Options.HUDChangeTimeout.  A HUD that
	// is dragged by a transparent prim it grows over the screen when
	// pressed needs it: until the region says the prim has grown, the
	// cursor is off it.
	Settle bool
}

// ErrOffView is a point of a drag on the screen outside the world view.
// A mouse held down is kept inside the window (the viewer clips it,
// llviewerwindow.cpp:1180-1183, and the grab tool does not turn that
// off), so no drag can start, pass through or end there.
// Why: doc/hud-screen.md#a-drag
var ErrOffView = errors.New("sl: a point off the world view")

// Contains reports whether p is a pixel of the world view, 0 to
// Width-1 across and 0 to Height-1 down.
func (v HUDView) Contains(p ScreenPoint) bool {
	return p.X >= 0 && p.Y >= 0 && p.X <= float64(v.Width-1) && p.Y <= float64(v.Height-1)
}

// DragOnScreen presses a worn HUD where a drag on the screen starts,
// moves the cursor through the points and lets go, sending what a
// viewer sends.
//
// The prim pressed is the one in front at the first point, and it is
// held for the whole drag, as a viewer holds it: every later point is
// where the cursor is on that prim, as the region last described it,
// and a point off it is sent as face -1 at S,T -1,-1.  root is the
// worn object's root; the linkset is read again at each of the points
// given, so a HUD that moves or grows while it is dragged is followed.
//
// A point off the view is refused with ErrOffView before anything is
// sent.  Time is shared out as Drag shares it.  It always lets go,
// cancellation included, and then, if the prim pressed is not at the
// size it had at the press, waits up to Options.HUDChangeTimeout for
// it to keep one size for a second: a HUD that grows a prim for the drag
// puts it back a moment after the release, and a drag placed on the prim
// before then starts where the prim was.  A prim that was never changed
// costs no wait.
// Why: doc/hud-screen.md#a-drag
func (w *Session) DragOnScreen(ctx context.Context, root *Object, d ScreenDrag) (err error) {
	if len(d.Points) == 0 {
		return fmt.Errorf("sl: a drag needs somewhere to start")
	}
	for i, p := range d.Points {
		if !d.View.Contains(p) {
			return fmt.Errorf("%w: point %d of the drag, %.0f,%.0f, is off the %dx%d view; nothing was sent",
				ErrOffView, i+1, p.X, p.Y, d.View.Width, d.View.Height)
		}
	}
	rate := d.Rate
	if rate <= 0 {
		rate = TouchRate
	}
	every := time.Second / time.Duration(rate)

	linkset, err := w.Linkset(ctx, root)
	if err != nil {
		return err
	}
	first, ok, err := d.View.Pick(linkset, d.Points[0])
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("sl: %s is not on the screen at %.0f,%.0f", root, d.Points[0].X, d.Points[0].Y)
	}
	held, link := first.Object, first.Link
	pressed := linkset[link-1].Scale
	at := func(p ScreenPoint) Touch {
		h, _, _ := d.View.PickOn(linkset, link, p)
		return withUV(linkset[link-1], h.Touch)
	}

	last := withUV(linkset[link-1], first.Touch)
	if err := w.touchStart(ctx, &held, last); err != nil {
		return err
	}
	defer func() {
		end, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = w.touchEnd(end, &held, last)
		if err == nil {
			err = w.settleAfterRelease(ctx, root, link, pressed, &held)
		}
	}()

	sent := time.Now()
	tick := func(t Touch) error {
		now := time.Now()
		err := w.touchMove(ctx, &held, t, now.Sub(sent))
		sent, last = now, t
		return err
	}

	if d.Settle {
		was := *linkset[link-1]
		err := poll(ctx, w.hudChangeWait(), 50*time.Millisecond,
			fmt.Sprintf("%s to change when pressed", held),
			func(ctx context.Context) (bool, error) {
				now, err := w.Linkset(ctx, root)
				if err != nil || len(now) < link {
					return false, err
				}
				p := now[link-1]
				if p.Scale == was.Scale && p.Position == was.Position && p.Rotation == was.Rotation {
					return false, nil
				}
				linkset = now
				return true, nil
			})
		if err != nil {
			return err
		}
	}

	if err := w.hold(ctx, at(d.Points[0]), d.Press, every, tick); err != nil {
		return err
	}
	// Along the path, picking afresh at every step as the cursor moves,
	// against the HUD as the region last described it: a HUD that grows
	// its prim once the drag has begun moving is seen grown a step after
	// the region says so, as the viewer sees it.
	// Why: doc/hud-screen.md#a-drag
	read := time.Now()
	if n := len(d.Points) - 1; n > 0 && d.Move > 0 {
		per := d.Move / time.Duration(n)
		for i := 0; i < n; i++ {
			from, to := d.Points[i], d.Points[i+1]
			steps := max(int(per/every), 1)
			for s := 1; s <= steps; s++ {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(every):
				}
				if time.Since(read) >= hudReread {
					if now, err := w.Linkset(ctx, root); err == nil && len(now) >= link {
						linkset = now
					}
					read = time.Now()
				}
				f := float64(s) / float64(steps)
				p := ScreenPoint{from.X + (to.X-from.X)*f, from.Y + (to.Y-from.Y)*f}
				if err := tick(at(p)); err != nil {
					return err
				}
			}
			if now, err := w.Linkset(ctx, root); err == nil && len(now) >= link {
				linkset = now
			}
		}
	}
	return w.hold(ctx, at(d.Points[len(d.Points)-1]), d.Dwell, every, tick)
}

// settleQuiet is how long the prim pressed must have kept one size after
// the release for the drag to be over.  A HUD that grows a prim for the
// drag was measured putting it back 400 ms after the release; a second
// is that and a margin.  The size it settles at is not asked: a resize
// leaves the prim at a new one.
// Why: doc/hud-screen.md#a-drag
const settleQuiet = time.Second

// settleAfterRelease returns at once if the prim pressed has the size it
// had at the press.  Otherwise it returns when the prim has kept one size
// for settleQuiet, whatever it is, and fails with ErrTimeout, saying the
// HUD was still changing, if it has not within Options.HUDChangeTimeout.
// Why: doc/hud-screen.md#a-drag
func (w *Session) settleAfterRelease(ctx context.Context, root *Object, link int, pressed msg.Vector3, held *Object) error {
	var last msg.Vector3
	var since time.Time
	first := true
	return poll(ctx, w.hudChangeWait(), 50*time.Millisecond,
		fmt.Sprintf("%s to stop changing after the release (the HUD was still changing)", held),
		func(ctx context.Context) (bool, error) {
			now, err := w.Linkset(ctx, root)
			if err != nil || len(now) < link {
				return false, err
			}
			scale := now[link-1].Scale
			if first && scale == pressed {
				return true, nil
			}
			if first || scale != last {
				last, since, first = scale, time.Now(), false
			}
			return time.Since(since) >= settleQuiet, nil
		})
}

// hudReread is how often a drag on the screen reads the HUD again as it
// moves.  The region describes a change to a pressed prim 95 to 197 ms
// after it is made (measured; doc/hud-screen.md#a-drag), so reading every
// tenth of a second sees one within a step of its arriving, and costs a
// read of the store, not of the grid.
const hudReread = 100 * time.Millisecond

// withUV fills in the texture coordinates of a touch on a prim from its
// appearance, as placeTouches does from a fresh read.
func withUV(s *Seen, t Touch) Touch {
	if t.Face < 0 || t.UV != (msg.Vector3{}) {
		return t
	}
	t.UV = t.ST
	n, ok := s.FaceCount()
	if !ok {
		return t
	}
	faces, err := s.Faces(n)
	if err != nil || t.Face >= len(faces) || faces[t.Face].Planar() || animated(s.TextureAnim, t.Face, n) {
		return t
	}
	t.UV = faces[t.Face].SurfaceToTexture(t.ST)
	return t
}
