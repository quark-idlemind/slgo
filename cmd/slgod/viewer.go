package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/agent"
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

	// digest gives a profile's stored password, which is what a
	// viewer's login is compared against.
	digest func(profile string) string

	mu       sync.Mutex
	circuits map[string]*viewer.Circuit // by profile name
	ctx      context.Context
}

// serveViewers starts the endpoint and returns a function that stops it.
func serveViewers(ctx context.Context, addr string, srv *server.Server,
	digest func(string) string, census *viewer.Census, trace *viewer.Trace,
	logf func(string, ...any)) (func(), error) {

	host, _, err := viewer.HostPort(addr)
	if err != nil {
		return nil, err
	}
	vh := &viewerHost{
		srv: srv, host: host, census: census, trace: trace, logf: logf,
		digest: digest, circuits: map[string]*viewer.Circuit{}, ctx: ctx,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("viewer login: %w", err)
	}
	hs := &http.Server{Handler: viewer.LoginHandler(vh.find, logf)}
	go func() {
		if err := hs.Serve(ln); err != nil && err != http.ErrServerClosed {
			logf("viewer login: %v", err)
		}
	}()

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
		// The simulator's own seed, for now: every capability the
		// viewer asks for it will get straight from the grid.  That
		// is right for textures and meshes, which is most of the
		// traffic, and wrong for exactly one -- EventQueueGet is
		// delivered once and acknowledged, so two readers split the
		// events between them at random.  Serving our own seed is
		// the next stage; until then a viewer and slgod share that
		// queue and both miss things.
		Seed: a.Account.SeedCapability,
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

	if c, ok := v.circuits[profile]; ok {
		return c, nil
	}
	c, err := viewer.Listen(v.host, a, v.census, v.trace, v.logf)
	if err != nil {
		return nil, err
	}
	c.Run(v.ctx)
	v.circuits[profile] = c
	return c, nil
}

func (v *viewerHost) closeAll() {
	v.mu.Lock()
	defer v.mu.Unlock()
	for name, c := range v.circuits {
		c.Close()
		delete(v.circuits, name)
	}
}
