package slate

// The link order of an object a step addresses with link N, made certain
// by the region's own count when the file asks for it with a linkmap
// header: a script dropped into the object's root says each link's number
// and key, and the store takes that over its reading of the packets.  The
// drop and the script's removal are two changes of the object's inventory,
// which the product under test hears, so nothing is dropped unless the
// file says so.  Every other object a step addresses with link N keeps the
// store's best reading, and the transcript says so once.  An object with a
// probe is not asked and not told: the probe's hello map is already the
// script's own count, link by link.
// Why: doc/slate-runner.md#link-order-from-the-objects-own-script

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// defaultLinkConfirm is how long the store is given to have seen every
// prim of a set the script numbered: a rezzed or worn object's children
// are described a little after the root.
const defaultLinkConfirm = 10 * time.Second

// linkUsers is the names a step addresses with link N, anywhere in the
// file: a touch, a drag, an expectation or a speaker that names an object
// and a link.
func linkUsers(s *Script) map[string]bool {
	users := map[string]bool{}
	walkLinks(reflect.ValueOf(s.Tests), users)
	walkLinks(reflect.ValueOf(s.Befores), users)
	walkLinks(reflect.ValueOf(s.Afters), users)
	walkLinks(reflect.ValueOf(s.Sequences), users)
	return users
}

var intPtr = reflect.TypeOf((*Int)(nil))

// walkLinks adds the name of every node that has a Name and a Link N.
// The syntax tree is walked as values: the nodes that take a link are a
// score of unrelated structs, and the field names are what they share.
func walkLinks(v reflect.Value, out map[string]bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			walkLinks(v.Elem(), out)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			walkLinks(v.Index(i), out)
		}
	case reflect.Struct:
		if l := v.FieldByName("Link"); l.IsValid() && l.Type() == intPtr && !l.IsNil() {
			if n := v.FieldByName("Name"); n.IsValid() && n.Type() == reflect.TypeOf(Ident{}) {
				out[n.FieldByName("Text").String()] = true
			}
		}
		for i := 0; i < v.NumField(); i++ {
			walkLinks(v.Field(i), out)
		}
	}
}

// setupLinkOrder takes the link order of each header object a linkmap
// header names, once per object, and says of every other object a step
// addresses with link N that its order is the store's reading.
func (r *runner) setupLinkOrder(ctx context.Context) error {
	users := linkUsers(r.s)
	asked := map[string]bool{}
	for _, l := range r.s.LinkMaps {
		asked[l.Name.Text] = true
	}
	for _, o := range r.s.Objects {
		name := o.Name.Text
		if !users[name] {
			continue
		}
		b := r.bind[name]
		if b == nil {
			continue
		}
		if !asked[name] {
			r.noteStoreOrder(b)
			continue
		}
		if err := r.confirmLinkOrder(ctx, b); err != nil {
			return err
		}
	}
	return nil
}

// noteStoreOrder says, once for each binding, that a binding a step
// addresses with link N and that nothing asked the object about is
// numbered by the store: a binding made by rez or wear, never one a
// linkmap names, and none with a probe.
func (r *runner) noteStoreOrder(b *binding) {
	if r.linkUse == nil {
		r.linkUse = linkUsers(r.s)
	}
	if !r.linkUse[b.name] || r.probed(b) || r.linkSaid[b.name] {
		return
	}
	if r.linkSaid == nil {
		r.linkSaid = map[string]bool{}
	}
	r.linkSaid[b.name] = true
	r.printf("slate: link order of %s is the store's best reading from the packets (no linkmap header for it)", b.name)
}

// isLinkMapLine says a line is the link map script's, said by a root it
// was dropped into: the run asked for it and takes it from LinkMap, so it
// is neither printed nor offered to an expectation.
func (r *runner) isLinkMapLine(l sl.Line) bool {
	return l.Type == sl.ChatOwner && r.linkDone[l.Source] && strings.HasPrefix(l.Text, "LINKMAP ")
}

// confirmLinkOrder has the object's own script number its links and the
// store take that, and says in one line of the transcript what became of
// it.  The file asked for it, so an object the tester may not modify, or on
// land that runs no scripts, is a setup error: a test that wanted the
// region's count does not go on from a guess.  Whatever else goes wrong
// (a script that did not answer, a store that never saw every prim) leaves
// the store's reading standing, and the line says so.
func (r *runner) confirmLinkOrder(ctx context.Context, b *binding) error {
	if r.probed(b) {
		return nil
	}
	all, err := r.sess.Backend().Objects(ctx, "", "")
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.printf("slate: link order of %s is the store's best reading from the packets (the objects of the region could not be read: %v)", b.name, err)
		return nil
	}
	cur := byID(all, b.seen.ID)
	if cur == nil {
		return nil
	}
	members, err := linksetOf(all, cur)
	if err != nil {
		return nil
	}
	root := members[0]
	if r.linkDone[root.ID] {
		return nil
	}
	if r.linkDone == nil {
		r.linkDone = map[msg.UUID]bool{}
	}
	r.linkDone[root.ID] = true

	best := func(why string) {
		r.printf("slate: link order of %s is the store's best reading from the packets (%s)", b.name, why)
	}
	lm, err := r.linkMap(ctx, &root.Object)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, w := range lm.Warnings {
		r.printf("slate: warning: link order of %s: %s", b.name, w)
	}
	switch {
	case errors.Is(err, sl.ErrCannotModify):
		return &setupError{fmt.Sprintf("linkmap %s: the tester may not modify it, so no script can be dropped in it to say its links (%v)", b.name, err)}
	case errors.Is(err, sl.ErrScriptsStopped):
		return &setupError{fmt.Sprintf("linkmap %s: %v", b.name, err)}
	case err != nil:
		best(fmt.Sprintf("its own script could not be had: %v", err))
		return nil
	}

	c, err := r.confirmWhenSeen(ctx, root, lm)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		best(fmt.Sprintf("its own script gave an order the store could not take: %v", err))
		return nil
	}
	links := fmt.Sprintf("%d links", len(lm.Links))
	if len(lm.Links) == 1 {
		links = "1 link"
	}
	if c.Corrected {
		r.printf("slate: link order of %s corrected by its own script: the store had other prims at %s (%s)", b.name, linkList(c.Moved), links)
		return nil
	}
	r.printf("slate: link order of %s confirmed by its own script (%s)", b.name, links)
	return nil
}

// linkMap is Session.LinkMap, with the run's budget for it when it has
// one, given back as it was found.
func (r *runner) linkMap(ctx context.Context, o *sl.Object) (sl.LinkMap, error) {
	if r.cfg.linkMap > 0 {
		prev := r.sess.Options()
		capped := prev
		capped.LinkMapTimeout = r.cfg.linkMap
		r.sess.SetOptions(capped)
		defer r.sess.SetOptions(prev)
	}
	return r.sess.LinkMap(ctx, o)
}

// confirmWhenSeen gives the store the script's order, again for as long as
// it has not yet been told of every prim the script counted.
func (r *runner) confirmWhenSeen(ctx context.Context, root *sl.Seen, lm sl.LinkMap) (*sl.LinkConfirmation, error) {
	wait := r.cfg.linkConfirm
	if wait == 0 {
		wait = defaultLinkConfirm
	}
	deadline := time.Now().Add(wait)
	for {
		c, err := r.sess.ConfirmLinkOrder(ctx, &root.Object, lm)
		if err == nil || !errors.Is(err, sl.ErrLinkSetDiffers) || time.Now().After(deadline) {
			return c, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(r.cfg.objectEvery()):
		}
	}
}

// linkList is link numbers as a sentence: "link 2", "links 2 and 4",
// "links 2, 3 and 5".
func linkList(ns []int) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = fmt.Sprint(n)
	}
	switch len(s) {
	case 0:
		return "no link"
	case 1:
		return "link " + s[0]
	}
	return "links " + strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}
