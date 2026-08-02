package agent

import (
	"bytes"
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"slgo/llsd"
	"slgo/msg"
)

// The event queue is a long poll against a capability, carrying what
// UDP no longer does: ParcelProperties, TeleportFinish,
// EstablishAgentCommunication and a growing list of others.  The
// template marks those UDPDeprecated and the simulator simply does not
// answer them on the circuit any more.
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
func (a *Agent) runEventQueue(ctx context.Context, fn EventHandler) {
	if !a.HasCap(EventQueueCap) {
		return
	}

	var ack any // undef on the first poll, then the last id seen
	fails := 0

	// Every way out of this loop tells the simulator we are
	// finished, including a poll cancelled in flight -- which is the
	// usual way it ends, and was the one path that used to skip it.
	defer func() { a.closeEventQueue(ack) }()

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
		resp, err := a.DoCap(ctx, CapRequest{
			Cap:    EventQueueCap,
			Method: http.MethodPost,
			Body:   body,
			Type:   "application/llsd+xml",
		})

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

		case resp.Status == http.StatusOK:
			fails = 0
			id, n := a.deliver(resp.Body, fn)
			if n == 0 {
				a.eq.timeouts.Add(1)
			}
			if id != nil {
				ack = id
			}

		case resp.Status == http.StatusBadGateway, resp.Status == 499:
			// The classic "nothing happened, ask again"
			// answer.  Not an error and not worth counting
			// as one.
			fails = 0
			a.eq.timeouts.Add(1)

		case resp.Status == http.StatusNotFound, resp.Status == http.StatusGone:
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
// acknowledge next time.
func (a *Agent) deliver(body []byte, fn EventHandler) (any, int) {
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
		a.noteEvent(name, em["body"])
		if fn != nil {
			fn(name, out)
		}
	}
	return m["id"], n
}

// closeEventQueue tells the simulator we are finished, so it does not
// hold a poll open for a session that has gone.
func (a *Agent) closeEventQueue(ack any) {
	if ack == nil {
		return
	}
	body, err := llsd.Encode(map[string]any{"ack": ack, "done": true})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = a.DoCap(ctx, CapRequest{
		Cap:    EventQueueCap,
		Method: http.MethodPost,
		Body:   body,
		Type:   "application/llsd+xml",
	})
}

// noteEvent records session state carried by an event.
//
// Only what belongs to the session goes here. Everything else is the
// clients' business and is passed through untouched.
func (a *Agent) noteEvent(name string, body any) {
	if name != "AgentGroupDataUpdate" {
		return
	}
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
