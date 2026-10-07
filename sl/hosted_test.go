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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

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

	presence   *pb.PresenceResponse
	neighbours *pb.NeighboursResponse

	objects []*pb.ObjectInfo
	region  *pb.RegionInfo

	// confirmed is every ConfirmLinkOrder asked of the daemon, and
	// confirm what it answers: a status error when it is one.
	confirmed []*pb.ConfirmLinkOrderRequest
	confirm   func(*pb.ConfirmLinkOrderRequest) (*pb.ConfirmLinkOrderResponse, error)
	friends   []*pb.Friend
	agents    []*pb.AgentInfo
	cap       *pb.CapResponse
	noted     chan *pb.NoteFriendRequest

	// fail, when set, is what every unary call answers with, which is
	// how the error half of each translation is reached.
	fail error

	// attachFail refuses the attach, which is a daemon that is not
	// holding the session that was asked for.
	attachFail error

	// offers is the record the Attached frame carries, nil for a
	// daemon that keeps none; handled is every Handled asked of it,
	// and handle what it answers with.
	offers  *pb.OfferRecord
	handled chan *pb.HandledRequest
	handle  func(*pb.HandledRequest) *pb.HandledResponse

	// walk is what a Move streams, the last of it the end, and walked
	// the request it was asked; faced is the Face asked for and face
	// what it answers.  See walk_test.go.
	walk   []*pb.MoveEvent
	walked chan *pb.MoveRequest

	// regionDetails is what RegionDetails answers, under regionMu.
	regionMu      sync.Mutex
	regionDetails *pb.RegionDetailsResponse

	// posture is what Posture answers; nil answers Unimplemented, as a
	// daemon from before it existed would.
	posture *pb.PostureResponse
	faced   chan *pb.FaceRequest
	face    func(*pb.FaceRequest) (*pb.FaceResponse, error)

	// other is what Status answers for any name but info's, the empty
	// one included: the session a daemon's default has moved to once
	// info's has gone.  Nil answers that nothing by that name is held.
	other *pb.AgentInfo
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
		handled:  make(chan *pb.HandledRequest, 8),
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
		Attached: &pb.Attached{Agent: d.info, Offers: d.offers},
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

// Neighbours answers the way the server does, which is what makes this
// worth having as a fake at all: the set is applied before the list is
// read, and turning them off drops the circuits rather than only
// refusing the next offer.
func (d *fakeDaemon) RegionDetails(context.Context, *pb.RegionDetailsRequest) (*pb.RegionDetailsResponse, error) {
	d.regionMu.Lock()
	defer d.regionMu.Unlock()
	if d.regionDetails == nil {
		return &pb.RegionDetailsResponse{}, nil
	}
	return d.regionDetails, nil
}

func (d *fakeDaemon) Neighbours(_ context.Context, r *pb.NeighboursRequest) (*pb.NeighboursResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	if d.neighbours == nil {
		d.neighbours = &pb.NeighboursResponse{}
	}
	if r.Set != nil {
		d.neighbours.On = r.GetSet()
		if !r.GetSet() {
			d.neighbours.Neighbours = nil
		}
	}
	return d.neighbours, nil
}

func (d *fakeDaemon) Flush(context.Context, *pb.FlushRequest) (*pb.FlushResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.FlushResponse{Forgotten: int32(len(d.objects))}, nil
}

func (d *fakeDaemon) ConfirmLinkOrder(_ context.Context, r *pb.ConfirmLinkOrderRequest) (*pb.ConfirmLinkOrderResponse, error) {
	d.confirmed = append(d.confirmed, r)
	if d.confirm != nil {
		return d.confirm(r)
	}
	return d.UnimplementedGridServer.ConfirmLinkOrder(context.Background(), r)
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

func (d *fakeDaemon) Handled(_ context.Context, r *pb.HandledRequest) (*pb.HandledResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	select {
	case d.handled <- r:
	default:
	}
	if d.handle == nil {
		return &pb.HandledResponse{Claimed: true}, nil
	}
	return d.handle(r), nil
}

func (d *fakeDaemon) Cap(context.Context, *pb.CapRequest) (*pb.CapResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return d.cap, nil
}

// Status answers for the session it is asked about, by name, as the
// daemon does.
func (d *fakeDaemon) Status(_ context.Context, r *pb.StatusRequest) (*pb.StatusResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	if r.GetAgent() == d.info.GetName() {
		return &pb.StatusResponse{Agent: d.info}, nil
	}
	if d.other == nil {
		return nil, errors.New("no agent by that name")
	}
	return &pb.StatusResponse{Agent: d.other}, nil
}

// TestAttachingSaysWhichSessionItGot: an empty name is passed through
// rather than resolved here, because the daemon picks -- the session it
// has held longest that has not stopped -- and the Attached frame says
// which.  Every client
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

// TestRefreshAsksAboutTheSessionItIsAttachedTo: Refresh is a Status
// call, and it used to be asked by the name the attach was asked with.
// An empty one took the daemon's default then and again at every
// refresh, so once the default moved -- the first avatar logged out
// while another was still up -- a refresh would have taken the other
// avatar's identity, and the session gone on sending as somebody it was
// not attached to.
func TestRefreshAsksAboutTheSessionItIsAttachedTo(t *testing.T) {
	t.Parallel()
	d, conn := dialFakeDaemon(t)
	d.other = &pb.AgentInfo{
		Name:       "helper",
		AgentId:    "c2de7e57-7e57-c0de-6355-a0471ecd3e72",
		SessionId:  "3c907e57-7e57-c0de-e29b-3fe3bad0d0f1",
		AvatarName: "Helper Resident",
	}

	h, err := AttachConn(context.Background(), conn, "")
	if err != nil {
		t.Fatalf("AttachConn: %v", err)
	}
	t.Cleanup(func() { h.Close() })

	info, err := h.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if info.Name != "quark" || info.AgentID != testAgentID || info.SessionID != testSessionID {
		t.Errorf("Refresh answered with %s (%s, session %s), not the session attached to",
			info.Name, info.AgentID, info.SessionID)
	}
	if got := h.Info().AvatarName; got != "Quark Idlemind" {
		t.Errorf("the session now believes it is %q", got)
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

// TestThePresenceCarriesTheControlsScriptsHold: the two masks cross as
// they are, each in its own field.
func TestThePresenceCarriesTheControlsScriptsHold(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.presence = &pb.PresenceResponse{Region: "Test Region", ScriptControlsTaken: 0x3, ScriptControlsPassedOn: 0x10}
	p, err := h.Presence(context.Background(), 0)
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.ScriptControlsTaken != 0x3 || p.ScriptControlsPassedOn != 0x10 {
		t.Errorf("controls taken %#x, passed on %#x", p.ScriptControlsTaken, p.ScriptControlsPassedOn)
	}
}

// TestThePresenceCarriesTheHealthAndWhetherItIsKnown: a health of zero
// that was said and one that was not come out differently.
func TestThePresenceCarriesTheHealthAndWhetherItIsKnown(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.presence = &pb.PresenceResponse{Region: "Test Region", Health: 73.5, HealthKnown: true}
	p, err := h.Presence(context.Background(), 0)
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.Health != 73.5 || !p.HealthKnown {
		t.Errorf("health %v known %v", p.Health, p.HealthKnown)
	}
	d.presence = &pb.PresenceResponse{Region: "Test Region", HealthKnown: true}
	if p, _ = h.Presence(context.Background(), 0); p.Health != 0 || !p.HealthKnown {
		t.Errorf("a health of zero came out as %v known %v", p.Health, p.HealthKnown)
	}
	d.presence = &pb.PresenceResponse{Region: "Test Region"}
	if p, _ = h.Presence(context.Background(), 0); p.HealthKnown {
		t.Error("a health nobody said came out known")
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

// TestTheMembershipListCrossesWithoutItsUnusableRows: the list of
// groups an avatar has joined is knowledge the daemon has and a client
// cannot ask for, so this translation is the only way it reaches
// anything that could act on it.
//
// A row whose key will not parse is dropped rather than kept as the
// zero uuid.  Zero is how "acting as no group" is spelled everywhere
// else here, so a zero in the list would read as a group that could be
// activated -- and activating it is a request to leave whatever group
// the avatar is in.
func TestTheMembershipListCrossesWithoutItsUnusableRows(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.presence = &pb.PresenceResponse{
		Region: "Test Region",
		Groups: []*pb.GroupMembership{
			{Id: theOther.String(), Name: "Pelmar Reach Builders", Powers: 0x101},
			{Id: "not a uuid", Name: "Nowhere"},
			{Id: msg.UUID{}.String(), Name: "No Group At All"},
		},
	}

	p, err := h.Presence(context.Background(), 0)
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if len(p.Groups) != 1 {
		t.Fatalf("Groups = %+v, want only the row with a usable key", p.Groups)
	}
	if p.Groups[0].ID != theOther || p.Groups[0].Name != "Pelmar Reach Builders" ||
		p.Groups[0].Powers != 0x101 {
		t.Errorf("Groups[0] = %+v", p.Groups[0])
	}

	// No groups at all is an empty list rather than a list of nothing:
	// it means "not told yet" as much as "belongs to none", and a
	// caller reads the length to find out.
	d.presence = &pb.PresenceResponse{Region: "Test Region"}
	if p, err = h.Presence(context.Background(), 0); err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if len(p.Groups) != 0 {
		t.Errorf("Groups = %+v for a daemon that sent none", p.Groups)
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
			TextureEntry: []byte{1, 2, 3}, TextureAnim: []byte{4, 5}, Text: "floating",
			AttachPoint: 6, AttachItem: theChild.String(),
			Click: 0, ClickKnown: true,
			SculptKind: 5, SculptId: theChild.String(), LinkNumber: 3, LinkKnown: true,
			Light:     &pb.PrimLight{Red: 255, Green: 128, Intensity: 0.5, Radius: 10, Cutoff: 1, Falloff: 0.75},
			Projector: &pb.PrimProjector{Texture: theChild.String(), Fov: 1.5, Focus: -0.25, Ambiance: 0.125},
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
	if len(o.TextureEntry) != 3 || len(o.TextureAnim) != 2 || o.Text != "floating" {
		t.Errorf("appearance came out as %+v", o)
	}
	if o.AttachPoint != 6 || o.AttachItem != theChild {
		t.Errorf("worn at %d from %s", o.AttachPoint, o.AttachItem)
	}
	if o.Click != 0 || !o.ClickKnown {
		t.Errorf("click %d known %v, want touch, known", o.Click, o.ClickKnown)
	}
	if o.Sculpt != (msg.SculptMark{Kind: msg.SculptMesh, ID: theChild}) {
		t.Errorf("sculpt %+v, want a mesh of %s", o.Sculpt, theChild)
	}
	if o.Light == nil || *o.Light != (msg.Light{Colour: [3]uint8{255, 128, 0}, Intensity: 0.5, Radius: 10, Cutoff: 1, Falloff: 0.75}) {
		t.Errorf("light %+v", o.Light)
	}
	if o.Projector == nil || *o.Projector != (msg.LightImage{Texture: theChild, FOV: 1.5, Focus: -0.25, Ambiance: 0.125}) {
		t.Errorf("projector %+v", o.Projector)
	}
	if o.LinkNumber != 3 {
		t.Errorf("link number %d, want 3", o.LinkNumber)
	}
	if !o.LinkKnown {
		t.Error("link known did not come through")
	}

	d.fail = errors.New("no such agent")
	if _, err := h.Objects(context.Background(), "", ""); err == nil {
		t.Error("Objects answered from a daemon that refused")
	}
}

// TestHowObjectsWereDescribedCrossesTheWire: a client that asks gets each
// object's descriptions by id, and a daemon older than the record, which
// sends none, gives an empty list for each object, not an error.
// Why: doc/objects.md#how-an-object-was-described
func TestHowObjectsWereDescribedCrossesTheWire(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	at := time.Unix(1_700_000_000, 123_000_000)
	d.objects = []*pb.ObjectInfo{
		{Id: thePrim.String(), Local: 77, How: []*pb.ObjectDescription{
			{Kind: 1, Parent: 3, Seq: 12, Message: 5, Block: 1, Blocks: 4, Refill: true, Listed: true, AtUnixNano: at.UnixNano()},
			{Kind: 3, Seq: 20, Count: 9, AtUnixNano: at.UnixNano()},
		}},
		{Id: theChild.String(), Local: 78},
	}
	got, err := h.Descriptions(context.Background())
	if err != nil {
		t.Fatalf("Descriptions: %v", err)
	}
	want := []Description{
		{Kind: DescFull, Parent: 3, Seq: 12, Message: 5, Block: 1, Blocks: 4, Refill: true, Listed: true, At: at},
		{Kind: DescTerse, Seq: 20, Count: 9, At: at},
	}
	if !reflect.DeepEqual(got[thePrim], want) {
		t.Errorf("descriptions %+v, want %+v", got[thePrim], want)
	}
	if ds, ok := got[theChild]; !ok || len(ds) != 0 {
		t.Errorf("an object with none: %v, %v", ds, ok)
	}
	d.fail = errors.New("no such agent")
	if _, err := h.Descriptions(context.Background()); err == nil {
		t.Error("Descriptions answered from a daemon that refused")
	}
}

// A backend that does not keep the record says so.
func TestABackendWithoutTheRecordSaysSo(t *testing.T) {
	t.Parallel()
	w, _ := newFakeSession(t)
	if _, err := w.Descriptions(context.Background()); !errors.Is(err, ErrNoDescriptions) {
		t.Errorf("err = %v, want ErrNoDescriptions", err)
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

// TestAConfirmedOrderCrossesTheWireBothWays: the keys go as strings and
// the daemon's answer comes back as it was said; a set the daemon holds
// confirmed says so in what Objects returns.
// Why: doc/objects.md#confirmed-by-the-objects-own-script
func TestAConfirmedOrderCrossesTheWireBothWays(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.confirm = func(r *pb.ConfirmLinkOrderRequest) (*pb.ConfirmLinkOrderResponse, error) {
		return &pb.ConfirmLinkOrderResponse{At: 1_700_000_000_250, Corrected: true, Moved: []uint32{3, 4}}, nil
	}
	got, err := h.ConfirmLinkOrder(context.Background(), thePrim, []msg.UUID{thePrim, theChild, theOther})
	if err != nil {
		t.Fatalf("ConfirmLinkOrder: %v", err)
	}
	if !got.Corrected || !reflect.DeepEqual(got.Moved, []int{3, 4}) || !got.At.Equal(time.UnixMilli(1_700_000_000_250)) {
		t.Errorf("confirmation %+v", got)
	}
	if len(d.confirmed) != 1 || !reflect.DeepEqual(d.confirmed[0].Keys, []string{thePrim.String(), theChild.String(), theOther.String()}) {
		t.Errorf("the daemon was asked %+v", d.confirmed)
	}

	d.objects = []*pb.ObjectInfo{
		{Id: thePrim.String(), Local: 77, LinkConfirmed: true, LinkConfirmedAt: 1_700_000_000_250, LinkCorrected: true, LinkMoved: []uint32{3}},
		{Id: theChild.String(), Local: 78},
	}
	seen, err := h.Objects(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c := seen[0].LinkConfirmed; c == nil || !c.Corrected || !reflect.DeepEqual(c.Moved, []int{3}) {
		t.Errorf("a confirmed set came as %+v", c)
	}
	if seen[1].LinkConfirmed != nil {
		t.Errorf("an unconfirmed one came as %+v", seen[1].LinkConfirmed)
	}
}

// TestADaemonsRefusalsComeBackAsTheErrorsOfThePackage: a daemon older
// than the call, a set the keys do not name, and a root it does not hold
// each have their own error.
func TestADaemonsRefusalsComeBackAsTheErrorsOfThePackage(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	keys := []msg.UUID{thePrim}
	if _, err := h.ConfirmLinkOrder(context.Background(), thePrim, keys); !errors.Is(err, ErrNotSupported) {
		t.Errorf("an older daemon: %v", err)
	}
	for code, want := range map[codes.Code]error{
		codes.InvalidArgument:    ErrLinkSetDiffers,
		codes.FailedPrecondition: ErrNotHere,
	} {
		d.confirm = func(*pb.ConfirmLinkOrderRequest) (*pb.ConfirmLinkOrderResponse, error) {
			return nil, status.Error(code, "the reason")
		}
		if _, err := h.ConfirmLinkOrder(context.Background(), thePrim, keys); !errors.Is(err, want) || !strings.Contains(err.Error(), "the reason") {
			t.Errorf("%v: %v, want %v and the daemon's words", code, err, want)
		}
	}
}
