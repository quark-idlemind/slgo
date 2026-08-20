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
	// name is what this client authenticated as, so that a message
	// about what is attached can say which program rather than a count.
	name string
	out  chan *pb.ServerPacket

	mu    sync.RWMutex
	subs  map[msg.ID]bool
	names map[string]bool // event queue events, which have no number
	all   bool

	closed  atomic.Bool
	dropped atomic.Uint64
}

// lock answers a client asking for one, waiting for its turn if it
// asked to wait.
//
// Waiting happens in a goroutine of its own so that the stream keeps
// being read: a client that is queued still sends and receives, and
// blocking the reader here would stop relaying its grid traffic while
// it waited.
func (c *Client) lock(ctx context.Context, h *Hosted, req *pb.Lock) {
	name := req.GetName()
	if name == "" {
		c.send(&pb.ServerPacket{Body: &pb.ServerPacket_Locked{
			Locked: &pb.Locked{Held: false, Holder: "a lock needs a name"},
		}})
		return
	}

	if req.GetTry() {
		ok, holder := h.lockSet().acquire(name, c)
		c.send(&pb.ServerPacket{Body: &pb.ServerPacket_Locked{
			Locked: &pb.Locked{Name: name, Held: ok, Holder: holderName(holder)},
		}})
		return
	}

	ready := h.lockSet().queue(name, c)
	go func() {
		select {
		case <-ready:
			c.send(&pb.ServerPacket{Body: &pb.ServerPacket_Locked{
				Locked: &pb.Locked{Name: name, Held: true},
			}})
		case <-ctx.Done():
			// Gone before its turn came.  Leaving it in the queue
			// would hand the lock to nobody and stall everyone behind
			// it.
			h.lockSet().giveUp(name, c)
		}
	}()
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

	// Replace or a set with something in it.  The second half is for a
	// client older than the flag, which cannot say what it meant by an
	// empty one -- proto3 does not carry the difference -- and so gets
	// what it always got.
	if s.Replace || s.Set != nil {
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
	// An empty name takes the default, the same as every other method:
	// the resolution lives here rather than in the client, so that all
	// clients agree about which session "none named" means.
	h, err := s.lookup(att.Agent)
	if err != nil {
		return err
	}
	// Refuse a session that is down, rather than let a client talk to
	// something that answers out of what it last heard and sends into
	// nothing.  Being told why beats a session that looks fine and is
	// not.
	if why := h.Down(); why != "" {
		return status.Errorf(codes.FailedPrecondition,
			"%s is not connected (%s); it will not come back on its own", h.Name, why)
	}

	c := &Client{
		host:  h,
		name:  clientName(ctx),
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
		case *pb.ClientPacket_Lock:
			c.lock(ctx, h, b.Lock)
		case *pb.ClientPacket_Unlock:
			h.lockSet().giveUp(b.Unlock.GetName(), c)
		case *pb.ClientPacket_Attach:
			return status.Error(codes.InvalidArgument, "attach may only be the first frame")
		}
	}
}

// attachItemString leaves a not-worn object's item empty rather than
// spelling out a zero uuid, so that "is it worn" is a test on the
// field being set.
func attachItemString(id msg.UUID) string {
	if id.IsZero() {
		return ""
	}
	return id.String()
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
	// Oldest first, not alphabetical: the order IS information.  The
	// first is the default, and a client looking for an agent that can
	// satisfy it should try them in this order.
	out := &pb.ListAgentsResponse{}
	held := map[string]bool{}
	for _, h := range s.Ranked() {
		held[h.Name] = true
		out.Agents = append(out.Agents, h.info())
	}

	// Then what could be started but is not.  Listing these is the
	// difference between "I have never heard of qi" and "qi is down",
	// which are different problems with different answers.
	for _, name := range s.knownProfiles() {
		if held[name] {
			continue
		}
		out.Agents = append(out.Agents, s.notHosted(name))
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
		Viewer:          s.viewerFor(h.Name),
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
		Groups:       memberships(a.Groups()),
	}, nil
}

// memberships crosses the group list a client has no other way to hear.
//
// Nothing waits for it here.  The daemon is told the list of its own
// accord shortly after the handshake, so by the time a client attaches
// it is long since in; and a Presence call that blocked for it would
// stall every caller for the one case where the answer is genuinely
// nothing.  An empty list is handed over as empty and the client is the
// one that says what that means.
func memberships(gs []agent.Group) []*pb.GroupMembership {
	if len(gs) == 0 {
		return nil
	}
	out := make([]*pb.GroupMembership, 0, len(gs))
	for _, g := range gs {
		out = append(out, &pb.GroupMembership{
			Id:     g.ID.String(),
			Name:   g.Name,
			Powers: g.Powers,
		})
	}
	return out
}

func vec(v msg.Vector3) *pb.Vector3 {
	return &pb.Vector3{X: v.X, Y: v.Y, Z: v.Z}
}

func quat(q msg.Quaternion) *pb.Quaternion {
	return &pb.Quaternion{X: q.X, Y: q.Y, Z: q.Z, W: q.W()}
}

// shape crosses the eighteen packed numbers that give a prim its form.
//
// A prim that nothing has described yet packs to all zeros, and sending
// that would be indistinguishable from a real shape at the far end, so
// it is left out instead.
func shape(p msg.PrimShape) *pb.PrimShape {
	if p.IsZero() {
		return nil
	}
	return &pb.PrimShape{
		PathCurve: uint32(p.PathCurve), ProfileCurve: uint32(p.ProfileCurve),
		PathBegin: uint32(p.PathBegin), PathEnd: uint32(p.PathEnd),
		PathScaleX: uint32(p.PathScaleX), PathScaleY: uint32(p.PathScaleY),
		PathShearX: uint32(p.PathShearX), PathShearY: uint32(p.PathShearY),
		PathTwist: int32(p.PathTwist), PathTwistBegin: int32(p.PathTwistBegin),
		PathRadiusOffset: int32(p.PathRadiusOffset),
		PathTaperX:       int32(p.PathTaperX), PathTaperY: int32(p.PathTaperY),
		PathRevolutions: uint32(p.PathRevolutions), PathSkew: int32(p.PathSkew),
		ProfileBegin: uint32(p.ProfileBegin), ProfileEnd: uint32(p.ProfileEnd),
		ProfileHollow: uint32(p.ProfileHollow),
	}
}

// Objects returns what the region has told this session about itself.
func (s *Server) Objects(ctx context.Context, req *pb.ObjectsRequest) (*pb.ObjectsResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	all := h.Agent().Objects().All()

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
			AttachPoint:  uint32(o.AttachPoint),
			AttachItem:   attachItemString(o.AttachItem),
			Rotation:     quat(o.Rotation),
			Shape:        shape(o.Shape),
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

// Land answers what the session was told about the ground it is on.
//
// The overlay is the reason this exists: four packets on arrival and
// none afterwards, so a client that was not attached when the avatar
// arrived has no way to ask for it and this is the only place it can
// come from.  The parcel is here because the same answer should say
// what the session believes it is standing on, and because a client
// whose ask goes unanswered has something to fall back on.
func (s *Server) Land(ctx context.Context, req *pb.LandRequest) (*pb.LandInfo, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	a := h.Agent()
	out := &pb.LandInfo{}
	if p := a.Parcel(); p != nil {
		out.ParcelKnown = true
		out.ParcelName = p.Name
		out.ParcelLocalId = p.LocalID
	}
	o := a.Overlay()
	if q := o.Quarters(); q != 0 {
		out.Overlay = o.Squares()
		out.OverlayQuarters = uint32(q)
	}
	return out, nil
}

// Neighbours answers what circuits this session holds to the regions
// around it, and turns them on or off when asked to.
//
// The set is applied before the answer is read, so a client that turned
// them off is told what it has now rather than what it had: off comes
// back with nothing held, because turning them off drops the circuits
// rather than merely refusing the next offer.  Turning them ON comes
// back with nothing held too, and that is not a failure -- the offers
// are the simulator's to repeat, which it does for as long as they go
// untaken, so a circuit appears a second or two later without anything
// being asked for.
func (s *Server) Neighbours(ctx context.Context, req *pb.NeighboursRequest) (*pb.NeighboursResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	a := h.Agent()

	if req.Set != nil {
		a.SetNeighbours(req.GetSet())
	}

	held := a.Neighbours()
	out := &pb.NeighboursResponse{
		On:         a.NeighboursOn(),
		Neighbours: make([]*pb.NeighbourInfo, 0, len(held)),
	}
	for _, n := range held {
		out.Neighbours = append(out.Neighbours, &pb.NeighbourInfo{
			Handle:    n.Handle,
			Address:   n.Addr,
			Name:      n.Name,
			Handshook: n.Handshook,
			Heard:     n.Heard,
		})
	}
	return out, nil
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
	return &pb.FlushResponse{Forgotten: int32(h.Agent().Objects().Flush())}, nil
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

// Control sends one AgentUpdate carrying the client's control flags.
//
// The whole of what the server contributes is the rest of the update:
// the camera, its axes and the draw distance, which it owns and a client
// does not have.  The bits themselves are passed through unread -- this
// has no idea which of them is a sit -- and are not remembered, because
// they are edge triggered and a state kept here would be resent for ever
// by the presence loop.
func (s *Server) Control(ctx context.Context, req *pb.ControlRequest) (*pb.ControlResponse, error) {
	h, err := s.lookup(req.Agent)
	if err != nil {
		return nil, err
	}
	if err := h.Agent().Control(ctx, req.Flags); err != nil {
		return nil, status.Errorf(codes.Unavailable, "%v", err)
	}
	return &pb.ControlResponse{}, nil
}

// lookup resolves an agent name, defaulting to the session that has
// been hosted longest.  See Server.Default for why that one.
func (s *Server) lookup(name string) (*Hosted, error) {
	if name == "" {
		h, ok := s.Default()
		if !ok {
			return nil, status.Error(codes.NotFound, "this server holds no sessions")
		}
		return h, nil
	}
	h, ok := s.Agent(name)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no agent named %q", name)
	}
	return h, nil
}

// clientName is what this connection proved it was, or "" when the
// server runs without authentication.
func clientName(ctx context.Context) string {
	if c, ok := connFrom(ctx); ok {
		name, _ := c.ok()
		return name
	}
	return ""
}

// knownProfiles is what the daemon said could be started.
func (s *Server) knownProfiles() []string {
	s.mu.RLock()
	list := s.profiles
	s.mu.RUnlock()
	if list == nil {
		return nil
	}
	return list()
}

// notHosted describes an agent the server is not holding: one that has
// never been asked for, or one whose login failed.
func (s *Server) notHosted(name string) *pb.AgentInfo {
	info := &pb.AgentInfo{Name: name, State: pb.AgentInfo_CONFIGURED}

	s.starts.mu.Lock()
	f := s.starts.failures[name]
	s.starts.mu.Unlock()
	if f != nil {
		info.State = pb.AgentInfo_FAILED
		info.Detail = errText(f.err)
		if wait := time.Until(f.before); wait > 0 {
			info.Detail += " (not retrying for " + wait.Round(time.Second).String() + ")"
		}
	}
	return info
}

func (h *Hosted) info() *pb.AgentInfo {
	state, detail := h.state()
	a := h.Agent()
	if a == nil {
		return &pb.AgentInfo{Name: h.Name, State: state, Detail: detail}
	}
	return &pb.AgentInfo{
		Name:           h.Name,
		AgentId:        a.Account.AgentID.String(),
		SessionId:      a.Account.SessionID.String(),
		AvatarName:     a.Account.Name(),
		Region:         a.RegionName(),
		ChannelVersion: a.ChannelVersion(),
		InventoryRoot:  a.Account.InventoryRoot.String(),
		Caps:           a.Caps().Names(),
		// Connected and State answer different questions and are both
		// kept: Connected is whether a circuit is up this instant,
		// State is what may be done about it.  A client too old to know
		// about State still gets a right answer from Connected.
		Connected: state == pb.AgentInfo_HOSTED,
		State:     state,
		Detail:    detail,
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
