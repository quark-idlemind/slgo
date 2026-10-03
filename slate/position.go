package slate

// The tester's position, for the 20 m check on a say.
// Why: doc/slate-runner.md#tester-position

import (
	"context"

	"github.com/quark-idlemind/slgo/msg"
)

// testerPos is where the tester's body is. exact is false when it cannot
// be known: the coarse reading of Where is never used on its own. It is
// read on the run's context, so it is on no step clock.
func (r *runner) testerPos(ctx context.Context) (pos msg.Vector3, exact bool, err error) {
	me, err := r.sess.Backend().Objects(ctx, "", r.sess.Me().String())
	if err != nil {
		return pos, false, err
	}
	if len(me) == 0 {
		return pos, false, nil
	}
	if me[0].Parent == 0 {
		// Standing: the terse-update position, not reckoned forward.
		return me[0].Position, true, nil
	}
	// Seated: Seen.Position is an offset from the seat. Where composes
	// the seat's position only when the chain is known, so look at the
	// chain first.
	all, err := r.sess.Backend().Objects(ctx, "", "")
	if err != nil {
		return pos, false, err
	}
	if _, ok := regionPos(all, me[0]); !ok {
		return pos, false, nil
	}
	p, err := r.sess.Where(ctx)
	if err != nil {
		return pos, false, err
	}
	return p.Position, true, nil
}
