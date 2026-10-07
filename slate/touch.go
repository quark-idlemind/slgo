package slate

// touch and drag: stimuli on one bound prim's face.
// Why: doc/slate-runner.md#stimuli

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// defaultDragOver is the drag's Move when the script gives no over.
const defaultDragOver = 500 * time.Millisecond

func stOf(st ST) msg.Vector3 { return msg.Vector3{X: float32(st.S.Value), Y: float32(st.T.Value)} }

// bound is the binding a stimulus names, or the step's refusal.
func (s *stepRun) bound(name Ident) (*binding, error) {
	b := s.r.lookup(name.Text)
	if b == nil {
		return nil, fmt.Errorf("%s is not an object", name.Text)
	}
	return b, nil
}

// touchStimulus is a click on the bound prim. Anywhere is the zero touch,
// a face without a point is its middle, and a point is ST with UV left
// for the session to work out. A touch returns when it is sent.
func (s *stepRun) touchStimulus(t *Touch) (*stimulus, error) {
	if t.Showing != nil {
		return s.showingStimulus(t)
	}
	b, resolve, err := s.target(t.Name, t.Link)
	if err != nil {
		return nil, err
	}
	if t.Element != nil {
		st, err := s.elementStimulus(t, b)
		if err != nil {
			return nil, err
		}
		return withLink(st, resolve), nil
	}
	if t.Button != nil {
		st, err := s.buttonStimulus(t, b)
		if err != nil {
			return nil, err
		}
		return withLink(st, resolve), nil
	}
	var touch sl.Touch
	what := "anywhere"
	if t.Face != nil {
		touch.Face = int(t.Face.Value)
		what = fmt.Sprintf("face %d", touch.Face)
		if t.At != nil {
			touch.ST = stOf(*t.At)
			what += fmt.Sprintf(" at %v %v", touch.ST.X, touch.ST.Y)
		}
	}
	return withLink(&stimulus{
		send: func(ctx context.Context, _ time.Duration) (string, error) {
			if err := s.r.actor(t.AsAvatar).Touch(ctx, &b.seen.Object, touch); err != nil {
				return "", err
			}
			return fmt.Sprintf("touched %s %s%s", b.name, what, asText(t.AsAvatar)), nil
		},
	}, resolve), nil
}

// dragStimulus presses at the first point, moves to the second and
// releases, on the stimulus budget. Drag always sends the release.
func (s *stepRun) dragStimulus(d *Drag) (*stimulus, error) {
	if d.Screen != nil {
		return s.screenDragStimulus(d)
	}
	b, resolve, err := s.target(d.Name, d.Link)
	if err != nil {
		return nil, err
	}
	face := int(d.Face.Value)
	over := defaultDragOver
	if d.Over != nil {
		over = d.Over.Value
	}
	drag := sl.Drag{
		Points: []sl.Touch{{Face: face, ST: stOf(d.From)}, {Face: face, ST: stOf(d.To)}},
		Move:   over, Press: d.pressFor(), Dwell: d.dwellFor(),
	}
	return withLink(&stimulus{
		blocking: true,
		send: func(ctx context.Context, budget time.Duration) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			if err := s.r.actor(d.AsAvatar).Drag(ctx, &b.seen.Object, drag); err != nil {
				return "", err
			}
			return fmt.Sprintf("dragged %s face %d over %s%s%s", b.name, face, over, d.holds(), asText(d.AsAvatar)), nil
		},
	}, resolve), nil
}

// pressFor and dwellFor are a drag's holds at its two ends, zero for
// none.
func (d *Drag) pressFor() time.Duration { return valueOr(d.Press) }
func (d *Drag) dwellFor() time.Duration { return valueOr(d.Dwell) }

func valueOr(d *Duration) time.Duration {
	if d == nil {
		return 0
	}
	return d.Value
}

// holds is what a drag's line says of its press and dwell, when it had
// either.
func (d *Drag) holds() string {
	s := ""
	if d.Press != nil {
		s += fmt.Sprintf(", pressed %s first", d.Press.Value)
	}
	if d.Dwell != nil {
		s += fmt.Sprintf(", held %s at the end", d.Dwell.Value)
	}
	return s
}
