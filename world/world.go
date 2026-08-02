// Package world does things in Second Life and waits for them to have
// happened.
//
// The layer below deals in messages: send one, then watch a channel for
// whichever of several replies means it worked, keeping enough state to
// recognise it when it comes.  Almost nothing in this protocol answers
// the question it was asked -- rezzing a prim produces no reply naming
// the prim, taking one into inventory produces no reply at all, and a
// script's output arrives as chat minutes later or not at all.  Every
// command written against that layer grew its own copy of the same
// bookkeeping, and each copy got a slightly different set of the
// lessons.
//
// So this package owns the reading, and offers functions that return
// when the thing has been observed to happen or report why it did not.
// Rez returns the prim it rezzed, having confirmed we own it.  Take
// returns the inventory item, having found it in the folder.  Run
// returns what a script said, having watched for a sentinel.
//
// What it does not do is pretend the grid is reliable.  Everything here
// takes a timeout, everything can fail, and a function that could not
// confirm what it did says so rather than returning as though it had.
package world

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"slgo/agent"
	"slgo/client"
	"slgo/msg"
	pb "slgo/proto/slgov1"
)

// ErrTimeout is reported when the simulator never confirmed something.
// It is worth distinguishing: it means the request may well have taken
// effect and we did not see it, which is a different situation from a
// refusal.
var ErrTimeout = errors.New("world: timed out waiting for the simulator")

// Subscriptions are the messages this package needs relayed to it.
// Passing anything less to Attach leaves it waiting for confirmations
// that will not arrive.
var Subscriptions = []string{
	"ObjectUpdate", "ObjectUpdateCompressed", "ObjectProperties",
	"ObjectPropertiesFamily", "KillObject",
	"UpdateCreateInventoryItem", "ReplyTaskInventory",
	"SendXferPacket", "AbortXfer", "TransferInfo", "TransferPacket",
	"ChatFromSimulator", "AlertMessage",
	"ScriptRunningReply", "ScriptQuestion", "ScriptDialog",
	"TeleportLocal", "TeleportFailed", "TeleportFinish",
	"AgentMovementComplete", "ParcelProperties",
}

// World is a connection to a hosted agent, with the bookkeeping needed
// to tell whether anything asked for actually happened.
//
// It is safe for concurrent use.  It reads the relay in one goroutine
// and everything else takes the lock.
type World struct {
	c    *client.Conn
	info *pb.AgentInfo

	me      msg.UUID
	sess    msg.UUID
	invRoot msg.UUID

	xfers     *client.Xfers
	transfers *client.Transfers

	mu sync.Mutex

	// What the simulator has said about objects.
	locals  map[msg.UUID]uint32   // object id to local id
	owners  map[msg.UUID]msg.UUID // object id to owner
	names   map[msg.UUID]string   // object id to name
	parents map[uint32]uint32     // local id to parent local id
	attach  map[msg.UUID]*Attached
	killed  map[uint32]bool

	// Replies keyed by what was asked.
	created map[uint32]*msg.UpdateCreateInventoryItem_InventoryData

	// taskInv holds the filename from ReplyTaskInventory and taskSeen
	// whether a reply arrived at all.  Both are needed: an object
	// holding nothing answers with an empty filename, so waiting for a
	// non-empty one waits for ever on an empty object.
	taskInv  map[msg.UUID]string
	taskSeen map[msg.UUID]bool

	// Chat subscriptions, and the channel that adds and removes them.
	//
	// Both go through the reader goroutine so that it is the only
	// thing that ever touches a subscription's channel: the only
	// writer and the only closer.  Closing from anywhere else races
	// with a send no matter how it is locked.
	chatSubs map[<-chan Line]*chatSub
	chatCtl  chan chatCmd
	readDone chan struct{}

	// Chat collectors, and everything heard, in arrival order.
	collectors []*collector
	alerts     []string
	propsFns   []func(*Properties)

	// OnAlert, if set, is called for every AlertMessage.  Alerts are
	// how a simulator refuses something it has no reply for.
	OnAlert func(string)
}

// Dial connects to a server and attaches to one of its agents.
func Dial(ctx context.Context, addr, agentName string) (*World, error) {
	c, err := client.Dial(ctx, addr)
	if err != nil {
		return nil, err
	}
	w, err := Attach(ctx, c, agentName)
	if err != nil {
		c.Close()
		return nil, err
	}
	return w, nil
}

// Attach starts on an existing connection.
func Attach(ctx context.Context, c *client.Conn, agentName string) (*World, error) {
	info, err := c.Attach(ctx, agentName, Subscriptions...)
	if err != nil {
		return nil, err
	}
	return New(c, info)
}

// New wraps a connection that is already attached.
func New(c *client.Conn, info *pb.AgentInfo) (*World, error) {
	me, err := msg.ParseUUID(info.AgentId)
	if err != nil {
		return nil, fmt.Errorf("world: bad agent id: %w", err)
	}
	sess, err := msg.ParseUUID(info.SessionId)
	if err != nil {
		return nil, fmt.Errorf("world: bad session id: %w", err)
	}
	root, _ := msg.ParseUUID(info.InventoryRoot)

	w := &World{
		c: c, info: info, me: me, sess: sess, invRoot: root,
		locals:   map[msg.UUID]uint32{},
		owners:   map[msg.UUID]msg.UUID{},
		names:    map[msg.UUID]string{},
		parents:  map[uint32]uint32{},
		attach:   map[msg.UUID]*Attached{},
		killed:   map[uint32]bool{},
		created:  map[uint32]*msg.UpdateCreateInventoryItem_InventoryData{},
		taskInv:  map[msg.UUID]string{},
		taskSeen: map[msg.UUID]bool{},
		chatSubs: map[<-chan Line]*chatSub{},
		chatCtl:  make(chan chatCmd),
		readDone: make(chan struct{}),
	}
	w.xfers = client.NewXfers(c)
	w.transfers = client.NewTransfers(c)
	go w.read(context.Background())
	return w, nil
}

// Conn is the connection underneath, for anything this package does
// not cover.  A message sent through it still reaches the same reader.
func (w *World) Conn() *client.Conn { return w.c }

// Me is the avatar's id, Session the session id, and Info what the
// server said when we attached.
func (w *World) Me() msg.UUID            { return w.me }
func (w *World) Session() msg.UUID       { return w.sess }
func (w *World) Info() *pb.AgentInfo     { return w.info }
func (w *World) InventoryRoot() msg.UUID { return w.invRoot }

// Close hangs up.
func (w *World) Close() error { return w.c.Close() }

// Send puts a message on the wire, reliably.
func (w *World) Send(ctx context.Context, m msg.Message) error {
	return w.c.Send(ctx, m, true)
}

// agentBlock fills the AgentID and SessionID that nearly every message
// starts with.
func (w *World) agentBlock() (msg.UUID, msg.UUID) { return w.me, w.sess }

// Settle waits, doing nothing, so the simulator's interest list can
// fill.
//
// A session that has just connected has been told about nothing.
// Rezzing immediately and then looking for the new object finds every
// object in the region looking new, because none of them had been
// mentioned before.
func (w *World) Settle(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// alertsSince renders the alerts heard after a mark, for an error.
func (w *World) alertsSince(mark int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if mark >= len(w.alerts) {
		return ""
	}
	said := w.alerts[mark:]
	for i, a := range said {
		said[i] = strconv.Quote(a)
	}
	return "; the simulator said " + strings.Join(said, ", ")
}

// Alerts returns the AlertMessage text heard so far.  A simulator that
// refuses something often says so only here.
func (w *World) Alerts() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.alerts...)
}

// read is the one goroutine that consumes the relay.
//
// It also owns the chat subscriptions, which is why it selects rather
// than ranging: adding and removing one has to happen here, in between
// deliveries, so that nothing can be closed while a delivery is in
// flight.
func (w *World) read(ctx context.Context) {
	msgs := w.c.Messages()
	defer func() {
		close(w.readDone)
		w.closeChat()
	}()

	for {
		select {
		case c := <-w.chatCtl:
			w.applyChat(c)

		case <-ctx.Done():
			return

		case m, ok := <-msgs:
			if !ok {
				return
			}
			if w.xfers.Handle(ctx, m) {
				continue
			}
			if w.transfers.Handle(m) {
				continue
			}
			v, err := m.Decode()
			if err != nil || v == nil {
				continue
			}
			w.handle(m, v)
		}
	}
}

func (w *World) handle(raw *client.Message, v msg.Message) {
	switch t := v.(type) {
	case *msg.ObjectUpdate:
		w.mu.Lock()
		for i := range t.ObjectData {
			o := &t.ObjectData[i]
			w.locals[o.FullID] = o.ID
			w.parents[o.ID] = o.ParentID
			delete(w.killed, o.ID)
			if item, ok := attachItem(o.NameValue); ok {
				w.attach[item] = &Attached{
					Object: Object{ID: o.FullID, Local: o.ID},
					Item:   item,
					Point:  attachPoint(o.State),
				}
			}
		}
		w.mu.Unlock()

	case *msg.KillObject:
		w.mu.Lock()
		for _, d := range t.ObjectData {
			w.killed[d.ID] = true
		}
		w.mu.Unlock()

	case *msg.ObjectPropertiesFamily:
		w.mu.Lock()
		w.owners[t.ObjectData.ObjectID] = t.ObjectData.OwnerID
		w.names[t.ObjectData.ObjectID] = trimNul(t.ObjectData.Name)
		w.mu.Unlock()

	case *msg.ObjectProperties:
		var out []*Properties
		w.mu.Lock()
		for i := range t.ObjectData {
			o := &t.ObjectData[i]
			w.owners[o.ObjectID] = o.OwnerID
			w.names[o.ObjectID] = trimNul(o.Name)
			out = append(out, &Properties{
				Object: o.ObjectID, Name: trimNul(o.Name),
				Description: trimNul(o.Description),
				Creator:     o.CreatorID, Owner: o.OwnerID, Group: o.GroupID,
				// CreationDate is microseconds.  Read as seconds it
				// dates a fresh prim to the year 56 million.
				Created:       time.UnixMicro(int64(o.CreationDate)),
				BaseMask:      o.BaseMask,
				OwnerMask:     o.OwnerMask,
				GroupMask:     o.GroupMask,
				EveryoneMask:  o.EveryoneMask,
				NextOwnerMask: o.NextOwnerMask,
				SaleType:      o.SaleType, SalePrice: o.SalePrice,
				InventorySerial: o.InventorySerial,
			})
		}
		fns := make([]func(*Properties), len(w.propsFns))
		copy(fns, w.propsFns)
		w.mu.Unlock()
		for _, p := range out {
			for _, fn := range fns {
				if fn != nil {
					fn(p)
				}
			}
		}

	case *msg.UpdateCreateInventoryItem:
		w.mu.Lock()
		for i := range t.InventoryData {
			d := t.InventoryData[i]
			w.created[d.CallbackID] = &d
		}
		w.mu.Unlock()

	case *msg.ReplyTaskInventory:
		w.mu.Lock()
		w.taskInv[t.InventoryData.TaskID] = trimNul(t.InventoryData.Filename)
		w.taskSeen[t.InventoryData.TaskID] = true
		w.mu.Unlock()

	case *msg.ChatFromSimulator:
		w.chat(raw, t)

	case *msg.AlertMessage:
		s := trimNul(t.AlertData.Message)
		w.mu.Lock()
		w.alerts = append(w.alerts, s)
		fn := w.OnAlert
		w.mu.Unlock()
		if fn != nil {
			fn(s)
		}
	}
}

// await polls a condition until it holds, the deadline passes, or the
// context is cancelled.
//
// Polling rather than signalling is deliberate.  What is being waited
// for usually arrives as several messages that have to agree with each
// other -- an object update and a properties reply, say -- and a
// predicate over the accumulated state says that plainly where a
// condition variable per fact would not.
func (w *World) await(ctx context.Context, timeout time.Duration, what string, ok func() bool) error {
	deadline := time.Now().Add(timeout)

	// Where the alert log stood when the wait began, so a timeout can
	// quote what the simulator said while we were waiting.
	//
	// A refusal usually arrives as an AlertMessage and NOT as a reply to
	// the thing refused, so without this every refusal is indistinguishable
	// from a slow simulator: the caller is told only that nothing happened,
	// which is the one fact that never explains anything.
	w.mu.Lock()
	mark := len(w.alerts)
	w.mu.Unlock()

	for {
		w.mu.Lock()
		done := ok()
		w.mu.Unlock()
		if done {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s (after %s)%s", ErrTimeout, what, timeout, w.alertsSince(mark))
		}
		t := time.NewTimer(100 * time.Millisecond)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

func trimNul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}

// capDo runs a capability request and insists on a 2xx.
func (w *World) capDo(ctx context.Context, r agent.CapRequest) ([]byte, error) {
	resp, err := w.c.DoCap(ctx, r)
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		name := r.Cap
		if name == "" {
			name = r.URL
		}
		return nil, fmt.Errorf("world: %s: status %d: %s", name, resp.Status, snippet(resp.Body))
	}
	return resp.Body, nil
}

func snippet(b []byte) string {
	const n = 300
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
