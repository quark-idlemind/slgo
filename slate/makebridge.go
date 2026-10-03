package slate

// MakeBridge makes the "slate bridge" item the probe messages travel
// through, once per account. Nothing dials here: the caller holds the
// session, as for Run.
// Why: doc/slate-runner.md#bring-up

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// bridgeName is the item's name, and the prim's until it is taken.
const bridgeName = "slate bridge"

// ErrBridgeExists is what MakeBridge returns when an item named exactly
// "slate bridge" is already in the Objects folder. Nothing was rezzed and
// nothing was made; the command treats it as success.
var ErrBridgeExists = errors.New(`slate: make-bridge: "slate bridge" is already in the Objects folder; nothing was made`)

// bridgeCfg is the budget of each step. Tests shorten it.
// Why: doc/slate-runner.md#timeouts-and-setup-budgets
type bridgeCfg struct {
	rez, name, take time.Duration
	cleanup         time.Duration // deleting a prim a later step left behind
}

func defaultBridgeCfg() bridgeCfg {
	return bridgeCfg{rez: 15 * time.Second, name: 20 * time.Second, take: 20 * time.Second, cleanup: 15 * time.Second}
}

// MakeBridge rezzes a 0.1 m prim one metre along +X from the tester's body,
// names it "slate bridge" and takes it into the Objects folder. It needs
// build rights where the tester stands.
//
// A failed step returns "slate: make-bridge: <step> (<budget>): <error>"
// and deletes a prim already rezzed (to the Trash). An item of that name
// already in Objects returns ErrBridgeExists before anything is rezzed.
func MakeBridge(ctx context.Context, sess *sl.Session) error {
	return makeBridge(ctx, sess, defaultBridgeCfg())
}

func stepErr(step string, budget time.Duration, err error) error {
	return fmt.Errorf("slate: make-bridge: %s (%s): %w", step, budget, err)
}

func makeBridge(ctx context.Context, sess *sl.Session, cfg bridgeCfg) error {
	if sess == nil {
		return errors.New("slate: make-bridge: needs a session")
	}
	objects, err := sess.ObjectsFolder(ctx)
	if err != nil {
		return fmt.Errorf("slate: make-bridge: the Objects folder: %w", err)
	}
	items, err := sess.FolderItems(ctx, objects)
	if err != nil {
		return fmt.Errorf("slate: make-bridge: reading the Objects folder: %w", err)
	}
	for _, it := range items {
		if it.Name == bridgeName {
			return ErrBridgeExists
		}
	}

	// The exact position, on no step clock: the coarse one is not enough.
	r := &runner{sess: sess}
	pos, exact, err := r.testerPos(ctx)
	if err != nil {
		return fmt.Errorf("slate: make-bridge: reading the tester's position: %w", err)
	}
	if !exact {
		return errors.New("slate: make-bridge: the tester's exact position is not known (the body is not in the store, or its seat is unknown); nothing was rezzed")
	}
	at := pos
	at.X++

	rctx, cancel := context.WithTimeout(ctx, cfg.rez)
	prim, err := sess.Rez(rctx, sl.RezOptions{At: at, Scale: msg.Vector3{X: 0.1, Y: 0.1, Z: 0.1}})
	cancel()
	if err != nil {
		if prim == nil {
			return stepErr("rez", cfg.rez, err)
		}
		// Rez hands back the prim it found at the last look with its error.
		return stepErr("rez", cfg.rez, r.discard(prim, err, cfg))
	}

	nctx, cancel := context.WithTimeout(ctx, cfg.name)
	err = sess.SetName(nctx, prim, bridgeName)
	cancel()
	if err != nil {
		return stepErr("rename", cfg.name, r.discard(prim, err, cfg))
	}

	tctx, cancel := context.WithTimeout(ctx, cfg.take)
	_, err = sess.Take(tctx, prim, objects, cfg.take)
	cancel()
	if err != nil {
		return stepErr("take", cfg.take, r.discard(prim, err, cfg))
	}
	return nil
}

// discard deletes a prim a failed step left in the region, to the Trash,
// on a context of its own since the step's may be the reason it failed.
// It returns the step's error, with what the delete did added when that
// failed too.
func (r *runner) discard(prim *sl.Object, cause error, cfg bridgeCfg) error {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.cleanup)
	defer cancel()
	trash, err := r.sess.TrashFolder(ctx)
	if err == nil {
		err = r.sess.Delete(ctx, prim, trash)
	}
	if err != nil {
		return fmt.Errorf("%w; the prim %s could not be deleted: %v", cause, prim.ID, err)
	}
	return cause
}
