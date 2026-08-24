package main

// What is outside inventory: where the avatar is, who else is there,
// and what the simulator will tell you about itself.

import (
	"context"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var worldCommands = map[string]*command{
	"where": {
		flags: func() any { return new(helpOnly) },
		brief: "the region and position this avatar is at",
		man:   "where",
		run:   cmdWhere,
	},
	"who": {
		flags: func() any { return new(helpOnly) },
		brief: "who else is in the region, nearest first",
		man:   "who",
		run:   cmdWho,
	},
	"look": {
		flags: func() any { return new(helpOnly) },
		brief: "what the simulator said about the region",
		man:   "look",
		run:   cmdLook,
	},
	"parcel": {
		params: "[X,Y]",
		flags:  func() any { return new(parcelOptions) },
		brief:  "the land under this avatar, or under a point in this region",
		man:    "parcel",
		run:    cmdParcel,
	},
	"landmark": {
		params: "[NAME]",
		flags:  func() any { return new(landmarkOptions) },
		brief:  "the landmarks in inventory, where one goes, and going there",
		man:    "landmark",
		run:    cmdLandmark,
	},
	"map": {
		flags: func() any { return new(mapOptions) },
		brief: "a picture of who is around this avatar, or of the whole region",
		man:   "map",
		run:   cmdMap,
	},
	"regions": {
		params: "NAME",
		flags:  func() any { return new(regionsOptions) },
		brief:  "find regions on the grid's map by the start of a name",
		man:    "regions",
		run:    cmdRegions,
	},
	"neighbours": {
		params: "[on|off]",
		flags:  func() any { return new(helpOnly) },
		brief:  "the circuits held to the regions around this one, which walking over a border needs",
		man:    "neighbours",
		run:    cmdNeighbours,
	},
	"caps": {
		params: "[TEXT]",
		flags:  func() any { return new(helpOnly) },
		brief:  "the capabilities this session was granted",
		man:    "caps",
		run:    cmdCaps,
	},
	"features": {
		params: "[TEXT]",
		flags:  func() any { return new(helpOnly) },
		brief:  "what the simulator says it supports",
		man:    "features",
		run:    cmdFeatures,
	},
	"lsl": {
		params: "[TEXT]",
		flags:  func() any { return new(lslOptions) },
		brief:  "the LSL this simulator implements: functions, constants, events, types",
		man:    "lsl",
		run:    cmdLSL,
	},
	"worn": {
		params: "[TEXT]",
		flags:  func() any { return new(wornOptions) },
		brief:  "the objects being worn, and where; -l for the ids",
		man:    "worn",
		run:    cmdWorn,
	},
	"objects": {
		params: "[TEXT]",
		flags:  func() any { return new(objectsOptions) },
		brief:  "the objects the region has described; -c for the prims inside each",
		man:    "objects",
		run:    cmdObjects,
	},
}

func cmdWhere(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("where", &o, out, args)
	if err != nil || done {
		return err
	}
	p, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, positionLine(p))
	if !p.ActiveGroup.IsZero() {
		// Named where a name is to be had.  The key is what everything
		// else takes and stays in the line for that reason, but it is
		// the name that says whether this is the group the land wants,
		// which is the question anybody reading this line has.
		fmt.Fprintf(out, "acting as group %s\n",
			describeGroup(nameOfGroup(p.Groups, p.ActiveGroup), p.ActiveGroup))
	}
	return nil
}

func cmdWho(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("who", &o, out, args)
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
	args, done, err := subOptions("look", &o, out, args)
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
	// In the same words "regions" uses.  It is one datum, and the two
	// commands are run one after the other: a "21" here beside a
	// "moderate" there reads as two different facts.
	fmt.Fprintf(out, "  access   %s\n", sl.AccessName(r.Access))
	fmt.Fprintf(out, "  water    %.1fm\n", r.WaterHeight)
	fmt.Fprintf(out, "  product  %s\n", r.ProductName)
	if n, err := sh.s.Known(ctx); err == nil {
		fmt.Fprintf(out, "  objects  %d described so far\n", n)
	}
	return nil
}

// regionsOptions is what regions was asked for.
type regionsOptions struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to give the map to answer [15]"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

// cmdRegions says where a region is, for a region the avatar is not in.
//
// It is "look" asked about somewhere else, and it can say much less:
// what the map answers with is a position, a maturity rating and a
// handle, because the rest of the block -- how many people are there,
// what the region allows, where the water is -- came back zero for every
// region on every run (see sl.FindRegions).
//
// Plural, because the search is by prefix and a listing is what comes
// back: "Sandbox" finds thirty-three regions and none of them is called
// Sandbox, and an exact name is one row among however many begin with
// it.  Picking the closest match would be this command deciding which of
// thirty-three places somebody meant, and doing it silently.
//
// The handle is in the line because it is the number a teleport is
// addressed to, and nothing else in the shell will tell anybody what it
// is.
func cmdRegions(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o regionsOptions
	args, done, err := subOptions("regions", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return usageError("regions")
	}
	// Joined with spaces: a region name has them in it, and quoting one
	// at a prompt is a thing to have to remember.
	found, err := sh.s.FindRegions(ctx, strings.Join(args, " "),
		time.Duration(o.Wait)*time.Second)
	if err != nil {
		return err
	}
	printRegions(out, found)
	return nil
}

// printRegions writes what the map answered, a region to a line.
//
// Shared rather than copied, because tp prints the same listing when a
// name matched several and it has to be the same listing: somebody
// reading a refusal and then running regions to look again should not be
// shown the same rows in two different shapes.
func printRegions(out io.Writer, found []sl.MapRegion) {
	for _, r := range found {
		fmt.Fprintf(out, "%-32s %5d, %-5d %-9s %d\n",
			r.Name, r.X, r.Y, sl.AccessName(r.Access), r.Handle)
	}
}

// cmdNeighbours says what circuits this session holds to the regions
// around it, and turns them on and off.
//
// It is the one command here that costs the daemon something lasting:
// on is a socket and a share of the traffic per neighbouring region --
// four around Pelmar Reach, up to eight anywhere -- held for as long as it
// is on.  That is why it is off unless somebody says so, and why the
// state is printed after a change rather than the change being silent.
//
// The argument is a word and not a flag.  "neighbours on" reads as the
// sentence somebody means, where "neighbours --set" would be a flag
// whose value is the whole of the command.
func cmdNeighbours(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("neighbours", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) > 1 {
		return usageError("neighbours")
	}

	var n *sl.Neighbours
	if len(args) == 1 {
		// Nothing else is accepted, and "yes" is not a synonym here.
		// Anything this took as a word it did not understand would be
		// a session left as it was by a command that looked like it
		// had changed it.
		switch strings.ToLower(args[0]) {
		case "on":
			n, err = sh.s.SetNeighbours(ctx, true)
		case "off":
			n, err = sh.s.SetNeighbours(ctx, false)
		default:
			return usageError("neighbours",
				fmt.Sprintf("%q is neither on nor off", args[0]))
		}
	} else {
		n, err = sh.s.Neighbours(ctx)
	}
	if err != nil {
		return err
	}
	printNeighbours(out, n)
	return nil
}

// printNeighbours writes the state and then a circuit to a line.
//
// It follows printRegions' shape, for the reason printRegions is shared
// with tp: these are regions, and somebody who has just run regions
// should not have to read a second layout to learn the same kind of
// fact.  The name and the square come first because they are what says
// WHICH region; the handle is left out entirely, where regions prints
// it, because nothing is addressed by a neighbour's handle -- a border
// is walked over rather than typed at.
//
// On with nothing held is a state worth spelling out.  A simulator
// offers a neighbour when the avatar is near one and offers none at all
// to an avatar in the middle of a region, so an empty listing usually
// means "not near a border" rather than anything having gone wrong.
func printNeighbours(out io.Writer, n *sl.Neighbours) {
	switch {
	case !n.On:
		fmt.Fprintln(out, "neighbours are off; this avatar cannot walk over a border")
		return
	case len(n.Held) == 0:
		fmt.Fprintln(out, "neighbours are on; no circuit is open, "+
			"which is what being away from a border looks like")
		return
	}
	fmt.Fprintf(out, "neighbours are on, %d %s held\n",
		len(n.Held), plural(len(n.Held), "circuit", "circuits"))
	for _, c := range n.Held {
		x, y := msg.GridCoords(c.Handle)
		name := c.Name
		switch {
		case !c.Handshook:
			// The name arrives in the handshake, so a circuit
			// without one has none to print.  Measured at about a
			// second on Agni, which is quick enough that seeing this
			// twice running means the offer came to nothing.
			name = "(no handshake yet)"
		case name == "":
			name = "(unnamed)"
		}
		// The address column is eighteen because that is what a
		// simulator's address measures: a dotted quad and a port, and
		// the ordinary ones here are seventeen or eighteen
		// ("203.0.113.11:13032" is eighteen).  A fifteen-character
		// quad reaches twenty-one and pads out rather than being cut,
		// so the rare long one grows the line instead of losing an
		// address.  The row is then 72 to 75 columns, which still has
		// room for the four spaces man puts in front of the copy of it
		// in neighbours.txt.
		fmt.Fprintf(out, "%-32s %5d, %-5d %-18s %d heard\n", name, x, y, c.Addr, c.Heard)
	}
}

func cmdCaps(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("caps", &o, out, args)
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
	args, done, err := subOptions("features", &o, out, args)
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
	rest, done, err := subOptions("worn", &o, out, args)
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

// objectsOptions is what objects was asked for.
type objectsOptions struct {
	Children bool   `getopt:"--children -c  the prims of each object as well, indented under it"`
	Owner    string `getopt:"--owner=WHO    only one owner's things: a uuid, or a pattern for the name"`
	Help     bool   `getopt:"--help -h      show what this command takes"`
}

// cmdObjects lists what the region has described, an object to a line.
//
// An object is a linkset, and what a person means by one is its root:
// listing every prim turns a hundred things into a thousand lines, most
// of them called "Object" and placed at an offset from something the
// listing does not say. So the roots are the listing and -c opens them
// up.
func cmdObjects(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o objectsOptions
	args, done, err := subOptions("objects", &o, out, args)
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
	whose, err := ownerFilter(o.Owner)
	if err != nil {
		return err
	}
	wearers := whoWears(all)
	// One request for the lot: an attachment says which avatar it hangs
	// off and every object says who owns it, and an id is a poor way to
	// say whose anything is.
	sh.s.Names(ctx, namesWanted(wearers, all), 10*time.Second)

	roots, kids, orphans := linksets(all)

	shown, hidden := 0, 0
	unnamed := 0
	for _, g := range sh.byOwner(roots, orphans) {
		if !whose(g) {
			// Somebody nobody has named is not a match for a pattern
			// about names, and how many were passed over that way is
			// worth a word: it is the difference between "nobody here
			// owns one" and "nobody has said".
			if g.named == "" {
				unnamed += len(g.roots) + len(g.orphans)
			}
			continue
		}
		// The owner heads the group rather than repeating down a
		// column, since a region is mostly one person's things at a
		// time and the name is the longest thing on the line.
		head := func() {
			if g.printed {
				return
			}
			g.printed = true
			fmt.Fprintf(out, "%s\n", g.name)
		}

		for _, r := range g.roots {
			mine := kids[r.Local]
			if !namesAnywhere(want, r, mine) {
				continue
			}
			head()
			fmt.Fprintf(out, "  %-36s %-28s %s\n", r.ID, r.Name, sh.whereIs(r, wearers))
			shown++
			for _, c := range mine {
				// Browsing shows the objects; searching shows what was
				// searched for.  Without -c the prims inside are the
				// object's business, but a prim whose own name was
				// asked for is the answer to the question and is shown
				// either way.
				if !o.Children && !hit(want, c) {
					hidden++
					continue
				}
				fmt.Fprintf(out, "    %-34s %-28s offset %s\n",
					c.ID, c.Name, offsetOf(c.Position))
				shown++
			}
		}

		// A child whose root has not been described has nothing to sit
		// under.  Leaving it out silently would be a listing that says
		// a region holds less than it does.
		for _, c := range g.orphans {
			if !namesAnywhere(want, c, nil) {
				continue
			}
			if !o.Children && !hit(want, c) {
				hidden++
				continue
			}
			head()
			fmt.Fprintf(out, "    %-34s %-28s offset %s, from a root nothing has described\n",
				c.ID, c.Name, offsetOf(c.Position))
			shown++
		}
	}

	switch {
	case shown == 0 && hidden == 0:
		fmt.Fprintln(out, "nothing matched")
	case hidden > 0:
		fmt.Fprintf(out, "%d more %s, not shown: -c lists them\n",
			hidden, pluralPrims(hidden))
	}
	if unnamed > 0 {
		fmt.Fprintf(out, "%d %s whose owner nobody has named were not matched\n",
			unnamed, pluralPrims(unnamed))
	}
	return nil
}

func pluralPrims(n int) string {
	if n == 1 {
		return "prim"
	}
	return "prims"
}

// whereIs is the last column: a place for something standing in the
// region, and who is wearing it for an attachment.
func (sh *Shell) whereIs(o *sl.Seen, wearers map[uint32]wearer) string {
	who, worn := wearers[o.Local]
	if !worn {
		return fmt.Sprintf("%.0f, %.0f, %.0f", o.Position.X, o.Position.Y, o.Position.Z)
	}
	where := "worn"
	if who.point != 0 {
		where = "worn on " + sl.AttachPointName(who.point)
	}
	// Whose it is has its own column, and an avatar wears its own
	// things, so saying the name again is noise.  It is worth saying
	// only when the two differ, which is the case worth noticing.
	if who.avatar != o.Owner {
		where += ", by " + sh.s.NameOr(who.avatar)
	}
	return where
}

// hit is whether this object's own name is what was searched for.  An
// empty search is browsing rather than searching, and hits nothing.
func hit(want string, o *sl.Seen) bool {
	return want != "" && strings.Contains(strings.ToLower(o.Name), want)
}

// namesAnywhere is whether a listing filtered by text should show this
// object.
//
// A root matches on its children's names as well as its own, because
// the name a person remembers is often on a prim inside: searching for
// "HearthEmbers" and being told nothing is here, when it is a prim of
// the chimney standing in front of them, would be a lie by omission.
func namesAnywhere(want string, root *sl.Seen, kids []*sl.Seen) bool {
	if want == "" {
		return true
	}
	if strings.Contains(strings.ToLower(root.Name), want) {
		return true
	}
	for _, c := range kids {
		if strings.Contains(strings.ToLower(c.Name), want) {
			return true
		}
	}
	return false
}

// linksets sorts objects into roots, the prims under each root, and the
// children whose root is not here.
//
// A root is a prim with no prim above it: one standing in the region,
// or the root of an attachment, which hangs off an avatar rather than
// off another prim.  Avatars themselves are not objects for this
// purpose -- an avatar is a person, and listing one among the furniture
// helps nobody.
func linksets(all []*sl.Seen) (roots []*sl.Seen, kids map[uint32][]*sl.Seen, orphans []*sl.Seen) {
	byLocal := make(map[uint32]*sl.Seen, len(all))
	for _, o := range all {
		byLocal[o.Local] = o
	}
	kids = map[uint32][]*sl.Seen{}
	for _, o := range all {
		if o.IsAvatar() {
			continue
		}
		up := byLocal[o.Parent]
		switch {
		case o.Parent == 0, up != nil && up.IsAvatar():
			roots = append(roots, o)
		case up == nil:
			orphans = append(orphans, o)
		default:
			root := rootOf(o, byLocal)
			if root == nil {
				orphans = append(orphans, o)
				continue
			}
			kids[root.Local] = append(kids[root.Local], o)
		}
	}
	return roots, kids, orphans
}

// rootOf walks up to the prim nothing is above, or nil if the chain
// leaves the store before it gets there.
func rootOf(o *sl.Seen, byLocal map[uint32]*sl.Seen) *sl.Seen {
	for up := 0; up < 16; up++ {
		if o.Parent == 0 {
			return o // standing in the region
		}
		parent := byLocal[o.Parent]
		if parent == nil {
			return nil // the chain leaves the store before the top
		}
		if parent.IsAvatar() {
			return o // the root of an attachment
		}
		o = parent
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

// namesWanted is every avatar the listing will have to name: the ones
// wearing something, and the ones who own anything.
func namesWanted(wearers map[uint32]wearer, all []*sl.Seen) []msg.UUID {
	seen := map[msg.UUID]bool{}
	var ids []msg.UUID
	add := func(id msg.UUID) {
		if id.IsZero() || seen[id] {
			return
		}
		seen[id] = true
		ids = append(ids, id)
	}
	for _, w := range wearers {
		add(w.avatar)
	}
	for _, s := range all {
		add(s.Owner)
	}
	return ids
}

// owned is one person's things: what they own that is standing here or
// worn, and the prims of theirs whose root is not here.
//
// id and named are what --owner is matched against.  The heading is not:
// it reads "(owner not known)" for things nobody has answered about, and
// a pattern for a person's name should not start matching those.
type owned struct {
	id      msg.UUID
	named   string
	name    string
	roots   []*sl.Seen
	orphans []*sl.Seen
	printed bool
}

// ownerFilter reads what --owner was given: a uuid, or a pattern for
// the name.
//
// A uuid is taken as one because nothing else looks like one, and it is
// the only way to name somebody the region has not answered about yet
// -- or to tell two people with the same name apart.  Anything else is
// a regular expression, matched without regard to case, since nobody
// types a resident's capitals the way they were registered.
//
// An empty filter matches everybody, which is what asking for nothing
// in particular means.
func ownerFilter(want string) (func(*owned) bool, error) {
	if want == "" {
		return func(*owned) bool { return true }, nil
	}
	if id, err := msg.ParseUUID(want); err == nil {
		return func(g *owned) bool { return g.id == id }, nil
	}
	re, err := regexp.Compile("(?i)" + want)
	if err != nil {
		return nil, fmt.Errorf("--owner wants a uuid or a pattern: %w", err)
	}
	// Somebody nobody has named cannot be matched by a pattern for a
	// name.  Saying so is better than quietly listing them, and better
	// than quietly leaving them out of a listing that claims to be
	// everything.
	return func(g *owned) bool { return g.named != "" && re.MatchString(g.named) }, nil
}

// byOwner gathers objects under whoever owns them, sorted by name.
//
// Whoever nobody has answered for goes last: a group of things with no
// name on it is the least useful group, and putting it first would be
// the first thing anybody read.
func (sh *Shell) byOwner(roots, orphans []*sl.Seen) []*owned {
	groups := map[msg.UUID]*owned{}
	group := func(s *sl.Seen) *owned {
		g := groups[s.Owner]
		if g == nil {
			g = &owned{id: s.Owner, named: sh.ownerName(s)}
			switch {
			case g.named != "":
				g.name = g.named
			case s.Owner.IsZero():
				g.name = "(owner not known)"
			default:
				// Known to be somebody, but nobody has said who.  The
				// shortened id at least tells one such owner from
				// another, which a shared heading would not.
				g.name = sh.s.NameOr(s.Owner)
			}
			groups[s.Owner] = g
		}
		return g
	}
	for _, r := range roots {
		g := group(r)
		g.roots = append(g.roots, r)
	}
	for _, c := range orphans {
		g := group(c)
		g.orphans = append(g.orphans, c)
	}

	out := make([]*owned, 0, len(groups))
	for _, g := range groups {
		out = append(out, g)
	}
	// Whoever has a name first, in name order; the rest after, since a
	// heap of things under an id is the least useful thing to read and
	// should not be the first thing anybody reads.
	sort.Slice(out, func(i, j int) bool {
		if (out[i].named == "") != (out[j].named == "") {
			return out[j].named == ""
		}
		return strings.ToLower(out[i].name) < strings.ToLower(out[j].name)
	})
	return out
}

// ownerName is what whoever owns this is called, and "" when nobody has
// said -- either because no owner is known at all, or because the name
// behind the id has not been answered for.
//
// The two are worth telling apart from a name that IS known: a pattern
// for a person's name cannot match what nobody has named, and quietly
// leaving those out of a filtered listing would be the difference
// between "nobody here owns one" and "nobody has said" going unsaid.
//
// A group owned object is the other case with no name: nothing here
// resolves a group name, so it stays an id.
func (sh *Shell) ownerName(o *sl.Seen) string {
	if o.Owner.IsZero() {
		return ""
	}
	return sh.s.Name(o.Owner)
}
