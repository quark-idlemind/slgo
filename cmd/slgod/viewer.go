package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/server"
	"github.com/quark-idlemind/slgo/viewer"
)

// viewerHost is the login endpoint a viewer is pointed at, and the
// circuits it hands out.
//
// One endpoint for every hosted avatar rather than one per session: a
// viewer says which avatar it wants by typing a name, which is what a
// person does anyway, and it means a viewer can be started for whichever
// session turns out to need watching without slgod having been told in
// advance which that would be.
type viewerHost struct {
	srv    *server.Server
	host   string
	census *viewer.Census
	trace  *viewer.Trace
	logf   func(string, ...any)

	// digest gives a profile's stored viewer password, which is what a
	// viewer's login is compared against.  Empty means this profile
	// was not set up to be handed over.
	digest func(profile string) string

	// base is the address a viewer reaches this daemon at, as a URL
	// prefix, so the capabilities handed out can name it.
	base string

	// circuits is looked up for every message the grid sends, so it is
	// a sync.Map rather than something with a mutex on that path: the
	// alternative is taking a lock several thousand times a second on
	// the goroutine the whole session reads through.
	circuits sync.Map // profile name -> *viewer.Circuit
	queues   sync.Map // profile name -> *viewer.EventQueue

	mu  sync.Mutex // held only while opening one
	ctx context.Context
}

// newViewerHost prepares the endpoint without starting it.
//
// Separate from serving because the relay has to be wired into each
// session as it logs in, which happens before there is any endpoint to
// serve -- and a session that came up without it could never pass
// anything to a viewer afterwards.
func newViewerHost(ctx context.Context, host string, srv *server.Server,
	digest func(string) string, census *viewer.Census, trace *viewer.Trace,
	logf func(string, ...any)) *viewerHost {
	return &viewerHost{
		srv: srv, host: host, census: census, trace: trace, logf: logf,
		digest: digest, ctx: ctx,
	}
}

// queueFor is the viewer's copy of the event queue for one profile.
//
// Made on demand and kept, because the session starts handing events to
// it the moment it logs in -- long before any viewer asks for one.
func (v *viewerHost) queueFor(profile string) *viewer.EventQueue {
	if q, ok := v.queues.Load(profile); ok {
		return q.(*viewer.EventQueue)
	}
	q, _ := v.queues.LoadOrStore(profile, viewer.NewEventQueue())
	return q.(*viewer.EventQueue)
}

// eventsFor is the hook a session hands its events to.
//
// The session stays the only thing polling the simulator's queue.  Two
// pollers would not each get a copy: events are delivered once and
// acknowledged, so they would be split between them at random, and
// neither would know it was missing any.
func (v *viewerHost) eventsFor(profile string) func(string, []byte) {
	q := v.queueFor(profile)
	return func(name string, body []byte) { q.Add(name, body) }
}

// relayFor is the hook a session hands its messages to.
//
// Nil until a viewer attaches, which is the ordinary case and has to be
// cheap: this runs on the session's dispatch goroutine for every
// message the region sends.
func (v *viewerHost) relayFor(profile string) func(*msg.Packet) {
	return func(p *msg.Packet) {
		if c, ok := v.circuits.Load(profile); ok {
			c.(*viewer.Circuit).FromSim(p)
		}
	}
}

// serve starts the login endpoint and returns a function that stops it.
func (vh *viewerHost) serve(addr string) (func(), error) {
	logf := vh.logf
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("viewer login: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", viewer.LoginHandler(vh.find, logf))
	mux.HandleFunc("/cap/", vh.serveCap)
	hs := &http.Server{Handler: mux}
	go func() {
		if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
			logf("viewer login: %v", err)
		}
	}()

	vh.base = strings.TrimSuffix(viewer.LoginURI(ln.Addr().String()), "/")
	logf("viewer logins at %s -- add a grid with that login URI and log in as the avatar",
		viewer.LoginURI(ln.Addr().String()))

	return func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
		vh.closeAll()
	}, nil
}

// find answers the login endpoint's question: is there a session for
// this name, and if so, on what terms.
func (v *viewerHost) find(first, last string) *viewer.Handover {
	name := first + " " + last
	profile, h := v.hostedNamed(name)
	if h == nil {
		return nil
	}
	a := h.Agent()
	if a == nil || a.Account == nil {
		return nil
	}

	// No viewer password, no handover.  Falling back to the account
	// password would mean the grid credential were typed into a
	// viewer's login box -- where viewers remember it -- for a login
	// that never leaves this machine.
	want := v.digest(profile)
	if want == "" {
		v.logf("viewer: %s has no viewer_password in its profile, so it cannot be handed over", profile)
		return nil
	}

	c, err := v.circuitFor(profile, a)
	if err != nil {
		v.logf("viewer: no circuit for %s: %v", name, err)
		return nil
	}
	addr := c.Addr()

	return &viewer.Handover{
		First:   a.Account.FirstName,
		Last:    a.Account.LastName,
		Digest:  agent.HashPassword(v.digest(profile)),
		Raw:     a.Account.Raw,
		SimIP:   addr.IP.String(),
		SimPort: addr.Port,
		// slgod's own seed, which is a proxy of the simulator's with
		// one entry changed.  Everything a viewer fetches -- the
		// textures, the meshes, the inventory -- still comes
		// straight from the grid; only the event queue comes past
		// here, because it has to have a single reader.
		Seed: v.base + "/cap/" + profile + "/seed",
	}
}

// hostedNamed finds the profile whose avatar has this name.
//
// By avatar name rather than by profile, because the name is what a
// person types into a viewer and the profile is an implementation
// detail of this daemon that nobody outside it knows.
func (v *viewerHost) hostedNamed(name string) (string, *server.Hosted) {
	for _, profile := range v.srv.Names() {
		h, ok := v.srv.Agent(profile)
		if !ok {
			continue
		}
		a := h.Agent()
		if a == nil || a.Account == nil {
			continue
		}
		if a.Account.Name() == name {
			return profile, h
		}
	}
	return "", nil
}

// circuitFor opens this avatar's viewer circuit, or returns the one it
// already has.
//
// On demand rather than at startup, because the port has to be in the
// login response and nothing else needs it: a session nobody watches
// never opens one.  Kept afterwards, because a viewer that is restarted
// logs in again and there is no reason to move the port under it.
func (v *viewerHost) circuitFor(profile string, a *agent.Agent) (*viewer.Circuit, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if c, ok := v.circuits.Load(profile); ok {
		return c.(*viewer.Circuit), nil
	}
	c, err := viewer.Listen(v.host, a, v.census, v.trace, v.logf)
	if err != nil {
		return nil, err
	}
	c.Run(v.ctx)
	v.circuits.Store(profile, c)
	return c, nil
}

func (v *viewerHost) closeAll() {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.circuits.Range(func(k, val any) bool {
		c := val.(*viewer.Circuit)
		if n := c.Dropped(); n > 0 {
			v.logf("viewer: %d messages were dropped for %v because the viewer fell behind", n, k)
		}
		c.Close()
		v.circuits.Delete(k)
		return true
	})
}

// serveCap routes the capabilities slgod serves itself.
//
// There are two, and only two: the seed, so that the answer can be
// rewritten, and the event queue, which is what has to be rewritten.
// Everything else a viewer asks for it asks the simulator directly.
//
// The path carries the profile because one daemon hosts several avatars
// and their queues must not be confused; it is not a secret and does not
// need to be, since the login endpoint decided who may attach.
func (v *viewerHost) serveCap(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/cap/")
	profile, what, ok := strings.Cut(rest, "/")
	if !ok || profile == "" {
		http.NotFound(w, r)
		return
	}

	switch what {
	case "event":
		v.queueFor(profile).ServeHTTP(w, r)
	case "seed":
		h, ok := v.srv.Agent(profile)
		if !ok {
			http.NotFound(w, r)
			return
		}
		a := h.Agent()
		if a == nil || a.Account == nil {
			http.Error(w, "that session is not up", http.StatusServiceUnavailable)
			return
		}
		(&viewer.Seed{
			Real:       a.Account.SeedCapability,
			EventQueue: v.base + "/cap/" + profile + "/event",
			Logf:       v.logf,
		}).ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}
