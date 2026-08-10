package main

// What is outside inventory: where the avatar is, who else is there,
// and what the simulator will tell you about itself.

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var worldCommands = map[string]*command{
	"where": {
		usage: "where",
		brief: "the region and position this avatar is at",
		run:   cmdWhere,
	},
	"who": {
		usage: "who",
		brief: "who else is in the region, nearest first",
		run:   cmdWho,
	},
	"look": {
		usage: "look",
		brief: "what the simulator said about the region",
		run:   cmdLook,
	},
	"caps": {
		usage: "caps [TEXT]",
		brief: "the capabilities this session was granted",
		run:   cmdCaps,
	},
	"features": {
		usage: "features [TEXT]",
		brief: "what the simulator says it supports",
		run:   cmdFeatures,
	},
	"lsl": {
		usage: "lsl [-fcetam] [TEXT]",
		brief: "the LSL this simulator implements: functions, constants, events, types",
		run:   cmdLSL,
	},
	"worn": {
		usage: "worn [-l] [TEXT]",
		brief: "the objects being worn, and where; -l for the ids",
		run:   cmdWorn,
	},
	"objects": {
		usage: "objects [TEXT]",
		brief: "the objects the region has described, by name",
		run:   cmdObjects,
	},
}

func cmdWhere(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("where", "", &o, out, args)
	if err != nil || done {
		return err
	}
	p, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s at %.0f, %.0f, %.0f\n", p.Region, p.Position.X, p.Position.Y, p.Position.Z)
	if !p.ActiveGroup.IsZero() {
		fmt.Fprintf(out, "acting as group %s\n", p.ActiveGroup)
	}
	return nil
}

func cmdWho(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("who", "", &o, out, args)
	if err != nil || done {
		return err
	}
	ps, err := sh.s.Nearby(ctx)
	if err != nil {
		return err
	}
	if len(ps) == 0 {
		fmt.Fprintln(out, "nobody else is in range")
		return nil
	}
	listed := make([]person, 0, len(ps))
	for i, p := range ps {
		fmt.Fprintf(out, "%2d  %-32s %6.1fm  %s\n", i+1, p.Name, p.Distance, p.ID)
		listed = append(listed, person{ID: p.ID, Name: p.Name})
	}
	sh.setListed(listed)
	return nil
}

func cmdLook(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("look", "", &o, out, args)
	if err != nil || done {
		return err
	}
	r, err := sh.s.Region(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s\n", r.Name)
	fmt.Fprintf(out, "  id       %s\n", r.ID)
	fmt.Fprintf(out, "  owner    %s\n", r.Owner)
	fmt.Fprintf(out, "  access   %d\n", r.Access)
	fmt.Fprintf(out, "  water    %.1fm\n", r.WaterHeight)
	fmt.Fprintf(out, "  product  %s\n", r.ProductName)
	if n, err := sh.s.Known(ctx); err == nil {
		fmt.Fprintf(out, "  objects  %d described so far\n", n)
	}
	return nil
}

func cmdCaps(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("caps", "[TEXT]", &o, out, args)
	if err != nil || done {
		return err
	}
	want := ""
	if len(args) > 0 {
		want = strings.ToLower(args[0])
	}
	caps := append([]string(nil), sh.s.Info().Caps...)
	sort.Strings(caps)
	for _, c := range caps {
		if want == "" || strings.Contains(strings.ToLower(c), want) {
			fmt.Fprintln(out, c)
		}
	}
	return nil
}

func cmdFeatures(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("features", "[TEXT]", &o, out, args)
	if err != nil || done {
		return err
	}
	f, err := sh.s.Features(ctx)
	if err != nil {
		return err
	}
	want := ""
	if len(args) > 0 {
		want = strings.ToLower(args[0])
	}
	for _, n := range f.Names() {
		if want != "" && !strings.Contains(strings.ToLower(n), want) {
			continue
		}
		switch v := f.Raw[n].(type) {
		case map[string]any:
			keys := make([]string, 0, len(v))
			for k := range v {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Fprintf(out, "%-30s %v\n", n, keys)
		default:
			fmt.Fprintf(out, "%-30s %v\n", n, v)
		}
	}
	return nil
}

// wornOptions is what worn was asked for.
type wornOptions struct {
	Long bool `getopt:"-l          the item and object ids as well"`
	Help bool `getopt:"--help -h   show what this command takes"`
}

// cmdWorn lists the attachments.
//
// slgod knows these because it was connected when they were described:
// an attachment is announced when it goes on and again at every login,
// so a program that started later never heard it and has to ask.
//
// The names come from inventory, not from the objects.  A worn object
// will not answer a request for its properties, and the name worth
// printing is the one in inventory anyway -- it is what the thing is
// called, and unlike the object it does not change.
func cmdWorn(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o wornOptions
	rest, done, err := subOptions("worn", "[TEXT]", &o, out, args)
	if err != nil || done {
		return err
	}
	want := ""
	if len(rest) > 0 {
		want = strings.ToLower(strings.Join(rest, " "))
	}

	worn, err := sh.s.WornObjects(ctx)
	if err != nil {
		return err
	}
	names := sh.itemNames(ctx)

	type row struct {
		point int
		name  string
		item  msg.UUID
		obj   msg.UUID
	}
	rows := make([]row, 0, len(worn))
	for _, a := range worn {
		// Not everything worn can be named.  The item may sit deeper
		// than the listing went, or have been deleted while still
		// worn, which Second Life allows.  The item id is then the
		// only handle there is, so print that rather than a word that
		// claims to know more.
		name, ok := names[a.Item]
		if !ok {
			name = a.Item.String()
		}
		if want != "" && !strings.Contains(strings.ToLower(name), want) {
			continue
		}
		rows = append(rows, row{a.Point, name, a.Item, a.Object.ID})
	}
	// By where they are worn, so the HUD ones group together.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].point != rows[j].point {
			return rows[i].point < rows[j].point
		}
		return rows[i].name < rows[j].name
	})

	if len(rows) == 0 {
		if want != "" {
			fmt.Fprintln(out, "nothing worn matched")
		} else {
			fmt.Fprintln(out, "nothing worn")
		}
		return nil
	}
	for _, r := range rows {
		if o.Long {
			// The item first: it is the one that does not change.  A
			// worn object is rezzed afresh, with a new key, every time
			// it goes on and every time the avatar logs in.
			fmt.Fprintf(out, "%-18s %-30s %-36s %s\n",
				sl.AttachPointName(r.point), r.name, r.item, r.obj)
			continue
		}
		fmt.Fprintf(out, "%-18s %s\n", sl.AttachPointName(r.point), r.name)
	}
	return nil
}

// itemNames maps inventory item ids to their names, for naming things
// that are known only by id.
//
// Depth-limited, because the whole tree is a hundred requests and this
// is wanted for a listing of eight things.  Anything not found is
// reported as such rather than guessed at.
func (sh *Shell) itemNames(ctx context.Context) map[msg.UUID]string {
	out := map[msg.UUID]string{}
	es, err := sh.s.ListInventory(ctx, "", 4)
	if err != nil {
		return out
	}
	for _, e := range es {
		if !e.Folder {
			out[e.ID] = e.Name
		}
	}
	return out
}

func cmdObjects(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("objects", "[TEXT]", &o, out, args)
	if err != nil || done {
		return err
	}
	all, err := sh.s.AllObjects(ctx, 30*time.Second)
	if err != nil {
		return err
	}
	want := ""
	if len(args) > 0 {
		want = strings.ToLower(args[0])
	}
	wearers := whoWears(all)
	if len(wearers) > 0 {
		// One request for all of them: an attachment says which avatar
		// it hangs off, and an id is a poor way to say whose it is.
		sh.s.Names(ctx, namesWanted(wearers), 5*time.Second)
	}

	n := 0
	for _, o := range all {
		if o.IsAvatar() {
			continue
		}
		if want != "" && !strings.Contains(strings.ToLower(o.Name), want) {
			continue
		}
		// Only a root prim standing in the region has a position that
		// is a place.  A child's is an offset from its root, and an
		// attachment's is an offset from the avatar -- printing either
		// as coordinates is three numbers that mean nothing.
		where := fmt.Sprintf("%.0f, %.0f, %.0f", o.Position.X, o.Position.Y, o.Position.Z)
		switch who, worn := wearers[o.Local]; {
		case worn:
			whose := sh.s.NameOr(who.avatar)
			where = "worn by " + whose
			if who.point != 0 {
				where = "worn on " + sl.AttachPointName(who.point) + ", " + whose
			}
		case o.Parent != 0:
			where = "linked, offset " + offsetOf(o.Position)
		}
		fmt.Fprintf(out, "%-36s %-28s %s\n", o.ID, o.Name, where)
		n++
	}
	if n == 0 {
		fmt.Fprintln(out, "nothing matched")
	}
	return nil
}

// offsetOf writes an offset to a tenth of a metre.
//
// A region coordinate is worth a whole metre and no more, but an offset
// of a tenth is a real difference between one prim and the next, and
// rounding those to metres printed a linkset as a column of zeros.
func offsetOf(v msg.Vector3) string {
	return tenth(v.X) + ", " + tenth(v.Y) + ", " + tenth(v.Z)
}

func tenth(f float32) string {
	r := math.Round(float64(f)*10) / 10
	if r == 0 {
		// Negative zero prints as "-0", which is a distinction
		// without a difference.
		r = 0
	}
	return strconv.FormatFloat(r, 'g', -1, 64)
}

// wearer is the avatar an attachment hangs off, and where on them.
type wearer struct {
	avatar msg.UUID
	point  int
}

// whoWears works out which of these objects are attachments, by local
// id.
//
// Being worn is not a property of the object alone: what says so is the
// avatar it is parented to.  A linked attachment is parented to its own
// root rather than to the avatar, so this walks up until it finds one
// -- otherwise every prim of a linked hud but the root would be listed
// as if it were standing in the region.
//
// The store holds everyone's attachments, not only ours, since that is
// how a viewer draws other people; so the answer is whose, not whether.
func whoWears(all []*sl.Seen) map[uint32]wearer {
	byLocal := make(map[uint32]*sl.Seen, len(all))
	for _, o := range all {
		byLocal[o.Local] = o
	}
	out := map[uint32]wearer{}
	for _, o := range all {
		if o.IsAvatar() || o.Parent == 0 {
			continue
		}
		point := o.AttachPoint
		for up, at := 0, byLocal[o.Parent]; at != nil && up < 8; up, at = up+1, byLocal[at.Parent] {
			if at.IsAvatar() {
				out[o.Local] = wearer{avatar: at.ID, point: point}
				break
			}
			if point == 0 {
				point = at.AttachPoint
			}
			if at.Parent == 0 {
				break
			}
		}
	}
	return out
}

func namesWanted(wearers map[uint32]wearer) []msg.UUID {
	seen := map[msg.UUID]bool{}
	var ids []msg.UUID
	for _, w := range wearers {
		if !seen[w.avatar] {
			seen[w.avatar] = true
			ids = append(ids, w.avatar)
		}
	}
	return ids
}
