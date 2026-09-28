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
// eighty times the login.  Without it a body is read to its end however
// long the sender keeps sending -- the seed with io.ReadAll, the login
// through an XML decoder -- and held in memory while it is.
const MaxRequestBody = 256 << 10

// MaxSeedAnswer is the most of the simulator's answer to a seed request
// that is read.  The answer is a map of capability names to URLs, and
// Firestorm asks for about 120 names (llviewerregion.cpp:3457), so it is
// some tens of kilobytes at most; that is inferred, not measured.  This
// is a megabyte, and a longer answer is refused rather than held.
const MaxSeedAnswer = 1 << 20

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

// withheldEvents are the events a viewer must not be given, each with
// the reason the daemon logs the first time one of its kind is withheld.
//
// Each names another simulator -- a neighbour, or the one a teleport or
// a crossing has taken the avatar to -- and a viewer holding this
// session already has the ids UseCircuitCode takes.  Given the address,
// it opens its own circuit there as this avatar, where none of
// Circuit's absorbs apply.  So neighbouring regions are void in the
// viewer.  TeleportFailed is let through: it names nowhere, and it is
// what takes a viewer out of the teleport tunnel.
// Why: doc/handover.md#what-a-viewer-is-not-given
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
// nothing has polled for a long while -- a viewer that stopped, or none
// yet, since the session hands its events here from login on.
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
// textures, meshes and inventory straight from the grid.  A simulator
// answer that is not a 2xx, or that cannot be read and rewritten, is a
// 502 to the viewer, and nothing of it is passed on.
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
	// llsd.Decode reads only XML.
	req.Header.Set("Accept", "application/llsd+xml")
	resp, err := client.Do(req)
	if err != nil {
		err = redact.Error(err)
		logf("viewer: asking the simulator for capabilities: %v", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		logf("viewer: asking the simulator for capabilities: it answered %s", resp.Status)
		http.Error(w, "the simulator answered the seed request "+resp.Status, http.StatusBadGateway)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxSeedAnswer+1))
	if err != nil {
		logf("viewer: reading the simulator's capabilities: %s", redact.Text(err.Error()))
		http.Error(w, "the simulator's answer to the seed request could not be read", http.StatusBadGateway)
		return
	}
	if len(body) > MaxSeedAnswer {
		logf("viewer: the simulator's answer to the seed request is over %d bytes; refused", MaxSeedAnswer)
		http.Error(w, "the simulator's answer to the seed request is larger than any seed answer", http.StatusBadGateway)
		return
	}

	out, n, err := s.rewrite(body)
	if err != nil {
		// The viewer is told nothing of the error, and the log is
		// given it with any URL cut: a decoder's complaint can quote
		// the body it choked on, and the body holds the simulator's
		// own queue.
		logf("viewer: the simulator's capabilities could not be handed on: %s", redact.Text(err.Error()))
		http.Error(w, "the simulator's answer to the seed request could not be read", http.StatusBadGateway)
		return
	}
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
//
// An answer it cannot read and rewrite is an error, and is never passed
// on as it came.  A viewer asks a seed that failed again, up to 30 times
// (Firestorm, indra/newview/llviewerregion.cpp:106 and 355-370), so
// failing closed costs a retry; failing open could hand it the
// simulator's own queue, and a viewer polling that takes the session's
// events.
func (s *Seed) rewrite(body []byte) ([]byte, int, error) {
	v, err := llsd.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	m := llsd.Map(v)
	if m == nil {
		return nil, 0, errors.New("the answer is not a map")
	}
	if _, ok := m["EventQueueGet"]; ok {
		m["EventQueueGet"] = s.EventQueue
	}
	out, err := llsd.Encode(m)
	if err != nil {
		return nil, 0, err
	}
	return out, len(m), nil
}
