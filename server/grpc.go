package server

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"slgo/agent"
	"slgo/msg"
	pb "slgo/proto/slgov1"
)

// Server implements the Grid service.
var _ pb.GridServer = (*Server)(nil)

// streamDepth is how far a client may fall behind before frames are
// dropped.  Dropping is deliberate: a slow client must not be able to
// stall the circuit that feeds it, and a lost relay costs nothing on
// the grid because the acknowledgement has already gone.
const streamDepth = 1024

// Client is one attached stream, the thing the C client calls a
// session_t.
type Client struct {
	host *Hosted
	out  chan *pb.ServerPacket

	mu   sync.RWMutex
	subs map[msg.ID]bool
	all  bool

	closed  atomic.Bool
	dropped atomic.Uint64
}

func (c *Client) wants(id msg.ID) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.all || c.subs[id]
}

// setSubs applies a subscription change.  A name this build's template
// does not have is ignored rather than refused: the client may know
// something we do not, and the number it wants will still relay.
func (c *Client) setSubs(s *pb.Subscribe) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if s.Set != nil {
		c.subs = map[msg.ID]bool{}
		c.all = false
	}
	apply := func(names []string, on bool) {
		for _, n := range names {
			if n == "*" {
				c.all = on
				continue
			}
			if info := msg.LookupName(n); info != nil {
				if on {
					c.subs[info.ID] = true
				} else {
					delete(c.subs, info.ID)
				}
			}
		}
	}
	apply(s.Set, true)
	apply(s.Add, true)
	apply(s.Remove, false)

	out := make([]string, 0, len(c.subs))
	if c.all {
		out = append(out, "*")
	}
	for id := range c.subs {
		if info := msg.Lookup(id); info != nil {
			out = append(out, info.Name)
		}
	}
	return out
}

// send queues a packet, dropping it if the client is behind.
func (c *Client) send(p *pb.ServerPacket) {
	if c.closed.Load() {
		return
	}
	select {
	case c.out <- p:
	default:
		c.dropped.Add(1)
	}
}

// relay hands a packet to every client that asked for it.
//
// It runs on the agent's dispatch goroutine and does no work beyond
// choosing recipients and handing over.  Acknowledgement has already
// happened by this point and does not depend on any of it, which is
// what makes "no client attached" mean "acknowledged and dropped".
func (h *Hosted) relay(p *msg.Packet) {
	if p.Message == nil && p.Body == nil {
		return // acknowledgements only
	}

	h.mu.RLock()
	if len(h.clients) == 0 {
		h.mu.RUnlock()
		return
	}
	var want []*Client
	for c := range h.clients {
		if c.wants(p.ID) {
			want = append(want, c)
		}
	}
	h.mu.RUnlock()
	if len(want) == 0 {
		return
	}

	body := p.Body
	if body == nil && p.Message != nil {
		b, err := p.Message.Encode()
		if err != nil {
			return
		}
		body = b
	}

	in := &pb.InboundMessage{
		Id:         uint32(p.ID),
		Body:       body,
		Sequence:   p.Header.Sequence,
		Flags:      uint32(p.Header.Flags),
		ReceivedAt: p.At.UnixMicro(),
	}
	if info := msg.Lookup(p.ID); info != nil {
		in.Name = info.Name
	}
	sp := &pb.ServerPacket{Body: &pb.ServerPacket_Message{Message: in}}

	for _, c := range want {
		c.send(sp)
	}
	h.relayed.Add(1)
}

// ------------------------------------------------------------- stream

// Stream is the packet channel.  The first frame must be an Attach.
func (s *Server) Stream(stream pb.Grid_StreamServer) error {
	ctx := stream.Context()

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	att := first.GetAttach()
	if att == nil {
		return status.Error(codes.InvalidArgument, "the first frame must be an attach")
	}
	h, ok := s.Agent(att.Agent)
	if !ok {
		return status.Errorf(codes.NotFound, "no agent named %q", att.Agent)
	}

	c := &Client{
		host: h,
		out:  make(chan *pb.ServerPacket, streamDepth),
		subs: map[msg.ID]bool{},
	}
	if len(att.Subscribe) > 0 {
		c.setSubs(&pb.Subscribe{Set: att.Subscribe})
	}

	h.attach(c)
	s.clients.Add(1)
	defer func() {
		c.closed.Store(true)
		h.detach(c)
		s.clients.Add(-1)
		h.dropped.Add(c.dropped.Load())
	}()

	if err := stream.Send(&pb.ServerPacket{
		Body: &pb.ServerPacket_Attached{Attached: &pb.Attached{Agent: h.info()}},
	}); err != nil {
		return err
	}

	// One goroutine reads what the client sends; this one writes.
	errc := make(chan error, 1)
	go func() { errc <- s.streamRecv(ctx, stream, c, h) }()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-errc:
			if err == io.EOF {
				return nil
			}
			return err
		case <-h.Agent.Done():
			_ = stream.Send(&pb.ServerPacket{Body: &pb.ServerPacket_Event{
				Event: &pb.AgentEvent{
					Kind:   pb.AgentEvent_DISCONNECTED,
					Detail: errText(h.Agent.Err()),
				},
			}})
			return nil
		case p := <-c.out:
			if err := stream.Send(p); err != nil {
				return err
			}
		}
	}
}

func (s *Server) streamRecv(ctx context.Context, stream pb.Grid_StreamServer, c *Client, h *Hosted) error {
	for {
		in, err := stream.Recv()
		if err != nil {
			return err
		}
		switch b := in.Body.(type) {
		case *pb.ClientPacket_Subscribe:
			c.setSubs(b.Subscribe)
		case *pb.ClientPacket_Message:
			if err := sendMessage(ctx, h, b.Message); err != nil {
				return err
			}
		case *pb.ClientPacket_Attach:
			return status.Error(codes.InvalidArgument, "attach may only be the first frame")
		}
	}
}

// sendMessage puts a client's message on the circuit.  The client
// supplies the number and the body; the sequence number and
// reliability are the server's, because they are circuit state.
func sendMessage(ctx context.Context, h *Hosted, m *pb.OutboundMessage) error {
	if m == nil {
		return status.Error(codes.InvalidArgument, "empty message")
	}
	id := msg.ID(m.Id)
	if m.Name != "" {
		info := msg.LookupName(m.Name)
		if info == nil {
			return status.Errorf(codes.InvalidArgument, "no message named %q", m.Name)
		}
		id = info.ID
	}
	if id == 0 {
		return status.Error(codes.InvalidArgument, "message needs an id or a name")
	}

	raw := msg.NewRaw(id, m.Body)
	var err error
	if m.Reliable {
		err = h.Agent.Send.SendReliable(ctx, raw)
	} else {
		err = h.Agent.Send.Send(ctx, raw)
	}
	if err != nil {
		return status.Errorf(codes.Unavailable, "send: %v", err)
	}
	return nil
}

// ------------------------------------------------------------- unary

func (s *Server) ListAgents(ctx context.Context, _ *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	out := &pb.ListAgentsResponse{}
	for _, n := range s.Names() {
		if h, ok := s.Agent(n); ok {
			out.Agents = append(out.Agents, h.info())
		}
	}
	return out, nil
}

func (s *Server) Status(ctx context.Context, req *pb.StatusRequest) (*pb.StatusResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	a := h.Agent
	rs := a.Recv.Stats()
	ss := a.Send.Stats()
	ds := a.Disp.Stats()

	out := &pb.StatusResponse{
		Agent:           h.info(),
		PacketsIn:       rs.Packets,
		PacketsOut:      ss.Sent,
		BytesIn:         rs.Bytes,
		BytesOut:        ss.Bytes,
		Resent:          ss.Resent,
		Abandoned:       ss.Abandoned,
		Duplicates:      ds.Duplicates,
		Undecodable:     rs.Failed,
		UnknownMessages: rs.Unknown,
		Clients:         int32(h.ClientCount()),
		Unhandled:       map[string]uint64{},
	}
	for id, n := range a.Disp.Unhandled() {
		out.Unhandled[id.String()] = n
	}
	return out, nil
}

func (s *Server) Cap(ctx context.Context, req *pb.CapRequest) (*pb.CapResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	resp, err := h.Agent.DoCap(ctx, agent.CapRequest{
		Cap:    req.Cap,
		Method: req.Method,
		Path:   req.Path,
		Body:   req.Body,
		Type:   req.ContentType,
	})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}
	return &pb.CapResponse{Status: int32(resp.Status), Body: resp.Body}, nil
}

func (s *Server) Send(ctx context.Context, req *pb.SendRequest) (*pb.SendResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	if err := sendMessage(ctx, h, req.Message); err != nil {
		return nil, err
	}
	return &pb.SendResponse{}, nil
}

// lookup resolves an agent name, defaulting to the only one when the
// server hosts exactly one.
func (s *Server) lookup(name string) (*Hosted, error) {
	if name == "" {
		if names := s.Names(); len(names) == 1 {
			name = names[0]
		} else {
			return nil, status.Error(codes.InvalidArgument,
				"this server hosts several agents, so one must be named")
		}
	}
	h, ok := s.Agent(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no agent named %q", name)
	}
	return h, nil
}

func (h *Hosted) info() *pb.AgentInfo {
	a := h.Agent
	connected := true
	select {
	case <-a.Done():
		connected = false
	default:
	}
	return &pb.AgentInfo{
		Name:           h.Name,
		AgentId:        a.Account.AgentID.String(),
		SessionId:      a.Account.SessionID.String(),
		AvatarName:     a.Account.Name(),
		Region:         a.RegionName(),
		ChannelVersion: a.ChannelVersion(),
		InventoryRoot:  a.Account.InventoryRoot.String(),
		Caps:           a.Caps.Names(),
		Connected:      connected,
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
