// Command slgo-parity runs the same script in the same object that the
// other client on this machine runs, so the two can be compared.
//
// That other client is a C# one built on LibreMetaverse, driving Second
// Life through a task-level gRPC service. It works, and it is the thing
// slgo would replace, so "slgo can do this too" is a claim that has to
// be demonstrated against the same object rather than argued from the
// source.
//
//	slgo-parity -object Box1
//
// What it checks is the whole round trip: find a named object, put a
// script inside it, have the simulator compile it, and collect what it
// says. Every one of those steps is a separate thing that can fail, and
// the C# path took a day to get right.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

const source = `default {
    state_entry() {
        llOwnerSay("Owner: " + llKey2Name(llGetOwner()));
        llOwnerSay("Object: " + llGetObjectName());
        llOwnerSay("Free memory: " + (string)llGetFreeMemory());
        llOwnerSay("SLGO-DONE");
    }
}`

func main() {
	server := flag.String("server", "127.0.0.1:7807", "slgod to attach to")
	agent := flag.String("agent", "example", "which hosted agent")
	object := flag.String("object", "Box1", "object to run the script in")
	name := flag.String("name", "slgo-parity", "what to call the script inside it")
	timeout := flag.Duration("timeout", 60*time.Second, "how long to wait for the sentinel")
	draw := flag.Float64("draw", 0, "set the draw distance first, which makes the region describe more of itself")
	flag.Parse()

	ctx := context.Background()
	w, err := sl.Dial(ctx, *server, *agent)
	if err != nil {
		die("attach: %v", err)
	}
	defer w.Close()

	where, err := w.Where(ctx)
	if err != nil {
		die("where: %v", err)
	}
	fmt.Printf("attached: %s at %v, camera %v, draw distance %.0fm\n",
		where.Region, where.Position, where.Camera, where.DrawDistance)

	if *draw > 0 {
		p, err := w.SetDrawDistance(ctx, float32(*draw))
		if err != nil {
			die("draw distance: %v", err)
		}
		fmt.Printf("draw:     %.0fm, settling\n", p.DrawDistance)
		if err := w.Settle(ctx, 10*time.Second); err != nil {
			die("settle: %v", err)
		}
	}

	// The object has to be in the interest list before it can be found,
	// and the interest list is computed from the draw distance.
	found, err := w.ObjectsNamed(ctx, *object, 30*time.Second)
	if err != nil {
		die("looking for %q: %v", *object, err)
	}
	if len(found) == 0 {
		// "Not there" and "beyond the draw distance" are the same answer
		// from ObjectsNamed, so say which by reporting what IS in range.
		all, aerr := w.AllObjects(ctx, 30*time.Second)
		if aerr != nil {
			die("no object named %q, and listing what is here failed: %v", *object, aerr)
		}
		fmt.Fprintf(os.Stderr, "no object named %q; %d object(s) in range:\n", *object, len(all))
		for i, s := range all {
			if i == 25 {
				fmt.Fprintf(os.Stderr, "  ... and %d more\n", len(all)-i)
				break
			}
			fmt.Fprintf(os.Stderr, "  %-28s %v owner=%s\n", s.Name, s.Position, s.Owner)
		}
		os.Exit(1)
	}
	target := found[0]
	fmt.Printf("found:    %s %s at %v\n", target.Name, target.ID, target.Position)

	start := time.Now()
	res, err := w.Run(ctx, sl.Script{
		In: &target.Object, Name: *name, Source: source,
		Done: "SLGO-DONE", Timeout: *timeout,
	})
	if err != nil {
		die("run: %v", err)
	}

	fmt.Printf("compiled: %v", res.Compiled)
	for _, e := range res.Errors {
		fmt.Printf("  %s", e)
	}
	fmt.Println()
	for _, l := range res.Lines {
		fmt.Printf("  | %s\n", l.Text)
	}
	if res.Fault != nil {
		fmt.Printf("fault:    %s\n", res.Fault)
	}
	fmt.Printf("finished: %v in %v (wall %v)\n", res.Finished, res.Elapsed, time.Since(start).Round(time.Millisecond))

	// Leave the object as it was found. An experiment that does not
	// clean up after itself is how an object ends up holding a dozen
	// numbered copies of the same script.
	if n, err := w.RemoveScripts(ctx, &target.Object, func(s string) bool { return s == *name }); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not remove %q: %v\n", *name, err)
	} else {
		fmt.Printf("cleaned:  %d script(s) removed\n", n)
	}
}

func die(format string, v ...any) {
	fmt.Fprintf(os.Stderr, "slgo-parity: "+format+"\n", v...)
	os.Exit(1)
}
