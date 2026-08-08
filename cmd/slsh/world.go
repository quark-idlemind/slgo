package main

// What is outside inventory: where the avatar is, who else is there,
// and what the simulator will tell you about itself.

import (
	"context"
	"fmt"
	"io"
	"sort"
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
	n := 0
	for _, o := range all {
		if o.IsAvatar() {
			continue
		}
		if want != "" && !strings.Contains(strings.ToLower(o.Name), want) {
			continue
		}
		fmt.Fprintf(out, "%-36s %-28s %.0f, %.0f, %.0f\n",
			o.ID, o.Name, o.Position.X, o.Position.Y, o.Position.Z)
		n++
	}
	if n == 0 {
		fmt.Fprintln(out, "nothing matched")
	}
	return nil
}
