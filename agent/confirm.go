package agent

// A set's link order confirmed by the region's own count: a script in the
// object said, for each link number, the key of the prim, and the store
// takes that over its reading of the packets.
// Why: doc/objects.md#confirmed-by-the-objects-own-script

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// LinkConfirmation is what a script's own numbering of a set did to the
// store: when it was applied, and whether it changed the order.  It is
// kept per set, bounded by the sets the store holds, dropped with the
// root, and dropped when anything changes which prims the set holds.
type LinkConfirmation struct {
	At time.Time

	// Corrected says the store's order was not the script's, and took it.
	// Moved is then the link numbers whose prim was another one.
	Corrected bool
	Moved     []int
}

// ErrSetDiffers is why ConfirmOrder changed nothing: the script's prims
// are not the ones the store holds under the root.  A prim the store has
// not been told of yet is the usual reason, and the order cannot be
// confirmed until it has.
var ErrSetDiffers = errors.New("the object's own prims are not the ones the store holds")

// ErrNoSuchObject is what ConfirmOrder says of a root the store does not
// hold.
var ErrNoSuchObject = errors.New("the store holds no such object")

// ConfirmOrder applies the order a script in the root numbered its prims
// in: keys[0] is link 1 and the root, keys[1] is link 2, and so on; a
// prim with no children is the one key.  Seated avatars are not in keys:
// they are numbered after the prims whatever the order (see Objects.
// sitters).
//
// The keys must be the root's own and every prim the store holds under
// it, once each, or nothing is changed and the error is ErrSetDiffers.
// When the store's order agrees, the set is marked confirmed; when it
// does not, the store takes the script's order, marks the set confirmed
// and says so in the result.  Either way the set is known, until a prim
// joins it or leaves it, or the root goes.
func (o *Objects) ConfirmOrder(root msg.UUID, keys []msg.UUID) (*LinkConfirmation, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	r := o.byID[root]
	if r == nil {
		return nil, fmt.Errorf("%w: %s", ErrNoSuchObject, root)
	}
	if len(keys) == 0 || keys[0] != root {
		return nil, fmt.Errorf("%w: link 1 of the script is not the root", ErrSetDiffers)
	}
	have := o.kids[r.Local]
	if len(keys)-1 != len(have) {
		return nil, fmt.Errorf("%w: the script has %d prims and the store %d", ErrSetDiffers, len(keys), len(have)+1)
	}
	seen := map[msg.UUID]bool{}
	for _, k := range keys[1:] {
		if seen[k] || !slices.Contains(have, k) {
			return nil, fmt.Errorf("%w: %s is not one of the store's prims under the root", ErrSetDiffers, k)
		}
		seen[k] = true
	}
	c := &LinkConfirmation{At: o.now()}
	if !slices.Equal(have, keys[1:]) {
		c.Corrected = true
		for i, k := range keys[1:] {
			if have[i] != k {
				c.Moved = append(c.Moved, 2+i)
			}
		}
		o.kids[r.Local] = slices.Clone(keys[1:])
		o.explicit[r.Local] = true
		for _, id := range have {
			o.byID[id].moved = false
		}
		delete(o.unordered, r.Local)
		delete(o.unwitnessed, r.Local)
		delete(o.rootless, r.Local)
		delete(o.verdicts, r.Local)
	}
	o.confirms[r.Local] = c
	return c, nil
}

// unconfirmLocked drops a set's confirmation because the prims it holds
// changed.  A set whose order was corrected has the script's order for
// the prims it had, and nothing says where the newcomer or the survivors
// stand now, so it is unknown from then on.
func (o *Objects) unconfirmLocked(set uint32) {
	c := o.confirms[set]
	if c == nil {
		return
	}
	delete(o.confirms, set)
	if c.Corrected {
		o.unordered[set] = true
	}
}
