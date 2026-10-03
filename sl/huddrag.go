package sl

import (
	"context"
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
// Time is shared out as Drag shares it.  It always lets go,
// cancellation included.
// Why: doc/hud-screen.md#a-drag
func (w *Session) DragOnScreen(ctx context.Context, root *Object, d ScreenDrag) error {
	if len(d.Points) == 0 {
		return fmt.Errorf("sl: a drag needs somewhere to start")
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
	// and reading the HUD again at each point given.
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
