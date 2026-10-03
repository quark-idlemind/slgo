package msg

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// feed runs a dispatcher over a channel of packets and returns once the
// channel has been consumed and every handler has finished.
func feed(t *testing.T, d *Dispatcher, pkts ...*Packet) {
	t.Helper()
	in := make(chan *Packet, len(pkts))
	for _, p := range pkts {
		in <- p
	}
	close(in)

	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background(), in) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("dispatcher did not finish")
	}
}

func pkt(seq uint32, m Message) *Packet {
	return &Packet{
		Header:  Header{Sequence: seq, Flags: FlagReliable},
		ID:      IDOf(m),
		Message: m,
	}
}

// resend is pkt as a retransmission arrives: reliable and flagged RESENT.
func resend(seq uint32, m Message) *Packet {
	p := pkt(seq, m)
	p.Header.Flags |= FlagResent
	return p
}

func TestDispatchToHandler(t *testing.T) {
	d := NewDispatcher()
	var got atomic.Int64
	d.MustHandle("CompletePingCheck", func(p *Packet) {
		got.Add(int64(p.Message.(*CompletePingCheck).PingID.PingID))
	})

	m1, m2 := &CompletePingCheck{}, &CompletePingCheck{}
	m1.PingID.PingID = 3
	m2.PingID.PingID = 4
	feed(t, d, pkt(1, m1), pkt(2, m2))

	if got.Load() != 7 {
		t.Errorf("handlers saw %d, want 7", got.Load())
	}
	if st := d.Stats(); st.Dispatched != 2 || st.Async != 2 || st.Inline != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestDispatchInlinePreservesOrder(t *testing.T) {
	d := NewDispatcher()
	var mu sync.Mutex
	var order []uint8
	d.MustHandle("CompletePingCheck", func(p *Packet) {
		mu.Lock()
		order = append(order, p.Message.(*CompletePingCheck).PingID.PingID)
		mu.Unlock()
	}, Inline())

	var pkts []*Packet
	for i := 1; i <= 20; i++ {
		m := &CompletePingCheck{}
		m.PingID.PingID = uint8(i)
		pkts = append(pkts, pkt(uint32(i), m))
	}
	feed(t, d, pkts...)

	for i, v := range order {
		if v != uint8(i+1) {
			t.Fatalf("inline handlers ran out of order: %v", order)
		}
	}
	if st := d.Stats(); st.Inline != 20 || st.Async != 0 {
		t.Errorf("stats = %+v", st)
	}
}

// TestDispatchConcurrencyBounded is the point of the semaphore: no more
// than N handlers are ever in flight, however many packets arrive.
func TestDispatchConcurrencyBounded(t *testing.T) {
	const limit = 4
	d := NewDispatcher(WithConcurrency(limit))

	var live, peak atomic.Int64
	d.MustHandle("CompletePingCheck", func(p *Packet) {
		n := live.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		live.Add(-1)
	})

	var pkts []*Packet
	for i := 1; i <= 100; i++ {
		pkts = append(pkts, pkt(uint32(i), &CompletePingCheck{}))
	}
	feed(t, d, pkts...)

	if peak.Load() > limit {
		t.Errorf("%d handlers ran at once, limit is %d", peak.Load(), limit)
	}
	if peak.Load() < 2 {
		t.Errorf("peak concurrency was %d, so nothing ran in parallel", peak.Load())
	}
	if live.Load() != 0 {
		t.Errorf("%d handlers still running after Run returned", live.Load())
	}
}

// TestDispatchWaitsForHandlers: Run must not return while a handler is
// still going, or a caller that shuts down cleanly loses work.
func TestDispatchWaitsForHandlers(t *testing.T) {
	d := NewDispatcher()
	var finished atomic.Bool
	d.MustHandle("CompletePingCheck", func(p *Packet) {
		time.Sleep(50 * time.Millisecond)
		finished.Store(true)
	})
	feed(t, d, pkt(1, &CompletePingCheck{}))
	if !finished.Load() {
		t.Error("Run returned before the handler finished")
	}
}

func TestDispatchSuppressesDuplicates(t *testing.T) {
	d := NewDispatcher()
	var n atomic.Int64
	d.MustHandle("CompletePingCheck", func(p *Packet) { n.Add(1) })

	// The same sequence number three times, as a retransmission
	// would arrive.
	feed(t, d,
		pkt(7, &CompletePingCheck{}),
		resend(7, &CompletePingCheck{}),
		pkt(8, &CompletePingCheck{}),
		resend(7, &CompletePingCheck{}),
	)
	if n.Load() != 2 {
		t.Errorf("handler ran %d times, want 2", n.Load())
	}
	if st := d.Stats(); st.Duplicates != 2 {
		t.Errorf("stats = %+v", st)
	}
}

// TestAnUnflaggedPacketUnderAKnownNumberIsHandled: the viewer drops only
// a packet flagged RESENT, so one that reuses a number without the flag
// -- a peer whose numbering started again -- is a new packet.  Its
// retransmission, flagged, is then a duplicate.
// Why: doc/duplicates.md
func TestAnUnflaggedPacketUnderAKnownNumberIsHandled(t *testing.T) {
	d := NewDispatcher()
	var n atomic.Int64
	d.MustHandle("CompletePingCheck", func(p *Packet) { n.Add(1) })

	feed(t, d,
		pkt(5, &CompletePingCheck{}),
		pkt(5, &CompletePingCheck{}),
		resend(5, &CompletePingCheck{}),
	)
	if n.Load() != 2 {
		t.Errorf("the handler ran %d times, want 2: the unflagged reuse handled, the resend not", n.Load())
	}
	if st := d.Stats(); st.Duplicates != 1 {
		t.Errorf("stats = %+v, want 1 duplicate", st)
	}
}

// TestAnUnreliablePacketsNumberIsNotRemembered: only a reliable packet is
// ever resent, so only its number is kept, as the viewer keeps it.
// Why: doc/duplicates.md
func TestAnUnreliablePacketsNumberIsNotRemembered(t *testing.T) {
	d := NewDispatcher()
	var n atomic.Int64
	d.MustHandle("CompletePingCheck", func(p *Packet) { n.Add(1) })

	unreliable := pkt(6, &CompletePingCheck{})
	unreliable.Header.Flags = 0
	feed(t, d, unreliable, resend(6, &CompletePingCheck{}))
	if n.Load() != 2 {
		t.Errorf("the handler ran %d times, want 2: an unreliable packet's number was remembered", n.Load())
	}
	if st := d.Stats(); st.Duplicates != 0 {
		t.Errorf("stats = %+v, want nothing suppressed", st)
	}
}

func TestDispatchDedupeEvicts(t *testing.T) {
	d := NewDispatcher(WithDedupe(4))
	var n atomic.Int64
	d.MustHandle("CompletePingCheck", func(p *Packet) { n.Add(1) })

	// Sequence 1 falls out of a four entry window before it repeats.
	feed(t, d,
		pkt(1, &CompletePingCheck{}),
		pkt(2, &CompletePingCheck{}),
		pkt(3, &CompletePingCheck{}),
		pkt(4, &CompletePingCheck{}),
		pkt(5, &CompletePingCheck{}),
		resend(1, &CompletePingCheck{}),
	)
	if n.Load() != 6 {
		t.Errorf("handler ran %d times, want 6 once 1 has been evicted", n.Load())
	}
}

// TestForgetLetsAReplacementPeerStartOverAtOne is what a circuit whose
// peer has been swapped needs from duplicate suppression.
//
// A sequence number belongs to a conversation and not to a socket.  A
// second viewer opens with 1 and 2, the numbers the first one used, and
// without forgetting them its handshake is thrown away as a
// retransmission and never reaches anything that would answer it.  The
// change of peer is noticed on the dispatch goroutine -- in a gate, as
// the viewer circuit does, or in a tap, as an agent's move does -- which
// is the one that owns the fields Forget writes, so that is where the
// forgetting has to be safe.  Both are driven here, and each forgets
// while the replacement's first packet is being dispatched, which is
// the packet that has to get through.
func TestForgetLetsAReplacementPeerStartOverAtOne(t *testing.T) {
	for _, via := range []string{"a tap", "a gate"} {
		t.Run(via, func(t *testing.T) {
			var n atomic.Int64
			var forget atomic.Bool
			var d *Dispatcher
			notice := func(p *Packet) {
				if forget.CompareAndSwap(true, false) {
					d.Forget()
				}
			}
			opt := WithTap(notice)
			if via == "a gate" {
				opt = WithGate(func(p *Packet) bool { notice(p); return true })
			}
			d = NewDispatcher(opt)
			d.MustHandle("CompletePingCheck", func(p *Packet) { n.Add(1) })

			feed(t, d, pkt(1, &CompletePingCheck{}), pkt(2, &CompletePingCheck{}))

			// The peer changes, and the same two numbers arrive again.
			forget.Store(true)
			feed(t, d, pkt(1, &CompletePingCheck{}), pkt(2, &CompletePingCheck{}))

			if forget.Load() {
				t.Fatalf("%s never saw the replacement's packets", via)
			}
			if n.Load() != 4 {
				t.Errorf("the handler ran %d times, want 4: the replacement's packets were taken for the first peer's", n.Load())
			}
			if st := d.Stats(); st.Duplicates != 0 || st.Gated != 0 {
				t.Errorf("stats = %+v, want nothing suppressed", st)
			}
		})
	}
}

// TestForgetDoesNotTurnDuplicateSuppressionOff: a peer that has not
// changed and repeats a sequence number really is retransmitting, and
// acting on it twice is what the ring exists to prevent.
func TestForgetDoesNotTurnDuplicateSuppressionOff(t *testing.T) {
	d := NewDispatcher()
	var n atomic.Int64
	d.MustHandle("CompletePingCheck", func(p *Packet) { n.Add(1) })

	feed(t, d, pkt(1, &CompletePingCheck{}))
	d.Forget()
	feed(t, d,
		pkt(1, &CompletePingCheck{}),
		resend(1, &CompletePingCheck{}),
		resend(1, &CompletePingCheck{}),
	)
	if n.Load() != 2 {
		t.Errorf("the handler ran %d times, want 2: one before forgetting and one after", n.Load())
	}
	if st := d.Stats(); st.Duplicates != 2 {
		t.Errorf("stats = %+v, want the two repeats after forgetting suppressed", st)
	}
}

// TestForgetWithDedupeOff is the case with nothing to forget.
func TestForgetWithDedupeOff(t *testing.T) {
	d := NewDispatcher(WithDedupe(0))
	var n atomic.Int64
	d.MustHandle("CompletePingCheck", func(p *Packet) { n.Add(1) })
	d.Forget()
	feed(t, d, pkt(1, &CompletePingCheck{}), pkt(1, &CompletePingCheck{}))
	if n.Load() != 2 {
		t.Errorf("handler ran %d times, want 2 with dedupe off", n.Load())
	}
}

func TestDispatchDedupeDisabled(t *testing.T) {
	d := NewDispatcher(WithDedupe(0))
	var n atomic.Int64
	d.MustHandle("CompletePingCheck", func(p *Packet) { n.Add(1) })
	feed(t, d, pkt(1, &CompletePingCheck{}), pkt(1, &CompletePingCheck{}))
	if n.Load() != 2 {
		t.Errorf("handler ran %d times, want 2 with dedupe off", n.Load())
	}
}

func TestDispatchUnhandledCounted(t *testing.T) {
	var seen []string
	d := NewDispatcher(OnUnhandled(func(p *Packet) {
		seen = append(seen, p.ID.String())
	}))

	feed(t, d,
		pkt(1, &CompletePingCheck{}),
		pkt(2, &CompletePingCheck{}),
		pkt(3, &ChatFromViewer{}),
	)

	if st := d.Stats(); st.Unhandled != 3 || st.Dispatched != 0 {
		t.Errorf("stats = %+v", st)
	}
	un := d.Unhandled()
	if un[IDOf(&CompletePingCheck{})] != 2 || un[IDOf(&ChatFromViewer{})] != 1 {
		t.Errorf("per-message counts = %v", un)
	}
	if len(seen) != 3 || seen[0] != "CompletePingCheck" {
		t.Errorf("hook saw %v", seen)
	}
}

func TestDispatchErrorHook(t *testing.T) {
	var got *Packet
	d := NewDispatcher(OnError(func(p *Packet) { got = p }))

	feed(t, d, &Packet{
		Header: Header{Sequence: 1},
		ID:     MakeID(FreqHigh, 253),
		Err:    ErrUnknownMessage,
		Body:   []byte{0xde, 0xad},
	})

	if got == nil {
		t.Fatal("error hook not called")
	}
	if st := d.Stats(); st.Errors != 1 {
		t.Errorf("stats = %+v", st)
	}
	// An unknown number is still worth counting per message, so a
	// build that predates a message can say which one it is missing.
	if d.Unhandled()[MakeID(FreqHigh, 253)] != 1 {
		t.Errorf("unknown message not counted: %v", d.Unhandled())
	}
}

func TestDispatchAckOnlyPacket(t *testing.T) {
	d := NewDispatcher()
	feed(t, d, &Packet{Header: Header{Sequence: 1}, Acks: []uint32{5}})
	if st := d.Stats(); st.AckOnly != 1 || st.Dispatched != 0 {
		t.Errorf("stats = %+v", st)
	}
}

func TestHandleRejectsBadName(t *testing.T) {
	d := NewDispatcher()
	if err := d.Handle("NoSuchMessage", func(*Packet) {}); err == nil {
		t.Error("expected an error for an unknown message name")
	} else if !strings.Contains(err.Error(), "NoSuchMessage") {
		t.Errorf("error should name the message: %v", err)
	}
	if err := d.Handle("CompletePingCheck", func(*Packet) {}); err != nil {
		t.Fatal(err)
	}
	if err := d.Handle("CompletePingCheck", func(*Packet) {}); err == nil {
		t.Error("expected an error registering a second handler")
	}
}

func TestDispatchStopsOnCancel(t *testing.T) {
	d := NewDispatcher()
	in := make(chan *Packet)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, in) }()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancellation")
	}
}

// TestDispatchAckWiring runs receiver, dispatcher and sender together
// over loopback UDP and checks the acknowledgement bookkeeping: an
// inbound reliable packet gets acknowledged, and an inbound
// acknowledgement stops a retransmission.
func TestDispatchAckWiring(t *testing.T) {
	// The peer we are talking to.
	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	defer peer.Close()

	out, err := net.Dial("udp", peer.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	sender := NewSender(out, WithAckDelay(10*time.Millisecond), WithRetransmit(30*time.Millisecond, 5))
	sctx, scancel := context.WithCancel(context.Background())
	sdone := make(chan error, 1)
	go func() { sdone <- sender.Run(sctx) }()
	defer func() { scancel(); <-sdone }()

	d := NewDispatcher(WithSender(sender))
	in := make(chan *Packet, 4)
	dctx, dcancel := context.WithCancel(context.Background())
	ddone := make(chan error, 1)
	go func() { ddone <- d.Run(dctx, in) }()
	defer func() { dcancel(); <-ddone }()

	// Something of ours is in flight and awaiting acknowledgement.
	if err := sender.SendReliable(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "our packet to go out", func() bool { return sender.Stats().Sent >= 1 })

	// A reliable packet arrives carrying an acknowledgement of it.
	in <- &Packet{
		Header:  Header{Sequence: 500, Flags: FlagReliable},
		ID:      IDOf(&CompletePingCheck{}),
		Message: &CompletePingCheck{},
		Acks:    []uint32{1},
	}

	// It should be acknowledged in turn...
	waitFor(t, "our acknowledgement", func() bool { return sender.Stats().AcksQueued >= 1 })
	// ... and ours should stop being retransmitted.
	time.Sleep(150 * time.Millisecond)
	if st := sender.Stats(); st.Resent != 0 {
		t.Errorf("resent %d times despite being acknowledged", st.Resent)
	}
}

func BenchmarkDispatch(b *testing.B) {
	d := NewDispatcher(WithConcurrency(8), WithDedupe(0))
	d.MustHandle("CompletePingCheck", func(p *Packet) {}, Inline())
	p := pkt(1, &CompletePingCheck{})
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		d.one(ctx, p)
	}
}

// TestDispatchConfirmsPacketAckMessages is the bug the live login
// found: a simulator sends most acknowledgements as a PacketAck message
// in its own datagram, not on the tail of another packet.  Confirming
// only p.Acks meant every reliable message we sent retransmitted until
// it was abandoned.
func TestDispatchConfirmsPacketAckMessages(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c, WithRetransmit(30*time.Millisecond, 5))
	defer stop()

	d := NewDispatcher(WithSender(s))

	if err := s.SendReliable(context.Background(), &CompletePingCheck{}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first send", func() bool { return c.count() == 1 })
	h, _, _ := DecodeHeader(c.all()[0])

	// The acknowledgement arrives as a message, with nothing in
	// Packet.Acks at all.
	in := make(chan *Packet, 1)
	in <- &Packet{
		Header:  Header{Sequence: 900},
		ID:      IDOf(&PacketAck{}),
		Message: &PacketAck{Packets: []PacketAck_Packets{{ID: h.Sequence}}},
	}
	close(in)

	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background(), in) }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond)
	if st := s.Stats(); st.Resent != 0 {
		t.Errorf("resent %d times despite a PacketAck confirming it", st.Resent)
	}
	if st := d.Stats(); st.AcksSeen != 1 {
		t.Errorf("dispatch stats = %+v, want one acknowledgement seen", st)
	}
	// It was acted on, so it is not unhandled.
	if st := d.Stats(); st.Unhandled != 0 {
		t.Errorf("PacketAck counted as unhandled: %+v", st)
	}
}

// TestDispatchPacketAckStillReachesAHandler: consuming it for
// bookkeeping must not hide it from someone who registered for it.
func TestDispatchPacketAckStillReachesAHandler(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c)
	defer stop()

	d := NewDispatcher(WithSender(s))
	seen := make(chan int, 1)
	d.MustHandle("PacketAck", func(p *Packet) {
		seen <- len(p.Message.(*PacketAck).Packets)
	}, Inline())

	in := make(chan *Packet, 1)
	in <- &Packet{
		Header:  Header{Sequence: 1},
		ID:      IDOf(&PacketAck{}),
		Message: &PacketAck{Packets: []PacketAck_Packets{{ID: 5}, {ID: 6}}},
	}
	close(in)
	if err := d.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}

	select {
	case n := <-seen:
		if n != 2 {
			t.Errorf("handler saw %d acks", n)
		}
	default:
		t.Error("a registered PacketAck handler never ran")
	}
}

// TestWithConcurrencyKeepsAtLeastOne: a zero would make the semaphore
// unbuffered and every asynchronous handler would deadlock against the
// dispatch loop, which is a worse answer than ignoring the argument.
func TestWithConcurrencyKeepsAtLeastOne(t *testing.T) {
	for _, n := range []int{0, -1} {
		d := NewDispatcher(WithConcurrency(n))
		if cap(d.sem) != 1 {
			t.Errorf("WithConcurrency(%d) gave a semaphore of %d", n, cap(d.sem))
		}
	}
	if d := NewDispatcher(WithConcurrency(3)); cap(d.sem) != 3 {
		t.Errorf("WithConcurrency(3) gave a semaphore of %d", cap(d.sem))
	}
}

// TestRelaySkipsWhatATapWouldRepeat is the whole reason the relay hook
// exists rather than reusing the tap.  Fed the input of
// TestTapSeesEverything, which the tap sees as 1, 2, 2, 3, and a packet
// of nothing but acks, the relay sees 1 and 2: the retransmission is
// gone, because forwarding it would reach the far end as a second
// message it could not tell from the first, and the packet that would
// not decode and the one with no message are gone, because there is
// nothing there to pass on.
func TestRelaySkipsWhatATapWouldRepeat(t *testing.T) {
	var relayed []uint32
	d := NewDispatcher(WithRelay(func(p *Packet) {
		relayed = append(relayed, p.Header.Sequence)
	}))

	m := &CompletePingCheck{}
	feed(t, d,
		pkt(1, m),
		pkt(2, m),
		resend(2, m), // a duplicate
		&Packet{Header: Header{Sequence: 3}, Err: ErrShort},     // would not decode
		&Packet{Header: Header{Sequence: 4}, Acks: []uint32{5}}, // nothing but acks
	)

	want := []uint32{1, 2}
	if len(relayed) != len(want) {
		t.Fatalf("the relay saw %v, want %v", relayed, want)
	}
	for i := range want {
		if relayed[i] != want[i] {
			t.Fatalf("the relay saw %v, want %v", relayed, want)
		}
	}
	if st := d.Stats(); st.Relayed != 2 {
		t.Errorf("stats = %+v, want 2 relayed", st)
	}
}

// TestOnDuplicateSeesWhatTheRelayIsNotOffered: a record kept from the
// relay misses every retransmission, since the relay is offered each
// message once.  OnDuplicate is given exactly those, so that the relay
// and it between them see each packet once.
func TestOnDuplicateSeesWhatTheRelayIsNotOffered(t *testing.T) {
	var relayed, repeated []uint32
	d := NewDispatcher(
		WithRelay(func(p *Packet) { relayed = append(relayed, p.Header.Sequence) }),
		OnDuplicate(func(p *Packet) { repeated = append(repeated, p.Header.Sequence) }),
	)

	m := &CompletePingCheck{}
	feed(t, d, pkt(1, m), pkt(2, m), resend(2, m), pkt(3, m), resend(1, m), resend(2, m))

	if got, want := fmt.Sprint(relayed), "[1 2 3]"; got != want {
		t.Errorf("the relay saw %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(repeated), "[2 1 2]"; got != want {
		t.Errorf("OnDuplicate saw %s, want %s", got, want)
	}
	if st := d.Stats(); st.Duplicates != 3 {
		t.Errorf("stats = %+v, want 3 duplicates", st)
	}
}

// TestRelaySeesMessagesNothingHandles: the messages a relay most needs
// to pass on are the ones this build has no handler for, so having no
// handler must not keep one from the relay.
func TestRelaySeesMessagesNothingHandles(t *testing.T) {
	var relayed, handled int
	d := NewDispatcher(WithRelay(func(*Packet) { relayed++ }))
	if err := d.Handle("CompletePingCheck", func(*Packet) { handled++ }); err != nil {
		t.Fatal(err)
	}

	feed(t, d,
		pkt(1, &CompletePingCheck{}), // handled here
		pkt(2, &StartPingCheck{}),    // not
	)

	if relayed != 2 {
		t.Errorf("the relay saw %d messages, want both", relayed)
	}
	if handled != 1 {
		t.Errorf("the handler ran %d times, want once", handled)
	}
	if st := d.Stats(); st.Unhandled != 1 {
		t.Errorf("stats = %+v, want one unhandled", st)
	}
}

// TestRelaySkipsConsumedPacketAck: acknowledgements are terminated at
// each end of a relay rather than forwarded, since the two sides number
// their packets independently.  A PacketAck the bookkeeping has already
// acted on is circuit machinery, not a message, and must not be offered.
func TestRelaySkipsConsumedPacketAck(t *testing.T) {
	c := newCapture()
	s, stop := runSender(t, c)
	defer stop()

	var relayed []string
	d := NewDispatcher(WithSender(s), WithRelay(func(p *Packet) {
		relayed = append(relayed, p.ID.String())
	}))

	feed(t, d,
		&Packet{
			Header:  Header{Sequence: 1},
			ID:      IDOf(&PacketAck{}),
			Message: &PacketAck{Packets: []PacketAck_Packets{{ID: 7}}},
		},
		pkt(2, &CompletePingCheck{}),
	)

	if len(relayed) != 1 {
		t.Fatalf("the relay saw %v, want only the ping", relayed)
	}
	if st := d.Stats(); st.Relayed != 1 {
		t.Errorf("stats = %+v, want 1 relayed", st)
	}
}

// TestTapSeesEverything is what the tap is for: capture, and noticing
// that anything at all has arrived.  It runs before routing and before
// duplicate suppression, so a retransmission and a packet nobody
// handles both reach it.
func TestTapSeesEverything(t *testing.T) {
	var tapped []uint32
	d := NewDispatcher(WithTap(func(p *Packet) {
		tapped = append(tapped, p.Header.Sequence)
	}))

	m := &CompletePingCheck{}
	feed(t, d,
		pkt(1, m),    // nothing is registered for it
		pkt(2, m),    //
		resend(2, m), // a duplicate
		&Packet{Header: Header{Sequence: 3}, Err: ErrShort}, // and one that did not decode
	)

	want := []uint32{1, 2, 2, 3}
	if len(tapped) != len(want) {
		t.Fatalf("the tap saw %v, want %v", tapped, want)
	}
	for i := range want {
		if tapped[i] != want[i] {
			t.Fatalf("the tap saw %v, want %v", tapped, want)
		}
	}
}

// TestMustHandlePanics: registrations are made at startup, where a name
// that is not in the template is a typo in the source rather than
// anything a running program can do about it.
func TestMustHandlePanics(t *testing.T) {
	d := NewDispatcher()
	defer func() {
		if recover() == nil {
			t.Error("MustHandle accepted a message name that is not in the template")
		}
	}()
	d.MustHandle("NoSuchMessage", func(*Packet) {})
}

// TestDispatchWithoutAContext: Run is called from enough places that
// one of them passing nil is a matter of time, and panicking on it
// would take the session down.
func TestDispatchWithoutAContext(t *testing.T) {
	d := NewDispatcher()
	var seen atomic.Int64
	if err := d.Handle("CompletePingCheck", func(*Packet) { seen.Add(1) }); err != nil {
		t.Fatal(err)
	}

	in := make(chan *Packet, 1)
	in <- pkt(1, &CompletePingCheck{})
	close(in)

	//lint:ignore SA1012 the nil is the point of the test
	if err := d.Run(nil, in); err != nil {
		t.Fatal(err)
	}
	if seen.Load() != 1 {
		t.Errorf("the handler ran %d times", seen.Load())
	}
}

// TestDispatchGivesUpWaitingForASlot: with every handler slot taken and
// the context cancelled, the loop must stop rather than block on a
// semaphore nothing is going to release.
func TestDispatchGivesUpWaitingForASlot(t *testing.T) {
	t.Parallel()

	d := NewDispatcher(WithConcurrency(1))
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	if err := d.Handle("CompletePingCheck", func(*Packet) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
	}); err != nil {
		t.Fatal(err)
	}

	in := make(chan *Packet, 2)
	in <- pkt(1, &CompletePingCheck{})
	in <- pkt(2, &CompletePingCheck{})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx, in) }()

	<-started // the one slot is taken
	cancel()

	// Run cannot return until the handler does, because it drains the
	// semaphore on the way out.
	select {
	case <-done:
		t.Fatal("Run returned while a handler was still going")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop")
	}
	// The second packet was dispatched but never got a slot.
	if st := d.Stats(); st.Async != 1 {
		t.Errorf("stats = %+v, want one handler ever started", st)
	}
}
