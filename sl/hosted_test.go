package sl

// The backend that talks to slgod, with a daemon standing in.
//
// Everything in hosted.go is a translation: the daemon answers in
// protobuf because that is what crosses the wire, and this turns it into
// the package's own types so that nothing above ever sees a generated
// struct.  A translation is exactly the kind of code that looks right
// and is wrong -- a field read off the neighbouring one, a uuid parsed
// out of the wrong string -- and none of it can be reached without
// something at the far end.
//
// So there is one here: a real gRPC server, in this process, on
// loopback.  client.Dial takes dial options for precisely this, and
// passing them skips the shared-secret handshake, which needs a file on
// disk that a test has no business reading.  What it cannot stand in for
// is a grid: the daemon here has no simulator behind it, so what is
// checked is the conversion in each direction and not what a real
// session would have answered.

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// fakeDaemon is a slgod with nothing behind it.
//
// It answers from fields, records what was asked, and relays what a test
// puts on its stream -- which is all a client can tell about a daemon
// anyway, since every answer it gives came from a simulator it is not
// obliged to have.
type fakeDaemon struct {
	pb.UnimplementedGridServer

	info *pb.AgentInfo

	// attached is the name each Stream was opened with, and relay is
	// what to push down it once it is open.
	attached chan string
	relay    chan *pb.ServerPacket

	// sent is everything the client put on the stream, and locked is
	// what to answer a Lock with.
	sent   chan *pb.ClientPacket
	locked func(*pb.Lock) *pb.Locked

	presence *pb.PresenceResponse
	objects  []*pb.ObjectInfo
	region   *pb.RegionInfo
	friends  []*pb.Friend
	agents   []*pb.AgentInfo
	cap      *pb.CapResponse
	noted    chan *pb.NoteFriendRequest

	// fail, when set, is what every unary call answers with, which is
	// how the error half of each translation is reached.
	fail error

	// attachFail refuses the attach, which is a daemon that is not
	// holding the session that was asked for.
	attachFail error
}

// newFakeDaemon starts one on loopback and attaches to it.
func newFakeDaemon(t *testing.T) (*Hosted, *fakeDaemon) {
	t.Helper()
	d, conn := dialFakeDaemon(t)
	h, err := AttachConn(context.Background(), conn, "quark")
	if err != nil {
		t.Fatalf("AttachConn: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h, d
}

// dialFakeDaemon starts one and connects, without attaching, for the
// tests that are about the attach itself.
func dialFakeDaemon(t *testing.T) (*fakeDaemon, *client.Conn) {
	t.Helper()
	d := &fakeDaemon{
		info: &pb.AgentInfo{
			Name: "quark", AgentId: testAgentID.String(), SessionId: testSessionID.String(),
			AvatarName: "Quark Idlemind", Region: "Test Region",
			InventoryRoot: testInvRoot.String(), ChannelVersion: "slgo test 1.0",
			Caps: []string{"SimulatorFeatures", "ViewerAsset"},
		},
		attached: make(chan string, 4),
		relay:    make(chan *pb.ServerPacket, 8),
		sent:     make(chan *pb.ClientPacket, 32),
		noted:    make(chan *pb.NoteFriendRequest, 4),
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening on loopback: %v", err)
	}
	srv := grpc.NewServer()
	pb.RegisterGridServer(srv, d)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return d, conn
}

func (d *fakeDaemon) Stream(s grpc.BidiStreamingServer[pb.ClientPacket, pb.ServerPacket]) error {
	first, err := s.Recv()
	if err != nil {
		return err
	}
	att := first.GetAttach()
	if att == nil {
		return errors.New("the first packet was not an attach")
	}
	if d.attachFail != nil {
		return d.attachFail
	}
	d.attached <- att.Agent
	if err := s.Send(&pb.ServerPacket{Body: &pb.ServerPacket_Attached{
		Attached: &pb.Attached{Agent: d.info},
	}}); err != nil {
		return err
	}

	// Everything the client says is recorded, and a lock is answered
	// because whoever asked for one is waiting on the stream for it.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			p, err := s.Recv()
			if err != nil {
				return
			}
			select {
			case d.sent <- p:
			default:
			}
			if l := p.GetLock(); l != nil && d.locked != nil {
				s.Send(&pb.ServerPacket{Body: &pb.ServerPacket_Locked{Locked: d.locked(l)}})
			}
		}
	}()

	for {
		select {
		case p := <-d.relay:
			if err := s.Send(p); err != nil {
				return err
			}
		case <-done:
			return nil
		case <-s.Context().Done():
			return s.Context().Err()
		}
	}
}

func (d *fakeDaemon) ListAgents(context.Context, *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.ListAgentsResponse{Agents: d.agents}, nil
}

func (d *fakeDaemon) Presence(_ context.Context, r *pb.PresenceRequest) (*pb.PresenceResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	out := d.presence
	if out != nil && r.DrawDistance > 0 {
		out.DrawDistance = r.DrawDistance
	}
	return out, nil
}

func (d *fakeDaemon) Objects(_ context.Context, r *pb.ObjectsRequest) (*pb.ObjectsResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.ObjectsResponse{Objects: d.objects}, nil
}

func (d *fakeDaemon) Region(context.Context, *pb.RegionRequest) (*pb.RegionInfo, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return d.region, nil
}

func (d *fakeDaemon) Flush(context.Context, *pb.FlushRequest) (*pb.FlushResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.FlushResponse{Forgotten: int32(len(d.objects))}, nil
}

func (d *fakeDaemon) Friends(context.Context, *pb.FriendsRequest) (*pb.FriendsResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.FriendsResponse{Friends: d.friends}, nil
}

func (d *fakeDaemon) NoteFriend(_ context.Context, r *pb.NoteFriendRequest) (*pb.NoteFriendResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	d.noted <- r
	return &pb.NoteFriendResponse{}, nil
}

func (d *fakeDaemon) Cap(context.Context, *pb.CapRequest) (*pb.CapResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return d.cap, nil
}

// TestAttachingSaysWhichSessionItGot: an empty name is passed through
// rather than resolved here, because the daemon picks -- the session it
// has held longest -- and the Attached frame says which.  Every client
// has to agree about what "none named" means, and one too old to know
// the rule must not be able to disagree with one that does.
func TestAttachingSaysWhichSessionItGot(t *testing.T) {
	t.Parallel()
	d, conn := dialFakeDaemon(t)

	h, err := AttachConn(context.Background(), conn, "")
	if err != nil {
		t.Fatalf("AttachConn: %v", err)
	}
	if got := <-d.attached; got != "" {
		t.Errorf("the daemon was asked for %q, want the choice left to it", got)
	}
	if h.Info().Name != "quark" {
		t.Errorf("the attached session is %q", h.Info().Name)
	}
}

// TestTheAgentInfoIsTranslatedFieldByField: this is the one place a
// protobuf becomes a package type, and every id in it arrives as a
// string -- so a field read off its neighbour would give a session that
// believes it is somebody else and is refused by the simulator for a
// reason it never explains.
func TestTheAgentInfoIsTranslatedFieldByField(t *testing.T) {
	t.Parallel()
	h, _ := newFakeDaemon(t)

	i := h.Info()
	if i.Name != "quark" || i.AvatarName != "Quark Idlemind" || i.Region != "Test Region" {
		t.Errorf("Info = %+v", i)
	}
	if i.AgentID != testAgentID || i.SessionID != testSessionID {
		t.Errorf("the session is %s/%s", i.AgentID, i.SessionID)
	}
	if i.InventoryRoot != testInvRoot || i.Channel != "slgo test 1.0" {
		t.Errorf("Info = %+v", i)
	}
	if !i.HasCap("ViewerAsset") || i.HasCap("LSLSyntax") {
		t.Errorf("the capabilities came out as %v", i.Caps)
	}
	// HasCap on the backend asks the connection rather than the info,
	// since a session can be told about capabilities after it attached.
	if !h.HasCap("ViewerAsset") || h.HasCap("LSLSyntax") {
		t.Error("HasCap disagrees with the capability list")
	}
	if h.Conn() == nil {
		t.Error("Conn answered with nothing")
	}
}

// TestAnAttachThatWasRefusedSaysWhichSessionItWanted: with several
// avatars hosted, "cannot attach" is useless on its own -- and a client
// that named none is in a different position from one that named the
// wrong one.
func TestAnAttachThatWasRefusedSaysWhichSessionItWanted(t *testing.T) {
	t.Parallel()

	t.Run("a session that was named", func(t *testing.T) {
		d, conn := dialFakeDaemon(t)
		d.attachFail = errors.New("no such agent")
		_, err := AttachConn(context.Background(), conn, "somebody else")
		if err == nil || !strings.Contains(err.Error(), `"somebody else"`) {
			t.Errorf("AttachConn = %v, want it to name what it asked for", err)
		}
	})

	t.Run("no session named", func(t *testing.T) {
		d, conn := dialFakeDaemon(t)
		d.attachFail = errors.New("nothing hosted")
		_, err := AttachConn(context.Background(), conn, "")
		if err == nil || !strings.Contains(err.Error(), "default session") {
			t.Errorf("AttachConn = %v, want it to say it asked for the default", err)
		}
	})
}

// TestAttachNeedsADaemonToReach: the address is in the message because a
// client usually has several to choose from, and "connection refused" on
// its own does not say which one it tried.
func TestAttachNeedsADaemonToReach(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := Attach(ctx, "127.0.0.1:1", "quark")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("Attach = %v, want it to name the address", err)
	}
}

// TestSessionsListsWhatTheDaemonHolds: hosted only, because a direct
// session is the only one there is -- and the names are what a client
// picks between.
func TestSessionsListsWhatTheDaemonHolds(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.agents = []*pb.AgentInfo{{Name: "quark"}, {Name: "somebody else"}}

	got, err := h.Sessions(context.Background())
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(got) != 2 || got[0] != "quark" || got[1] != "somebody else" {
		t.Errorf("Sessions = %v", got)
	}

	d.fail = errors.New("the daemon is going down")
	if _, err := h.Sessions(context.Background()); err == nil {
		t.Error("Sessions listed what a daemon that refused is holding")
	}
}

// TestThePresenceIsTranslatedVectorByVector: three vectors that all look
// alike, and a camera read as a position puts the interest list in the
// wrong place -- which shows up much later as a region that describes
// nothing.
func TestThePresenceIsTranslatedVectorByVector(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.presence = &pb.PresenceResponse{
		Position:     &pb.Vector3{X: 128, Y: 129, Z: 25},
		LookAt:       &pb.Vector3{X: 1},
		Camera:       &pb.Vector3{X: 128, Y: 129, Z: 26},
		DrawDistance: 128,
		RegionHandle: 1099511628032,
		Region:       "Test Region",
		ActiveGroup:  theOther.String(),
	}

	p, err := h.Presence(context.Background(), 0)
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.Position != (msg.Vector3{X: 128, Y: 129, Z: 25}) || p.LookAt != (msg.Vector3{X: 1}) {
		t.Errorf("Presence = %+v", p)
	}
	if p.Camera != (msg.Vector3{X: 128, Y: 129, Z: 26}) {
		t.Errorf("the camera came out as %v", p.Camera)
	}
	if p.RegionHandle != 1099511628032 || p.Region != "Test Region" {
		t.Errorf("Presence = %+v", p)
	}
	if p.ActiveGroup != theOther {
		t.Errorf("the active group came out as %s", p.ActiveGroup)
	}

	// A draw distance above zero sets it, and the answer says what it
	// ended up as.
	if p, err = h.Presence(context.Background(), 256); err != nil || p.DrawDistance != 256 {
		t.Errorf("Presence(256) = %v, %v", p.DrawDistance, err)
	}

	// A vector the daemon left out is no vector rather than a nil
	// dereference: protobuf sends nothing for a message field it was
	// not given.
	d.presence = &pb.PresenceResponse{Region: "Test Region"}
	if p, err = h.Presence(context.Background(), 0); err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.Position != (msg.Vector3{}) || !p.ActiveGroup.IsZero() {
		t.Errorf("an answer with no vectors came out as %+v", p)
	}

	d.fail = errors.New("no such agent")
	if _, err := h.Presence(context.Background(), 0); err == nil {
		t.Error("Presence answered from a daemon that refused")
	}
}

// TestObjectsWithoutAReadableIdAreDropped: every id crosses the wire as
// a string, and an object whose own id will not parse is not an object
// -- keeping it would put something in the region list that no call
// taking an id could ever be used on.
func TestObjectsWithoutAReadableIdAreDropped(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.objects = []*pb.ObjectInfo{
		{
			Id: thePrim.String(), Local: 77, Parent: 3, Pcode: 9,
			Name: "workbench", Owner: testAgentID.String(),
			Position: &pb.Vector3{X: 128}, Scale: &pb.Vector3{X: 0.5, Y: 0.5, Z: 0.5},
			TextureEntry: []byte{1, 2, 3}, Text: "floating",
			AttachPoint: 6, AttachItem: theChild.String(),
		},
		{Id: "not a uuid", Local: 78},
	}

	got, err := h.Objects(context.Background(), "", "")
	if err != nil {
		t.Fatalf("Objects: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Objects = %+v, want the unreadable one dropped", got)
	}
	o := got[0]
	if o.ID != thePrim || o.Local != 77 || o.Name != "workbench" {
		t.Errorf("the object came out as %+v", o.Object)
	}
	if o.Owner != testAgentID || o.Parent != 3 || o.PCode != 9 {
		t.Errorf("the object came out as %+v", o)
	}
	if o.Position != (msg.Vector3{X: 128}) || o.Scale.X != 0.5 {
		t.Errorf("the object is at %v scaled %v", o.Position, o.Scale)
	}
	if len(o.TextureEntry) != 3 || o.Text != "floating" {
		t.Errorf("appearance came out as %+v", o)
	}
	if o.AttachPoint != 6 || o.AttachItem != theChild {
		t.Errorf("worn at %d from %s", o.AttachPoint, o.AttachItem)
	}

	d.fail = errors.New("no such agent")
	if _, err := h.Objects(context.Background(), "", ""); err == nil {
		t.Error("Objects answered from a daemon that refused")
	}
}

// TestTheRegionCarriesWhetherItHasBeenHeardOfAtAll: the handshake
// happens once, before any client is listening, so a client that
// attached afterwards is asking the daemon what it heard -- and a daemon
// that has not heard it yet must say so rather than answer with an empty
// region.
func TestTheRegionCarriesWhetherItHasBeenHeardOfAtAll(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.region = &pb.RegionInfo{
		Id: testRegionID.String(), Handle: 1099511628032, Name: "Test Region",
		Flags: 7, FlagsExtended: 9, Access: 13, Owner: testAgentID.String(),
		EstateManager: true, WaterHeight: 20,
		ProductName: "Estate / Full Region", ProductSku: "023",
		ColoName: "Dallas", CpuClass: 5, CpuRatio: 8, Protocols: 3,
		Known: true,
	}

	r, known, err := h.Region(context.Background())
	if err != nil || !known {
		t.Fatalf("Region = %v, %v", known, err)
	}
	if r.ID != testRegionID || r.Name != "Test Region" || r.Handle != 1099511628032 {
		t.Errorf("Region = %+v", r)
	}
	if r.Flags != 7 || r.Extended != 9 || r.Access != 13 || r.Owner != testAgentID {
		t.Errorf("Region = %+v", r)
	}
	if !r.EstateManager || r.WaterHeight != 20 || r.ProductSKU != "023" {
		t.Errorf("Region = %+v", r)
	}
	if r.ColoName != "Dallas" || r.CPUClass != 5 || r.CPURatio != 8 || r.Protocols != 3 {
		t.Errorf("Region = %+v", r)
	}

	d.region = &pb.RegionInfo{Known: false}
	if _, known, err = h.Region(context.Background()); err != nil || known {
		t.Errorf("Region said known %v for a handshake that has not arrived", known)
	}

	d.fail = errors.New("no such agent")
	if _, _, err := h.Region(context.Background()); err == nil {
		t.Error("Region answered from a daemon that refused")
	}
}

// TestFriendsWithoutAReadableIdAreDropped: the friend list arrives only
// in the login response and can never be asked for again, so the daemon
// is the only thing that has it -- and an entry whose id will not parse
// is somebody nothing can be sent to.
func TestFriendsWithoutAReadableIdAreDropped(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.friends = []*pb.Friend{
		{Id: theOther.String(), Online: true, RightsGiven: 1, RightsHas: 4},
		{Id: "not a uuid"},
	}

	got, err := h.Friends(context.Background())
	if err != nil {
		t.Fatalf("Friends: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Friends = %+v, want the unreadable one dropped", got)
	}
	if got[0].ID != theOther || !got[0].Online {
		t.Errorf("the friend came out as %+v", got[0])
	}
	if got[0].RightsGiven != 1 || got[0].RightsHas != 4 {
		t.Errorf("the rights came out as %+v", got[0])
	}

	// Accepting an offer is the one thing the grid never reports, so
	// the client has to tell the daemon what it watched happen.
	if err := h.NoteFriend(context.Background(), theOther, true); err != nil {
		t.Fatalf("NoteFriend: %v", err)
	}
	n := <-d.noted
	if n.Id != theOther.String() || !n.Online {
		t.Errorf("noted %+v", n)
	}

	d.fail = errors.New("no such agent")
	if _, err := h.Friends(context.Background()); err == nil {
		t.Error("Friends answered from a daemon that refused")
	}
	if err := h.NoteFriend(context.Background(), theOther, true); err == nil {
		t.Error("NoteFriend recorded a friendship with a daemon that refused")
	}
}

// TestAMessageGoesOutOnTheStreamAndComesBackOnIt: the stream is what
// keeps a client's sends in order with everything else it is doing, and
// it is the same stream the relay arrives on.
func TestAMessageGoesOutOnTheStreamAndComesBackOnIt(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)

	say := &msg.ChatFromViewer{}
	say.ChatData.Message = append([]byte("hello"), 0)
	if err := h.Send(context.Background(), say, true); err != nil {
		t.Fatalf("Send: %v", err)
	}
	p := <-d.sent
	out := p.GetMessage()
	if out == nil || !out.Reliable {
		t.Fatalf("the daemon was sent %+v", p.Body)
	}
	if msg.ID(out.Id) != msg.IDOf(say) {
		t.Errorf("sent message %d", out.Id)
	}

	// And the other way: a message the daemon relays turns up on
	// Messages, which is what the session's reader consumes.
	body, err := say.Encode()
	if err != nil {
		t.Fatal(err)
	}
	d.relay <- &pb.ServerPacket{Body: &pb.ServerPacket_Message{Message: &pb.InboundMessage{
		Id: uint32(msg.IDOf(say)), Name: "ChatFromViewer", Body: body,
		Sequence: 42, ReceivedAt: time.Now().UnixMicro(),
	}}}
	select {
	case m := <-h.Messages():
		if m.Name != "ChatFromViewer" || m.Sequence != 42 || len(m.Body) != len(body) {
			t.Errorf("the relayed message came out as %+v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing came back on the relay")
	}
}

// TestLocksGoOverTheStreamSoThatDyingGivesThemBack: a lock is only worth
// having if it is returned when its holder dies, and the stream is what
// the daemon already watches for exactly that -- which is why this is
// not an rpc of its own.
func TestLocksGoOverTheStreamSoThatDyingGivesThemBack(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.locked = func(l *pb.Lock) *pb.Locked {
		if l.Try && l.Name == "taken" {
			return &pb.Locked{Name: l.Name, Held: false, Holder: "somebody else"}
		}
		return &pb.Locked{Name: l.Name, Held: true}
	}

	if err := h.Lock(context.Background(), "the workbench"); err != nil {
		t.Fatalf("Lock: %v", err)
	}
	got, by, err := h.TryLock(context.Background(), "the workbench")
	if err != nil || !got || by != "" {
		t.Errorf("TryLock = %v, %q, %v", got, by, err)
	}
	// Somebody else holding it is an answer rather than a failure: it
	// is what lets a caller move on to another of a pool.
	if got, by, err = h.TryLock(context.Background(), "taken"); err != nil || got {
		t.Errorf("TryLock took a lock somebody else holds: %v, %v", got, err)
	}
	if by != "somebody else" {
		t.Errorf("TryLock said it was held by %q", by)
	}
	if err := h.Unlock("the workbench"); err != nil {
		t.Errorf("Unlock: %v", err)
	}
}

// TestFlushAndTheCapabilityGoStraightThrough: neither has anything to
// translate, which is the point -- the daemon holds the capability URLs
// and does the http, so inventory and asset upload stay out of it.
func TestFlushAndTheCapabilityGoStraightThrough(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.objects = []*pb.ObjectInfo{{Id: thePrim.String()}, {Id: theOther.String()}}
	d.cap = &pb.CapResponse{Status: 200, Body: []byte("some llsd")}

	if n, err := h.Flush(context.Background()); err != nil || n != 2 {
		t.Errorf("Flush = %d, %v", n, err)
	}
	resp, err := h.DoCap(context.Background(), agent.CapRequest{Cap: "SimulatorFeatures"})
	if err != nil {
		t.Fatalf("DoCap: %v", err)
	}
	if resp.Status != 200 || string(resp.Body) != "some llsd" {
		t.Errorf("DoCap = %+v", resp)
	}

	d.fail = errors.New("no such agent")
	if _, err := h.Flush(context.Background()); err == nil {
		t.Error("Flush reported on a daemon that refused")
	}
	if _, err := h.DoCap(context.Background(), agent.CapRequest{Cap: "x"}); err == nil {
		t.Error("DoCap reported on a daemon that refused")
	}
}

// TestClosingAHostedSessionLeavesTheAvatarLoggedIn: that is the whole
// reason for hosting one -- the session outlives the program talking to
// it -- so Close hangs up and nothing more, and what it ends is the
// stream.
func TestClosingAHostedSessionLeavesTheAvatarLoggedIn(t *testing.T) {
	t.Parallel()
	h, _ := newFakeDaemon(t)

	select {
	case <-h.Done():
		t.Fatal("a fresh attachment says it has ended")
	default:
	}
	if err := h.Err(); err != nil {
		t.Errorf("Err = %v before anything went wrong", err)
	}

	if err := h.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	select {
	case <-h.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("a closed attachment never said it had ended")
	}
	// The stream ending is not a failure, so Err says so or says why.
	if err := h.Err(); err != nil && !errors.Is(err, io.EOF) {
		t.Logf("Err after Close = %v", err)
	}
}

// TestAgentNameIsAskedRatherThanTested: an avatar named by the
// environment is still a named avatar, so a caller that would move on to
// another when this one is busy has to ask this rather than test the
// string it was given.
func TestAgentNameIsAskedRatherThanTested(t *testing.T) {
	t.Setenv(EnvAgent, "  from the environment  ")
	if got := AgentName("named"); got != "named" {
		t.Errorf("AgentName = %q, want what was named to win", got)
	}
	if got := AgentName(""); got != "from the environment" {
		t.Errorf("AgentName = %q, want the environment's answer, trimmed", got)
	}
	t.Setenv(EnvAgent, "")
	if got := AgentName(""); got != "" {
		t.Errorf("AgentName = %q, want the choice left to the daemon", got)
	}
}
