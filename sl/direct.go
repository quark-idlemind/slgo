package sl

// The backend that holds the session here.
//
// No daemon, nothing to set up, and the avatar logs out when the
// program does.  That last part is the whole trade: a hosted session
// outlives the program talking to it, which is what makes a client
// restartable and lets two programs share one avatar.
//
// Everything a hosted backend asks slgod for, this answers from the
// agent in this process.  The answers have to agree; see
// backend_test.go, which runs one suite against both.

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// Direct is a session this process holds.
type Direct struct {
	a        *agent.Agent
	info     *Info
	messages chan *Message
}

var _ Backend = (*Direct)(nil)

// relayDepth is how far behind a reader may fall before messages are
// dropped.  Dropping is right: the alternative is stalling the circuit,
// and a message nobody could keep up with is worth less than the
// session staying up.
const relayDepth = 1024

// Login logs in and holds the session.
//
// The subscriptions a hosted session names are not needed here --
// nothing is being filtered for a wire -- so everything the circuit
// carries reaches the relay.
func Login(ctx context.Context, l agent.Login) (*Direct, error) {
	acct, err := l.Do(ctx)
	if err != nil {
		return nil, err
	}

	d := &Direct{messages: make(chan *Message, relayDepth)}
	opts := agent.Options{
		// Keeping the undecoded body is what lets one tap feed the
		// relay without the agent knowing what any of it means.
		Recv: []msg.ReceiverOption{msg.KeepBody()},
		Tap:  d.tap,
	}
	if d.a, err = agent.Connect(ctx, acct, opts); err != nil {
		return nil, err
	}
	d.info = &Info{
		Name:          "direct",
		AgentID:       acct.AgentID,
		SessionID:     acct.SessionID,
		AvatarName:    acct.Name(),
		Region:        d.a.RegionName(),
		InventoryRoot: acct.InventoryRoot,
		Channel:       d.a.ChannelVersion(),
		Caps:          d.a.Caps.Names(),
	}
	return d, nil
}

// Agent is the session underneath, for anything this package does not
// cover.  Direct only, by definition.
func (d *Direct) Agent() *agent.Agent { return d.a }

// Logout ends the session properly, which is the part a daemon would
// have made unnecessary.  Direct only: a hosted session is not the
// client's to end.
func (d *Direct) Logout(ctx context.Context, timeout time.Duration) error {
	return d.a.Logout(ctx, timeout)
}

// tap turns a packet into the same thing slgod would have relayed.
//
// It runs on the dispatch goroutine, so it hands over and returns: a
// reader that has stopped must not be able to stop the circuit.
func (d *Direct) tap(p *msg.Packet) {
	if p.Message == nil && p.Body == nil {
		return // acknowledgements carry nothing to relay
	}
	body := p.Body
	if body == nil {
		b, err := p.Message.Encode()
		if err != nil {
			return
		}
		body = b
	}
	m := &Message{
		ID:       p.ID,
		Body:     body,
		Sequence: p.Header.Sequence,
		Flags:    uint32(p.Header.Flags),
		At:       p.At,
	}
	if info := msg.Lookup(p.ID); info != nil {
		m.Name = info.Name
	}
	select {
	case d.messages <- m:
	default: // the reader is behind; dropping beats stalling the circuit
	}
}

func (d *Direct) Info() *Info               { return d.info }
func (d *Direct) Messages() <-chan *Message { return d.messages }
func (d *Direct) Done() <-chan struct{}     { return d.a.Done() }
func (d *Direct) Err() error                { return d.a.Err() }
func (d *Direct) HasCap(name string) bool   { return d.a.HasCap(name) }

// Close ends the session.  Unlike a hosted one there is nobody else
// holding it, so this logs out rather than merely hanging up.
func (d *Direct) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := d.a.Logout(ctx, 10*time.Second)
	d.a.Close()
	return err
}

func (d *Direct) Send(ctx context.Context, m msg.Message, reliable bool) error {
	if reliable {
		return d.a.Send.SendReliable(ctx, m)
	}
	return d.a.Send.Send(ctx, m)
}

func (d *Direct) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	return d.a.DoCap(ctx, r)
}

func (d *Direct) Presence(ctx context.Context, drawDistance float32) (*Presence, error) {
	if drawDistance > 0 {
		l := d.a.Look()
		l.Far = drawDistance
		d.a.SetLook(l)
	}
	l := d.a.Look()
	return &Presence{
		Position:     d.a.Position(),
		LookAt:       l.At,
		Camera:       l.Center,
		DrawDistance: l.Far,
		RegionHandle: d.a.RegionHandle(),
		Region:       d.a.RegionName(),
		ActiveGroup:  d.a.ActiveGroup(),
	}, nil
}

func (d *Direct) Objects(ctx context.Context, named, id string) ([]*Seen, error) {
	var out []*Seen
	for _, o := range d.a.Objects().All() {
		if named != "" && o.Name != named {
			continue
		}
		if id != "" && o.ID.String() != id {
			continue
		}
		s := &Seen{
			Object:       Object{ID: o.ID, Local: o.Local, Name: o.Name},
			Owner:        o.Owner,
			Position:     o.Position,
			Scale:        o.Scale,
			Rotation:     o.Rotation,
			Parent:       o.Parent,
			PCode:        o.PCode,
			TextureEntry: o.TextureEntry,
			Shape:        o.Shape,
			Text:         o.Text,
			AttachPoint:  o.AttachPoint,
			AttachItem:   o.AttachItem,
		}
		out = append(out, s)
	}
	return out, nil
}

func (d *Direct) Region(ctx context.Context) (*Region, bool, error) {
	r, known := d.a.Region()
	return &Region{
		ID: r.ID, Handle: r.Handle, Name: r.Name,
		Flags: r.Flags, Extended: r.Extended,
		Access: r.Access, Owner: r.Owner,
		EstateManager: r.EstateManager,
		WaterHeight:   r.WaterHeight,
		ProductName:   r.ProductName, ProductSKU: r.ProductSKU,
		ColoName: r.ColoName,
		CPUClass: r.CPUClass, CPURatio: r.CPURatio,
		Protocols: r.Protocols,
	}, known, nil
}

// Lock is nothing to do here.
//
// A lock exists so that two clients of one slgod do not use the same
// object at once.  A direct session IS the only client -- Second Life
// will not have one avatar logged in twice -- so there is nobody to
// contend with and nothing to wait for.
func (d *Direct) Lock(ctx context.Context, name string) error { return nil }

// TryLock always succeeds, for the same reason Lock does nothing.
func (d *Direct) TryLock(ctx context.Context, name string) (bool, string, error) {
	return true, "", nil
}

// Unlock likewise.
func (d *Direct) Unlock(name string) error { return nil }

func (d *Direct) Flush(ctx context.Context) (int, error) {
	return d.a.Objects().Flush(), nil
}

func (d *Direct) Friends(ctx context.Context) ([]Friend, error) {
	fs := d.a.Friends()
	out := make([]Friend, 0, len(fs))
	for _, f := range fs {
		out = append(out, Friend{
			ID: f.ID, Online: f.Online,
			RightsGiven: f.RightsGiven, RightsHas: f.RightsHas,
		})
	}
	return out, nil
}

func (d *Direct) NoteFriend(ctx context.Context, id msg.UUID, online bool) error {
	d.a.NoteFriend(id, online)
	return nil
}

// activeGroup settles which group the avatar acts as, the way slgod
// does at startup: one group joined means there is nothing to choose,
// several means the answer is not derivable and nothing is picked.
//
// A parcel usually grants building to a group rather than to
// individuals, and a fresh login has none active, so without this a
// direct session cannot build where a hosted one can.
func (d *Direct) activeGroup(ctx context.Context) (string, bool) {
	joined := d.a.WaitGroups(ctx, 10*time.Second)
	if len(joined) != 1 {
		return "", false
	}
	g := joined[0]
	m := &msg.ActivateGroup{}
	m.AgentData.AgentID = d.a.Account.AgentID
	m.AgentData.SessionID = d.a.Account.SessionID
	m.AgentData.GroupID = g.ID
	if err := d.a.Send.SendReliable(ctx, m); err != nil {
		return "", false
	}
	return g.Name, true
}

func (d *Direct) String() string {
	return fmt.Sprintf("direct session as %s", d.info.AvatarName)
}
