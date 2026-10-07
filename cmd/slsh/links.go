package main

// links asks an object for the region's own numbering of its prims, by a
// script it drops into the root, and sets it beside the numbering the
// store read off the packets.
//
//	links OBJECT
//
// The script is the one LinkMap uses: it says each link's number, key and
// name to the owner and removes itself, so the avatar must own the object
// and be allowed to modify it, and one that is not is refused with a
// sentence before anything is dropped.  What comes back is the truth the
// region counts by, and the store is told it (ConfirmLinkOrder), which is
// the way a set the packets could not vouch for becomes known.
// Why: doc/scripts.md#the-links-of-an-object-from-its-own-script

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var linksCommands = map[string]*command{
	"links": {
		params:   "OBJECT",
		flags:    func() any { return new(linksFlags) },
		brief:    "the link number of each prim of an object as its own script counts them, beside the store's",
		keywords: "link numbers numbering order prims children root llgetlinknumber script count verify check which",
		man:      "links",
		run:      cmdLinks,
	},
}

type linksFlags struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to let the region describe itself [30]"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

// cmdLinks prints the object's own numbering beside the store's, says
// whether they agree, and gives the store the object's.
func cmdLinks(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o linksFlags
	args, done, err := subOptions("links", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return usageError("links")
	}
	obj, err := sh.objectNamed(ctx, args[0], o.Wait)
	if err != nil {
		return err
	}
	before, err := sh.s.Backend().Objects(ctx, "", "")
	if err != nil {
		return err
	}
	top := seenByID(before, obj.ID)
	if top == nil {
		return fmt.Errorf("%s is not in the region, or is beyond the draw distance", obj)
	}
	if !top.IsRootIn(before) {
		return fmt.Errorf("%s is a prim of another object, linked under local id %d; name the root: its link numbers are the whole object's", obj, top.Parent)
	}

	started := time.Now()
	lm, err := sh.s.LinkMap(ctx, &top.Object)
	switch {
	case errors.Is(err, sl.ErrCannotModify):
		return fmt.Errorf("%s cannot be asked for its own numbering (%v).  A script cannot be put in an object "+
			"this avatar may not modify, so the order of its prims is the store's best reading from the "+
			"packets, which `objects --how` shows", obj.Name, strings.TrimPrefix(err.Error(), "sl: "))
	case err != nil:
		return err
	}

	// The store's numbering as it stood, read before the object's own is
	// given to it.
	store := map[msg.UUID]int{}
	for _, s := range before {
		if s.Parent == top.Local || s.ID == top.ID {
			store[s.ID] = s.LinkNumber
		}
	}
	known := top.LinkKnown

	name := top.Name
	if name == "" {
		name = top.ID.String()
	}
	fmt.Fprintf(out, "%s: %s, as the object's own script numbered them (%.1f s)\n",
		name, links(len(lm.Links)), time.Since(started).Seconds())
	fmt.Fprintf(out, "  %-4s  %-36s  %-28s  %s\n", "link", "key", "name", "store")
	var differ []int
	missing := 0
	for _, e := range lm.Links {
		n, seen := store[e.Key]
		col := "-"
		if seen {
			col = fmt.Sprint(n)
			if n != e.Number {
				col += "  differs"
				differ = append(differ, e.Number)
			}
		} else {
			col += "  not seen by the store"
			missing++
		}
		fmt.Fprintf(out, "  %-4d  %-36s  %-28s  %s\n", e.Number, e.Key, e.Name, col)
	}
	if lm.Seated > 0 {
		fmt.Fprintf(out, "  %s, numbered after the prims and not listed\n", sitting(lm.Seated))
	}
	for _, w := range lm.Warnings {
		fmt.Fprintf(out, "warning: %s\n", w)
	}

	switch {
	case missing > 0:
		fmt.Fprintf(out, "verdict: the store has not seen %s of this object's prims yet, so it cannot be compared or told\n", links(missing))
		return nil
	case len(differ) > 0:
		sort.Ints(differ)
		fmt.Fprintf(out, "verdict: the store's order differs from the object's own at %s; the packets said otherwise\n", linkWords(differ))
	case known:
		fmt.Fprintln(out, "verdict: the store's order agrees with the object's own, and the store had it as known")
	default:
		fmt.Fprintln(out, "verdict: the store's order agrees with the object's own, though the store had it as not known")
	}

	c, err := sh.s.ConfirmLinkOrder(ctx, &top.Object, lm)
	switch {
	case errors.Is(err, sl.ErrNotSupported):
		fmt.Fprintln(out, "the store was not told: this daemon is older than the call")
	case err != nil:
		fmt.Fprintf(out, "the store was not told: %v\n", err)
	case c.Corrected:
		fmt.Fprintf(out, "the store now takes the object's order, which it had other prims at %s\n", linkWords(c.Moved))
	default:
		fmt.Fprintln(out, "the store now has this order confirmed by the object's own script")
	}
	return nil
}

func seenByID(all []*sl.Seen, id msg.UUID) *sl.Seen {
	for _, s := range all {
		if s.ID == id {
			return s
		}
	}
	return nil
}

func links(n int) string {
	if n == 1 {
		return "1 link"
	}
	return fmt.Sprintf("%d links", n)
}

func sitting(n int) string {
	if n == 1 {
		return "1 avatar sits on it"
	}
	return fmt.Sprintf("%d avatars sit on it", n)
}

// linkWords is link numbers as a phrase: "link 2", "links 2 and 4",
// "links 2, 3 and 5".
func linkWords(ns []int) string {
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
