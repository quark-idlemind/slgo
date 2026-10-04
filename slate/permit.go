package slate

// allow permission NAME... from OBJ: the permission requests a run grants.
// Every other request is refused, as before (denyPermissions in arm.go).
// Why: doc/slate-language.md#stimuli (Permissions)

import (
	"fmt"
	"sort"
	"strings"

	"github.com/quark-idlemind/slgo/sl"
)

// permitWords are the nameable permissions, and no others. PermissionDebit
// is not here on purpose: paying stays behind allow pay and pay.Gate.
// Land, estate and experience powers are not here either, nor are the bits
// the grid does not implement.
var permitWords = map[string]sl.Perms{
	"take-controls":       sl.PermissionTakeControls,
	"trigger-animation":   sl.PermissionTriggerAnimation,
	"attach":              sl.PermissionAttach,
	"change-links":        sl.PermissionChangeLinks,
	"track-camera":        sl.PermissionTrackCamera,
	"control-camera":      sl.PermissionControlCamera,
	"teleport":            sl.PermissionTeleport,
	"override-animations": sl.PermissionOverrideAnimations,
}

// neverGranted is withheld from every grant whatever the file says, so the
// mask cannot carry it even if a request asks for it with named bits.
const neverGranted = sl.PermissionDebit

// permitWordList is the nameable words, sorted, for a message.
func permitWordList() string {
	var w []string
	for n := range permitWords {
		w = append(w, n)
	}
	sort.Strings(w)
	return strings.Join(w, ", ")
}

// checkPermits checks every allow permission header. It runs after the
// object and item headers, so a header may come before the binding it names.
func (c *checker) checkPermits() error {
	for _, a := range c.s.Permits {
		for _, n := range a.Names {
			switch {
			case n.Text == "debit":
				return c.err(n.Span, "a permission never grants debit; pay with allow pay and --pay")
			case permitWords[n.Text] == 0:
				return c.err(n.Span, "%s is not a permission a file can name (one of %s)", n.Text, permitWordList())
			}
		}
		if !c.declared[a.From.Text] {
			if _, ok := c.items[a.From.Text]; !ok {
				return c.err(a.From.Span, "%s is not an object or an item", a.From.Text)
			}
		}
	}
	return nil
}

// permitMask is the bits the file allows for the object id q.Object. The
// object is matched by id against the prims of a binding, never by name: an
// object's name can be anybody's.
func (r *runner) permitMask(q *sl.Permission) sl.Perms {
	var mask sl.Perms
	for _, a := range r.s.Permits {
		if !r.requesterIs(a.From.Text, q) {
			continue
		}
		for _, n := range a.Names {
			mask |= permitWords[n.Text]
		}
	}
	return mask &^ neverGranted
}

// requesterIs says whether q comes from the object or item name: a prim of
// the object's linkset, or of an object the run wore or rezzed from the
// item, whose binding records the item.  An object a product's script
// rezzed (expect rez) is not the run's, and is not matched.
func (r *runner) requesterIs(name string, q *sl.Permission) bool {
	if b := r.lookup(name); b != nil && b.owns(q.Object) {
		return true
	}
	it := r.itemHdr[name]
	if it == nil {
		return false
	}
	match := func(b *binding) bool {
		id, _ := r.itemOf(b)
		return id == it.ID && b.owns(q.Object)
	}
	for _, b := range r.bind {
		if match(b) {
			return true
		}
	}
	if r.cur != nil {
		for _, b := range r.cur.as {
			if match(b) {
				return true
			}
		}
	}
	return false
}

// grantedText is the transcript line of a partial or full grant.
func grantedText(who string, granted, wanted sl.Perms) string {
	s := fmt.Sprintf("permission granted to %s: %s", who, granted)
	if rest := wanted &^ granted; rest != 0 {
		s += fmt.Sprintf("; refused: %s", rest)
	}
	return s
}
