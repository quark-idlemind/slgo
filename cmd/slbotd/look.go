package main

// Looking: where the avatar is, who and what is around it, and what the
// grid will say about a person or a place.
//
// Nothing here changes anything.  These are the commands somebody sends
// to find out what an avatar they are not looking at is doing, which is
// most of what a daemon like this is asked.

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

var lookCommands = map[string]*command{
	"where": {
		flags: func() any { return new(helpOnly) },
		brief: "the region and position this avatar is at",
		group: groupLooking,
		run:   cmdWhere,
	},
	"who": {
		flags: func() any { return new(whoFlags) },
		brief: "who else is in the region, nearest first",
		group: groupLooking,
		run:   cmdWho,
	},
	"look": {
		flags: func() any { return new(helpOnly) },
		brief: "what the simulator said about the region",
		group: groupLooking,
		run:   cmdLook,
	},
	"parcel": {
		flags: func() any { return new(helpOnly) },
		brief: "the land under this avatar",
		group: groupLooking,
		run:   cmdParcel,
	},
	"regions": {
		params: "NAME",
		flags:  func() any { return new(helpOnly) },
		brief:  "find regions on the grid's map by the start of a name",
		group:  groupLooking,
		run:    cmdRegions,
	},
	"objects": {
		params: "[TEXT]",
		flags:  func() any { return new(objectsFlags) },
		brief:  "the objects the region has described",
		group:  groupLooking,
		run:    cmdObjects,
	},
	"worn": {
		flags: func() any { return new(helpOnly) },
		brief: "the objects this avatar is wearing, and where",
		group: groupLooking,
		run:   cmdWorn,
	},
	"lookup": {
		params: "NAME",
		flags:  func() any { return new(helpOnly) },
		brief:  "find somebody on the grid by part of a name",
		group:  groupLooking,
		run:    cmdLookup,
	},
	"profile": {
		params: "[NAME|UUID]",
		flags:  func() any { return new(helpOnly) },
		brief:  "what the grid says about somebody; with nothing named, this avatar",
		group:  groupLooking,
		run:    cmdProfile,
	},
	"friends": {
		flags: func() any { return new(friendsFlags) },
		brief: "who is on the friend list and online; -a for all of them",
		group: groupLooking,
		run:   cmdFriends,
	},
}

// nameWait is how long a command waits for the grid to turn ids into
// names.  Short: every one of these prints something useful without the
// names, and a listing that stopped for ten seconds to decorate itself
// would be a worse listing.
const nameWait = 3 * time.Second

// objectWait is how long a command waits for the region to describe
// itself.  Long, because it is not a round trip: the region volunteers
// object updates and the wait is for enough of them to have arrived.
const objectWait = 30 * time.Second

func cmdWhere(ctx context.Context, r *req, out io.Writer, args []string) error {
	if _, done, err := subOptions("where", new(helpOnly), out, args); err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	return sayPosition(ctx, s, out)
}

// sayPosition is the one line every command that moves the avatar ends
// with, so that they all agree about what "where" means.
func sayPosition(ctx context.Context, s *sl.Session, out io.Writer) error {
	p, err := s.Where(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s at %.0f, %.0f, %.0f\n",
		p.Region, p.Position.X, p.Position.Y, p.Position.Z)
	return nil
}

// whoFlags is what who was asked for.
type whoFlags struct {
	Long bool `getopt:"-l          the ids as well as the names"`
	Help bool `getopt:"--help -h   show what this command takes"`
}

func cmdWho(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o whoFlags
	if _, done, err := subOptions("who", &o, out, args); err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	people, err := s.Nearby(ctx)
	if err != nil {
		return err
	}
	if len(people) == 0 {
		fmt.Fprintln(out, "nobody else is in range")
		return nil
	}
	for _, p := range people {
		name := p.Name
		if name == "" {
			name = p.ID.String()
		}
		far := fmt.Sprintf("%5.0fm", p.Distance)
		if p.Distance < 0 {
			far = "    ? "
		}
		if o.Long {
			fmt.Fprintf(out, "%s  %s  %s\n", far, p.ID, name)
		} else {
			fmt.Fprintf(out, "%s  %s\n", far, name)
		}
	}
	return nil
}

func cmdLook(ctx context.Context, r *req, out io.Writer, args []string) error {
	if _, done, err := subOptions("look", new(helpOnly), out, args); err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	reg, err := s.Region(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s, %s\n", reg.Name, sl.AccessName(reg.Access))
	fmt.Fprintf(out, "%s\n", reg.ID)
	if reg.ProductName != "" {
		fmt.Fprintf(out, "%s\n", reg.ProductName)
	}
	fmt.Fprintf(out, "water at %.1fm\n", reg.WaterHeight)
	if n, err := s.Known(ctx); err == nil {
		fmt.Fprintf(out, "%d objects described\n", n)
	}
	return nil
}

func cmdParcel(ctx context.Context, r *req, out io.Writer, args []string) error {
	if _, done, err := subOptions("parcel", new(helpOnly), out, args); err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	p, err := s.Parcel(ctx, 10*time.Second)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s, %dm²\n", p.Name, p.Area)
	if p.Desc != "" {
		fmt.Fprintln(out, p.Desc)
	}
	owner := "somebody"
	if p.IsGroupOwned {
		owner = "a group"
	}
	if n := s.NameOr(p.Owner); n != "" {
		owner = n
	} else if !p.Owner.IsZero() {
		if names := s.Names(ctx, []msg.UUID{p.Owner}, nameWait); names[p.Owner] != "" {
			owner = names[p.Owner]
		}
	}
	fmt.Fprintf(out, "owned by %s\n", owner)
	fmt.Fprintf(out, "%d of %d prims, %d of them this avatar's\n",
		p.TotalPrims, p.MaxPrims, p.OwnerPrims)
	return nil
}

func cmdRegions(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("regions", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) == 0 {
		return usage("regions", "the start of a region name")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	found, err := s.FindRegions(ctx, strings.Join(rest, " "), 15*time.Second)
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Fprintln(out, "the map knows no region beginning with that")
		return nil
	}
	for _, f := range found {
		fmt.Fprintf(out, "%s  (%d, %d)  %s\n", f.Name, f.X, f.Y, sl.AccessName(f.Access))
	}
	return nil
}

// objectsFlags is what objects was asked for.
type objectsFlags struct {
	Long bool `getopt:"-l          the ids and positions as well as the names"`
	Help bool `getopt:"--help -h   show what this command takes"`
}

func cmdObjects(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o objectsFlags
	rest, done, err := subOptions("objects", &o, out, args)
	if err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}

	var seen []*sl.Seen
	if len(rest) == 0 {
		seen, err = s.AllObjects(ctx, objectWait)
	} else {
		seen, err = s.ObjectsNamed(ctx, strings.Join(rest, " "), objectWait)
	}
	if err != nil {
		return err
	}
	// Roots only: a listing of every prim in every linkset is a
	// thousand lines of the same object, and the root is the thing
	// anybody names.
	roots, err := s.Roots(ctx, seen)
	if err != nil {
		return err
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].Name < roots[j].Name })
	if len(roots) == 0 {
		fmt.Fprintln(out, "the region has described nothing by that name")
		return nil
	}
	for _, obj := range roots {
		name := obj.Name
		if name == "" {
			name = "(unnamed)"
		}
		if o.Long {
			fmt.Fprintf(out, "%s  %s  %.0f, %.0f, %.0f\n",
				obj.ID, name, obj.Position.X, obj.Position.Y, obj.Position.Z)
		} else {
			fmt.Fprintln(out, name)
		}
	}
	fmt.Fprintf(out, "%d objects\n", len(roots))
	return nil
}

func cmdWorn(ctx context.Context, r *req, out io.Writer, args []string) error {
	if _, done, err := subOptions("worn", new(helpOnly), out, args); err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	worn, err := s.WornObjects(ctx)
	if err != nil {
		return err
	}
	if len(worn) == 0 {
		fmt.Fprintln(out, "nothing is attached")
		return nil
	}
	for _, w := range worn {
		name := w.Object.Name
		if name == "" {
			name = w.Object.ID.String()
		}
		fmt.Fprintf(out, "%-24s %s\n", name, sl.AttachPointName(w.Point))
	}
	return nil
}

func cmdLookup(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("lookup", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) == 0 {
		return usage("lookup", "part of somebody's name")
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	found, err := s.Lookup(ctx, strings.Join(rest, " "))
	if err != nil {
		return err
	}
	if len(found) == 0 {
		fmt.Fprintln(out, "nobody by that name")
		return nil
	}
	for _, f := range found {
		line := fmt.Sprintf("%s  %s", f.ID, f.Name)
		if f.Display != "" && f.Display != f.Name {
			line += " (" + f.Display + ")"
		}
		fmt.Fprintln(out, line)
	}
	return nil
}

func cmdProfile(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("profile", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}

	who := s.Me()
	if len(rest) > 0 {
		who, err = personNamed(ctx, s, strings.Join(rest, " "))
		if err != nil {
			return err
		}
	}
	p, err := s.Profile(ctx, who, 15*time.Second)
	if err != nil {
		return err
	}
	if !p.Known {
		return fmt.Errorf("the grid says nothing about %s", who)
	}
	name := s.NameOr(who)
	if name == "" {
		name = who.String()
	}
	fmt.Fprintf(out, "%s  %s\n", name, who)
	if p.BornOn != "" {
		fmt.Fprintf(out, "born %s\n", p.BornOn)
	}
	if p.Caption != "" {
		fmt.Fprintln(out, p.Caption)
	}
	if p.About != "" {
		fmt.Fprintln(out, p.About)
	}
	for _, g := range p.Groups {
		fmt.Fprintf(out, "  group: %s\n", g.Name)
	}
	return nil
}

// friendsFlags is what friends was asked for.
type friendsFlags struct {
	All  bool `getopt:"-a          everyone, not only whoever is online"`
	Help bool `getopt:"--help -h   show what this command takes"`
}

func cmdFriends(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o friendsFlags
	if _, done, err := subOptions("friends", &o, out, args); err != nil || done {
		return err
	}
	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	var people []sl.Person
	if o.All {
		people, err = s.FriendList(ctx)
	} else {
		people, err = s.OnlineFriends(ctx)
	}
	if err != nil {
		return err
	}
	if len(people) == 0 {
		if o.All {
			fmt.Fprintln(out, "no friends")
		} else {
			fmt.Fprintln(out, "no friends are online")
		}
		return nil
	}
	for _, p := range people {
		name := p.Name
		if name == "" {
			name = p.ID.String()
		}
		fmt.Fprintln(out, name)
	}
	return nil
}

// personNamed turns what somebody typed into an avatar id.
//
// A uuid is taken as itself.  A name is looked for among the people
// this session already knows -- which is everybody nearby and everybody
// it has exchanged a message with -- and then, failing that, asked of
// the grid.  A name that means two people is refused with both ids
// rather than guessed at: the commands that take a person hand things
// over and send messages, and the wrong guess cannot be taken back.
func personNamed(ctx context.Context, s *sl.Session, what string) (msg.UUID, error) {
	if id, err := msg.ParseUUID(what); err == nil {
		return id, nil
	}
	if ids := s.Find(what); len(ids) == 1 {
		return ids[0], nil
	} else if len(ids) > 1 {
		return msg.UUID{}, severalPeople(s, what, ids)
	}

	found, err := s.Lookup(ctx, what)
	if err != nil {
		return msg.UUID{}, err
	}
	// The whole of a name beats part of one.  A search for "Example
	// Resident" that also matched "Example Resident 2" has already been
	// told which was meant.
	var whole []sl.Found
	for _, f := range found {
		if strings.EqualFold(f.Name, what) || strings.EqualFold(f.Username, what) {
			whole = append(whole, f)
		}
	}
	if len(whole) == 1 {
		return whole[0].ID, nil
	}
	if len(whole) == 0 && len(found) == 1 {
		return found[0].ID, nil
	}
	if len(found) == 0 {
		return msg.UUID{}, fmt.Errorf("nobody here or on the grid is called %q", what)
	}
	ids := make([]msg.UUID, 0, len(found))
	for _, f := range found {
		ids = append(ids, f.ID)
	}
	return msg.UUID{}, severalPeople(s, what, ids)
}

func severalPeople(s *sl.Session, what string, ids []msg.UUID) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%d people answer to %q; name one by uuid:", len(ids), what)
	for _, id := range ids {
		name := s.NameOr(id)
		fmt.Fprintf(&b, "\n  %s %s", id, name)
	}
	return fmt.Errorf("%s", b.String())
}
