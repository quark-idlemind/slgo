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
	"sync"
	"syscall"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/auth"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/server"
	"github.com/quark-idlemind/slgo/viewer"
)

func main() {
	var (
		listen    = flag.String("listen", ":7807", "address to serve clients on")
		noAuth    = flag.Bool("no-auth", false, "serve without authentication; loopback only, and it is not checked")
		verbose   = flag.Bool("v", false, "log every message the grid sends")
		start     = flag.String("start", "", "override the profile's start location")
		trace     = flag.String("trace", "", "write a packet trace to this file")
		traceMsgs = flag.String("trace-messages", "",
			"comma separated message names to trace; empty traces every one")
		traceBodies = flag.Bool("trace-bodies", false,
			"write each traced message out in full, rather than one line naming it")
		viewerAt = flag.String("viewer", "",
			"serve viewer logins on this address, so a real viewer can be handed a session")
		neighbours = flag.Bool("neighbours", false,
			"hold a circuit to each neighbouring region, so the avatar can walk over a border;"+
				" a profile's own neighbours setting wins over this")
		group groupFlag
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

	// The record of what crossed the wire.  It is here rather than in
	// the viewer work proper because it is worth having before there
	// is a viewer: a baseline of what an ordinary session receives is
	// what a handover's trace has to be compared against, and it
	// cannot be collected afterwards.
	//
	// Both are shared by every session, so one file holds the whole
	// daemon in arrival order.  Which avatar a packet belonged to is
	// not recorded yet; when a viewer circuit exists there will be two
	// directions to tell apart and that is the point to add it.
	census := viewer.NewCensus()
	var tracer *viewer.Trace
	if *trace != "" {
		f, err := os.Create(*trace)
		if err != nil {
			log.Fatalf("trace: %v", err)
		}
		defer f.Close()
		var only []string
		if *traceMsgs != "" {
			only = strings.Split(*traceMsgs, ",")
			for i := range only {
				only[i] = strings.TrimSpace(only[i])
			}
		}
		tracer = viewer.NewTrace(f, only, *traceBodies)
		log.Printf("tracing to %s", *trace)
		defer func() {
			written, skipped := tracer.Stats()
			log.Printf("trace: %d entries written, %d skipped by the filter", written, skipped)
			log.Printf("census:\n%s", census.Report())
		}()
	}

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
	//
	// Guarded because a session may now come up minutes after the
	// others, from the retry below, while settle is reading this.
	var hostMu sync.Mutex
	hosted := map[string]agent.Login{}

	// The viewer endpoint, prepared before any session comes up.  The
	// relay has to be wired into a session as it logs in -- a session
	// that came up without it could never pass anything to a viewer
	// afterwards -- and that is long before there is an endpoint to
	// serve.
	var viewers *viewerHost
	if *viewerAt != "" {
		host, _, err := viewer.HostPort(*viewerAt)
		if err != nil {
			log.Fatalf("viewer: %v", err)
		}
		viewers = newViewerHost(ctx, host, srv, func(profile string) string {
			hostMu.Lock()
			defer hostMu.Unlock()
			return hosted[profile].ViewerPassword
		}, census, tracer, log.Printf)
	}

	// waiting is a profile whose login failed in a way that may clear,
	// kept until the server is set up enough to receive it.
	type waiting struct {
		name  string
		login agent.Login
		opts  agent.Options
	}
	var pending []waiting

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
		// Asked for always, not when a viewer turns up.  A viewer is
		// handed a session that is already running, and the login
		// server answered that session's one question hours earlier;
		// a block not requested then cannot be requested now, and
		// the viewer would come up missing it with no way to say so.
		//
		// It is close to free.  Measured on Aditi: 2.84s against
		// 2.93s for a plain login, which is noise, for eleven more
		// top-level blocks -- most of the bulk being the Library
		// skeleton, which is the same for every avatar.
		login.Options = append(login.Options, agent.ViewerOptions...)

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
			// Off unless asked for: a border crossing needs
			// these circuits, and a daemon that only ever
			// acts where its avatar stands does not.  The
			// profile has the last word and the flag is what
			// a profile with no opinion gets, because this
			// is a property of an avatar rather than of the
			// process -- one daemon holds an avatar somebody
			// walks about with and another that runs
			// benchmarks in one region.  Either can be
			// turned over afterwards; see
			// agent.SetNeighbours and slsh's neighbours.
			Neighbours: holdNeighbours(login, *neighbours),
			// So that a child circuit opening and closing is
			// visible.  A client can list what is held now,
			// but only as it stands: one that opened and
			// closed between two questions shows up here and
			// nowhere else.
			Log: func(format string, v ...any) {
				log.Printf("%s: "+format, append([]any{name}, v...)...)
			},
		}
		if viewers != nil {
			// The session stays the only reader of the simulator's
			// event queue; this is a copy for a viewer, not a
			// second poller.  Two pollers would split the events
			// between them at random.
			opts.OnEvent = viewers.eventsFor(name)
			// What the region says, passed on to a viewer if one
			// is attached.  The relay rather than the tap,
			// because a tap sees retransmissions and the far end
			// cannot tell one of those from a second message.
			opts.Relay = viewers.relayFor(name)
			// The avatar moving out from under an attached
			// viewer, which only the daemon finds out: the grid
			// announces a teleport to the session, and a viewer
			// that did not ask for one is told nothing at all.
			// Set here and chained by server.StartAgent with the
			// notice it sends its own clients, so neither loses
			// the other.
			opts.OnRegionChange = viewers.movedFor(name)
		}
		if *trace != "" {
			// The tap, so this is the wire as it really was --
			// retransmissions included, since it runs ahead of
			// duplicate suppression.  That is right for a
			// transcript and worth stating, because the viewer
			// side records from the relay instead and will not
			// show them.
			//
			// Nothing is forwarded anywhere yet, so every packet
			// is recorded as having had no viewer to go to.
			opts.Tap = func(p *msg.Packet) {
				census.Record(viewer.MessageName(p), viewer.FromSim, p.At, viewer.NoViewer)
				tracer.Write(viewer.FromSim, p, viewer.NoViewer)
			}
			// The other half.  Without it the record answers
			// "what arrived" and not "was it ever sent", and the
			// second is the question a relay gets asked.
			opts.SendTap = func(p *msg.Packet) {
				census.Record(viewer.MessageName(p), viewer.ToSim, p.At, viewer.Forwarded)
				tracer.Write(viewer.ToSim, p, viewer.Forwarded)
			}
		}
		return login, opts, nil
	}

	// up is everything that follows a session coming up, wherever it
	// came up from -- the command line, or a retry minutes later.  One
	// place, so that a session started the slow way is not half set up.
	up := func(name string, login agent.Login, h *server.Hosted) {
		h.Log = log.Printf
		hostMu.Lock()
		hosted[name] = login
		hostMu.Unlock()
		a := h.Agent()
		log.Printf("%s: %s in %s, %d capabilities",
			name, a.Account.Name(), orUnknown(a.RegionName()), len(a.Caps()))

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

	for _, name := range flag.Args() {
		login, opts, err := loginFor(name)
		if err != nil {
			log.Printf("%s: NOT hosted: %v", name, err)
			continue
		}

		log.Printf("%s: logging in...", name)
		h, err := srv.StartAgent(ctx, name, login, opts)
		if err == nil {
			up(name, login, h)
			continue
		}
		// A refusal a wait cannot clear is the end of it for this
		// profile.  It is not fatal: one expired password should not
		// take down the sessions that did come up, which are somebody's
		// benchmark in progress.
		if !agent.RetryableLogin(err) {
			log.Printf("%s: NOT hosted: %v", name, err)
			continue
		}
		// Everything else is worth asking again for.  A grid hands
		// back a dead seed capability often enough that a daemon which
		// gives up on the first one is a daemon somebody has to go and
		// restart -- seen here: hobb failed with a 404 on the seed
		// capability and came up first try a moment later.
		log.Printf("%s: not up yet (%v); trying again", name, err)
		pending = append(pending, waiting{name: name, login: login, opts: opts})
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
	if len(hosted) == 0 && len(pending) == 0 {
		log.Fatal("no session came up; nothing to serve")
	}

	// settle is everything a session needs after it is up.  Used both
	// for the ones named on the command line and for any started later
	// on request, so that a session cannot be half set up depending on
	// how it came to exist.
	settle := func(h *server.Hosted) {
		name := h.Name
		want := ""
		hostMu.Lock()
		l, known := hosted[name]
		hostMu.Unlock()
		if known {
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

	// The ones that were not up yet.  Started here rather than where
	// they failed, because a session that arrives this way has to be
	// settled like any other and settle is only defined by now -- an
	// avatar that came up on the second attempt and could not build
	// would be a worse bug than the one this fixes.
	//
	// The waits are the server's own, which are generous on purpose: a
	// login server throttles a client that hammers it, and what is
	// being waited for usually takes a while to clear.  They repeat,
	// so this keeps trying until the profile comes up, the refusal
	// turns into one no wait can clear, or the daemon stops.
	for _, w := range pending {
		go func(w waiting) {
			for attempt := 0; ; attempt++ {
				delay := server.ReconnectDelays[len(server.ReconnectDelays)-1]
				if attempt < len(server.ReconnectDelays) {
					delay = server.ReconnectDelays[attempt]
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(delay):
				}

				h, err := srv.StartAgent(ctx, w.name, w.login, w.opts)
				if err == nil {
					up(w.name, w.login, h)
					settle(h)
					return
				}
				if !agent.RetryableLogin(err) {
					log.Printf("%s: NOT hosted: %v", w.name, err)
					return
				}
			}
		}(w)
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

	// Now that the sessions are up there is something to hand over.
	if viewers != nil {
		stopViewers, err := viewers.serve(*viewerAt)
		if err != nil {
			log.Fatalf("viewer: %v", err)
		}
		defer stopViewers()
		// After serve, because until it has bound the listener there
		// is no address to tell anybody.  A server left without this
		// answers "no viewer logins", which is the truth for a daemon
		// started without -viewer and the answer every client here
		// gets today.
		srv.SetViewer(viewers)
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

// holdNeighbours decides whether one session starts out holding
// circuits to the regions around it.
//
// The profile wins where it has an opinion, and -neighbours is what the
// rest get.  That order and not the other one: the flag is the daemon
// speaking for every avatar it holds, and the profile is one avatar
// saying what it is for -- so a daemon started with the flag for the
// avatar somebody drives should not thereby put four extra circuits
// under the one that runs benchmarks, and a profile that asks for them
// should get them whatever the daemon was started with.
//
// -group goes the other way and is not an inconsistency: it can name
// the profile it is for, so overruling one avatar there costs nobody
// else anything, where a -neighbours that won would be paid for by
// every session the daemon holds.
//
// Neither is the last word.  slsh's neighbours turns them over on a
// session that is already up, which is the point of the whole thing;
// this is only where it starts.
func holdNeighbours(l agent.Login, flag bool) bool {
	if l.Neighbours != nil {
		return *l.Neighbours
	}
	return flag
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
