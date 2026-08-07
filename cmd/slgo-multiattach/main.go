// Can one attachment point hold more than one object?
//
// The auto objects are worn one per HUD point, and there are only eight
// HUD points, so eight is the ceiling on how many independent readings
// can be taken at once -- unless ATTACHMENT_ADD works, in which case
// the ceiling is the 38-attachment total instead.
//
// Nothing in this tree has ever set that bit: AttachAdd is defined and
// unused, and Wear sets the point bare, so every attach replaces what
// is on the point.  This finds out, rather than assuming.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7809", "the slgod to attach to")
	name := flag.String("agent", "helper", "which hosted agent")
	point := flag.Int("point", sl.HUDBottomLeft, "the attachment point to pile onto")
	first := flag.String("first", "auto", "item already worn there")
	second := flag.String("second", "auto probe", "item to add alongside it")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	s, err := sl.Dial(ctx, *addr, *name)
	if err != nil {
		log.Fatalf("attach: %v", err)
	}
	defer s.Close()

	folder, err := objectsFolder(ctx, s)
	if err != nil {
		log.Fatal(err)
	}
	items, err := s.FolderItems(ctx, folder)
	if err != nil {
		log.Fatal(err)
	}

	var seed, probe *sl.Item
	for _, it := range items {
		switch it.Name {
		case *first:
			seed = it
		case *second:
			probe = it
		}
	}
	if seed == nil {
		log.Fatalf("no item named %q to copy from", *first)
	}
	if probe == nil {
		probe, err = s.CopyItem(ctx, seed.ID, folder, *second, 60*time.Second)
		if err != nil {
			log.Fatalf("making %q: %v", *second, err)
		}
		fmt.Printf("made %q (%s)\n", probe.Name, probe.ID)
	}

	before := worn(ctx, s, *point)
	fmt.Printf("before: %d object(s) on %s\n", len(before), sl.AttachPointName(*point))
	for _, a := range before {
		fmt.Printf("   %-12s %s\n", a.Object.Name, a.Object.ID)
	}

	// The whole experiment: the point with the add bit set.
	fmt.Printf("attaching %q to %s WITH AttachAdd...\n", probe.Name, sl.AttachPointName(*point))
	if _, err := s.Wear(ctx, probe, *point|sl.AttachAdd, 60*time.Second); err != nil {
		log.Fatalf("wearing it: %v", err)
	}

	after := worn(ctx, s, *point)
	fmt.Printf("after: %d object(s) on %s\n", len(after), sl.AttachPointName(*point))
	for _, a := range after {
		fmt.Printf("   %-12s %s\n", a.Object.Name, a.Object.ID)
	}

	switch {
	case len(after) > len(before):
		fmt.Println("\nYES: a point holds several objects, so the pool is bounded by the 38-attachment total, not by 8 HUD points.")
	case len(after) == len(before):
		fmt.Println("\nNO: it replaced what was there. Eight HUD points is the ceiling.")
	default:
		fmt.Println("\nUNCLEAR: fewer than before; look at the list above.")
	}
}

// worn is what is on one attachment point now.
func worn(ctx context.Context, s *sl.Session, point int) []*sl.Attached {
	all, err := s.WornObjects(ctx)
	if err != nil {
		log.Fatalf("listing what is worn: %v", err)
	}
	var out []*sl.Attached
	for _, a := range all {
		if a.Point == point {
			out = append(out, a)
		}
	}
	return out
}

func objectsFolder(ctx context.Context, s *sl.Session) (msg.UUID, error) {
	top, err := s.ListInventory(ctx, "/", 0)
	if err != nil {
		return msg.UUID{}, err
	}
	for _, e := range top {
		if e.IsFolder() && e.Name == "Objects" {
			return e.ID, nil
		}
	}
	return s.InventoryRoot(), nil
}
