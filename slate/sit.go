package slate

// sit and stand. A sit that took leaves the avatar seated until a stand,
// and the failure block says so.
// Why: doc/slate-runner.md#cleanup-and-what-a-failure-leaves-behind

import (
	"context"
	"fmt"
	"time"
)

// seatedText is what the failure block's seated line says.
func (r *runner) seatedText() string {
	if r.seat == "" {
		return "no"
	}
	return r.seat
}

// sitStimulus sits on the bound prim. The budget is never zero, since
// zero is sl's own 15 s. ErrSitRefused and a timeout fail with sl's text.
func (s *stepRun) sitStimulus(st *Sit) (*stimulus, error) {
	b, err := s.bound(st.Name)
	if err != nil {
		return nil, err
	}
	return &stimulus{
		blocking: true,
		send: func(ctx context.Context, budget time.Duration) (string, error) {
			if _, err := s.r.sess.Sit(ctx, &b.seen.Object, budget); err != nil {
				return "", err
			}
			s.r.seat = fmt.Sprintf("%s %q", b.name, b.seen.Name)
			if s.r.pr != nil && s.r.pr.links[b.name] != nil {
				s.r.printf("slate: step %d: seated on %s: link numbers at and above the seated avatar's are not probed, and a message addressed only to one is not observed", s.n, b.name)
			}
			return fmt.Sprintf("sat on %s", b.name), nil
		},
	}, nil
}

// standStimulus stands the avatar up. One that is already standing is
// not an error.
func (s *stepRun) standStimulus() *stimulus {
	return &stimulus{
		blocking: true,
		send: func(ctx context.Context, budget time.Duration) (string, error) {
			if err := s.r.sess.Stand(ctx, budget); err != nil {
				return "", err
			}
			s.r.seat = ""
			return "stood", nil
		},
	}
}
