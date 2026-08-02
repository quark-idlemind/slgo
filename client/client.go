// Package client attaches to a server holding grid connections.
//
// It deliberately holds no grid state: the circuit, its sequence
// numbers and the capability URLs stay on the far end, which is the
// whole point.  A client may be stopped, rebuilt and restarted as often
// as you like without the grid noticing, which is what makes it the
// place to put the code you are actually working on.
package client

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"slgo/agent"
	"slgo/msg"
	pb "slgo/proto/slgov1"
)

// Conn is a connection to a server.
type Conn struct {
	cc   *grpc.ClientConn
	grid pb.GridClient

	agent string

	mu   sync.RWMutex
	info *pb.AgentInfo
	caps map[string]bool

	stream   pb.Grid_StreamClient
	messages chan *Message
	events   chan *pb.AgentEvent

	closeOnce sync.Once
	done      chan struct{}
	err       atomic.Value
}

// Message is a grid message relayed by the server.
type Message struct {
	ID       msg.ID
	Name     string
	Sequence uint32
	Flags    uint32
	Body     []byte
	At       time.Time
}

// Decode turns the relayed bytes into a typed message.  A nil result
// with a nil error means the number is not in this build's template --
// the server relayed it anyway, and Body is still there.
func (m *Message) Decode() (msg.Message, error) {
	v := msg.New(m.ID)
	if v == nil {
		return nil, nil
	}
	if err := v.Decode(m.Body); err != nil {
		return nil, err
	}
	return v, nil
}

// Reliable reports whether the simulator wanted this acknowledged.  The
// server has already done it.
func (m *Message) Reliable() bool { return m.Flags&msg.FlagReliable != 0 }

// Dial connects to a server without opening a stream.  Use Attach to
// start receiving messages.
func Dial(ctx context.Context, addr string, opts ...grpc.DialOption) (*Conn, error) {
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	cc, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, err
	}
	return &Conn{
		cc:       cc,
		grid:     pb.NewGridClient(cc),
		caps:     map[string]bool{},
		messages: make(chan *Message, 1024),
		events:   make(chan *pb.AgentEvent, 32),
		done:     make(chan struct{}),
	}, nil
}

// Close ends the connection.
func (c *Conn) Close() error {
	c.finish(nil)
	return c.cc.Close()
}

func (c *Conn) finish(err error) {
	c.closeOnce.Do(func() {
		if err != nil {
			c.err.Store(err)
		}
		close(c.done)
		close(c.messages)
		close(c.events)
	})
}

// Done is closed when the stream ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

// Err reports why it ended.
func (c *Conn) Err() error {
	if v := c.err.Load(); v != nil {
		return v.(error)
	}
	return nil
}

// Messages yields relayed grid messages, and is closed when the stream
// ends.
func (c *Conn) Messages() <-chan *Message { return c.messages }

// Events yields notices about the grid connection itself.
func (c *Conn) Events() <-chan *pb.AgentEvent { return c.events }

// Attach opens the packet stream against one of the server's agents and
// subscribes to the named messages.  "*" means everything; naming
// nothing means nothing is relayed until Subscribe says otherwise.
func (c *Conn) Attach(ctx context.Context, agentName string, subscribe ...string) (*pb.AgentInfo, error) {
	stream, err := c.grid.Stream(context.WithoutCancel(ctx))
	if err != nil {
		return nil, err
	}
	if err := stream.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Attach{
		Attach: &pb.Attach{Agent: agentName, Subscribe: subscribe},
	}}); err != nil {
		return nil, err
	}

	// The server answers an attach with who we are attached to.
	first, err := stream.Recv()
	if err != nil {
		return nil, err
	}
	att := first.GetAttached()
	if att == nil {
		return nil, fmt.Errorf("client: server answered attach with %T", first.Body)
	}

	c.mu.Lock()
	c.agent = agentName
	c.stream = stream
	c.info = att.Agent
	c.caps = make(map[string]bool, len(att.Agent.GetCaps()))
	for _, n := range att.Agent.GetCaps() {
		c.caps[n] = true
	}
	c.mu.Unlock()

	go c.recvLoop(stream)
	return att.Agent, nil
}

func (c *Conn) recvLoop(stream pb.Grid_StreamClient) {
	for {
		p, err := stream.Recv()
		if err != nil {
			c.finish(err)
			return
		}
		switch b := p.Body.(type) {
		case *pb.ServerPacket_Message:
			m := b.Message
			out := &Message{
				ID:       msg.ID(m.Id),
				Name:     m.Name,
				Sequence: m.Sequence,
				Flags:    m.Flags,
				Body:     m.Body,
			}
			if m.ReceivedAt != 0 {
				out.At = time.UnixMicro(m.ReceivedAt)
			}
			select {
			case c.messages <- out:
			default: // a client that stops reading loses messages
			}
		case *pb.ServerPacket_Event:
			select {
			case c.events <- b.Event:
			default:
			}
		}
	}
}

// Info is what the server said about the attached agent.
func (c *Conn) Info() *pb.AgentInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info
}

// Subscribe replaces the set of messages relayed to this stream.
func (c *Conn) Subscribe(names ...string) error {
	return c.sub(&pb.Subscribe{Set: nonNil(names)})
}

// Watch adds to it.
func (c *Conn) Watch(names ...string) error {
	return c.sub(&pb.Subscribe{Add: names})
}

// Unwatch removes from it.
func (c *Conn) Unwatch(names ...string) error {
	return c.sub(&pb.Subscribe{Remove: names})
}

func (c *Conn) sub(s *pb.Subscribe) error {
	c.mu.RLock()
	stream := c.stream
	c.mu.RUnlock()
	if stream == nil {
		return errors.New("client: not attached")
	}
	return stream.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Subscribe{Subscribe: s}})
}

// nonNil keeps a Set of nothing distinguishable from no Set at all, so
// Subscribe() with no names really does clear the subscription.
func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// Send puts a message on the circuit.  The server assigns the sequence
// number and handles reliability.
func (c *Conn) Send(ctx context.Context, m msg.Message, reliable bool) error {
	body, err := m.Encode()
	if err != nil {
		return err
	}
	return c.SendRaw(ctx, m.MsgInfo().ID, body, reliable)
}

// SendRaw is Send for a message number and body, without a type.
func (c *Conn) SendRaw(ctx context.Context, id msg.ID, body []byte, reliable bool) error {
	out := &pb.OutboundMessage{Id: uint32(id), Body: body, Reliable: reliable}

	c.mu.RLock()
	stream, name := c.stream, c.agent
	c.mu.RUnlock()

	// On a stream when there is one, so ordering with everything
	// else the client is sending is preserved.
	if stream != nil {
		return stream.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Message{Message: out}})
	}
	_, err := c.grid.Send(ctx, &pb.SendRequest{Agent: name, Message: out})
	return err
}

// ListAgents asks what the server hosts.
func (c *Conn) ListAgents(ctx context.Context) ([]*pb.AgentInfo, error) {
	resp, err := c.grid.ListAgents(ctx, &pb.ListAgentsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.Agents, nil
}

// Status asks about the attached agent.
func (c *Conn) Status(ctx context.Context) (*pb.StatusResponse, error) {
	c.mu.RLock()
	name := c.agent
	c.mu.RUnlock()
	return c.grid.Status(ctx, &pb.StatusRequest{Agent: name})
}

// ---------------------------------------------------------- capabilities

// A Conn is an agent.CapDoer, so anything written against capabilities
// runs unchanged on either side of the link.
//
// This is the return on making the server a gateway rather than a
// participant.  agent.FetchInventory takes a CapDoer: give it an Agent
// and it makes the HTTPS requests itself, give it a Conn and the server
// makes them on its behalf.  The inventory code does not know which,
// and the server does not know what inventory is.
var _ agent.CapDoer = (*Conn)(nil)

// HasCap reports whether the attached agent offered a capability.
func (c *Conn) HasCap(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.caps[name]
}

// Caps lists them.
func (c *Conn) Caps() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]string, 0, len(c.caps))
	for n := range c.caps {
		out = append(out, n)
	}
	return out
}

// DoCap makes a capability request through the server.
func (c *Conn) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	c.mu.RLock()
	name := c.agent
	c.mu.RUnlock()

	resp, err := c.grid.Cap(ctx, &pb.CapRequest{
		Agent:       name,
		Cap:         r.Cap,
		Method:      r.Method,
		Path:        r.Path,
		Body:        r.Body,
		ContentType: r.Type,
	}, grpc.MaxCallRecvMsgSize(64<<20))
	if err != nil {
		return nil, err
	}
	return &agent.CapResponse{Status: int(resp.Status), Body: resp.Body}, nil
}
