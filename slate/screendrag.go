package slate

// drag OBJ on screen: a drag given in pixels of a virtual world view, on
// a worn HUD.
// Why: doc/slate-runner.md#a-drag-on-the-screen

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// The world view a drag on the screen is given in unless the run says
// otherwise: a 1920x1080 window less Firestorm's menu bar.
const (
	DefaultScreenWidth  = 1920
	DefaultScreenHeight = 1025
)

// screen is the run's world view, with the defaults filled in.
func (r *runner) screen() sl.HUDView {
	v := r.opt.Screen
	if v.Width <= 0 || v.Height <= 0 {
		v.Width, v.Height = DefaultScreenWidth, DefaultScreenHeight
	}
	return v
}

// screenDragStimulus presses at the start, moves to the end over the
// drag's time and lets go, through Session.DragOnScreen, on the stimulus
// budget. A start given as a face is turned into pixels when the step is
// prepared, from the linkset as it is then.
func (s *stepRun) screenDragStimulus(d *Drag) (*stimulus, error) {
	b, err := s.bound(d.Name)
	if err != nil {
		return nil, err
	}
	sd := d.Screen
	over := defaultDragOver
	if d.Over != nil {
		over = d.Over.Value
	}
	view := s.r.screen()
	var root *sl.Seen
	var from, to sl.ScreenPoint
	return &stimulus{
		blocking: true,
		prepare: func(ctx context.Context) error {
			root = b.prims()[0]
			if !sl.IsHUDPoint(root.AttachPoint) {
				worn := "not worn"
				if root.AttachPoint != 0 {
					worn = "worn on " + sl.AttachPointName(root.AttachPoint)
				}
				return s.sentence("%q is %s; a drag on the screen needs an object worn on a HUD point, and nothing was sent", nameOf(b), worn)
			}
			if sd.Face == nil {
				from = sl.ScreenPoint{X: sd.FromPixels.S.Value, Y: sd.FromPixels.T.Value}
			} else {
				linkset, err := s.r.sess.Linkset(ctx, &root.Object)
				if err != nil {
					return err
				}
				link := 1
				if sd.Link != nil && sd.Link.Value > 0 {
					link = int(sd.Link.Value)
				}
				st := msg.Vector3{X: float32(sd.At.S.Value), Y: float32(sd.At.T.Value)}
				if from, err = view.PointOf(linkset, link, int(sd.Face.Value), st); err != nil {
					return s.screenFault(b, err)
				}
			}
			to = sl.ScreenPoint{X: sd.To.S.Value, Y: sd.To.T.Value}
			if sd.By {
				to = sl.ScreenPoint{X: from.X + to.X, Y: from.Y + to.Y}
			}
			// A mouse held down stays in the window, so a start or an end
			// off the view is no drag; the line between two points on it is
			// on it too.
			// Why: doc/hud-screen.md#a-drag
			if !view.Contains(from) {
				return s.sentence("the drag would start at %.0f,%.0f, off the %dx%d view; nothing was sent", from.X, from.Y, view.Width, view.Height)
			}
			if !view.Contains(to) {
				return s.sentence("the drag would end at %.0f,%.0f, off the %dx%d view; nothing was sent", to.X, to.Y, view.Width, view.Height)
			}
			return nil
		},
		send: func(ctx context.Context, budget time.Duration) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			err := s.r.sess.DragOnScreen(ctx, &root.Object, sl.ScreenDrag{
				View: view, Points: []sl.ScreenPoint{from, to}, Move: over, Settle: sd.Settle,
				Press: d.pressFor(), Dwell: d.dwellFor(),
			})
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("dragged %s on the screen from %.0f,%.0f to %.0f,%.0f over %s%s", b.name, from.X, from.Y, to.X, to.Y, over, d.holds()), nil
		},
	}, nil
}

// screenFault is the sentence for a face that cannot be put on the
// screen.
func (s *stepRun) screenFault(b *binding, err error) error {
	return s.sentence("%q cannot be put on the screen: %v; give the start in pixels, and nothing was sent", nameOf(b), err)
}

// nameOf is what a sentence calls a bound object: its name, or, for one
// whose name is not known -- a worn object is not named by the region --
// the name the file bound it to.
func nameOf(b *binding) string {
	if b.seen.Name != "" {
		return b.seen.Name
	}
	return b.name
}
