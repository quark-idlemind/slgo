// Command slgod holds grid connections and serves clients.
//
//	slgod -listen :7807 example builder
//
// Each argument names a profile under ~/.config/slgo.  The connections
// stay up until the process is signalled; clients attach and detach
// freely without the grid noticing.
//
// slgod's own settings -- the machine identity it presents to the login
// server -- live in ~/.config/slgod/config, written on the first run.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/auth"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/server"
)

func main() {
	var (
		listen  = flag.String("listen", ":7807", "address to serve clients on")
		noAuth  = flag.Bool("no-auth", false, "serve without authentication; loopback only, and it is not checked")
		verbose = flag.Bool("v", false, "log every message the grid sends")
		start   = flag.String("start", "", "override the profile's start location")
		group   groupFlag
	)
	flag.Var(&group, "group",
		"group to act as, by name or uuid, or PROFILE=GROUP; overrides the profile's own")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: slgod [-listen addr] [-v] profile [profile...]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	log.SetFlags(log.Ltime)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New()

	// Which computer the login server is told this is.  Made up once
	// and kept, so every login comes from the same machine; see
	// machine.go.
	mach, machPath, err := loadMachineID()
	if err != nil {
		log.Fatalf("cannot start: %v", err)
	}
	log.Printf("machine %s, id0 %s (from %s)", mach.MAC, mach.ID0, machPath)

	// The logins that came up, so that the group loop below knows which
	// profile each hosted session was made from.
	hosted := map[string]agent.Login{}

	// loginFor is how a profile becomes a login, in one place, so that
	// a session started later on request is set up exactly like one
	// named on the command line.  The server calls it too; see
	// SetProfiles.
	loginFor := func(name string) (agent.Login, agent.Options, error) {
		login, err := agent.LoadProfile(name)
		if err != nil {
			return login, agent.Options{}, err
		}
		if *start != "" {
			login.Start = *start
		}
		if login.Channel == "" {
			login.Channel = "slgo"
		}
		// A profile may name its own, for an account that has
		// always logged in from somewhere else.
		if login.MAC == "" {
			login.MAC = mach.MAC
		}
		if login.ID0 == "" {
			login.ID0 = mach.ID0
		}

		opts := agent.Options{
			// A message nobody has registered for is how a
			// protocol change announces itself.
			OnUnhandled: func(p *msg.Packet) {
				if *verbose {
					log.Printf("%s: no handler for %s", name, p.ID)
				}
			},
			OnError: func(p *msg.Packet) {
				log.Printf("%s: undecodable packet: %v", name, p.Err)
			},
		}
		return login, opts, nil
	}

	for _, name := range flag.Args() {
		login, opts, err := loginFor(name)
		if err != nil {
			log.Printf("%s: NOT hosted: %v", name, err)
			continue
		}

		log.Printf("%s: logging in...", name)
		h, err := srv.StartAgent(ctx, name, login, opts)
		if err != nil {
			// Not fatal.  One expired password should not take down
			// the sessions that did come up, which are somebody's
			// benchmark in progress.
			log.Printf("%s: NOT hosted: %v", name, err)
			continue
		}
		h.Log = log.Printf
		hosted[name] = login
		a := h.Agent()
		log.Printf("%s: %s in %s, %d capabilities",
			name, a.Account.Name(), orUnknown(a.RegionName()), len(a.Caps))

		// The server re-establishes a session that ends; this
		// just says so.
		go func(name string, h *server.Hosted) {
			for {
				a := h.Agent()
				if a == nil {
					return
				}
				<-a.Done()
				if err := a.Err(); err != nil {
					log.Printf("%s: connection ended: %v", name, err)
				} else {
					log.Printf("%s: connection ended", name)
					return // a clean logout is not retried
				}
				// Wait for the supervisor to put a new
				// one in place.
				for h.Agent() == a {
					select {
					case <-ctx.Done():
						return
					case <-time.After(time.Second):
					}
				}
				if a2 := h.Agent(); a2 != nil {
					log.Printf("%s: reconnected, %s in %s",
						name, a2.Account.Name(), orUnknown(a2.RegionName()))
				}
			}
		}(name, h)
	}

	// The active group, which decides whether a parcel lets this avatar
	// build at all.
	//
	// A parcel usually grants "create objects" to a GROUP rather than to
	// individuals, and a login starts with NONE active. A viewer hides
	// this by storing the group in its settings and re-sending it every
	// time, which makes it feel permanent; headless it is not. So an
	// avatar that builds happily through a viewer cannot rez a thing
	// here, and the refusal blames the land -- the wrong place to look.
	//
	// This belongs to the session rather than to a client: it is settled
	// here, and every client attached to the agent shares it.  A
	// restarted slgod is a fresh login, so it must be settled again --
	// and so is a RECONNECT, which is why the answer is handed to the
	// server to remember rather than sent from here.  See server/group.go.
	if len(hosted) == 0 {
		log.Fatal("no session came up; nothing to serve")
	}

	// settle is everything a session needs after it is up.  Used both
	// for the ones named on the command line and for any started later
	// on request, so that a session cannot be half set up depending on
	// how it came to exist.
	settle := func(h *server.Hosted) {
		name := h.Name
		want := ""
		if l, ok := hosted[name]; ok {
			want = l.Group
		} else if l, _, err := loginFor(name); err == nil {
			want = l.Group
		}

		g, why, err := chooseGroup(ctx, h.Agent(), group.For(name, want))
		if err != nil {
			// Not fatal: an ambiguous group name is a reason this
			// avatar cannot build, not a reason to take the others
			// down with it.
			log.Printf("%s: no active group: %v", name, err)
			return
		}
		if g.IsZero() {
			log.Printf("%s: no active group (%s); parcels that only let a group build will refuse", name, why)
			return
		}
		// Through the server, which remembers it and puts it back
		// after a reconnect -- a fresh login has no active group, and
		// losing it is otherwise silent.
		if err := h.SetGroup(ctx, g); err != nil {
			log.Printf("%s: could not activate group: %v", name, err)
		} else {
			log.Printf("%s: acting as group %s (%s)", name, why, g)
		}
	}

	for _, name := range srv.Names() {
		if h, ok := srv.Agent(name); ok {
			settle(h)
		}
	}

	// What may be started later, and how.  Without these the server can
	// only serve what it was given on the command line.
	srv.SetProfiles(func() []string {
		names, err := agent.ListProfiles()
		if err != nil {
			return nil
		}
		return names
	}, loginFor)

	// A session started on request belongs to the daemon, not to the
	// call that asked for it, and gets the same settling as the rest.
	srv.SetBase(ctx, log.Printf, func(h *server.Hosted) {
		log.Printf("%s: started on request, %s in %s",
			h.Name, h.Agent().Account.Name(), orUnknown(h.Agent().RegionName()))
		settle(h)
	})

	// slgod holds a live Second Life session, so an unauthenticated one
	// reachable off this machine lets anyone drive the avatar. On by
	// default for that reason; --no-auth is for a loopback-only run.
	if !*noAuth {
		secret, err := auth.LoadSecret("")
		if err != nil {
			log.Fatalf("cannot start: %v\n"+
				"Create one with:  (umask 077; mkdir -p ~/.config/slrun; "+
				"openssl rand -hex 32 > ~/.config/slrun/secret)\n"+
				"Or pass -no-auth to serve loopback without it.", err)
		}
		a, err := auth.New(secret)
		if err != nil {
			log.Fatalf("cannot start: %v", err)
		}
		srv.SetAuth(a)
		log.Print("TLS on, certificate self-signed and unverified; " +
			"authentication is mutual and bound to the TLS session")
	} else {
		log.Print("WARNING: serving without authentication")
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving gRPC on %s for %s", ln.Addr(), strings.Join(srv.Names(), ", "))

	go func() {
		if err := srv.Serve(ctx, ln); err != nil {
			log.Printf("serve: %v", err)
		}
	}()

	<-ctx.Done()
	stop()
	log.Print("logging out...")

	shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	srv.Close(shutdown)
	log.Print("done")
}

func orUnknown(s string) string {
	if s == "" {
		return "an unnamed region"
	}
	return s
}

// chooseGroup decides which group to act as, and says why.
//
// The simulator lists every group the avatar has joined, so the choice
// can usually be made without being told: one group means there is no
// ambiguity to resolve. Several means the answer is not derivable and
// the operator has to name it -- guessing there would silently pick the
// wrong land rights, and building nothing is better than building in
// the wrong place under the wrong group.
func chooseGroup(ctx context.Context, a *agent.Agent, want string) (msg.UUID, string, error) {
	// A uuid needs no list, so it works even if the list never arrives.
	if id, err := msg.ParseUUID(want); want != "" && err == nil {
		return id, want, nil
	}

	joined := a.WaitGroups(ctx, 15*time.Second)

	if want != "" {
		var match []agent.Group
		for _, g := range joined {
			if strings.EqualFold(g.Name, want) {
				match = append(match, g)
			}
		}
		switch len(match) {
		case 1:
			return match[0].ID, match[0].Name, nil
		case 0:
			return msg.UUID{}, "", fmt.Errorf("no group named %q; joined: %s", want, groupNames(joined))
		default:
			return msg.UUID{}, "", fmt.Errorf("%q names %d groups; use the uuid", want, len(match))
		}
	}

	switch len(joined) {
	case 1:
		return joined[0].ID, joined[0].Name, nil
	case 0:
		return msg.UUID{}, "none joined", nil
	default:
		return msg.UUID{}, fmt.Sprintf("%d joined, none chosen: %s", len(joined), groupNames(joined)), nil
	}
}

func groupNames(gs []agent.Group) string {
	var names []string
	for _, g := range gs {
		names = append(names, strconv.Quote(g.Name))
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// groupFlag is -group, which may be given once for every session.
//
// A bare value applies to every profile, which is what it has always
// meant and is unambiguous while only one avatar is hosted.  With
// several, a value has to say which:
//
//	slgod -group Builders example
//	slgod -group example=Builders -group qi=Testers example qi
//
// The ordinary place for this is the profile's "group =" line.  The
// flag is for a one-off, and it wins.
type groupFlag struct {
	all  string
	each map[string]string
}

func (g *groupFlag) String() string {
	if g == nil || (g.all == "" && len(g.each) == 0) {
		return ""
	}
	if g.all != "" {
		return g.all
	}
	var parts []string
	for k, v := range g.each {
		parts = append(parts, k+"="+v)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func (g *groupFlag) Set(s string) error {
	// A group NAME may contain an "=" in principle, so only a
	// left-hand side that looks like a profile name is taken as one.
	name, want, ok := strings.Cut(s, "=")
	if !ok {
		g.all = s
		return nil
	}
	if g.each == nil {
		g.each = map[string]string{}
	}
	g.each[strings.TrimSpace(name)] = strings.TrimSpace(want)
	return nil
}

// For is the group this profile should act as: the flag if it names
// one, otherwise whatever the profile itself asked for.
func (g *groupFlag) For(name, fromProfile string) string {
	if want, ok := g.each[name]; ok {
		return want
	}
	if g.all != "" {
		return g.all
	}
	return fromProfile
}
