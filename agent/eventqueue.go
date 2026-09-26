package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quark-idlemind/slgo/internal/redact"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// The event queue is a long poll against a capability, carrying what
// UDP no longer does: ParcelProperties, TeleportFinish,
// EstablishAgentCommunication and a growing list of others.  The
// template says so two ways -- ParcelProperties is UDPDeprecated and
// TeleportFinish is UDPBlackListed -- and EstablishAgentCommunication
// is in neither, being an event with no UDP message behind it at all.
// Whichever, the simulator does not answer them on the circuit.
//
// A poll is posted, the simulator holds it open until it has something
// or it times out, and the reply carries an id that the next poll
// acknowledges.  That sequence has to be kept running continuously,
// which is why this lives beside the circuit rather than in a client:
// a client restart would lose the sequence and drop whatever arrived
// in the gap.

// EventQueueCap is the capability the queue is polled through.
const EventQueueCap = "EventQueueGet"

// An EventHandler receives one event.  Body is LLSD encoded and is not
// inspected on the way through.
type EventHandler func(name string, body []byte)

// EventStats counts what the queue has done.
type EventStats struct {
	Polls    uint64
	Events   uint64
	Timeouts uint64 // polls that returned nothing, which is normal
	Errors   uint64
}

type eventQueue struct {
	polls    atomic.Uint64
	events   atomic.Uint64
	timeouts atomic.Uint64
	errors   atomic.Uint64

	// The poll that is running, if one is.  A move ends the poll
	// against the region being left and starts another against the
	// one arrived at; stopped closes when the first has finished, so
	// that "no poll was left behind" is something a caller can watch
	// rather than assume.
	mu      sync.Mutex
	cancel  context.CancelFunc
	stopped chan struct{}
}

// startEventQueue spawns the poll against whatever queue the current
// capabilities name.
func (a *Agent) startEventQueue(ctx context.Context, fn EventHandler) {
	qctx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})

	a.eq.mu.Lock()
	a.eq.cancel, a.eq.stopped = cancel, stopped
	a.eq.mu.Unlock()

	a.spawn(func() error {
		defer close(stopped)
		defer cancel()
		a.runEventQueue(qctx, fn)
		return nil
	})
}

// stopEventQueue ends the poll against the region being left.
//
// It does not wait for the goroutine to have gone, and that is
// deliberate.  The poll's last act is a done post over HTTP to a
// simulator that has just handed this agent away, which is allowed five
// seconds; and the caller may be the event handler itself, which runs on
// that very goroutine, so waiting for it could be waiting for the
// caller.  Nothing needs it gone: its context is cancelled so it cannot
// poll again, and it holds the URL it started on rather than reading the
// capability set that is about to be replaced.
func (a *Agent) stopEventQueue() {
	a.eq.mu.Lock()
	cancel := a.eq.cancel
	a.eq.cancel, a.eq.stopped = nil, nil
	a.eq.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// EventStats reports on the event queue.
func (a *Agent) EventStats() EventStats {
	return EventStats{
		Polls:    a.eq.polls.Load(),
		Events:   a.eq.events.Load(),
		Timeouts: a.eq.timeouts.Load(),
		Errors:   a.eq.errors.Load(),
	}
}

// eventBackoff is the wait after a failed poll, the last repeating.  A
// queue that is refusing us is usually a session that has gone; the
// circuit's own watchdog is what decides that, so this only has to
// avoid spinning.
var eventBackoff = []time.Duration{
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
}

// runEventQueue polls until the context is cancelled or the session
// ends.
//
// The URL is resolved once, here, and never looked up again.  The queue
// belongs to one region: a poll that read the capability by name each
// time round would, the moment a move replaced the set, acknowledge the
// region it had left to the region it had arrived in -- and post the
// done that closes the old queue to the new one.
func (a *Agent) runEventQueue(ctx context.Context, fn EventHandler) {
	url, ok := a.Caps().Get(EventQueueCap)
	if !ok {
		return
	}

	var ack any // undef on the first poll, then the last id seen
	fails := 0

	// Every way out of this loop tells the simulator we are
	// finished, including a poll cancelled in flight -- which is the
	// usual way it ends, and was the one path that used to skip it.
	defer func() { a.closeEventQueue(url, ack) }()

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.done:
			return
		default:
		}

		body, err := llsd.Encode(map[string]any{"ack": ack, "done": false})
		if err != nil {
			return
		}
		a.eq.polls.Add(1)
		status, reply, err := a.postEventQueue(ctx, url, body)

		switch {
		case err != nil:
			// A cancelled poll is a shutdown, not a failure.
			if ctx.Err() != nil {
				return
			}
			a.eq.errors.Add(1)
			if !a.eventWait(ctx, fails) {
				return
			}
			fails++
			continue

		case status == http.StatusOK:
			fails = 0
			id, n := a.deliver(ctx, reply, fn)
			if n == 0 {
				a.eq.timeouts.Add(1)
			}
			if id != nil {
				ack = id
			}

		case status == http.StatusBadGateway, status == 499:
			// The classic "nothing happened, ask again"
			// answer.  Not an error and not worth counting
			// as one.
			fails = 0
			a.eq.timeouts.Add(1)

		case status == http.StatusNotFound, status == http.StatusGone:
			// The queue is finished with us, so there is
			// nothing to close.  The circuit's watchdog
			// decides whether the session is over.
			ack = nil
			return

		default:
			a.eq.errors.Add(1)
			if !a.eventWait(ctx, fails) {
				return
			}
			fails++
		}
	}
}

// eventWait backs off, reporting false if the session ended first.
func (a *Agent) eventWait(ctx context.Context, fails int) bool {
	d := eventBackoff[len(eventBackoff)-1]
	if fails < len(eventBackoff) {
		d = eventBackoff[fails]
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-a.done:
		return false
	case <-t.C:
		return true
	}
}

// deliver hands each event to the handler and returns the id to
// acknowledge next time.  ctx is the poll's, which a move cancels.
func (a *Agent) deliver(ctx context.Context, body []byte, fn EventHandler) (any, int) {
	v, err := llsd.Decode(bytes.NewReader(body))
	if err != nil {
		a.eq.errors.Add(1)
		return nil, 0
	}
	m := llsd.Map(v)
	if m == nil {
		return nil, 0
	}

	events, _ := m["events"].([]any)
	n := 0
	for _, e := range events {
		em := llsd.Map(e)
		if em == nil {
			continue
		}
		name, _ := em["message"].(string)
		if name == "" {
			continue
		}
		// Re-encode the body rather than pass the decoded tree:
		// the relay is bytes all the way to the client, and the
		// client decodes it with the same package.
		out, err := llsd.Encode(em["body"])
		if err != nil {
			continue
		}
		a.eq.events.Add(1)
		n++
		// The agent reads its own session state off the queue before
		// anything else sees it. A relay hands events to whichever
		// client is attached, and a client may be attached late, or
		// never -- so state the session owns cannot be learned there.
		//
		// An event earlier in this body may have moved the avatar and
		// cancelled ctx.  What follows it is the region left's, and is
		// still relayed but applied only where it is the avatar's own.
		// Why: doc/history/teleport.md#what-follows-a-move-in-the-same-body
		a.noteEvent(ctx, name, em["body"])
		if fn != nil {
			fn(name, out)
		}
	}
	return m["id"], n
}

// postEventQueue posts one poll and reads the answer.
//
// It does not go through DoCap, which resolves a capability by name out
// of the set the session holds now.  This request is addressed to the
// queue the poll was started against and has to reach it whether or not
// that is still the region the avatar is in -- which is the whole
// difficulty of closing the old queue after a move.
func (a *Agent) postEventQueue(ctx context.Context, url string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, redact.Error(err)
	}
	req.Header.Set("Content-Type", "application/llsd+xml")
	req.Header.Set("Accept", "application/llsd+xml")

	// The queue's URL is a credential like any capability's, and a
	// failed poll can end the session with this error as its reason,
	// which the daemon logs; see package redact.
	resp, err := a.http().Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("agent: %s: %w", EventQueueCap, redact.Error(err))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("agent: %s: %w", EventQueueCap, err)
	}
	// An event can carry a URL to fetch or post to next, and one the
	// simulator offered is one this session may use.  DoCap does this
	// for every capability reply; the queue is a capability reply too.
	a.rememberURLs(b)
	return resp.StatusCode, b, nil
}

// closeEventQueue tells the simulator we are finished, so it does not
// hold a poll open for a session that has gone.
func (a *Agent) closeEventQueue(url string, ack any) {
	if ack == nil {
		return
	}
	body, err := llsd.Encode(map[string]any{"ack": ack, "done": true})
	if err != nil {
		return
	}
	// Its own context: the usual way here is a poll whose context has
	// just been cancelled, and one more request has to be allowed
	// after that or the queue is never closed at all.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = a.postEventQueue(ctx, url, body)
}

// noteEvent acts on what an event says about this session.
//
// Only what belongs to the session goes here. Everything else is the
// clients' business and is passed through untouched.
//
// What belongs to the avatar is applied whichever region said it.  What
// belongs to the region is applied only while ctx, the poll's, stands:
// a move cancels it, and an event behind a TeleportFinish or a
// CrossedRegion in the same body describes the region just left -- its
// parcel, its neighbours.  deliver still relays such an event, since a
// client may have asked for it.
//
// Every arm runs inline, on the goroutine that polls the queue and hands
// events on, so a client is never told something about this session
// before the session itself has acted on it.  For the two that move the
// avatar that is not a nicety: see noteTeleportFinish.
func (a *Agent) noteEvent(ctx context.Context, name string, body any) {
	// The avatar's own.
	switch name {
	case "AgentGroupDataUpdate":
		a.noteGroups(body)
		return
	}

	// The region's.
	if ctx.Err() != nil {
		return
	}
	switch name {
	case "TeleportFinish":
		a.noteTeleportFinish(body)
	case "CrossedRegion":
		a.noteCrossedRegion(body)
	case "EnableSimulator":
		a.noteEnableSimulator(body)
	case "ParcelProperties":
		a.noteParcel(body)
	}
}

// noteGroups records the memberships an AgentGroupDataUpdate carries.
func (a *Agent) noteGroups(body any) {
	m := llsd.Map(body)
	if m == nil {
		return
	}
	rows, _ := m["GroupData"].([]any)
	gs := make([]Group, 0, len(rows))
	for _, r := range rows {
		rm := llsd.Map(r)
		if rm == nil {
			continue
		}
		// A uuid arrives as text here: llsd renders <uuid> as a
		// string, so asking for a msg.UUID would quietly match
		// nothing and leave the list empty.
		id, err := msg.ParseUUID(llsd.String(rm, "GroupID"))
		if err != nil {
			continue
		}
		gs = append(gs, Group{
			ID:     id,
			Name:   llsd.String(rm, "GroupName"),
			Powers: uint64(llsd.Int(rm, "GroupPowers")),
		})
	}
	if len(gs) == 0 {
		return
	}
	a.mu.Lock()
	a.groups = gs
	a.mu.Unlock()
}
