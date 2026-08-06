// Command slgo-reg finds out whether a script put into an object over
// the protocol actually runs, and whether what it says comes back.
//
// The smallest possible script: it says one thing when it starts, and
// starts again when it is rezzed or attached. If that is heard from a
// rezzed prim but not from a worn one, the problem is attachments; if it
// is silent both ways, the problem is putting the script in or starting
// it. Either answer halves what is left to look at.
//
//	slgo-reg -rez          build a prim, put the script in, listen
//	slgo-reg -rez -wear    ...then take it and wear it, and listen again
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

const hello = `default
{
    state_entry()
    {
        llOwnerSay("Hello World");
    }

    on_rez(integer param)
    {
        llResetScript();
    }

    attach(key id)
    {
        if (id != NULL_KEY) llResetScript();
    }
}`

func main() {
	server := flag.String("server", "127.0.0.1:7807", "slgod")
	agent := flag.String("agent", "example", "hosted agent")
	rez := flag.Bool("rez", false, "build a prim and put the script in it")
	wear := flag.Bool("wear", false, "then take it and wear it")
	listen := flag.Duration("listen", 20*time.Second, "how long to listen after each step")
	say := flag.String("say", "", "send this on the channel first, then listen")
	script := flag.String("script", "", "install this LSL file instead of the built-in hello")
	hammer := flag.String("hammer", "", "comma separated commands to send one after another, 10s apart")
	sweep := flag.String("sweep", "", "comma separated CHANNELS to say a marker on, one after another")
	channel := flag.Int("channel", -1701, "channel to say it on")
	flag.Parse()

	ctx := context.Background()
	w, err := sl.Dial(ctx, *server, *agent)
	if err != nil {
		die("attach: %v", err)
	}
	defer w.Close()

	if !*rez {
		lines := w.Chat(sl.ChatFilter{}, 64)
		defer w.StopChat(lines)
		if *say != "" {
			if err := w.Say(ctx, *say, int32(*channel)); err != nil {
				die("say: %v", err)
			}
			fmt.Printf("said %q on %d\n", *say, *channel)
		}
		drain(lines, *listen, "listening")
		return
	}

	where, err := w.Where(ctx)
	if err != nil {
		die("where: %v", err)
	}
	at := where.Position
	at.X += 2

	built, err := w.Build(ctx, []sl.Prim{{Name: "slgo hello", Position: at}})
	if err != nil {
		die("build: %v", err)
	}
	fmt.Printf("built %s at %v\n", built.Root.Name, at)

	// Listen from BEFORE the script goes in: state_entry runs the moment
	// it starts, and a subscription made afterwards would miss the one
	// line the whole test is about.
	lines := w.Chat(sl.ChatFilter{}, 64)
	defer w.StopChat(lines)

	source := hello
	if *script != "" {
		b, err := os.ReadFile(*script)
		if err != nil {
			die("%v", err)
		}
		source = string(b)
	}

	up, err := w.InstallScript(ctx, built.Root, "slgo hello script", source, true)
	if err != nil {
		die("install: %v", err)
	}
	fmt.Printf("installed, compiled=%v errors=%v\n", up.Compiled, up.Errors)

	drain(lines, *listen, "rezzed")

	if *sweep != "" {
		for _, c := range strings.Split(*sweep, ",") {
			c = strings.TrimSpace(c)
			n, err := strconv.ParseInt(c, 10, 64)
			if err != nil {
				die("bad channel %q: %v", c, err)
			}
			ch := int32(n)
			fmt.Printf(">>> channel %d (int32 %d)\n", n, ch)
			// Every chat type, because "which of whisper, normal and
			// shout does the simulator accept here" is one of the few
			// things left that could differ.
			for _, t := range []uint8{0, 1, 2} {
				if err := w.SayAs(ctx, fmt.Sprintf("mark%s-type%d", c, t), ch, t); err != nil {
					die("say: %v", err)
				}
			}
			drain(lines, 6*time.Second, "channel "+c)
		}
		return
	}

	// Hammer it: the register answered once and then went quiet, so what
	// matters is not whether it replies but whether it keeps replying.
	if *hammer != "" {
		for _, cmd := range strings.Split(*hammer, ",") {
			cmd = strings.TrimSpace(cmd)
			fmt.Printf(">>> %s\n", cmd)
			if err := w.Say(ctx, cmd, int32(*channel)); err != nil {
				die("say: %v", err)
			}
			drain(lines, 10*time.Second, cmd)
		}
	}

	if *wear {
		objects, err := w.ObjectsFolder(ctx)
		if err != nil {
			die("objects folder: %v", err)
		}
		if _, err := w.Take(ctx, built.Root, objects, 60*time.Second); err != nil {
			die("take: %v", err)
		}
		fmt.Println("taken into inventory")

		if _, err := w.Worn(ctx, objects, "slgo hello", 31); err != nil {
			die("wear: %v", err)
		}
		fmt.Println("worn on HUD Center 2")
		drain(lines, *listen, "worn")
	}
}

func drain(lines <-chan sl.Line, d time.Duration, why string) {
	fmt.Printf("--- listening %v (%s)\n", d, why)
	deadline := time.After(d)
	heard := 0
	for {
		select {
		case l := <-lines:
			heard++
			fmt.Printf("    [type %d from %s] %q\n", l.Type, l.Source, l.Text)
		case <-deadline:
			if heard == 0 {
				fmt.Println("    (nothing at all)")
			}
			return
		}
	}
}

func die(format string, v ...any) {
	fmt.Fprintf(os.Stderr, "slgo-reg: "+format+"\n", v...)
	os.Exit(1)
}
