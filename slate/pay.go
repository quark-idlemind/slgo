package slate

// pay: both gates, the transcript line before the call, and the
// session's own waits held to the stimulus budget.
// Why: doc/slate-runner.md#stimuli

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// payStimulus pays the bound prim. `allow pay` was enforced by Check;
// Options.Pay is the process's half, refused here before anything is
// printed or sent. A payment is never sent twice.
func (s *stepRun) payStimulus(p *Pay) (*stimulus, error) {
	b, err := s.bound(p.Name)
	if err != nil {
		return nil, err
	}
	amount := int(p.Amount.Value)
	return &stimulus{
		blocking: true,
		prepare: func(context.Context) error {
			if !s.r.opt.Pay {
				return errors.New("paying is off")
			}
			return nil
		},
		send: func(ctx context.Context, budget time.Duration) (string, error) {
			r := s.r
			reason := p.Reason
			if !p.HasReason {
				reason = b.seen.Name // what PayObject sends for an empty reason
			}
			// Printed even when the call then fails.
			r.printEvent(&event{at: time.Now(), text: fmt.Sprintf("pay L$%d to %q %s reason %q", amount, b.seen.Name, b.seen.ID, reason)})
			s.payment = fmt.Sprintf("L$%d to %q", amount, b.seen.Name)

			// PayObject waits more than once and takes no timeout: each wait
			// is MoneyTimeout, so that is the budget too.
			prev := r.sess.Options()
			capped := prev
			capped.MoneyTimeout = budget
			r.sess.SetOptions(capped)
			defer r.sess.SetOptions(prev)
			ctx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()

			id, _, err := r.sess.PayObject(ctx, &b.seen.Object, amount, p.Reason)
			if err != nil {
				// A PayRefused or PayUnconfirmed says whether the balance moved.
				s.payment += ": " + err.Error()
				return "", err
			}
			s.payment += " transaction " + id.String()
			return fmt.Sprintf("paid L$%d to %s", amount, b.name), nil
		},
	}, nil
}
