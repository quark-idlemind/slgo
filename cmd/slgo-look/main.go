// Command slgo-look asks the questions rather than doing anything:
// where the avatar is, what is in inventory, and what is nearby.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/world"
)

var (
	addr    = flag.String("server", "127.0.0.1:7807", "slgod address")
	profile = flag.String("agent", "example", "hosted agent")
	draw    = flag.Float64("draw", 0, "set the draw distance in metres")
	named   = flag.String("named", "", "list objects with this name")
	id      = flag.String("id", "", "describe one object by uuid")
	inv     = flag.Bool("inventory", false, "list inventory")
	region  = flag.Bool("region", false, "describe the region")
	flush   = flag.Bool("flush", false, "empty the server's object cache")
	all     = flag.Bool("all", false, "list every object nearby")
	settle  = flag.Duration("settle", 15*time.Second, "how long to let the region arrive")
)

func main() {
	flag.Parse()
	ctx := context.Background()

	w, err := world.Dial(ctx, *addr, *profile)
	if err != nil {
		log.Fatal(err)
	}
	defer w.Close()

	// Where we are, and how far we are being told about.
	if *draw > 0 {
		p, err := w.SetDrawDistance(ctx, float32(*draw))
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("draw distance now %.0f m\n", p.DrawDistance)
	}
	p, err := w.Where(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%s in %s\n", w.Info().AvatarName, p.Region)
	fmt.Printf("  position       %s\n", vec(p.Position))
	fmt.Printf("  camera         %s\n", vec(p.Camera))
	fmt.Printf("  looking at     %s\n", vec(p.LookAt))
	fmt.Printf("  draw distance  %.0f m\n", p.DrawDistance)
	fmt.Printf("  region handle  %d\n", p.RegionHandle)

	if *region {
		r, err := w.Region(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("\nregion %q\n", r.Name)
		fmt.Printf("  id             %s\n", r.ID)
		fmt.Printf("  handle         %d\n", r.Handle)
		fmt.Printf("  flags          %#08x (extended %#x)\n", r.Flags, r.Extended)
		fmt.Printf("  access         %d\n", r.Access)
		fmt.Printf("  owner          %s\n", r.Owner)
		fmt.Printf("  estate manager %v\n", r.EstateManager)
		fmt.Printf("  water height   %.1f m\n", r.WaterHeight)
		fmt.Printf("  product        %q (sku %s)\n", r.ProductName, r.ProductSKU)
		fmt.Printf("  hosted at      %s, cpu class %d, ratio %d\n",
			r.ColoName, r.CPUClass, r.CPURatio)
		fmt.Printf("  protocols      %#x\n", r.Protocols)
	}

	if *flush {
		n, err := w.Flush(ctx)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("\nflushed %d objects from the cache\n", n)
	}

	if *inv {
		start := time.Now()
		tree, err := w.Inventory(ctx)
		if err != nil {
			log.Fatal(err)
		}
		folders, items := tree.Counts()
		fmt.Printf("\ninventory: %d folders, %d items in %s\n",
			folders, items, time.Since(start).Round(time.Millisecond))
		n := 0
		tree.Walk(func(f *world.Folder, depth int) bool {
			if n++; n > 12 {
				return false
			}
			fmt.Printf("  %*s%-40s %3d items\n", depth*2, "", f.Name,
				len(tree.Contents(f.ID)))
			return true
		})
		if n > 12 {
			fmt.Printf("  ... and %d more folders\n", folders-12)
		}
	}

	needRegion := *all || *named != "" || *id != ""
	if !needRegion {
		return
	}

	// Nothing beyond the draw distance is described at all, and what is
	// within it arrives over several seconds.
	fmt.Printf("\nletting the region arrive (%s)\n", *settle)
	if err := w.Settle(ctx, *settle); err != nil {
		log.Fatal(err)
	}

	if *id != "" {
		u, err := msg.ParseUUID(*id)
		if err != nil {
			log.Fatalf("bad -id: %v", err)
		}
		s, err := w.ObjectByID(ctx, u, 20*time.Second)
		if err != nil {
			log.Fatal(err)
		}
		describe(s)
	}

	if *named != "" {
		start := time.Now()
		found, err := w.ObjectsNamed(ctx, *named, 60*time.Second)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("\n%d objects named %q (%s)\n",
			len(found), *named, time.Since(start).Round(time.Millisecond))
		for _, s := range found {
			describe(s)
		}
	}

	if *all {
		start := time.Now()
		found, err := w.AllObjects(ctx, 60*time.Second)
		if err != nil {
			log.Fatal(err)
		}
		roots, avatars, ours := 0, 0, 0
		for _, s := range found {
			if s.IsRoot() {
				roots++
			}
			if s.IsAvatar() {
				avatars++
			}
			if s.Owner == w.Me() {
				ours++
			}
		}
		fmt.Printf("\n%d objects nearby in %s: %d roots, %d avatars, %d ours\n",
			len(found), time.Since(start).Round(time.Millisecond), roots, avatars, ours)
		fmt.Printf("%-11s %-30s %-22s %s\n", "local", "name", "position", "owner")
		shown := 0
		for _, s := range found {
			if shown++; shown > 400 {
				break
			}
			role := "root"
			if !s.IsRoot() {
				role = fmt.Sprintf("child/%d", s.Parent)
			}
			fmt.Printf("%-11d %-30s %-22s %-9s %s\n",
				s.Local, trunc(s.Name, 30), vec(s.Position), role, short(s.Owner))
		}
	}
}

func describe(s *world.Seen) {
	fmt.Printf("\n%s\n", s.Object)
	fmt.Printf("  owner     %s\n", s.Owner)
	fmt.Printf("  position  %s\n", vec(s.Position))
	fmt.Printf("  scale     %s\n", vec(s.Scale))
	fmt.Printf("  pcode     %d (avatar: %v)\n", s.PCode, s.IsAvatar())
	if s.IsRoot() {
		fmt.Printf("  root\n")
	} else {
		fmt.Printf("  child of local %d\n", s.Parent)
	}
}

func vec(v msg.Vector3) string {
	return fmt.Sprintf("<%.1f, %.1f, %.1f>", v.X, v.Y, v.Z)
}

func short(u msg.UUID) string {
	s := u.String()
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func trunc(s string, n int) string {
	if s == "" {
		return "-"
	}
	if len(s) > n {
		return s[:n-1] + "*"
	}
	return s
}
