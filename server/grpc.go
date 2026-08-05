package server

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
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

	mu    sync.RWMutex
	subs  map[msg.ID]bool
	names map[string]bool // event queue events, which have no number
	all   bool

	closed  atomic.Bool
	dropped atomic.Uint64
}

func (c *Client) wants(id msg.ID) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.all || c.subs[id]
}

// wantsEvent gates event queue events, which are named rather than
// numbered: some match a template message and some have no UDP
// equivalent at all, so the name is all there is to go on.
func (c *Client) wantsEvent(name string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.all || c.names[name]
}

// setSubs applies a subscription change.  A name this build's template
// does not have is ignored rather than refused: the client may know
// something we do not, and the number it wants will still relay.
func (c *Client) setSubs(s *pb.Subscribe) []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	if s.Set != nil {
		c.subs = map[msg.ID]bool{}
		c.names = map[string]bool{}
		c.all = false
	}
	if c.names == nil {
		c.names = map[string]bool{}
	}
	apply := func(names []string, on bool) {
		for _, n := range names {
			if n == "*" {
				c.all = on
				continue
			}
			// A name goes in both maps.  It may be a
			// template message, an event queue event, or --
			// like ParcelProperties -- a message that used
			// to arrive on the circuit and now arrives on
			// the queue.
			if on {
				c.names[n] = true
			} else {
				delete(c.names, n)
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

	out := make([]string, 0, len(c.names))
	if c.all {
		out = append(out, "*")
	}
	for n := range c.names {
		out = append(out, n)
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

// relayEvent hands an event queue event to every client that asked for
// it by name.  As with a circuit message, the server does not look
// inside: the body crosses as the LLSD bytes it arrived as.
func (h *Hosted) relayEvent(name string, body []byte) {
	h.mu.RLock()
	var want []*Client
	for c := range h.clients {
		if c.wantsEvent(name) {
			want = append(want, c)
		}
	}
	h.mu.RUnlock()
	if len(want) == 0 {
		return
	}

	sp := &pb.ServerPacket{Body: &pb.ServerPacket_Event{Event: &pb.InboundEvent{
		Message:    name,
		Body:       body,
		ReceivedAt: time.Now().UnixMicro(),
	}}}
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
		host:  h,
		out:   make(chan *pb.ServerPacket, streamDepth),
		subs:  map[msg.ID]bool{},
		names: map[string]bool{},
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

	// The stream deliberately does not watch the agent.  A session
	// that ends is re-established under it, and the client keeps
	// its stream and its subscriptions across that; it learns what
	// happened from the events Hosted.notify sends.
	//
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
		err = h.Agent().Send.SendReliable(ctx, raw)
	} else {
		err = h.Agent().Send.Send(ctx, raw)
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
	a := h.Agent()
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

// Presence answers where the avatar is and optionally changes how far
// it is asked to see.
//
// The draw distance goes out on every AgentUpdate, which the server
// sends because it has to keep being sent.  A client changes it here
// and the next update carries it.
func (s *Server) Presence(ctx context.Context, req *pb.PresenceRequest) (*pb.PresenceResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	a := h.Agent()

	if req.DrawDistance > 0 {
		l := a.Look()
		l.Far = req.DrawDistance
		a.SetLook(l)
	}

	l := a.Look()
	return &pb.PresenceResponse{
		Position:     vec(a.Position()),
		LookAt:       vec(l.At),
		Camera:       vec(l.Center),
		DrawDistance: l.Far,
		RegionHandle: a.RegionHandle(),
		Region:       a.RegionName(),
		ActiveGroup:  a.ActiveGroup().String(),
	}, nil
}

func vec(v msg.Vector3) *pb.Vector3 {
	return &pb.Vector3{X: v.X, Y: v.Y, Z: v.Z}
}

// Objects returns what the region has told this session about itself.
func (s *Server) Objects(ctx context.Context, req *pb.ObjectsRequest) (*pb.ObjectsResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	all := h.Agent().Objects.All()

	out := &pb.ObjectsResponse{Known: int32(len(all))}
	for _, o := range all {
		if req.Named != "" && o.Name != req.Named {
			continue
		}
		if req.Id != "" && o.ID.String() != req.Id {
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

// Region answers what the simulator said about itself.
func (s *Server) Region(ctx context.Context, req *pb.RegionRequest) (*pb.RegionInfo, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	r, known := h.Agent().Region()
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

// Friends is the friend list and who is logged in.
//
// Held here because the grid says both once and to whoever was there:
// the list only in the login response, online status in a burst that
// arrives before any client can have attached.
func (s *Server) Friends(ctx context.Context, req *pb.FriendsRequest) (*pb.FriendsResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	fs := h.Agent().Friends()
	out := &pb.FriendsResponse{Friends: make([]*pb.Friend, 0, len(fs))}
	for _, f := range fs {
		out.Friends = append(out.Friends, &pb.Friend{
			Id:          f.ID.String(),
			Online:      f.Online,
			RightsGiven: f.RightsGiven,
			RightsHas:   f.RightsHas,
		})
	}
	return out, nil
}

// NoteFriend records a friendship the client saw formed.
//
// The one thing the grid never reports is your own acceptance of an
// offer, so this is the client handing over a fact only it was in a
// position to know.  Nothing is decoded here; an id is an id.
func (s *Server) NoteFriend(ctx context.Context, req *pb.NoteFriendRequest) (*pb.NoteFriendResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	id, err := msg.ParseUUID(req.Id)
	if err != nil {
		return nil, fmt.Errorf("server: %q is not a uuid: %w", req.Id, err)
	}
	h.Agent().NoteFriend(id, req.Online)
	return &pb.NoteFriendResponse{}, nil
}

// Flush empties the object cache.
func (s *Server) Flush(ctx context.Context, req *pb.FlushRequest) (*pb.FlushResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	return &pb.FlushResponse{Forgotten: int32(h.Agent().Objects.Flush())}, nil
}

func (s *Server) Cap(ctx context.Context, req *pb.CapRequest) (*pb.CapResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	resp, err := h.Agent().DoCap(ctx, agent.CapRequest{
		Cap:    req.Cap,
		Method: req.Method,
		Path:   req.Path,
		Body:   req.Body,
		Type:   req.ContentType,
		URL:    req.Url,
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
	a := h.Agent()
	if a == nil {
		return &pb.AgentInfo{Name: h.Name}
	}
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
