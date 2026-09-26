package sl

// Whether the land will run a script, told before a run rather than
// learned by waiting one out: on land that does not run an object's
// scripts the simulator reports them running all the same, and they say
// nothing.  Why: doc/ground.md#where-the-land-stops-running-scripts

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// parcelHeight is how far above the ground a parcel's rules reach:
// PARCEL_HEIGHT, "Height above ground that parcel boundary ends"
// (llinventory/llparcel.h:46).  That scripts stop below it, measured
// from the highest ground under an object to the object's bottom, is
// inferred from one spot.
const parcelHeight = 50

// scriptsBlocked says why the land will not run scripts in o, or ""
// where it will and where that cannot be told.
//
// The region's flags stop every script, worn or not.  Past them, a worn
// object, one nothing has described, land that has not arrived, and a
// parcel or an object that does not answer are all "".  A parcel whose
// scripts are off for everyone runs only its owner's, and its group's
// where group scripts are on, and that matters only to an object whose
// bottom is within parcelHeight of the highest ground under it.  The
// prim the script is in is what is placed; a linkset has not been
// measured.
func (w *Session) scriptsBlocked(ctx context.Context, o *Object) string {
	if r, known, err := w.b.Region(ctx); err == nil && known && r != nil {
		switch {
		case r.Flags&agent.RegionEstateSkipScripts != 0:
			return "an administrator has temporarily stopped scripts in this region"
		case r.Flags&agent.RegionSkipScripts != 0:
			return "this region is not running any scripts"
		}
	}

	p, ok := w.placedInWorld(ctx, o)
	if !ok {
		return ""
	}
	west, south, east, north, bottom := p.bounds()
	ground, known, err := w.b.Ground(ctx, west, south, east, north)
	if err != nil || !known {
		return ""
	}
	above := bottom - ground
	if above >= parcelHeight {
		return ""
	}

	parcel, err := w.ParcelAt(ctx, p.at.X, p.at.Y, DefaultParcelTimeout)
	if err != nil || parcel.Flags&agent.ParcelAllowOtherScripts != 0 {
		return ""
	}
	groupScripts := parcel.Flags&agent.ParcelAllowGroupScripts != 0
	if groupScripts && parcel.Group.IsZero() {
		// A parcel of no group has not been measured.
		return ""
	}
	owner, group := p.root.Owner, msg.UUID{}
	if owner.IsZero() || (groupScripts && owner != parcel.Owner) {
		if owner, group, ok = w.ownership(ctx, p.root.ID, DefaultParcelTimeout); !ok {
			return ""
		}
	}
	if owner == parcel.Owner {
		return ""
	}
	runs, and := "only its owner's scripts", "and this object's"
	if groupScripts {
		if group == parcel.Group {
			return ""
		}
		runs, and = "only its owner's and its group's scripts", "this object is not in its group, and its"
	}
	return fmt.Sprintf("the land doesn't run this object's scripts here: parcel %q runs %s "+
		"up to %d m above the ground, %s bottom is %.2f m above it (slgo doc/ground.md)",
		parcel.Name, runs, parcelHeight, and, above)
}

// placed is a prim where it is in the region, and the root of its
// object, which is what owns it.
type placed struct {
	at    msg.Vector3
	rot   msg.Quaternion
	scale msg.Vector3
	root  *Seen
}

// placedInWorld finds o in the region, and a worn object is not in it.
// Why: doc/ground.md#worn-objects
func (w *Session) placedInWorld(ctx context.Context, o *Object) (*placed, bool) {
	if o == nil || o.ID.IsZero() {
		return nil, false
	}
	found, err := w.b.Objects(ctx, "", o.ID.String())
	if err != nil || len(found) == 0 || found[0].AttachPoint != 0 {
		return nil, false
	}
	s := found[0]
	p := &placed{at: s.Position, rot: s.Rotation, scale: s.Scale, root: s}
	if s.Parent == 0 {
		return p, true
	}
	all, err := w.b.Objects(ctx, "", "")
	if err != nil {
		return nil, false
	}
	for _, r := range all {
		if r.Local != s.Parent {
			continue
		}
		// The parent of a worn root is the avatar, and of a worn
		// child a root that is itself parented.
		if r.IsAvatar() || r.Parent != 0 || r.AttachPoint != 0 {
			return nil, false
		}
		p.at = add3(r.Position, r.Rotation.Rotate(s.Position))
		p.rot = r.Rotation.Mul(s.Rotation)
		p.root = r
		return p, true
	}
	return nil, false
}

// bounds is the box around the prim, square to the region, in region
// metres, and its bottom.  For an unrotated prim it is the prim.
func (p *placed) bounds() (west, south, east, north, bottom float32) {
	ex := p.rot.Rotate(msg.Vector3{X: 1})
	ey := p.rot.Rotate(msg.Vector3{Y: 1})
	ez := p.rot.Rotate(msg.Vector3{Z: 1})
	half := func(a, b, c float32) float32 {
		return (abs32(a)*p.scale.X + abs32(b)*p.scale.Y + abs32(c)*p.scale.Z) / 2
	}
	hx, hy, hz := half(ex.X, ey.X, ez.X), half(ex.Y, ey.Y, ez.Y), half(ex.Z, ey.Z, ez.Z)
	return p.at.X - hx, p.at.Y - hy, p.at.X + hx, p.at.Y + hy, p.at.Z - hz
}

func abs32(v float32) float32 { return float32(math.Abs(float64(v))) }

// ownership asks who owns an object and what group it is in.  The
// family request is answered for a root prim and not a child; see
// selectForNames.
func (w *Session) ownership(ctx context.Context, id msg.UUID, timeout time.Duration) (owner, group msg.UUID, ok bool) {
	w.mu.Lock()
	delete(w.groups, id)
	w.mu.Unlock()
	if err := w.Send(ctx, w.familyRequest(id)); err != nil {
		return msg.UUID{}, msg.UUID{}, false
	}
	err := w.await(ctx, timeout, "who owns "+id.String(), func() bool {
		g, has := w.groups[id]
		if has {
			owner, group = w.owners[id], g
		}
		return has
	})
	return owner, group, err == nil
}
