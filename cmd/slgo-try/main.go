// Command slgo-try runs a few scripts in a HUD and prints what they
// said.
//
// It exists to show what the world package is for.  Every experiment
// here was written before against the message layer, and each one came
// to two or three hundred lines of finding the attachment, chasing
// item ids, starting a listener before the compile and polling for
// confirmations.  The same experiments are below in a dozen lines
// each, because the coordination is the package's problem now.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"slgo/world"
)

var (
	addr    = flag.String("server", "127.0.0.1:7807", "slgod address")
	profile = flag.String("agent", "example", "hosted agent")
	hud     = flag.String("hud", "Test HUD", "attachment to run scripts in")
)

// done is the sentinel every script below ends with.  Run returns as
// soon as it is heard, so a script that finishes early does not cost
// the whole timeout.
const done = "SLGO-DONE"

type try struct {
	name   string
	source string
	expect string // what the run should show, for the summary
}

var tries = []try{
	{"slgo try ok", `default
{
    state_entry()
    {
        llOwnerSay("hello from " + llGetScriptName());
        llOwnerSay("` + done + `");
    }
}
`, "compiles and speaks"},

	{"slgo try syntax", `default
{
    state_entry()
    {
        llOwnerSay("unterminated
    }
}
`, "does not compile"},

	{"slgo try divzero", `default
{
    state_entry()
    {
        integer d = 0;
        llOwnerSay("dividing");
        integer n = 1 / d;
        llOwnerSay("` + done + `");
    }
}
`, "compiles, then faults and never reaches the sentinel"},

	{"slgo try stack", `integer deep(integer n)
{
    return deep(n + 1);
}

default
{
    state_entry()
    {
        llOwnerSay("recursing");
        deep(0);
        llOwnerSay("` + done + `");
    }
}
`, "compiles, then collides and never reaches the sentinel"},

	{"slgo try texture", `default
{
    state_entry()
    {
        llSetTexture("no texture at all", 0);
        llOwnerSay("still here");
        llOwnerSay("` + done + `");
    }
}
`, "complains but carries on"},
}

func main() {
	flag.Parse()
	ctx := context.Background()

	w, err := world.Dial(ctx, *addr, *profile)
	if err != nil {
		log.Fatal(err)
	}
	defer w.Close()
	fmt.Printf("%s in %s\n\n", w.Info().AvatarName, w.Info().Region)

	objects, err := w.Objects(ctx)
	if err != nil {
		log.Fatal(err)
	}
	worn, err := w.Worn(ctx, objects, *hud, world.HUDCenter1)
	if err != nil {
		log.Fatal(err)
	}
	obj := &worn.Object
	fmt.Printf("running in %s\n", obj)

	// Leftovers from an earlier run would talk over this one.
	kept := map[string]bool{}
	for _, t := range tries {
		kept[t.name] = true
	}
	n, err := w.RemoveScripts(ctx, obj, func(name string) bool {
		return strings.HasPrefix(name, "slgo ") && !kept[name]
	})
	if err != nil {
		log.Fatal(err)
	}
	if n > 0 {
		fmt.Printf("removed %d leftover scripts\n", n)
	}

	bad := 0
	for _, t := range tries {
		fmt.Printf("\n=== %s (%s) ===\n", t.name, t.expect)

		res, err := w.Run(ctx, world.Script{
			In:      obj,
			Name:    t.name,
			Source:  t.source,
			Done:    done,
			Timeout: 20 * time.Second,
		})
		if err != nil {
			fmt.Printf("  failed: %v\n", err)
			bad++
			continue
		}

		fmt.Printf("  compiled %v, finished %v, %s\n",
			res.Compiled, res.Finished, res.Elapsed.Round(time.Millisecond))
		if res.Fault != nil {
			fmt.Printf("  FAULT: %s\n", res.Fault)
		}
		for _, e := range res.Errors {
			fmt.Printf("  compile error: %s\n", e)
		}
		for _, l := range res.Lines {
			fmt.Printf("  [%s] %s\n", world.ChatTypeName(l.Type), l.Text)
		}
	}

	if bad > 0 {
		os.Exit(1)
	}
}
