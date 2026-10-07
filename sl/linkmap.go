package sl

// The region's own numbering of an object's links, read from a script
// dropped into its root that says each link's number, key and name and
// removes itself.
// Why: doc/scripts.md#the-links-of-an-object-from-its-own-script

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// linkMapName is the inventory item the script lives in, made on first use
// and kept, and what the copy inside an object is called.  A second drop
// into an object that still holds one is numbered by the object
// ("slgo linkmap 1"), which is why the clean-up matches a number after it.
const linkMapName = "slgo linkmap"

// linkMapVersion is what the item's description says of the source it
// holds.  A copy whose description is another is stale: it is saved over,
// in place, and the description set again once it has compiled.  Raise it
// when linkMapSource changes.
const linkMapVersion = "slgo linkmap v2"

// linkMapSource is the script.  It says to the owner only, one line for
// each prim by link number (0 for a prim that is not linked; link 1 up to
// llGetNumberOfPrims otherwise), then one that ends it, and removes
// itself.  Seated avatars come after the prims in the region's numbering
// and are left out of the lines, counted in the last, which also says how
// many link lines were said before it: the number a reader waits for.
// Nothing else is said.
const linkMapSource = `// slgo linkmap v2
// Says each link of this object to its owner, then removes itself.
default
{
    state_entry()
    {
        integer n = llGetNumberOfPrims();
        integer i = 1;
        integer last = n;
        integer seated = 0;
        integer sent = 0;
        if (n == 1) { i = 0; last = 0; }
        for (; i <= last; ++i)
        {
            key k = llGetLinkKey(i);
            if (llGetAgentSize(k) == ZERO_VECTOR)
            {
                llOwnerSay("LINKMAP " + (string)i + " " + (string)k + " " + llGetLinkName(i));
                ++sent;
            }
            else
                ++seated;
        }
        llOwnerSay("LINKMAP done " + (string)sent + " " + (string)seated);
        llRemoveInventory(llGetScriptName());
    }
}
`

// linkMapGone is how long the script is given to have removed itself
// before it is taken out, and again after that is asked for.  A test that
// proves one that does not shortens it.
var linkMapGone = 5 * time.Second

// ErrCannotModify is why LinkMap did not drop anything: the object is not
// one the tester may put a script in, and hear it from.  The script goes
// into the root, which takes modify rights on it, and says to its owner,
// who is the only one who would hear it.
var ErrCannotModify = errors.New("sl: the object cannot be modified by this avatar")

// ErrScriptsStopped is why LinkMap did not drop anything: the land or the
// region will not run the script.  See ScriptsBlocked.
var ErrScriptsStopped = errors.New("sl: scripts do not run there")

// LinkEntry is one link of an object as the region numbers it.
type LinkEntry struct {
	// Number is the link number: 0 for an object of one prim that nothing
	// sits on, and 1 for the root otherwise.
	Number int
	Key    msg.UUID
	Name   string

	// Local is the prim's local id when the store knows its key, or zero.
	Local uint32
}

// LinkMap is an object's links as its own script numbered them.
type LinkMap struct {
	// Root is the object the script was dropped into.
	Root msg.UUID

	// Links are the prims, by link number, starting at the root.  Avatars
	// sitting on the object are not here: the region numbers them after
	// the prims, whatever order the prims are in, and Seated counts them.
	Links  []LinkEntry
	Seated int

	// Taken is when the script's last line was heard.
	Taken time.Time

	// Warnings is what went wrong around the map without spoiling it: a
	// script that did not remove itself and was taken out, or could not
	// be.
	Warnings []string
}

// PrimKeys are the prims' keys in link order, the root first.
func (m LinkMap) PrimKeys() []msg.UUID {
	out := make([]msg.UUID, len(m.Links))
	for i, l := range m.Links {
		out[i] = l.Key
	}
	return out
}

// NumberOf is the link number of the prim with this key.
func (m LinkMap) NumberOf(key msg.UUID) (int, bool) {
	for _, l := range m.Links {
		if l.Key == key {
			return l.Number, true
		}
	}
	return 0, false
}

// NumberOfLocal is the link number of the prim with this local id, where
// the store knew the key.
func (m LinkMap) NumberOfLocal(local uint32) (int, bool) {
	for _, l := range m.Links {
		if l.Local != 0 && l.Local == local {
			return l.Number, true
		}
	}
	return 0, false
}

// MayModify says whether the avatar may put a script in the object, and
// when not, why: the viewer's rule, with the masks the object's properties
// give for the avatar, for its group and for everyone.  It asks the
// region for the properties, which selects the object, and deselects it
// again.
func (w *Session) MayModify(ctx context.Context, o *Object) (bool, string, error) {
	p, err := w.Properties(ctx, o, w.linkMapWait())
	if err != nil {
		return false, "", err
	}
	// Selecting is how the properties are asked for; leaving the object
	// selected is not part of the question.
	if local, lerr := w.local(ctx, o); lerr == nil {
		_ = w.Send(ctx, w.deselectMsg(local))
	}
	group, _ := w.ActiveGroup(ctx)
	ok, why := mayModify(p, w.me, group)
	return ok, why, nil
}

// mayModify is the rule: modify rights from the owner's mask for an
// object the avatar owns, the group's for one of its active group, and
// everyone's otherwise.
func mayModify(p *Properties, me, group msg.UUID) (bool, string) {
	switch {
	case p.Owner == me:
		if p.OwnerMask&PermModify == 0 {
			return false, "its permissions do not let the owner modify it"
		}
		return true, ""
	case !group.IsZero() && p.Group == group && p.GroupMask&PermModify != 0:
		return true, ""
	case p.EveryoneMask&PermModify != 0:
		return true, ""
	}
	return false, "this avatar neither owns it nor may modify it"
}

// linkMapItem finds the script in inventory, making it on first use and
// saving it over when it is another version's.
func (w *Session) linkMapItem(ctx context.Context) (*Item, error) {
	folder, err := w.FolderOfType(ctx, FolderTypeOf(AssetLSLText))
	if err != nil {
		return nil, fmt.Errorf("sl: the folder for scripts: %w", err)
	}
	items, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, err
	}
	var found []*Item
	for _, it := range items {
		if it.Name == linkMapName && IsScript(it) {
			found = append(found, it)
		}
	}
	switch len(found) {
	case 0:
		return w.makeLinkMapItem(ctx)
	case 1:
		it := found[0]
		if it.Desc == linkMapVersion {
			return it, nil
		}
		return w.saveLinkMapItem(ctx, it)
	}
	ids := make([]string, len(found))
	for i, it := range found {
		ids[i] = it.ID.String()
	}
	return nil, fmt.Errorf("sl: %d items in the scripts folder are named %q (%s); keep one", len(found), linkMapName, strings.Join(ids, ", "))
}

// makeLinkMapItem creates the item with a description that does not yet
// claim the current source, so that one that failed to compile is stale.
func (w *Session) makeLinkMapItem(ctx context.Context) (*Item, error) {
	it, err := w.CreateItem(ctx, linkMapName, "slgo linkmap, not compiled", int8(AssetLSLText), int8(AssetLSLText))
	if err != nil {
		return nil, err
	}
	return w.saveLinkMapItem(ctx, it)
}

// saveLinkMapItem saves the source into the item and, once it has
// compiled, says in the description that it holds this version.
func (w *Session) saveLinkMapItem(ctx context.Context, it *Item) (*Item, error) {
	res, err := w.SaveScript(ctx, it.ID, linkMapSource)
	if err != nil {
		return nil, fmt.Errorf("sl: saving %q: %w", linkMapName, err)
	}
	if !res.Compiled {
		return nil, fmt.Errorf("sl: %q did not compile: %s", linkMapName, strings.Join(res.Errors, "; "))
	}
	got, err := w.SetItem(ctx, it.ID, linkMapName, linkMapVersion, nil)
	if err != nil {
		return nil, fmt.Errorf("sl: marking %q as %s: %w", linkMapName, linkMapVersion, err)
	}
	return got, nil
}

// LinkMap has the region number an object's links, by a script of its own
// that it drops into the root and that removes itself: each link's number,
// key and name, as llGetLinkKey and llGetLinkName say them.
//
// The avatar must own the object and be allowed to modify it, or
// ErrCannotModify is returned before anything is dropped; a script is not
// put into an object that may not be modified, and what it says goes to
// the object's owner.  Land that will not run scripts is refused the same
// way, with ErrScriptsStopped.  The script is kept in the avatar's
// inventory, made on first use, so a map costs a drop and about a second,
// not an upload.  Its lines are heard from the object's root only, on
// owner chat, and a map not complete when LinkMapTimeout is up is an
// ErrTimeout saying how many links were heard, and of how many when the end
// was.  The end line names how many link lines were sent and the reader
// holds on for exactly that many, however late, under the one timeout.  The
// object's own contents are read afterwards and a script that did not
// remove itself is removed.
// Why: doc/scripts.md#the-links-of-an-object-from-its-own-script
func (w *Session) LinkMap(ctx context.Context, o *Object) (LinkMap, error) {
	var m LinkMap
	if o == nil {
		return m, errors.New("sl: no object to map the links of")
	}
	if ok, why, err := w.MayModify(ctx, o); err != nil {
		return m, fmt.Errorf("sl: the permissions of %s: %w", o, err)
	} else if !ok {
		return m, fmt.Errorf("%w: %s", ErrCannotModify, why)
	}
	if why := w.ScriptsBlocked(ctx, o); why != "" {
		return m, fmt.Errorf("%w: %s", ErrScriptsStopped, why)
	}
	item, err := w.linkMapItem(ctx)
	if err != nil {
		return m, err
	}

	// Listening starts before the drop: the script speaks as it starts.
	// The root's own speech on owner chat is all that is heard; the depth
	// leaves room for every prim of the largest linkset and the end.
	ch := w.Chat(ChatFilter{Source: o.ID, Types: []uint8{ChatOwner}}, 1100)
	defer w.StopChat(ch)
	if err := w.PutScriptInObject(ctx, o, item, true); err != nil {
		return m, err
	}
	m, heardErr := w.hearLinkMap(ctx, ch, o)
	if m.Root.IsZero() {
		m.Root = o.ID
	}
	// Whatever was heard, the object is left as it was found.
	m.Warnings = append(m.Warnings, w.clearLinkMap(ctx, o)...)
	if heardErr != nil {
		return m, heardErr
	}
	w.fillLocals(ctx, &m)
	return m, nil
}

// hearLinkMap reads the script's lines from the object until it holds as
// many link lines as the end line says were sent, or the time is up.  The
// end may come before any of them and the lines in any order: nothing is
// assumed of how late they are, only that the one timeout bounds the lot.
func (w *Session) hearLinkMap(ctx context.Context, ch <-chan Line, o *Object) (LinkMap, error) {
	m := LinkMap{Root: o.ID}
	timeout := w.linkMapWait()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	byNumber := map[int]LinkEntry{}
	var ended bool
	var sent, seated int
	var endAt time.Time
	for !ended || len(byNumber) < sent {
		select {
		case l, ok := <-ch:
			if !ok {
				return m, errors.New("sl: the session ended while the links were being read")
			}
			kind, e, n, sat, err := parseLinkMapLine(l.Text)
			switch {
			case err != nil || kind == lineOther:
				// A line of the object that is not the script's is not
				// its business.
			case kind == lineLink:
				if prev, dup := byNumber[e.Number]; dup && prev.Key != e.Key {
					return m, fmt.Errorf("sl: the script of %s gave link %d two keys", o, e.Number)
				}
				byNumber[e.Number] = e
			case kind == lineDone:
				ended, sent, seated, endAt = true, n, sat, l.At
			}
		case <-deadline.C:
			if dropped := w.ChatDropped(ch); dropped > 0 {
				return m, fmt.Errorf("sl: %d lines of the script of %s were lost to a full buffer", dropped, o)
			}
			switch {
			case ended:
				return m, fmt.Errorf("%w: the script of %s said it sent %d link lines and %d were heard (after %s)", ErrTimeout, o, sent, len(byNumber), timeout)
			case len(byNumber) == 0:
				return m, fmt.Errorf("%w: no line from the script in %s (after %s); it may not have run", ErrTimeout, o, timeout)
			}
			return m, fmt.Errorf("%w: %d links of %s were heard and not the end of the script (after %s)", ErrTimeout, len(byNumber), o, timeout)
		case <-ctx.Done():
			return m, ctx.Err()
		}
	}
	return w.finishLinkMap(m, o, byNumber, sent, seated, endAt)
}

// finishLinkMap checks that what was heard is every prim, once, by the
// numbers the script counted, and puts it in order.  A set of one prim
// with nobody sitting is link 0, and any other set starts at link 1.
func (w *Session) finishLinkMap(m LinkMap, o *Object, got map[int]LinkEntry, sent, seated int, at time.Time) (LinkMap, error) {
	first := 1
	if sent == 1 && seated == 0 {
		first = 0
	}
	if len(got) != sent {
		return m, fmt.Errorf("sl: the script of %s sent %d link lines and gave %d links", o, sent, len(got))
	}
	for i := 0; i < sent; i++ {
		e, ok := got[first+i]
		if !ok {
			return m, fmt.Errorf("sl: the script of %s gave no link %d of %d", o, first+i, sent)
		}
		m.Links = append(m.Links, e)
	}
	if m.Links[0].Key != o.ID {
		return m, fmt.Errorf("sl: link %d of %s is %s, not the object", first, o, m.Links[0].Key)
	}
	m.Seated, m.Taken = seated, at
	return m, nil
}

// fillLocals gives each entry the local id the store has for its key.
func (w *Session) fillLocals(ctx context.Context, m *LinkMap) {
	all, err := w.fetch(ctx, "", "")
	if err != nil {
		return
	}
	locals := make(map[msg.UUID]uint32, len(all))
	for _, s := range all {
		locals[s.ID] = s.Local
	}
	for i := range m.Links {
		m.Links[i].Local = locals[m.Links[i].Key]
	}
}

// clearLinkMap makes sure no copy of the script is left in the object: it
// is given a moment to have removed itself, and a copy still there is
// removed.  What could not be done is returned as warnings.
func (w *Session) clearLinkMap(ctx context.Context, o *Object) []string {
	// On a context of its own: a run that ended because its caller gave
	// up is the one most likely to have left the script behind.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*linkMapGone)
	defer cancel()
	var left []TaskItem
	look := func(ctx context.Context) (bool, error) {
		items, err := w.TaskInventory(ctx, o)
		if err != nil {
			return false, err
		}
		left = left[:0]
		for _, it := range items {
			if isLinkMapCopy(it.Name) {
				left = append(left, it)
			}
		}
		return len(left) == 0, nil
	}
	if err := poll(ctx, linkMapGone, 500*time.Millisecond, "the script to remove itself", look); err == nil {
		return nil
	} else if len(left) == 0 {
		return []string{fmt.Sprintf("could not read what %s holds, so that the script is gone is not confirmed: %v", o, err)}
	}
	var warns []string
	for _, it := range left {
		if err := w.RemoveFromObject(ctx, o, it.ID); err != nil {
			warns = append(warns, fmt.Sprintf("%q could not be removed from %s: %v", it.Name, o, err))
		}
	}
	if err := poll(ctx, linkMapGone, 500*time.Millisecond, "the script to go", look); err != nil {
		warns = append(warns, fmt.Sprintf("%q may still be in %s: %v", linkMapName, o, err))
		return warns
	}
	return append(warns, fmt.Sprintf("the script did not remove itself from %s and was removed", o))
}

// isLinkMapCopy says whether an item of an object is a copy of the script:
// its name, or the name an object gives a second one.
func isLinkMapCopy(name string) bool {
	if name == linkMapName {
		return true
	}
	n, ok := strings.CutPrefix(name, linkMapName+" ")
	if !ok {
		return false
	}
	_, err := strconv.Atoi(n)
	return err == nil
}

type linkMapLineKind int

const (
	lineOther linkMapLineKind = iota
	lineLink
	lineDone
)

// parseLinkMapLine reads one line of the script's: "LINKMAP 3 <key>
// <name>" for a prim, "LINKMAP done <link lines sent> <seated>" for the
// end.  The name is the rest of the line and may hold spaces, or be empty.
func parseLinkMapLine(text string) (kind linkMapLineKind, e LinkEntry, sent, seated int, err error) {
	rest, ok := strings.CutPrefix(text, "LINKMAP ")
	if !ok {
		return lineOther, e, 0, 0, nil
	}
	f := strings.SplitN(rest, " ", 3)
	if f[0] == "done" {
		g := strings.Fields(rest)
		if len(g) != 3 {
			return lineOther, e, 0, 0, fmt.Errorf("sl: %q is not the end of a link map", text)
		}
		sent, err = strconv.Atoi(g[1])
		if err == nil {
			seated, err = strconv.Atoi(g[2])
		}
		if err != nil || sent < 1 || seated < 0 {
			return lineOther, e, 0, 0, fmt.Errorf("sl: %q is not the end of a link map", text)
		}
		return lineDone, e, sent, seated, nil
	}
	if len(f) < 2 {
		return lineOther, e, 0, 0, fmt.Errorf("sl: %q is not a link", text)
	}
	n, nerr := strconv.Atoi(f[0])
	key, kerr := msg.ParseUUID(f[1])
	if nerr != nil || kerr != nil || n < 0 {
		return lineOther, e, 0, 0, fmt.Errorf("sl: %q is not a link", text)
	}
	e = LinkEntry{Number: n, Key: key}
	if len(f) == 3 {
		e.Name = f[2]
	}
	return lineLink, e, 0, 0, nil
}
