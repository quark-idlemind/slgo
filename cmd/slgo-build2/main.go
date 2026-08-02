// Command slgo-build2 builds a multi-prim object in one call and
// checks the result against what was asked for.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"slgo/msg"
	"slgo/world"
)

var (
	addr    = flag.String("server", "127.0.0.1:7807", "slgod address")
	profile = flag.String("agent", "example", "hosted agent")
	at      = flag.String("at", "24,248,30", "where to build, region local")
	take    = flag.Bool("take", false, "take it into inventory afterwards")
	n       = flag.Int("n", 3, "how many of the prims to build")
)

func main() {
	flag.Parse()
	ctx := context.Background()

	w, err := world.Dial(ctx, *addr, *profile)
	if err != nil {
		log.Fatal(err)
	}
	defer w.Close()
	fmt.Printf("%s in %s\n", w.Info().AvatarName, w.Info().Region)

	var x, y, z float32
	if _, err := fmt.Sscanf(*at, "%f,%f,%f", &x, &y, &z); err != nil {
		log.Fatalf("bad -at %q", *at)
	}

	fmt.Println("settling so the interest list fills")
	if err := w.Settle(ctx, 10*time.Second); err != nil {
		log.Fatal(err)
	}

	// A little tower: a wide base, a middle, and a turned cap.
	prims := []world.Prim{
		{
			Name:        "slgo tower base",
			Description: "the root",
			Position:    msg.Vector3{X: x, Y: y, Z: z},
			Size:        msg.Vector3{X: 1.0, Y: 1.0, Z: 0.25},
		},
		{
			Name:        "slgo tower middle",
			Description: "second prim",
			Position:    msg.Vector3{X: x, Y: y, Z: z + 0.5},
			Size:        msg.Vector3{X: 0.6, Y: 0.6, Z: 0.6},
		},
		{
			Name:        "slgo tower cap",
			Description: "third prim, turned a quarter turn about Z",
			Position:    msg.Vector3{X: x, Y: y, Z: z + 1.0},
			Size:        msg.Vector3{X: 0.4, Y: 0.4, Z: 0.4},
			Rotation:    msg.PackQuaternion(0, 0, 0.3826834, 0.9238795), // 45 degrees
		},
	}

	if *n < 1 || *n > len(prims) {
		log.Fatalf("-n must be between 1 and %d", len(prims))
	}
	prims = prims[:*n]

	fmt.Printf("\nbuilding %d prims\n", len(prims))
	start := time.Now()
	b, err := w.Build(ctx, prims)
	if err != nil {
		log.Fatalf("build: %v", err)
	}
	fmt.Printf("built in %s\n", time.Since(start).Round(time.Millisecond))

	fmt.Printf("\nroot  %s\n", b.Root)
	for i, p := range b.Parts {
		parent, known := w.Parent(p)
		role := fmt.Sprintf("child of %d", parent)
		switch {
		case !known:
			role = "not mentioned"
		case parent == 0:
			role = "root"
		}
		fmt.Printf("  part %d  local %-11d %-22s %s\n", i, p.Local, p.Name, role)
	}

	// The check that matters: the first prim asked for is the root,
	// and everything else hangs off it.
	ok := true
	if parent, known := w.Parent(b.Root); !known || parent != 0 {
		fmt.Printf("\nWRONG: the root reports parent %d\n", parent)
		ok = false
	}
	for _, p := range b.Parts[1:] {
		if parent, known := w.Parent(p); !known || parent != b.Root.Local {
			fmt.Printf("\nWRONG: %s reports parent %d, wanted %d\n",
				p.Name, parent, b.Root.Local)
			ok = false
		}
	}
	if ok {
		fmt.Printf("\nOK: %d prims linked, first one is the root\n", len(b.Parts))
	}

	// And the simulator's own account of the root.
	props, err := w.Properties(ctx, b.Root, 15*time.Second)
	if err != nil {
		log.Printf("properties: %v", err)
	} else {
		fmt.Printf("\nas the simulator has it:\n")
		fmt.Printf("  name        %q\n", props.Name)
		fmt.Printf("  description %q\n", props.Description)
		fmt.Printf("  owner       %s\n", props.Owner)
		fmt.Printf("  created     %s\n", props.Created.Format(time.RFC3339))
	}

	if *take {
		objects, err := w.Objects(ctx)
		if err != nil {
			log.Fatal(err)
		}
		it, err := w.Take(ctx, b.Root, objects, 40*time.Second)
		if err != nil {
			log.Fatalf("taking it: %v", err)
		}
		fmt.Printf("\ntaken into inventory as %s %q\n", it.ID, it.Name)
	}
}
