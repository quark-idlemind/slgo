// Command slgo-auto sets an avatar up for benchmarking.
//
// automate and autobench run their scripts in worn objects called
// "auto", "auto 2" and so on, and take one of them for as long as a run
// lasts.  Getting them there is the slow part -- a rez, a name, a take
// and a wear apiece -- and it only has to happen once per account.  This
// does it up front, so the first benchmark of the day is not the one
// that pays.
//
//	slgo-auto -n 12
//	slgo-auto -agent qi -n 12
//
// Twelve is three concurrent benchmarks of four objects each.  Fewer is
// fine; the pool bounds how much can run at once, not whether anything
// can.
//
// An avatar that may not rez can still be set up, provided it has been
// GIVEN one object by an avatar that may: everything after the first is
// a copy, and copying something already owned asks the land nothing.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/sl"
)

func main() {
	var (
		addr  = flag.String("addr", "", "the slgod to attach to; default sl-host, or this machine")
		agent = flag.String("agent", "", "the profile to use; the daemon's default, by default")
		n     = flag.Int("n", 4, "how many auto objects to set up")
		list  = flag.Bool("l", false, "only say what is already there")
	)
	flag.Parse()

	if *n < 1 {
		*n = 1
	}
	if max := len(session.AutoPoints); *n > max {
		log.Printf("only %d attachment slots are defined; using that", max)
		*n = max
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	s, err := sl.Dial(ctx, slhost.MustAddr(*addr), *agent)
	if err != nil {
		log.Fatalf("cannot attach: %v", err)
	}
	defer s.Close()

	// Say which avatar, always.  With several hosted, an unnamed one is
	// the daemon's choice and not something the reader can work out.
	fmt.Printf("%s (%s)\n", s.Info().AvatarName, s.Info().Name)

	if *list {
		report(ctx, s)
		return
	}

	start := time.Now()
	objs, err := session.SetupAuto(ctx, s, *n)
	if err != nil {
		log.Fatalf("setting up: %v", err)
	}

	fmt.Printf("%d of %d objects ready in %v\n", len(objs), *n, time.Since(start).Round(time.Second))
	for i, o := range objs {
		fmt.Printf("  %2d  %-10s %s  on %s\n",
			i, session.AutoName(i), o.ID, sl.AttachPointName(session.AutoPoints[i]))
	}
	if len(objs) < *n {
		fmt.Fprintf(os.Stderr,
			"\nonly %d of %d: fewer objects means less can run at once, not that anything is broken\n",
			len(objs), *n)
	}
}

// report says what is worn now, without changing anything.
func report(ctx context.Context, s *sl.Session) {
	worn, err := s.WornObjects(ctx)
	if err != nil {
		log.Fatalf("listing what is worn: %v", err)
	}
	fmt.Printf("%d attachment(s):\n", len(worn))
	for _, a := range worn {
		fmt.Printf("  %-14s %s  on %s\n", a.Object.Name, a.Object.ID, sl.AttachPointName(a.Point))
	}
}
