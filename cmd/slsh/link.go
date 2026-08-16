package main

// Joining prims into one object, and taking one apart.
//
//	link    join objects into one, with the first as the root
//	unlink  take a linked object apart
//
// Linking has been possible in the sl package since Build had to make
// anything bigger than one prim -- sl.Link is what puts a described
// object together -- and there has never been a way to ask for it from
// the prompt.  Taking one apart was possible nowhere at all, so
// sl.Unlink is new and this is the command that wanted it.
//
// # Why link takes several names and unlink takes one
//
// link names a root and everything that goes under it, so each argument
// is one object and a name with a space in it is quoted:
//
//	link chair "left leg" "right leg"
//
// unlink names one thing, so its arguments are joined back into one name
// the way detach's are, and "unlink a lamp" means what it looks like.
// The two rules differ because the commands differ, and the alternative
// -- making link quote-free by taking the root and then a list -- needs a
// separator between the two halves that is not a space, which is a new
// thing to remember for a command whose whole job is one sentence long.
//
// # What unlink does with a root, and with a child
//
// The whole of it comes apart when given a root, and just that prim
// leaves when given a child.  That is the viewer's pair of behaviours --
// clicking an object selects the linkset and Unlink frees all of it,
// while Edit Linked Parts selects one prim and Unlink frees only that --
// and it falls out of what a delink message is: the local ids in it are
// the prims being FREED, so the caller says which they are.  See
// sl.Unlink for the citation.
//
// Naming a child is a real thing to want and not a mistake to guard
// against: a prim of a linkset has its own name, "objects -c" prints
// them, and a linkset is otherwise all or nothing.
//
// # What happens to the name
//
// A linkset answers to its root's name, so "chair" is the four-prim
// chair while the four prims are linked and is one prim afterwards.  The
// pieces keep the names they had inside it, which for anything built by
// hand is often "Object" for all of them, so after an unlink several
// things in the region can answer to one word -- and objectNamed refuses
// an ambiguous name rather than picking.  That is why unlink lists what
// it freed with keys instead of printing a success line: the keys are
// the only handle on the pieces that is certain to work, and the moment
// they are wanted is the moment the object comes apart.
//
// # Why the report is read back rather than counted
//
// Both commands say what the object is now, and both work it out from
// what the region says afterwards rather than from how many arguments
// they were given.  Linking something that was already a linkset brings
// its prims along, so "link a b" can make an object of seven; and a
// person watching wants to know that.  Counting the arguments would
// print "2 prims" and be wrong in exactly the case worth reporting.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/quark-idlemind/slgo/sl"
)

var linkCommands = map[string]*command{
	"link": {
		params: "ROOT CHILD...",
		flags:  func() any { return new(linkFlags) },
		brief:  "join objects into one; the first is the root and its name is the object's",
		run:    cmdLink,
	},
	"unlink": {
		params: "NAME|UUID",
		flags:  func() any { return new(linkFlags) },
		brief:  "take a linked object apart; naming one prim of it frees only that prim",
		run:    cmdUnlink,
	},
}

type linkFlags struct {
	Wait int  `getopt:"--wait -w=SECONDS  how long to let the region describe itself [30]"`
	Help bool `getopt:"--help -h          show what this command takes"`
}

// cmdLink joins objects into one.
func cmdLink(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o linkFlags
	args, done, err := subOptions("link", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 2 {
		return fmt.Errorf("link joins objects into one: name the root and then at least one thing " +
			"to go under it, as in \"link chair leg seat\"")
	}

	objs := make([]*sl.Object, 0, len(args))
	for _, a := range args {
		obj, err := sh.objectNamed(ctx, a, o.Wait)
		if err != nil {
			return err
		}
		// Two arguments naming one prim is a typo, and the simulator
		// would take the message without comment: a prim cannot be
		// linked to itself, so nothing would happen and the report
		// would say so in numbers rather than in words.
		for _, had := range objs {
			if had.Local == obj.Local {
				return fmt.Errorf("%s is named twice; link takes each object once", obj)
			}
		}
		objs = append(objs, obj)
	}

	if err := sh.s.Link(ctx, objs[0], objs[1:]...); err != nil {
		return err
	}

	root, kids, err := sh.linksetOf(ctx, objs[0], o.Wait)
	if err != nil {
		return fmt.Errorf("the link went through and what it made could not be read back: %w", err)
	}
	fmt.Fprintf(out, "%s is one object of %d prims\n", root.Object, len(kids)+1)
	return nil
}

// cmdUnlink takes a linked object apart.
func cmdUnlink(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o linkFlags
	args, done, err := subOptions("unlink", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) < 1 {
		return usageError("unlink")
	}

	named, err := sh.objectNamed(ctx, strings.Join(args, " "), o.Wait)
	if err != nil {
		return err
	}
	root, kids, err := sh.linksetOf(ctx, named, o.Wait)
	if err != nil {
		return err
	}
	if len(kids) == 0 {
		return fmt.Errorf("%s is one prim and is not linked to anything, so there is nothing to take apart", root.Object)
	}

	// A root means all of it and a child means that prim, which is the
	// difference between the two things a person can mean by the name
	// they typed.  The root is never among what is sent: it has no
	// parent to lose.  See sl.Unlink.
	freed := kids
	if named.Local != root.Local {
		freed = []*sl.Seen{seenAmong(kids, named)}
	}

	prims := make([]*sl.Object, 0, len(freed))
	for _, k := range freed {
		prims = append(prims, &k.Object)
	}
	if err := sh.s.Unlink(ctx, prims...); err != nil {
		return err
	}

	if len(freed) < len(kids) {
		// One prim out of a set that is still a set.  What is left is
		// worth a word, since the name it answers to has just come to
		// mean fewer prims than it did.  It is never one prim: freeing
		// the last child leaves nothing linked at all, which is the
		// whole thing coming apart and is reported below as that.
		left := 1 // the root
		for _, k := range kids {
			if k.Local != freed[0].Local {
				left++
			}
		}
		fmt.Fprintf(out, "%s is out of %s, which is %d prims now\n", freed[0].Object, root.Object, left)
		return nil
	}

	fmt.Fprintf(out, "%s came apart into %d objects:\n", root.Object, len(kids)+1)
	pieces := append([]*sl.Seen{root}, kids...)
	for _, p := range pieces {
		fmt.Fprintf(out, "  %-36s %s\n", p.ID, p.Name)
	}
	if shared := sharedNames(pieces); shared != "" {
		fmt.Fprintf(out, "%s; name one by its key, which \"objects -c\" prints too\n", shared)
	}
	return nil
}

// seenAmong is the region's description of a named prim, out of the
// children of its root.
//
// The fallback is for a chain deeper than the one level the simulator
// builds -- a prim under a prim under a root.  Nothing here has seen one,
// and describing what was asked for is a better answer to it than
// refusing something a person can see in front of them.
func seenAmong(kids []*sl.Seen, o *sl.Object) *sl.Seen {
	for _, k := range kids {
		if k.Local == o.Local {
			return k
		}
	}
	return &sl.Seen{Object: *o}
}

// sharedNames is the warning that the pieces cannot all be told apart by
// name, or "" when they can.
//
// It is printed only when there is a clash, because that is when it says
// something: a linkset put together prim by prim is a row of things all
// called "Object", and one built from a described object usually is not.
func sharedNames(pieces []*sl.Seen) string {
	seen := map[string]int{}
	var clashing []string
	for _, p := range pieces {
		seen[p.Name]++
		if seen[p.Name] == 2 {
			clashing = append(clashing, fmt.Sprintf("%q", p.Name))
		}
	}
	if len(clashing) == 0 {
		return ""
	}
	return "more than one of these is called " + strings.Join(clashing, " and ")
}

// linksetOf is the prims of the linkset an object belongs to: the root,
// and the prims hanging off it.
//
// It reads the region rather than trusting what resolving the name gave
// back, because an sl.Object says nothing about what it is linked to and
// that is the whole question here.  The walk up is rootOf's, so an
// attachment is treated the way the listing treats one: the root of a
// worn linkset hangs off the avatar and is a root all the same.
func (sh *Shell) linksetOf(ctx context.Context, o *sl.Object, wait int) (*sl.Seen, []*sl.Seen, error) {
	all, err := sh.s.AllObjects(ctx, waitFor(wait))
	if err != nil {
		return nil, nil, err
	}
	byLocal := make(map[uint32]*sl.Seen, len(all))
	for _, s := range all {
		byLocal[s.Local] = s
	}
	me := byLocal[o.Local]
	if me == nil {
		return nil, nil, fmt.Errorf("%s is no longer among the objects in range", o)
	}
	root := rootOf(me, byLocal)
	if root == nil {
		// The prim above it is beyond the draw distance or has not been
		// described yet, so which object this belongs to is not
		// something that can be answered from here.
		return nil, nil, fmt.Errorf("%s is a prim of an object whose root nothing here has described; "+
			"\"objects\" lists what can be seen", me.Object)
	}

	var kids []*sl.Seen
	for _, s := range all {
		if s.Parent == root.Local && s.Local != root.Local {
			kids = append(kids, s)
		}
	}
	return root, kids, nil
}
