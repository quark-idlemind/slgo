package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/internal/server"
	"github.com/quark-idlemind/slgo/internal/viewer"
	"github.com/quark-idlemind/slgo/msg"
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

	// credMu guards creds, the passwords minted for a single login.
	// An ordinary mutex and not a sync.Map: this is touched when
	// somebody starts a viewer and on the one login that follows, which
	// is a handful of times a day rather than a hot path.
	credMu sync.Mutex
	creds  map[string]*oneTime

	// tokenMu guards capTokens, the capability token minted for each
	// profile at its last successful viewer login.  Touched once per
	// login and once per capability request, which for the event
	// queue is a long poll rather than a hot path.
	tokenMu   sync.Mutex
	capTokens map[string]string
}

// capTokenBytes is the length of a capability token.
//
// Sixteen bytes, the same as a uuid, which is the size Second Life's
// own capability URLs use for exactly this job: an unguessable path
// element standing in for an authorisation.
const capTokenBytes = 16

// oneTime is a viewer password minted for a single login.
//
// The plaintext is not here and is nowhere else either.  It is returned
// to whoever asked and forgotten; what stays behind is the digest a
// login is compared against, which is the same thing a profile holds
// and no more use to anyone who reads it than that is.
type oneTime struct {
	digest string
	expiry time.Time
}

// viewerCredentialLife is how long a minted password is good for.
//
// Five minutes: it has to cover a viewer starting up and reaching its
// login, which took forty-five to fifty seconds warm.  What makes a
// password on a command line safe enough is that it works once and the
// endpoint is on loopback, not the clock.
// Why: doc/handover.md#how-long-a-minted-password-lasts
const viewerCredentialLife = 5 * time.Minute

// viewerPasswordChars is how long a minted password is.
//
// Sixteen because that is what a viewer's login box will hold --
// panel_login.xml:144 gives the password field max_length_chars="16" --
// so a password that has to be typed or pasted by hand still can be.
// The command line does not care (llloginhandler.cpp:169 md5s whatever
// it is given, whole), but a credential that works one way and is
// silently truncated the other is a bad hour for somebody.  Sixteen hex
// digits is 64 bits, from crypto/rand, for a secret that lives five
// minutes and works once.
const viewerPasswordChars = 16

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
		capTokens: map[string]string{},
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
	// Said once per KIND of withheld event rather than once per event
	// or once per session.  A region with four neighbours introduces
	// them over and over, so the first is unreadable; and a session
	// that has said its piece about neighbours would otherwise say
	// nothing at all the first time a teleport is withheld, which is
	// different news about a different thing.
	var mu sync.Mutex
	said := map[string]bool{}
	return func(name string, body []byte) {
		_, _, before := q.Stats()
		q.Add(name, body)
		if _, _, after := q.Stats(); after == before {
			return
		}
		mu.Lock()
		first := !said[name]
		said[name] = true
		mu.Unlock()
		if first {
			v.logf("viewer: %s: %s", profile, viewer.WhyWithheld(name))
		}
	}
}

// movedFor is the hook a session hands a region change to.
//
// A viewer attached to a session that another client teleports is told
// nothing by the protocol, because in an ordinary session the viewer is
// the client that asked.  See viewer.Circuit.RegionChanged for what is
// said and why that is all that is said.
func (v *viewerHost) movedFor(profile string) func(string, uint64, uint32) {
	return func(region string, _ uint64, _ uint32) {
		if c, ok := v.circuits.Load(profile); ok {
			c.(*viewer.Circuit).RegionChanged(region)
		}
	}
}

// relayFor is the hook a session hands its messages to.
//
// It does nothing until a viewer has a circuit, which is the ordinary
// case and has to be cheap: this runs on the session's dispatch
// goroutine for every message the region sends.
func (v *viewerHost) relayFor(profile string) func(*msg.Packet) {
	return func(p *msg.Packet) {
		if c, ok := v.circuits.Load(profile); ok {
			c.(*viewer.Circuit).FromSim(p)
		}
	}
}

// hasCircuit reports whether a viewer circuit has been opened for this
// profile.  A nil host has none.
func (v *viewerHost) hasCircuit(profile string) bool {
	if v == nil {
		return false
	}
	_, ok := v.circuits.Load(profile)
	return ok
}

// simTap records, for -trace, each packet the simulator sends a session,
// once.
//
// A session with a viewer circuit has each message recorded by the
// circuit, which is offered it by the relay and records what became of
// it -- forwarded, absorbed, dropped, or held for a viewer not yet
// joined -- and each retransmission by simRepeat.  So the tap records
// only for a session without one, where nothing is forwarded: from the
// tap, the wire as it really was, retransmissions included, since the
// tap runs ahead of duplicate suppression.
func simTap(profile string, v *viewerHost, census *viewer.Census, trace *viewer.Trace) msg.Handler {
	return func(p *msg.Packet) {
		if v.hasCircuit(profile) {
			return
		}
		census.Record(viewer.MessageName(p), viewer.FromSim, p.At, viewer.NoViewer)
		trace.Write(viewer.FromSim, p, viewer.NoViewer)
	}
}

// simRepeat records, for -trace, a retransmission the session drops as
// already handled, for a session with a viewer circuit: the relay the
// circuit records from is not offered one, and simTap, which saw it,
// leaves such a session to the circuit.
func simRepeat(profile string, v *viewerHost, census *viewer.Census, trace *viewer.Trace) msg.Handler {
	return func(p *msg.Packet) {
		if !v.hasCircuit(profile) {
			return
		}
		census.Record(viewer.MessageName(p), viewer.FromSim, p.At, viewer.Retransmission)
		trace.Write(viewer.FromSim, p, viewer.Retransmission)
	}
}

// serve starts the login endpoint and returns a function that stops it.
//
// It is TLS or it does not run.  There used to be a plain HTTP path
// and it is gone: what crosses this endpoint is the viewer_password
// digest inbound and, outbound, the whole login response --
// secure_session_id, the session id, the circuit code -- followed by
// every capability URL the session holds.  None of that is a thing to
// hand to a listener because the flag defaulted that way.
//
// The scheme is not a detail of transport either.  It reaches the
// viewer, because the login URI a person adds to a grid list and the
// seed capability in the login response are both built from the
// address bound here.
func (vh *viewerHost) serve(addr, certFile, keyFile string) (func(), error) {
	ln, tlsCfg, err := bindViewer(addr, certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return vh.serveOn(ln, tlsCfg), nil
}

// bindViewer loads the certificate and takes the address, and does
// nothing else.  It is split from serving because the address is taken
// before any avatar is logged in -- see instance.go -- and served once
// there is something to hand over.
func bindViewer(addr, certFile, keyFile string) (net.Listener, *tls.Config, error) {
	// Loaded before the listener is bound, so that an unreadable or
	// mismatched pair is a startup error naming the file rather than a
	// line in the log a minute later, when the viewer cannot connect
	// and nothing says why.  http.Server.ServeTLS would read them on
	// the serving goroutine, which is too late to return.
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("viewer login: certificate: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("viewer login: %w", err)
	}
	return ln, tlsCfg, nil
}

// serveOn serves viewer logins on a listener bindViewer took, and
// returns a function that stops it.
func (vh *viewerHost) serveOn(ln net.Listener, tlsCfg *tls.Config) func() {
	logf := vh.logf

	mux := http.NewServeMux()
	mux.Handle("/", viewer.LoginHandler(vh.find, logf))
	mux.HandleFunc("/cap/", vh.serveCap)
	hs := viewerHTTPServer(mux, tlsCfg)
	go func() {
		// The paths are empty because the pair is already in
		// TLSConfig; ServeTLS only reads files when it is not.
		if err := hs.ServeTLS(ln, "", ""); err != nil && err != http.ErrServerClosed {
			logf("viewer login: %v", err)
		}
	}()

	uri := viewer.LoginURI(ln.Addr().String())
	vh.base = strings.TrimSuffix(uri, "/")
	logf("viewer logins at %s -- add a grid with that login URI and log in as the avatar", uri)

	return func() {
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(shutdown)
		vh.closeAll()
	}
}

// Limits on the viewer endpoint's connections.
//
// Its login is reached by anybody who can reach the port, before they
// have shown anything, and an http.Server with none of these set waits
// for a request for as long as the sender cares to take over it: a
// header sent a byte a minute, a body that never ends.  Each such
// connection is a goroutine and its buffers, held on the sender's say.
//
// The read and write limits have to leave room for what is slow on
// purpose.  An event queue poll is held for viewer.PollHold before it
// is answered, and a seed request waits up to half a minute on the
// simulator; and Go cancels a request's context when the connection's
// read deadline passes under a running handler, which would cut a poll
// off without an answer.  So both are a minute, well clear of either,
// and the header limit -- which is what a slow sender meets first --
// is short.  How many polls may be held at once is the queue's own
// business; see viewer.MaxHeldPolls.  How large a body may be is the
// handlers'; see viewer.MaxRequestBody.
const (
	viewerHeaderTimeout = 10 * time.Second
	viewerReadTimeout   = time.Minute
	viewerWriteTimeout  = time.Minute
	viewerIdleTimeout   = 2 * time.Minute
	viewerHeaderBytes   = 64 << 10
)

// viewerHTTPServer is the server the viewer endpoint runs on, with its
// limits.
func viewerHTTPServer(h http.Handler, tlsCfg *tls.Config) *http.Server {
	return &http.Server{
		Handler:           h,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: viewerHeaderTimeout,
		ReadTimeout:       viewerReadTimeout,
		WriteTimeout:      viewerWriteTimeout,
		IdleTimeout:       viewerIdleTimeout,
		MaxHeaderBytes:    viewerHeaderBytes,
	}
}

// The three answers the server gives a client about all this, which is
// the whole of server.Viewer.  They are here because everything they
// are made of is here: where the endpoint was bound, which profiles may
// be handed over, and the circuits.
var _ server.Viewer = (*viewerHost)(nil)

// LoginURI is the address to add to a viewer's grid list.
//
// The daemon's log says it once, at startup; this is for a person who
// attaches with a shell an hour later and has to ask.
func (v *viewerHost) LoginURI() string {
	if v.base == "" {
		return ""
	}
	return viewer.LoginURI(v.base)
}

// Attached reports whether a viewer has taken this profile's circuit.
//
// A circuit exists from the first login and is kept afterwards, so its
// existence says only that a viewer once arrived; the handshake, undone
// by a logout or by viewer.SilenceTimeout of silence, is the nearest
// thing to a live answer.
func (v *viewerHost) Attached(profile string) bool {
	c, ok := v.circuits.Load(profile)
	return ok && c.(*viewer.Circuit).Joined()
}

// Mint makes a password good for one login as this profile.
//
// The plaintext is returned and not kept.  It reaches a viewer's argv,
// which every process this user owns can read, and that is what being
// single use and short lived is for: what an onlooker gets is a
// credential that is either already spent or about to expire, for a
// session they would have to be on this machine to reach anyway.
//
// A profile with no viewer_password is refused, which is the same
// refusal find() makes below and for the same reason: that setting is
// what marks a profile as one that may be handed to a viewer at all,
// and minting around it would turn a deliberate omission into nothing.
func (v *viewerHost) Mint(profile string) (string, time.Duration, error) {
	if v.digest(profile) == "" {
		return "", 0, fmt.Errorf("%s has no viewer_password, so it cannot be handed to a viewer; "+
			"add a \"viewer_password = ...\" line to that profile and restart slgod", profile)
	}
	pass, err := newViewerPassword()
	if err != nil {
		// Deliberately says nothing about what was being made.
		return "", 0, fmt.Errorf("no viewer password could be made: %w", err)
	}

	v.credMu.Lock()
	defer v.credMu.Unlock()
	if v.creds == nil {
		v.creds = map[string]*oneTime{}
	}
	// One outstanding per profile: minting again drops the last one,
	// so a person who starts a viewer twice cannot leave a live
	// credential behind them.  Only ever as many entries as there are
	// profiles, so nothing grows.
	v.creds[profile] = &oneTime{
		digest: agent.HashPassword(pass),
		expiry: time.Now().Add(viewerCredentialLife),
	}
	return pass, viewerCredentialLife, nil
}

// oneTimeFor is the live minted digest for this profile, and the
// function that spends it, or nothing.
//
// Expiry is checked here rather than swept on a timer: a stale entry is
// harmless -- it is a digest of a secret nobody has -- and a sweeper
// would be a goroutine whose only job is to delete something already
// being ignored.
func (v *viewerHost) oneTimeFor(profile string) (string, func()) {
	v.credMu.Lock()
	defer v.credMu.Unlock()
	c, ok := v.creds[profile]
	if !ok {
		return "", nil
	}
	if time.Now().After(c.expiry) {
		delete(v.creds, profile)
		return "", nil
	}
	return c.digest, func() {
		v.credMu.Lock()
		defer v.credMu.Unlock()
		// Only if it is still the same one.  A credential minted
		// again while this login was in flight belongs to whoever
		// asked for it second, and spending theirs here would refuse
		// them a viewer for no reason they could see.
		if now, ok := v.creds[profile]; ok && now == c {
			delete(v.creds, profile)
		}
	}
}

// newViewerPassword is a fresh secret, from crypto/rand.
//
// Hex rather than base64 so that nothing in it can be eaten by a shell,
// a URL or an XML-RPC encoder on the way to the viewer -- this string
// crosses all three.
func newViewerPassword() (string, error) {
	b := make([]byte, viewerPasswordChars/2)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// find answers the login endpoint's question: is there a session for
// this name, and if so, on what terms.
//
// A session that is mid-teleport is handed over like any other, on
// purpose.  Nothing here is read at login time and used later -- the
// address is slgod's own, the seed resolves the current region when the
// viewer asks -- and the only refusal this could give is the one a
// wrong password gets.
// Why: doc/handover.md#a-session-in-the-middle-of-a-teleport
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

	// The password minted for a viewer being started right now, if
	// there is one.  It is offered ALONGSIDE the profile's rather than
	// instead of it, so a handover that works today goes on working.
	once, spend := v.oneTimeFor(profile)

	hand := &viewer.Handover{
		First:   a.Account.FirstName,
		Last:    a.Account.LastName,
		Digest:  agent.HashPassword(v.digest(profile)),
		OneTime: once,
		UseOnce: spend,
		Raw:     a.Account.Raw,
	}

	// Everything that costs something happens in here, and here runs
	// only after the password has matched: the UDP socket is opened,
	// and the token the capabilities are gated on is minted.  Looking
	// a session up is now free and tells the asker nothing.
	hand.Admit = func() error {
		c, err := v.circuitFor(profile)
		if err != nil {
			return fmt.Errorf("no circuit for %s: %w", name, err)
		}
		token, err := v.mintCapToken(profile)
		if err != nil {
			return fmt.Errorf("no capability token for %s: %w", name, err)
		}
		addr := c.Addr()
		hand.SimIP = addr.IP.String()
		hand.SimPort = addr.Port
		// slgod's own seed, which is a proxy of the simulator's with
		// one entry changed.  Everything a viewer fetches -- the
		// textures, the meshes, the inventory -- still comes
		// straight from the grid; only the event queue comes past
		// here, because it has to have a single reader.
		hand.Seed = v.capBase(profile, token) + "/seed"
		// A person at a viewer has the wheel from here on.
		h.ViewerAttached()
		return nil
	}
	return hand
}

// capBase is the prefix of the two capability URLs handed to a viewer.
//
// The token is a path element rather than a query parameter because a
// viewer treats a capability URL as opaque -- it sends back exactly what
// it was given -- and because the seed proxy passes the request body on
// to the simulator without ever looking at the URL it arrived by.  There
// is nothing on either side to teach.
func (v *viewerHost) capBase(profile, token string) string {
	return v.base + "/cap/" + profile + "/" + token
}

// mintCapToken issues this profile's capability token, replacing any
// token issued before.
//
// Replacing rather than accumulating: a login is a viewer taking the
// session, and the only viewer that could still be holding an older
// token is one that has just been displaced.  One circuit and one
// peer per profile is already the rule (a second viewer on the same
// avatar takes the circuit from the first), so a second live set of
// capability URLs would outlive the thing it belonged to.
func (v *viewerHost) mintCapToken(profile string) (string, error) {
	b := make([]byte, capTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	v.tokenMu.Lock()
	v.capTokens[profile] = token
	v.tokenMu.Unlock()
	return token, nil
}

// capTokenOK reports whether this is the token currently issued for the
// profile.
//
// Constant time, and length-safe: this is a bearer credential for every
// capability the session holds, which is more than the login password
// buys, since the login password only ever yields one of these.
func (v *viewerHost) capTokenOK(profile, token string) bool {
	v.tokenMu.Lock()
	want := v.capTokens[profile]
	v.tokenMu.Unlock()
	// A profile with no token is compared against a stand-in of a
	// token's length rather than refused at once.  Refusing at once
	// was quicker, and the difference said which profiles a viewer has
	// logged in to -- the same fault the login endpoint had in its
	// wording, here in its timing.
	//
	// The stand-in is not a secret, so matching it is not enough:
	// "have" is what refuses a caller who presents the stand-in
	// itself.
	have := want != ""
	if !have {
		want = noCapToken
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(want)) == 1 && have
}

// noCapToken is what a token is compared against when the profile has
// none: the length of a real one, and made of a character a minted
// token never contains.
var noCapToken = strings.Repeat("-", capTokenBytes*2)

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
//
// Being kept, it may outlive the session it was opened for: a profile
// logged out and hosted again is a new Hosted.  So the session is looked
// up by profile each time it is wanted, and is nil while nothing is
// hosted under that name.
func (v *viewerHost) circuitFor(profile string) (*viewer.Circuit, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if c, ok := v.circuits.Load(profile); ok {
		return c.(*viewer.Circuit), nil
	}
	session := func() *agent.Agent {
		if h, ok := v.srv.Agent(profile); ok {
			return h.Agent()
		}
		return nil
	}
	c, err := viewer.Listen(v.host, session, v.census, v.trace, v.logf)
	if err != nil {
		return nil, err
	}
	// What the viewer sends reaches the session down this circuit, past
	// the server, so the server is told of it here.
	c.OnForward(func(m msg.Message) {
		if h, ok := v.srv.Agent(profile); ok {
			h.ViewerSent(msg.IDOf(m))
		}
	})
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
	profile, rest, ok := strings.Cut(rest, "/")
	if !ok || profile == "" {
		http.NotFound(w, r)
		return
	}
	token, what, ok := strings.Cut(rest, "/")
	if !ok || token == "" {
		http.NotFound(w, r)
		return
	}

	// The login endpoint's decision, carried this far.  Without this
	// the profile name was the whole of what these two paths asked
	// for, and the answer to the seed is every capability URL the
	// session holds -- inventory, uploads, script compilation -- each
	// of which is a bearer credential good against the grid itself,
	// with slgod no longer in the way.
	//
	// Not found rather than forbidden, and the same answer for a
	// profile that is not hosted, one with no viewer_password, and a
	// wrong token: a caller guessing at profile names learns nothing
	// from the difference.
	if !v.capTokenOK(profile, token) {
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
		// The seed of the region the avatar is in NOW, which is not
		// Account.SeedCapability: that names the region this session
		// logged in to and goes on naming it after every teleport, so
		// a viewer attaching after a move was being handed the
		// capabilities of a simulator the avatar had left -- its
		// inventory, its textures, its uploads, all of them answered
		// by somewhere else or not at all.  The agent is asked
		// because the agent is the only thing that knows: the seed of
		// every region after the first arrives inside a
		// TeleportFinish or a CrossedRegion that nothing above that
		// package reads.
		seed := a.Seed()
		if seed == "" {
			// No seed for the region the avatar is in: a login
			// response that carried none, or a move whose seed was
			// not a URL.  Saying so beats proxying to an empty URL
			// and answering a viewer with whatever that produces.
			http.Error(w, "this session has no capabilities to hand on", http.StatusServiceUnavailable)
			return
		}
		(&viewer.Seed{
			Real:       seed,
			EventQueue: v.capBase(profile, token) + "/event",
			Logf:       v.logf,
		}).ServeHTTP(w, r)
	default:
		http.NotFound(w, r)
	}
}
