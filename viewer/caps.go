package viewer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/internal/redact"
	"github.com/quark-idlemind/slgo/llsd"
)

// The event queue has one reader, and that is the whole reason this
// file exists.
//
// Events are delivered once and acknowledged.  Two things polling the
// same queue do not each get a copy: they split the events between them,
// at random, and neither can tell it is missing any.  So a viewer must
// not be given the simulator's own EventQueueGet -- it would take
// teleport confirmations, parcel properties and group notices out from
// under the session, and the session would take the viewer's.
//
// Everything else is handed over untouched.  GetTexture, GetMesh2,
// ViewerAsset and the inventory capabilities are ordinary request and
// response, so a viewer talking straight to the simulator's HTTP host is
// both correct and the right thing for the bytes: textures and meshes
// are most of a viewer's traffic and none of it needs to come past here.

// PollHold is how long an event queue poll is held open with nothing to
// say before it is answered empty.
//
// A viewer polls again immediately, so this is not a delay to anything;
// it is how long a request sits on a connection.  Rather shorter than
// the simulator's own, so that a viewer waiting here is never the reason
// something looks slow.
const PollHold = 20 * time.Second

// MaxHeldPolls is how many polls one queue holds open at once.
//
// A viewer polls again when a poll is answered, so it has one waiting
// here at a time, and two for a moment when a new viewer takes over
// from one that has not yet noticed it was displaced; a few more is
// margin.  Each held poll is a request and a goroutine for up to
// PollHold, so without a number here a caller with the capability could
// hold as many as it liked.  One past the limit is answered at once, as
// unavailable, rather than held with the rest.
const MaxHeldPolls = 4

// MaxRequestBody is the most any request to this package's handlers
// may carry.
//
// A viewer's login is about three kilobytes of XML-RPC (the Firestorm
// capture in agent/testdata is 3164 bytes).  Its seed request is a
// list of capability names and its poll an acknowledgement; neither
// has been measured, but both are lists of short strings, and this is
// eighty times the login.  Without it a body was read to its end however
// long the sender kept sending -- the seed with io.ReadAll, the login
// through an XML decoder -- and held in memory while it was.
const MaxRequestBody = 256 << 10

// EventQueue holds what the session's queue produced, for one viewer to
// collect.
//
// The session stays the only reader of the real queue and hands a copy
// here; this is a fan-out, not a second reader.
type EventQueue struct {
	mu      sync.Mutex
	waiting []event
	id      int64
	woken   chan struct{}
	held    int // polls waiting for something to say

	delivered uint64
	dropped   uint64
	withheld  uint64
}

// withheldEvents are the events a viewer must not be given.
//
// EstablishAgentCommunication is the simulator introducing a neighbour
// region: its UDP address and its seed capability, so that a viewer can
// open a child connection and draw what is over the border.  Handed to
// a viewer here, that is exactly what it does -- and the connection it
// opens is direct, using this session's own agent and session ids, to a
// simulator that has never heard of slgod.  The handover stops being a
// handover: part of the session is then in the daemon and part of it is
// in the viewer, and nothing can see both.
//
// So it is withheld, and the cost is plain rather than hidden: the
// viewer draws this region and nothing beyond it.  Neighbouring
// regions are void.  Making them work means slgod holding the child
// connections itself and handing on what they say, which is the same
// piece of work as region crossing and is not built.
//
// EnableSimulator is the other half of that introduction and was
// relayed for as long as this front end has existed, which made the
// paragraph above half true at best.  It carries a neighbour's handle,
// IP and port and no capability of any kind -- and a capability is not
// what opening a circuit takes.  UseCircuitCode's whole content is the
// circuit code, the session id and the agent id, and a viewer holding
// this session already has all three: they are in the login response it
// was replayed, and Circuit.checkCircuit exists precisely because the
// viewer sends them back on attaching.  So an address is the only thing
// it was missing, and this message is an address.  The seed in
// EstablishAgentCommunication buys the neighbour's HTTP capabilities;
// the UDP circuit, which is where the object updates arrive and where
// anything a viewer might send AS this agent would go, needs none of
// it.  The reference viewer does exactly this on receiving one --
// enables the circuit and sends UseCircuitCode to the address it names
// -- which is a reading of published viewer source rather than
// something measured here, and the withholding does not rest on it: the
// message plus what the viewer already holds is sufficient on its own.
//
// A neighbour circuit is worse than the one EstablishAgentCommunication
// would open, not better.  Every absorb in this package -- the logout
// that would end the grid session, the teleports, the circuit claim --
// is enforced on the one circuit slgod owns, and a circuit the viewer
// opened itself is under none of it.
//
// Withheld, then, and it is the older hole rather than one the teleport
// work made: it has been relayed since this front end existed, and
// stage 0's capture has three of them arriving every few seconds.  What
// it costs is that a viewer no longer creates the neighbouring regions
// at all, so anything it was managing to draw across a border stops --
// which is the sentence above finally becoming true rather than a new
// restriction.
//
// TeleportFinish is the same failure arriving by a different road, and
// it opened when the daemon learned to follow a teleport.  It carries
// the new simulator's address and its seed capability, and it does not
// need a viewer to have asked for anything: `slsh tp` moves the
// session, the simulator puts a TeleportFinish on the queue, slgod fans
// it out, and a viewer handed it opens a circuit straight to the real
// simulator with this session's agent id, session id and circuit code.
// Half the session would then be in the daemon and half in the viewer,
// with each one's sequence numbers meaningless to the other, and
// nothing anywhere able to see both.  So it is withheld here as well,
// and Circuit.FromSim absorbs it on the circuit in case a grid ever
// sends it there.
//
// CrossedRegion is that message for an avatar that walked, and it is
// held on the same terms.  It names a simulator and carries its seed,
// nobody asked for it at all, and slgod acts on one itself when it
// arrives (agent/crossing.go) -- so a viewer given it would be a second
// thing following the same crossing, by its own circuit, with these
// ids.  Withheld here and absorbed in Circuit.FromSim, both roads,
// which costs nothing while no grid sends one and closes the hole the
// day one does.
//
// TeleportFailed is NOT withheld, and it is the one of these that can
// be let through.  It carries no address and no invitation to
// connect anywhere: it is a reason string and, in a viewer, the message
// that clears any teleport state and puts up a notice.  Nothing it can
// do is worse than the truth it carries, and if a TeleportStart ever
// does reach a viewer past the arm that absorbs it, this is the message
// that gets the viewer out of the tunnel again.  Withholding it would
// buy tidiness and cost the only safety net there is.
//
// TeleportStart and TeleportProgress arrive on the circuit rather than
// here, and are absorbed in Circuit.FromSim; the reasoning is there.
//
// Each is held with the reason it is held, because the daemon says that
// reason out loud once per kind and a sentence kept somewhere else
// would end up describing the wrong one.
var withheldEvents = map[string]string{
	"EstablishAgentCommunication": "neighbouring regions are not offered to the viewer, " +
		"so it will draw this region and nothing beyond it",
	"EnableSimulator": "a neighbouring simulator's address is not offered to the viewer: " +
		"it needs no capability to open a circuit there with this session's own ids",
	"TeleportFinish": "the avatar has teleported, and the viewer is not told where to: " +
		"handed the address it would open a circuit to that simulator itself",
	"CrossedRegion": "the avatar has crossed a border, and the viewer is not told where to: " +
		"the daemon follows the crossing and the viewer would open its own circuit",
}

// WhyWithheld is why a viewer is not given this event, or empty for an
// event it is given.
func WhyWithheld(name string) string { return withheldEvents[name] }

type event struct {
	name string
	body []byte // LLSD, as it arrived
}

// QueueLimit is how many events may be waiting for a viewer that is not
// collecting them.
//
// Bounded for the same reason the packet backlog is: the session must
// not grow without limit because a window is open somewhere and idle.
// Unlike packets, these are rare enough that reaching this at all means
// the viewer has stopped polling.
const QueueLimit = 512

// NewEventQueue prepares one.
func NewEventQueue() *EventQueue {
	return &EventQueue{woken: make(chan struct{}, 1)}
}

// Add takes one event from the session.
//
// It never blocks: this is called from the session's event poll, and a
// viewer that has stopped collecting must not be able to stop the
// session's queue from draining.
func (q *EventQueue) Add(name string, body []byte) {
	if withheldEvents[name] != "" {
		q.mu.Lock()
		q.withheld++
		q.mu.Unlock()
		return
	}

	q.mu.Lock()
	if len(q.waiting) >= QueueLimit {
		// Oldest first.  A viewer this far behind has stopped
		// polling, and the recent events are the ones it would
		// still act on.
		q.waiting = q.waiting[1:]
		q.dropped++
	}
	q.waiting = append(q.waiting, event{name: name, body: append([]byte(nil), body...)})
	q.mu.Unlock()

	select {
	case q.woken <- struct{}{}:
	default:
	}
}

// Stats is how many events were handed on, how many were dropped for a
// viewer that stopped collecting, and how many were withheld because a
// viewer must not act on them.
func (q *EventQueue) Stats() (delivered, dropped, withheld uint64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.delivered, q.dropped, q.withheld
}

// take returns what is waiting, if anything.
func (q *EventQueue) take() ([]event, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.waiting) == 0 {
		return nil, q.id
	}
	out := q.waiting
	q.waiting = nil
	q.id++
	q.delivered += uint64(len(out))
	return out, q.id
}

// ServeHTTP answers a viewer's EventQueueGet.
//
// The shape is the simulator's: hold the request until there is
// something to say, then answer with an id the next poll acknowledges.
// A poll with nothing to report is answered with an empty event list
// rather than left to time out, so a viewer never waits on this longer
// than PollHold.
func (q *EventQueue) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Body != nil {
		defer r.Body.Close()
		// The ack is read and discarded.  Acknowledgement is
		// between the session and the simulator; this queue hands
		// over what it holds and forgets it, so there is nothing
		// for a viewer to acknowledge and nothing to resend.
		_, err := io.Copy(io.Discard, http.MaxBytesReader(w, r.Body, MaxRequestBody))
		if tooLarge(err) {
			http.Error(w, "the request is larger than any poll", http.StatusRequestEntityTooLarge)
			return
		}
	}

	events, id := q.take()
	if len(events) == 0 {
		if !q.hold() {
			http.Error(w, "too many polls are already waiting on this queue", http.StatusServiceUnavailable)
			return
		}
		defer q.unhold()
		select {
		case <-q.woken:
			events, id = q.take()
		case <-time.After(PollHold):
		case <-r.Context().Done():
			return
		}
	}

	list := make([]any, 0, len(events))
	for _, e := range events {
		v, err := llsd.Decode(bytes.NewReader(e.body))
		if err != nil {
			continue
		}
		list = append(list, map[string]any{"message": e.name, "body": v})
	}

	body, err := llsd.Encode(map[string]any{"id": id, "events": list})
	if err != nil {
		http.Error(w, "could not encode events", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/llsd+xml")
	_, _ = w.Write(body)
}

// tooLarge reports a body cut off at MaxRequestBody, as distinct from
// one that merely ended badly, which was ignored before there was a
// limit and still is.
func tooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

// hold takes one of the MaxHeldPolls places, if there is one.
func (q *EventQueue) hold() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.held >= MaxHeldPolls {
		return false
	}
	q.held++
	return true
}

func (q *EventQueue) unhold() {
	q.mu.Lock()
	q.held--
	q.mu.Unlock()
}

// Seed answers a viewer's seed capability request.
//
// It asks the simulator the same question, on the session's behalf, and
// hands back the answer with one entry changed: EventQueueGet points
// here.  Everything else is the simulator's own URL, so a viewer fetches
// textures, meshes and inventory straight from the grid.
//
// Proxied rather than enumerated because the set grows.  A capability
// this build has never heard of is one a viewer may still need, and
// listing the ones we know would quietly withhold the rest.
type Seed struct {
	// Real is the simulator's seed capability.
	Real string

	// EventQueue is the URL to advertise in place of the simulator's.
	EventQueue string

	HTTP *http.Client
	Logf func(string, ...any)
}

func (s *Seed) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logf := s.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	var asked []byte
	if r.Body != nil {
		defer r.Body.Close()
		var err error
		asked, err = io.ReadAll(http.MaxBytesReader(w, r.Body, MaxRequestBody))
		if tooLarge(err) {
			// Refused rather than passed on cut short: a list of
			// names with its end missing is not a question worth
			// putting to the simulator.
			http.Error(w, "the request is larger than any seed request", http.StatusRequestEntityTooLarge)
			return
		}
	}

	client := s.client()
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Both errors can carry the simulator's seed URL, which is the
	// session's credential for every capability; neither the log nor
	// the viewer is given it.  See package redact.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Real, bytes.NewReader(asked))
	if err != nil {
		http.Error(w, redact.Error(err).Error(), http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/llsd+xml")
	resp, err := client.Do(req)
	if err != nil {
		err = redact.Error(err)
		logf("viewer: asking the simulator for capabilities: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	out, n := s.rewrite(body)
	logf("viewer: handed on %d capabilities, with %s pointed here", n, "EventQueueGet")
	w.Header().Set("Content-Type", "application/llsd+xml")
	_, _ = w.Write(out)
}

// client is the one the seed is asked through, copied so that it can
// be given a redirect check whatever it was configured with.
//
// The request is the daemon's, made because a viewer asked, and Go
// follows a redirect by default to wherever it points.  The simulator
// has no reason to send its seed anywhere else, and an answer that did
// would have the daemon post to some other address on a viewer's
// behalf -- so a redirect is followed on the seed's own host and no
// further.  A check the caller set is kept, and asked after this one.
func (s *Seed) client() *http.Client {
	c := http.Client{Timeout: 30 * time.Second}
	if s.HTTP != nil {
		c = *s.HTTP
	}
	theirs := c.CheckRedirect
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("viewer: seed capability: stopped after %d redirects", len(via))
		}
		if req.URL.Host != via[0].URL.Host {
			return fmt.Errorf("viewer: seed capability: refused a redirect to %s, off the simulator's host", req.URL.Host)
		}
		if theirs != nil {
			return theirs(req, via)
		}
		return nil
	}
	return &c
}

// rewrite replaces the event queue's URL and leaves everything else
// exactly as the simulator gave it.
func (s *Seed) rewrite(body []byte) ([]byte, int) {
	v, err := llsd.Decode(bytes.NewReader(body))
	if err != nil {
		// Unreadable, so pass it on rather than lose it.  A viewer
		// given the simulator's own queue is a bug; a viewer given
		// nothing at all cannot start.
		return body, 0
	}
	m := llsd.Map(v)
	if m == nil {
		return body, 0
	}
	if _, ok := m["EventQueueGet"]; ok {
		m["EventQueueGet"] = s.EventQueue
	}
	out, err := llsd.Encode(m)
	if err != nil {
		return body, len(m)
	}
	return out, len(m)
}
