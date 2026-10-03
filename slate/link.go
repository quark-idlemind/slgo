package slate

// What a step does with the probes and the bridge: send, a touch or drag
// on a link, expect link, and expect say on a channel only the bridge
// hears.
// Why: doc/slate-runner.md#probe-and-bridge

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// target is the binding a touch or drag acts on: the named one, or for a
// link N the prim at that number. With a probe the hello map answers now
// and a link not in it fails the step before anything is sent; without
// one the store does, in the stimulus's prepare (see withLink), because a
// product can relink itself.
func (s *stepRun) target(name Ident, link *Int) (*binding, func(context.Context) error, error) {
	b, err := s.bound(name)
	if err != nil || link == nil {
		return b, nil, err
	}
	if s.r.probed(b) {
		lb, err := s.r.linkBinding(s.r.ctx, nil, b, link.Value)
		return lb, nil, err
	}
	tb := &binding{name: linkName(b.name, int32(link.Value))}
	return tb, func(ctx context.Context) error {
		lb, err := s.r.linkBinding(ctx, nil, b, link.Value)
		if err != nil {
			return s.linkErr(err)
		}
		*tb = *lb
		return nil
	}, nil
}

// withLink has a stimulus resolve its link, when it has one to resolve,
// before it does anything else in its prepare.
func withLink(st *stimulus, resolve func(context.Context) error) *stimulus {
	if resolve == nil {
		return st
	}
	inner := st.prepare
	st.prepare = func(ctx context.Context) error {
		if err := resolve(ctx); err != nil {
			return err
		}
		if inner == nil {
			return nil
		}
		return inner(ctx)
	}
	return st
}

// linkKey is a link key written in the script: the null key when omitted.
func (s *stepRun) linkKey(k *Key) (msg.UUID, error) {
	if k == nil || k.Null {
		return msg.UUID{}, nil
	}
	if k.Use != nil {
		v, err := s.capture(k.Use, CapUUID)
		return v.id, err
	}
	return msg.ParseUUID(k.ID)
}

// sendStimulus has the probe in the sender's prim call llMessageLinked,
// by one line on CONTROL that the bridge relays. The line is made, and
// its length and the link checked, before anything is said. A bad report
// from that prim fails the step (badScan).
// Why: doc/slate-runner.md#stimuli
func (s *stepRun) sendStimulus(sd *Send) (*stimulus, error) {
	b, err := s.bound(sd.Name)
	if err != nil {
		return nil, err
	}
	pp, err := s.r.primAt(b, sd.From.Value)
	if err != nil {
		return nil, err
	}
	key, err := s.linkKey(sd.Key)
	if err != nil {
		return nil, err
	}
	target := sd.To.Int.Value
	if sd.To.Word != "" {
		target = int64(LinkWords[sd.To.Word])
	}
	pr := s.r.pr
	text := sd.Text
	if c := sd.TextCapture; c != nil {
		v, err := s.capture(c, CapText)
		if err != nil {
			return nil, err
		}
		if text = v.text; !linkText(text) {
			return nil, s.sentence("%s is %q and link text must be bytes 0x20-0x7E, tab or newline", c, text)
		}
	}
	line, err := sendLine(pr.nonce, pp.seen.ID, pr.command[b.name], pp.link, int32(target), int32(sd.Num.Value), key, text)
	if err != nil {
		if sd.TextCapture != nil {
			return nil, s.sentence("%s is %q and %v", sd.TextCapture, text, err)
		}
		return nil, err
	}
	return &stimulus{
		send: func(ctx context.Context, _ time.Duration) (string, error) {
			if err := s.r.sess.Say(ctx, line, pr.control); err != nil {
				return "", err
			}
			s.sentTo = pp
			return fmt.Sprintf("sent from link %d of %s: target %d num %d text %q", pp.link, b.name, target, sd.Num.Value, text), nil
		},
	}, nil
}

// badScan fails the step when the prim that was sent a command said it
// could not parse it, after the command went.
func (s *stepRun) badScan() {
	if s.sentTo == nil || s.state == stFailed {
		return
	}
	for _, ev := range s.r.log.events[s.t.mark:] {
		if ev.kind == evWire && ev.wire.Kind == wireBad && ev.prim == s.sentTo && !ev.at.Before(s.arm) {
			s.why = "the probe rejected the command"
			s.fail(1, "%s", s.why)
			return
		}
	}
}

// linkExpect matches a link report of the object's linkset. An overflow
// report never matches, and the first report of a message passes an
// expectation with no heard by.
// Why: doc/slate-runner.md#link
func (s *stepRun) linkExpect(x *expState) error {
	lx := x.e.Link
	b := s.r.lookup(lx.Name.Text)
	if b == nil {
		return fmt.Errorf("%s is not an object", lx.Name.Text)
	}
	if s.r.pr == nil || s.r.pr.links[b.name] == nil {
		return fmt.Errorf("%s has no probe", b.name)
	}
	key, err := s.linkKey(lx.Key)
	if err != nil {
		return err
	}
	text, err := s.textMatch(lx.Text)
	if err != nil {
		return err
	}
	x.match = func(ev *event) bool {
		if ev.kind != evWire || ev.wire.Kind != wireLink || ev.prim == nil || ev.prim.b != b {
			return false
		}
		m := ev.wire
		if m.Sender != int32(lx.From.Value) || m.Num != int32(lx.Num.Value) || m.Key != key || !text.match(m.Text) {
			return false
		}
		return lx.HeardBy == nil || m.HeardBy == int32(lx.HeardBy.Value)
	}
	if !x.neg {
		x.onMatch = func(ev *event) { s.bindMatched(x, nil, groupSrc{text, ev.wire.Text}) }
	}
	return nil
}

// listenChannel is the channel an expectation names when only the bridge
// hears it.
func listenChannel(c ExpectChan) (int32, bool) {
	if c.Kind != ChanNumber || c.Int.Value == 0 || c.Int.Value == 2147483647 {
		return 0, false
	}
	return int32(c.Int.Value), true
}

// fwdExpect matches what the bridge forwarded from a listen channel. The
// text is the raw tail, and the speaker is the key and name the bridge
// heard.
// Why: doc/slate-runner.md#bridge-lines
func (s *stepRun) fwdExpect(ctx context.Context, x *expState, ch int32) error {
	sx := x.e.Say
	if s.r.pr == nil {
		return fmt.Errorf("channel %d needs the bridge, and the file has no listen for it", ch)
	}
	text, err := s.textMatch(sx.Text)
	if err != nil {
		return err
	}
	var why string
	from, err := s.speaker(ctx, sx.From, &why)
	if err != nil {
		return err
	}
	x.noteFn = s.linkNote(&why)
	x.match = func(ev *event) bool {
		if ev.kind != evWire || ev.wire.Kind != wireFwd || ev.wire.Channel != ch {
			return false
		}
		m := ev.wire
		return text.match(m.Tail) && from(sl.Line{Source: m.Speaker, From: m.Name, Text: m.Tail})
	}
	if !x.neg {
		x.onMatch = func(ev *event) {
			v := capValue{typ: CapText, text: ev.wire.Tail}
			s.bindMatched(x, &v, groupSrc{text, ev.wire.Tail})
		}
	}
	return nil
}
