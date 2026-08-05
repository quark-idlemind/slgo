package main

// slchat without slgod: the session in this process.
//
// The daemon is the better arrangement and the reason it exists has not
// changed -- a session that outlives the program talking to it can be
// restarted, rebuilt and debugged without the grid noticing, and two
// programs can share one avatar.  But it is a second thing to set up
// and keep running, and somebody who only wants to talk should not have
// to.  So the same App runs against either: the Grid interface is
// satisfied here by an agent.Agent in this process rather than by a
// connection to one somewhere else.
//
// What is lost is exactly what the daemon was for.  Quitting logs the
// avatar out, and everything the session learned -- the region's
// objects, who is online -- is learned again from scratch next time.

import (
	"context"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// Direct is a Grid backed by a session this process holds.
type Direct struct {
	a        *agent.Agent
	messages chan *client.Message
}

// relayDepth is how far behind the display may fall before messages are
// dropped.  Dropping is right: the alternative is stalling the circuit
// that feeds it, and a chat line nobody could keep up with is worth
// less than the session staying up.
const relayDepth = 1024

// LoginDirect logs in and returns the session, in the shape the rest of
// slchat expects.
func LoginDirect(ctx context.Context, l agent.Login) (*Direct, *pb.AgentInfo, error) {
	acct, err := l.Do(ctx)
	if err != nil {
		return nil, nil, err
	}

	d := &Direct{messages: make(chan *client.Message, relayDepth)}

	opts := agent.Options{
		// Keeping the undecoded body is what lets one tap feed the
		// display without the agent having to know what any of it
		// means -- the same trick slgod's relay uses.
		Recv: []msg.ReceiverOption{msg.KeepBody()},
		Tap:  d.tap,
	}
	if d.a, err = agent.Connect(ctx, acct, opts); err != nil {
		return nil, nil, err
	}

	info := &pb.AgentInfo{
		Name:           "direct",
		AgentId:        acct.AgentID.String(),
		SessionId:      acct.SessionID.String(),
		AvatarName:     acct.Name(),
		Region:         d.a.RegionName(),
		InventoryRoot:  acct.InventoryRoot.String(),
		ChannelVersion: d.a.ChannelVersion(),
		Caps:           d.a.Caps.Names(),
		Connected:      true,
	}
	return d, info, nil
}

// tap turns a packet into the same thing slgod would have relayed.
//
// It runs on the dispatch goroutine, so it hands over and returns: a
// display that has stopped reading must not be able to stop the
// circuit.
func (d *Direct) tap(p *msg.Packet) {
	if p.Message == nil && p.Body == nil {
		return // acknowledgements carry nothing to show
	}
	body := p.Body
	if body == nil {
		b, err := p.Message.Encode()
		if err != nil {
			return
		}
		body = b
	}
	m := &client.Message{
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
	default: // the display is behind; drop it rather than block the circuit
	}
}

// Logout ends the session, which is the part that has no equivalent
// when a daemon is holding it.
func (d *Direct) Logout(ctx context.Context, timeout time.Duration) error {
	return d.a.Logout(ctx, timeout)
}

// ---------------------------------------------------------------- Grid

func (d *Direct) Send(ctx context.Context, m msg.Message, reliable bool) error {
	if reliable {
		return d.a.Send.SendReliable(ctx, m)
	}
	return d.a.Send.Send(ctx, m)
}

func (d *Direct) Messages() <-chan *client.Message { return d.messages }
func (d *Direct) Done() <-chan struct{}            { return d.a.Done() }
func (d *Direct) Err() error                       { return d.a.Err() }

func (d *Direct) Objects(ctx context.Context, named, id string) (*pb.ObjectsResponse, error) {
	all := d.a.Objects.All()
	out := &pb.ObjectsResponse{Known: int32(len(all))}
	for _, o := range all {
		if named != "" && o.Name != named {
			continue
		}
		if id != "" && o.ID.String() != id {
			continue
		}
		out.Objects = append(out.Objects, &pb.ObjectInfo{
			Id:           o.ID.String(),
			Local:        o.Local,
			Parent:       o.Parent,
			Pcode:        uint32(o.PCode),
			Scale:        vec(o.Scale),
			Position:     vec(o.Position),
			Name:         o.Name,
			Owner:        o.Owner.String(),
			TextureEntry: o.TextureEntry,
			Text:         o.Text,
		})
	}
	return out, nil
}

func (d *Direct) Presence(ctx context.Context, drawDistance float32) (*pb.PresenceResponse, error) {
	if drawDistance > 0 {
		l := d.a.Look()
		l.Far = drawDistance
		d.a.SetLook(l)
	}
	l := d.a.Look()
	return &pb.PresenceResponse{
		Position:     vec(d.a.Position()),
		LookAt:       vec(l.At),
		Camera:       vec(l.Center),
		DrawDistance: l.Far,
		RegionHandle: d.a.RegionHandle(),
		Region:       d.a.RegionName(),
		ActiveGroup:  d.a.ActiveGroup().String(),
	}, nil
}

func (d *Direct) Region(ctx context.Context) (*pb.RegionInfo, error) {
	r, known := d.a.Region()
	return &pb.RegionInfo{
		Id: r.ID.String(), Handle: r.Handle, Name: r.Name,
		Flags: r.Flags, FlagsExtended: r.Extended,
		Access: uint32(r.Access), Owner: r.Owner.String(),
		EstateManager: r.EstateManager,
		WaterHeight:   r.WaterHeight,
		ProductName:   r.ProductName, ProductSku: r.ProductSKU,
		ColoName: r.ColoName,
		CpuClass: r.CPUClass, CpuRatio: r.CPURatio,
		Protocols: r.Protocols,
		Known:     known,
	}, nil
}

func (d *Direct) Friends(ctx context.Context) ([]*pb.Friend, error) {
	fs := d.a.Friends()
	out := make([]*pb.Friend, 0, len(fs))
	for _, f := range fs {
		out = append(out, &pb.Friend{
			Id:          f.ID.String(),
			Online:      f.Online,
			RightsGiven: f.RightsGiven,
			RightsHas:   f.RightsHas,
		})
	}
	return out, nil
}

func (d *Direct) NoteFriend(ctx context.Context, id msg.UUID, online bool) error {
	d.a.NoteFriend(id, online)
	return nil
}

func (d *Direct) HasCap(name string) bool { return d.a.HasCap(name) }

func (d *Direct) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	return d.a.DoCap(ctx, r)
}

func vec(v msg.Vector3) *pb.Vector3 { return &pb.Vector3{X: v.X, Y: v.Y, Z: v.Z} }

var _ Grid = (*Direct)(nil)

// activeGroup settles which group the avatar acts as, the way slgod
// does at startup.
//
// A parcel usually grants building and sometimes chat range to a GROUP
// rather than to individuals, and a fresh login has none active.  A
// viewer hides this by storing the choice and re-sending it every time;
// headless it has to be done again on every login.  One group joined
// means there is nothing to choose, and several means the answer is not
// derivable, so nothing is picked.
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
	return fmt.Sprintf("direct session as %s", d.a.Account.Name())
}
