// Command slgod holds grid connections and serves clients.
//
//	slgod -listen :7807 example builder
//
// Each argument names a profile under ~/.config/slgo.  The connections
// stay up until the process is signalled; clients attach and detach
// freely without the grid noticing.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
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
		group   = flag.String("group", "", "group to act as, by name or uuid; the only one joined, by default")
	)
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

	for _, name := range flag.Args() {
		login, err := agent.LoadProfile(name)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		if *start != "" {
			login.Start = *start
		}
		if login.Channel == "" {
			login.Channel = "slgo"
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

		log.Printf("%s: logging in...", name)
		h, err := srv.Host(ctx, name, login, opts)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
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
	// once, at login, and every client attached to the agent shares it.
	// A restarted slgod is a fresh login, so it must be settled again.
	for _, name := range srv.Names() {
		h, ok := srv.Agent(name)
		if !ok {
			continue
		}
		a := h.Agent()
		g, why, err := chooseGroup(ctx, a, *group)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		if g.IsZero() {
			log.Printf("%s: no active group (%s); parcels that only let a group build will refuse", name, why)
			continue
		}
		if err := activateGroup(a, g); err != nil {
			log.Printf("%s: could not activate group: %v", name, err)
		} else {
			log.Printf("%s: acting as group %s (%s)", name, why, g)
		}
	}

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

// activateGroup makes a group the avatar's active one.
//
// Fire and forget, like most of this protocol: the simulator answers
// with an AgentDataUpdate, which the agent records, and there is nothing
// to wait for here.
func activateGroup(a *agent.Agent, group msg.UUID) error {
	m := &msg.ActivateGroup{}
	m.AgentData.AgentID = a.Account.AgentID
	m.AgentData.SessionID = a.Account.SessionID
	m.AgentData.GroupID = group
	return a.Send.Send(context.Background(), m)
}
