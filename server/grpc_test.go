package server

// The wire contract, method by method.
//
// server_test.go is about the relay -- what reaches a client and what
// does not -- and gets there through a real client on a real stream.
// This file is about the answers themselves, so it calls the methods
// directly: every one of them is a translation of grid state into a
// response, and what is worth pinning is the translation and the
// refusals, not the plumbing that carries them.
//
// The refusals matter more than they look.  Every method resolves an
// agent name first, and "no agent named qi" versus "this server holds
// no sessions" is the difference between a typo and a daemon that never
// came up -- so each has to be reachable and each has to say which.

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

var aFriend = msg.MustParseUUID("54867e57-7e57-c0de-a176-4adcefbc98bb")

// TestTheUnaryCallsAnswerFromWhatTheSessionWasTold walks the calls that
// exist only to hand over state the server was given once and a client
// has no other way to reach: the friend list arrives in the login
// response, online status in a burst before any client could have
// attached, and the region description once at handshake.
func TestTheUnaryCallsAnswerFromWhatTheSessionWasTold(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()
	h, _ := r.srv.Agent("example")

	// Presence, and the one thing it can change: how far the simulator
	// is asked to describe.
	p, err := r.srv.Presence(ctx, &pb.PresenceRequest{DrawDistance: 96})
	if err != nil {
		t.Fatal(err)
	}
	if p.GetDrawDistance() != 96 {
		t.Errorf("draw distance = %v, want the 96 that was just set", p.GetDrawDistance())
	}
	if p.GetPosition().GetX() != 1 || p.GetPosition().GetZ() != 3 {
		t.Errorf("position = %v, want where the simulator said the avatar arrived", p.GetPosition())
	}
	// Zero leaves it alone rather than meaning "see nothing".
	again, err := r.srv.Presence(ctx, &pb.PresenceRequest{Agent: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if again.GetDrawDistance() != 96 {
		t.Errorf("draw distance = %v after a request that set none", again.GetDrawDistance())
	}

	region, err := r.srv.Region(ctx, &pb.RegionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !region.GetKnown() || region.GetName() != "Testville" {
		t.Errorf("region = %q known=%v", region.GetName(), region.GetKnown())
	}

	// A friendship the client saw formed is the one thing the grid
	// never reports, so it is handed over rather than observed.
	if _, err := r.srv.NoteFriend(ctx, &pb.NoteFriendRequest{Id: aFriend.String(), Online: true}); err != nil {
		t.Fatal(err)
	}
	fs, err := r.srv.Friends(ctx, &pb.FriendsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(fs.GetFriends()) != 1 || fs.GetFriends()[0].GetId() != aFriend.String() ||
		!fs.GetFriends()[0].GetOnline() {
		t.Errorf("friends = %v", fs.GetFriends())
	}

	// Nothing is decoded on the way through, so an id that is not one
	// is refused here rather than stored and puzzled over later.
	if _, err := r.srv.NoteFriend(ctx, &pb.NoteFriendRequest{Id: "not a uuid"}); err == nil {
		t.Error("a friend id that is not a uuid was accepted")
	}

	// The object cache, which fills from updates nobody asked for.
	prim := msg.MustParseUUID("14ff7e57-7e57-c0de-3d5f-1eb0c71e7610")
	upd := &msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{ID: 4242, FullID: prim, PCode: 9}}}
	r.sim.send(upd, 0)
	waitFor(t, 5*time.Second, "the object update to be recorded", func() bool {
		return h.Agent().Objects().Count() > 0
	})

	all, err := r.srv.Objects(ctx, &pb.ObjectsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if all.GetKnown() != 1 || len(all.GetObjects()) != 1 || all.GetObjects()[0].GetLocal() != 4242 {
		t.Fatalf("objects = %v", all.GetObjects())
	}
	// An object nobody is wearing has no item, so "is it worn" is a
	// test on the field being set rather than on a zero uuid.
	if got := all.GetObjects()[0].GetAttachItem(); got != "" {
		t.Errorf("attach item = %q for an object that is not worn", got)
	}

	// Both filters, each of which can exclude everything.
	if got, err := r.srv.Objects(ctx, &pb.ObjectsRequest{Named: "nothing has this name"}); err != nil {
		t.Fatal(err)
	} else if len(got.GetObjects()) != 0 || got.GetKnown() != 1 {
		t.Errorf("a name filter that matches nothing returned %d of %d", len(got.GetObjects()), got.GetKnown())
	}
	if got, err := r.srv.Objects(ctx, &pb.ObjectsRequest{Id: aFriend.String()}); err != nil {
		t.Fatal(err)
	} else if len(got.GetObjects()) != 0 {
		t.Errorf("an id filter matched something else: %v", got.GetObjects())
	}
	if got, err := r.srv.Objects(ctx, &pb.ObjectsRequest{Id: prim.String()}); err != nil {
		t.Fatal(err)
	} else if len(got.GetObjects()) != 1 {
		t.Errorf("an id filter did not find the object it named: %v", got.GetObjects())
	}

	// And emptying it, which is the client saying "I have moved, forget
	// what you were told".
	flushed, err := r.srv.Flush(ctx, &pb.FlushRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if flushed.GetForgotten() != 1 {
		t.Errorf("flush forgot %d objects, want 1", flushed.GetForgotten())
	}

	// A message the session has no handler for is counted rather than
	// ignored, because a message nobody registered for is how a
	// protocol change announces itself.
	chat := &msg.ChatFromSimulator{}
	chat.ChatData.Message = []byte("nobody is listening\x00")
	r.sim.send(chat, 0)
	var st *pb.StatusResponse
	waitFor(t, 5*time.Second, "the unhandled message to be counted", func() bool {
		st, err = r.srv.Status(ctx, &pb.StatusRequest{})
		return err == nil && len(st.GetUnhandled()) > 0
	})
	if st.GetUnhandled()["ChatFromSimulator"] == 0 {
		t.Errorf("unhandled = %v, want ChatFromSimulator among them", st.GetUnhandled())
	}
	// The object update above carried an empty placement blob, which
	// nothing reads and which is counted all the same.
	if got := st.GetPlacementWidths(); len(got) != 1 || got[0] != 1 {
		t.Errorf("placement widths = %v, want the one empty blob", got)
	}

	// And Send refuses what sendMessage refuses, rather than reporting
	// that nothing was put on the wire as success.
	if _, err := r.srv.Send(ctx, &pb.SendRequest{}); err == nil {
		t.Error("a send with no message was reported as sent")
	}
}

// TestARelayEncodesWhatItWasNotGivenTheBytesOf: a session kept without
// KeepBody hands over the decoded message and no bytes, and the relay
// has to produce the bytes itself -- a client is promised the body of
// everything it subscribed to, whatever the session chose to keep.
func TestARelayEncodesWhatItWasNotGivenTheBytesOf(t *testing.T) {
	t.Parallel()

	h := &Hosted{Name: "example", clients: map[*Client]bool{}}
	c := &Client{out: make(chan *pb.ServerPacket, 4), subs: map[msg.ID]bool{}, names: map[string]bool{}}
	c.setSubs(&pb.Subscribe{Set: []string{"ChatFromSimulator"}})
	h.attach(c)

	chat := &msg.ChatFromSimulator{}
	chat.ChatData.Message = []byte("encoded on the way out\x00")
	h.relay(&msg.Packet{ID: msg.IDOf(chat), Message: chat})

	select {
	case p := <-c.out:
		in := p.GetMessage()
		if in.GetName() != "ChatFromSimulator" || len(in.GetBody()) == 0 {
			t.Errorf("relayed %+v, want an encoded ChatFromSimulator", in)
		}
	default:
		t.Fatal("a message with no bytes kept was not relayed at all")
	}

	// One that will not encode is dropped rather than relayed empty: a
	// client cannot tell "no body" from "a body of nothing", and the
	// acknowledgement has already gone, so there is nothing to fail.
	tooLong := &msg.ChatFromSimulator{}
	tooLong.ChatData.FromName = make([]byte, 300) // longer than its length prefix
	h.relay(&msg.Packet{ID: msg.IDOf(tooLong), Message: tooLong})
	select {
	case p := <-c.out:
		t.Errorf("a message that would not encode was relayed as %v", p)
	default:
	}

	// Acknowledgements carry neither, and stop here.
	h.relay(&msg.Packet{})
	if n := h.relayed.Load(); n != 1 {
		t.Errorf("%d relays counted, want the one that had a body", n)
	}
}

// TestAnItemIsNamedOnlyWhenSomethingIsWorn: an empty item is how a
// client tells an attachment from an ordinary prim, so a zero uuid must
// never be spelled out.
func TestAnItemIsNamedOnlyWhenSomethingIsWorn(t *testing.T) {
	t.Parallel()

	if got := attachItemString(msg.UUID{}); got != "" {
		t.Errorf("attachItemString(zero) = %q, want empty", got)
	}
	if got := attachItemString(aFriend); got != aFriend.String() {
		t.Errorf("attachItemString(%v) = %q", aFriend, got)
	}
}

// TestEveryCallSaysWhichAgentItCouldNotFind: naming one that is not
// hosted and naming none on a server holding nothing are different
// faults, and a client that cannot tell them apart cannot say whether
// the daemon is up or the name is a typo.
// TestAttachmentsAreWhatTheSimulatorLastSaid: the list at the end of
// AvatarAppearance crosses as it came, for this avatar by default and
// for any other on request, with a pending entry kept as an empty id
// rather than turned into the zero uuid.
func TestAttachmentsAreWhatTheSimulatorLastSaid(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()
	h, _ := r.srv.Agent("example")
	me := h.Agent().Account.AgentID
	someoneElse := msg.MustParseUUID("644a7e57-7e57-c0de-8347-80c1bf3eeddb")
	body := msg.MustParseUUID("0c8c7e57-7e57-c0de-78ff-aad5be0fe09a")

	// Nothing heard is "not known", not "wearing nothing".
	got, err := r.srv.Attachments(ctx, &pb.AttachmentsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetKnown() {
		t.Errorf("attachments known before any appearance arrived: %v", got)
	}

	before := time.Now()
	m := &msg.AvatarAppearance{}
	m.Sender.ID = me
	m.AppearanceData = []msg.AvatarAppearance_AppearanceData{{AppearanceVersion: 1, CofVersion: 117}}
	m.AttachmentBlock = []msg.AvatarAppearance_AttachmentBlock{
		{ID: body, AttachmentPoint: 40},
		{AttachmentPoint: 2}, // pending
	}
	r.sim.send(m, 0)
	other := &msg.AvatarAppearance{}
	other.Sender.ID = someoneElse
	r.sim.send(other, 0)
	waitFor(t, 5*time.Second, "both appearances to be kept", func() bool {
		held, _ := h.Agent().Appearances().Stats()
		return held == 2
	})

	got, err = r.srv.Attachments(ctx, &pb.AttachmentsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetKnown() || got.GetCofVersion() != 117 {
		t.Errorf("known=%v cof=%d, want the version it was baked from", got.GetKnown(), got.GetCofVersion())
	}
	if at := time.UnixMicro(got.GetReceivedAt()); at.Before(before.Add(-time.Second)) || at.After(time.Now()) {
		t.Errorf("received at %v, want about now", at)
	}
	a := got.GetAttachments()
	if len(a) != 2 || a[0].GetObjectId() != body.String() || a[0].GetPoint() != 40 ||
		a[1].GetObjectId() != "" || a[1].GetPoint() != 2 {
		t.Errorf("attachments = %v", a)
	}

	theirs, err := r.srv.Attachments(ctx, &pb.AttachmentsRequest{Avatar: someoneElse.String()})
	if err != nil {
		t.Fatal(err)
	}
	if !theirs.GetKnown() || len(theirs.GetAttachments()) != 0 {
		t.Errorf("someone else's = %v, want known and wearing nothing", theirs)
	}
	if _, err := r.srv.Attachments(ctx, &pb.AttachmentsRequest{Avatar: "nobody"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("an avatar that is not an id: %v, want InvalidArgument", err)
	}
}

func TestEveryCallSaysWhichAgentItCouldNotFind(t *testing.T) {
	t.Parallel()

	empty := New()
	ctx := context.Background()

	calls := map[string]func(agentName string) error{
		"Status": func(n string) error {
			_, err := empty.Status(ctx, &pb.StatusRequest{Agent: n})
			return err
		},
		"Presence": func(n string) error {
			_, err := empty.Presence(ctx, &pb.PresenceRequest{Agent: n})
			return err
		},
		"Objects": func(n string) error {
			_, err := empty.Objects(ctx, &pb.ObjectsRequest{Agent: n})
			return err
		},
		"Region": func(n string) error {
			_, err := empty.Region(ctx, &pb.RegionRequest{Agent: n})
			return err
		},
		"Attachments": func(n string) error {
			_, err := empty.Attachments(ctx, &pb.AttachmentsRequest{Agent: n})
			return err
		},
		"Neighbours": func(n string) error {
			_, err := empty.Neighbours(ctx, &pb.NeighboursRequest{Agent: n})
			return err
		},
		"Friends": func(n string) error {
			_, err := empty.Friends(ctx, &pb.FriendsRequest{Agent: n})
			return err
		},
		"NoteFriend": func(n string) error {
			_, err := empty.NoteFriend(ctx, &pb.NoteFriendRequest{Agent: n, Id: aFriend.String()})
			return err
		},
		"Flush": func(n string) error {
			_, err := empty.Flush(ctx, &pb.FlushRequest{Agent: n})
			return err
		},
		"Cap": func(n string) error {
			_, err := empty.Cap(ctx, &pb.CapRequest{Agent: n, Cap: "Anything"})
			return err
		},
		"Send": func(n string) error {
			_, err := empty.Send(ctx, &pb.SendRequest{Agent: n})
			return err
		},
		"Control": func(n string) error {
			_, err := empty.Control(ctx, &pb.ControlRequest{Agent: n})
			return err
		},
		// The agent is resolved before the endpoint is looked for, so
		// that a name nobody holds is answered the same way here as
		// everywhere else rather than as "no viewer logins".
		"ViewerCredential": func(n string) error {
			_, err := empty.ViewerCredential(ctx, &pb.ViewerCredentialRequest{Agent: n})
			return err
		},
	}

	for name, call := range calls {
		err := call("qi")
		if status.Code(err) != codes.NotFound || !strings.Contains(errText(err), `"qi"`) {
			t.Errorf("%s with an unknown name: %v; want NotFound naming qi", name, err)
		}
		err = call("")
		if status.Code(err) != codes.NotFound || !strings.Contains(errText(err), "no sessions") {
			t.Errorf("%s with no name on an empty server: %v; want NotFound saying so", name, err)
		}
	}
}

// TestSendingNeedsSomethingToSend: the client supplies the number and
// the body, so each of the ways it can supply neither has to be
// refused before anything reaches the circuit.
func TestSendingNeedsSomethingToSend(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()
	h, _ := r.srv.Agent("example")

	for _, tc := range []struct {
		what string
		m    *pb.OutboundMessage
		says string
	}{
		{"nothing at all", nil, "empty message"},
		{"neither an id nor a name", &pb.OutboundMessage{}, "needs an id or a name"},
		{"a name no template has", &pb.OutboundMessage{Name: "NoSuchMessage"}, "no message named"},
	} {
		err := sendMessage(ctx, h, nil, "test", tc.m)
		if status.Code(err) != codes.InvalidArgument || !strings.Contains(errText(err), tc.says) {
			t.Errorf("%s: %v; want InvalidArgument saying %q", tc.what, err, tc.says)
		}
	}

	// A name is looked up rather than trusted, and either way the body
	// crosses untouched.
	chat := &msg.ChatFromViewer{}
	chat.ChatData.Message = []byte("by name\x00")
	body, err := chat.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.srv.Send(ctx, &pb.SendRequest{
		Message: &pb.OutboundMessage{Name: "ChatFromViewer", Body: body},
	}); err != nil {
		t.Fatal(err)
	}
	// And unreliably, which is the other half of the framing decision
	// the server makes for the client.
	if _, err := r.srv.Send(ctx, &pb.SendRequest{
		Message: &pb.OutboundMessage{Id: uint32(msg.IDOf(chat)), Body: body, Reliable: true},
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "both chats to reach the simulator", func() bool {
		return counted(r.sim, "ChatFromViewer") >= 2
	})

	// A circuit that has gone is reported as unavailable rather than
	// pretended about: the message did not go anywhere.
	h.Agent().Close()
	err = sendMessage(ctx, h, nil, "test", &pb.OutboundMessage{Name: "ChatFromViewer", Body: body, Reliable: true})
	if err != nil && status.Code(err) != codes.Unavailable {
		t.Errorf("sending on a closed circuit: %v; want Unavailable if anything", err)
	}
}

// TestAnEventReachesOnlyTheClientsThatNamedIt is the event queue half
// of the relay.  Events are named rather than numbered -- some match a
// template message, some have no UDP equivalent at all -- so the name
// is the whole of what a subscription can be matched against.
func TestAnEventReachesOnlyTheClientsThatNamedIt(t *testing.T) {
	r := newRig(t, agent.Caps{})
	h, _ := r.srv.Agent("example")

	wanted := r.dial(t, "TeleportFinish")
	defer wanted.Close()
	uninterested := r.dial(t, "ChatFromSimulator")
	defer uninterested.Close()
	everything := r.dial(t, "*")
	defer everything.Close()
	waitFor(t, 5*time.Second, "all three clients to attach", func() bool {
		return h.ClientCount() == 3
	})

	h.relayEvent("TeleportFinish", []byte("<llsd><map/></llsd>"))

	select {
	case ev := <-wanted.Events():
		if ev.Name != "TeleportFinish" || string(ev.Body) != "<llsd><map/></llsd>" {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the client that named the event never saw it")
	}
	select {
	case ev := <-everything.Events():
		if ev.Name != "TeleportFinish" {
			t.Errorf("the client watching everything got %q", ev.Name)
		}
	case <-time.After(5 * time.Second):
		t.Fatal(`a client subscribed to "*" did not receive an event`)
	}
	select {
	case ev := <-uninterested.Events():
		t.Errorf("a client that named another event received %q", ev.Name)
	case <-time.After(200 * time.Millisecond):
	}

	// Nobody watching is not an error and costs nothing: the event has
	// already been handled by the agent by the time it gets here.
	nobody := &Hosted{Name: "nobody", clients: map[*Client]bool{}}
	nobody.relayEvent("SomethingNobodyAskedFor", nil)
	if n := nobody.relayed.Load(); n != 0 {
		t.Errorf("an event nobody wanted was counted as relayed %d times", n)
	}
}

// TestASlowClientIsDroppedRatherThanStallingTheCircuit is the rule that
// keeps one client from becoming everybody's problem: the relay runs on
// the agent's dispatch goroutine, so a client that will not read must
// lose frames rather than block the session that feeds it.
func TestASlowClientIsDroppedRatherThanStallingTheCircuit(t *testing.T) {
	t.Parallel()

	c := &Client{out: make(chan *pb.ServerPacket, 1)}
	p := &pb.ServerPacket{}

	c.send(p)
	c.send(p) // nowhere to put it
	if got := c.dropped.Load(); got != 1 {
		t.Errorf("%d frames dropped, want 1", got)
	}

	// And once the stream is finished nothing is queued at all, so a
	// relay cannot hold the last frames of a client that has gone.
	c.closed.Store(true)
	<-c.out
	c.send(p)
	select {
	case p := <-c.out:
		t.Errorf("a closed client was still handed %v", p)
	default:
	}
}

// TestSubscriptionsAreNamesInTwoMaps: a name may be a template message,
// an event, or -- like ParcelProperties -- something that used to be one
// and is now the other, so it goes in both maps and either arrival
// matches.
func TestSubscriptionsAreNamesInTwoMaps(t *testing.T) {
	t.Parallel()

	// Without the name map a stream would have made, because the code
	// guards against exactly that -- an adjustment that only adds does
	// not go through the branch that builds the maps afresh.
	c := &Client{subs: map[msg.ID]bool{}}

	got := c.setSubs(&pb.Subscribe{Add: []string{"ChatFromSimulator", "TeleportFinish"}})
	if len(got) != 2 {
		t.Errorf("subscribed to %v, want both names", got)
	}
	if !c.wants(msg.LookupName("ChatFromSimulator").ID) {
		t.Error("a template name did not subscribe to its number")
	}
	if !c.wantsEvent("TeleportFinish") {
		t.Error("an event name that is in no template was not remembered")
	}

	// Removing takes the name out of both maps, which is what makes
	// Unwatch symmetrical with Watch.
	c.setSubs(&pb.Subscribe{Remove: []string{"ChatFromSimulator", "TeleportFinish"}})
	if c.wants(msg.LookupName("ChatFromSimulator").ID) || c.wantsEvent("TeleportFinish") {
		t.Error("a removed name is still subscribed")
	}

	// "*" is everything, and it too can be taken back.
	c.setSubs(&pb.Subscribe{Set: []string{"*"}})
	if !c.wants(msg.LookupName("ChatFromSimulator").ID) || !c.wantsEvent("anything at all") {
		t.Error(`"*" did not subscribe to everything`)
	}
	if got := c.setSubs(&pb.Subscribe{Add: []string{"AgentDataUpdate"}}); got[0] != "*" {
		t.Errorf(`the answer is %v; "*" should be reported first`, got)
	}
	c.setSubs(&pb.Subscribe{Remove: []string{"*"}})
	if c.wantsEvent("anything at all") {
		t.Error(`removing "*" left everything subscribed`)
	}

	// A name this build's template has never heard of is ignored
	// rather than refused: the client may know something we do not.
	if got := c.setSubs(&pb.Subscribe{Set: []string{"NoSuchMessageAnywhere"}}); len(got) != 1 {
		t.Errorf("an unknown name was refused: %v", got)
	}
}

// TestAClearedSubscriptionStopsTheRelay: a client that asks for nothing
// gets nothing, which is the whole use of asking.
//
// Replace is what carries it.  An empty Set does not survive proto3 --
// an empty repeated field is written as no field at all -- so this
// arrived indistinguishable from a frame that named no Set, and the
// wipe below never happened.  A client on a flooded link asking to be
// left alone was quietly ignored.
func TestAClearedSubscriptionStopsTheRelay(t *testing.T) {
	t.Parallel()
	c := &Client{subs: map[msg.ID]bool{}, names: map[string]bool{}}

	c.setSubs(&pb.Subscribe{Set: []string{"*"}, Replace: true})
	if !c.wants(msg.LookupName("ChatFromSimulator").ID) {
		t.Fatal("a subscription to everything does not want chat")
	}

	if got := c.setSubs(&pb.Subscribe{Replace: true}); len(got) != 0 {
		t.Errorf("clearing left %v subscribed", got)
	}
	if c.wants(msg.LookupName("ChatFromSimulator").ID) || c.wantsEvent("anything at all") {
		t.Error("the relay carried on after the subscription was cleared")
	}
}

// TestASetWithNamesStillWipesWithoutTheFlag: a client older than
// Replace cannot say what it meant by an empty Set, but a Set with
// something in it has always meant replace and still must.
func TestASetWithNamesStillWipesWithoutTheFlag(t *testing.T) {
	t.Parallel()
	c := &Client{subs: map[msg.ID]bool{}, names: map[string]bool{}}

	c.setSubs(&pb.Subscribe{Set: []string{"*"}})
	c.setSubs(&pb.Subscribe{Set: []string{"ChatFromSimulator"}})
	if c.wantsEvent("anything at all") {
		t.Error(`"*" survived a Set that replaced it`)
	}
	if !c.wants(msg.LookupName("ChatFromSimulator").ID) {
		t.Error("the name that replaced it did not take")
	}
}

// TestTheStreamRefusesFramesThatAreOutOfOrder: an attach names the
// session, so it has to come first and cannot come twice -- a second
// one would be a client changing session under its own subscriptions.
func TestTheStreamRefusesFramesThatAreOutOfOrder(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()

	cc, err := grpc.NewClient(r.ln.Addr().String(), plaintext())
	if err != nil {
		t.Fatal(err)
	}
	defer cc.Close()
	grid := pb.NewGridClient(cc)

	// Something that is not an attach, first.
	s, err := grid.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Subscribe{
		Subscribe: &pb.Subscribe{Set: []string{"*"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Errorf("a stream that did not attach first: %v; want InvalidArgument", err)
	}

	// Nothing at all: the client hangs up before saying anything, which
	// must end the stream rather than leave it waiting.
	s, err = grid.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	s.CloseSend()
	if _, err := s.Recv(); err == nil {
		t.Error("a stream that sent nothing was answered")
	}

	// And an attach after the first frame.
	s, err = grid.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Attach{
		Attach: &pb.Attach{Agent: "example"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); err != nil {
		t.Fatalf("the attach itself was refused: %v", err)
	}
	if err := s.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Attach{
		Attach: &pb.Attach{Agent: "example"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Errorf("a second attach: %v; want InvalidArgument", err)
	}

	// A client that says it is finished ends the stream cleanly, which
	// is not a failure and must not be reported as one.
	s, err = grid.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Attach{
		Attach: &pb.Attach{Agent: "example"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseSend(); err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := s.Recv(); err != nil {
			if err != io.EOF {
				t.Errorf("a client that finished cleanly ended with %v", err)
			}
			break
		}
	}

	// A message the server cannot make sense of ends the stream too,
	// rather than being dropped silently -- the client asked for
	// something to go on the wire and it did not.
	s, err = grid.Stream(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Attach{
		Attach: &pb.Attach{Agent: "example"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(&pb.ClientPacket{Body: &pb.ClientPacket_Message{
		Message: &pb.OutboundMessage{Name: "NoSuchMessage"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Recv(); status.Code(err) != codes.InvalidArgument {
		t.Errorf("a message with no such name: %v; want InvalidArgument", err)
	}
}

// TestALockNeedsAName: a lock with no name would be one lock shared by
// everything that forgot to name one, which is worse than no lock.
func TestALockNeedsAName(t *testing.T) {
	r := newRig(t, agent.Caps{})
	h, _ := r.srv.Agent("example")

	c := &Client{host: h, ctl: make(chan *pb.ServerPacket, 4), jammed: make(chan struct{})}
	c.lock(context.Background(), h, &pb.Lock{})

	select {
	case p := <-c.ctl:
		l := p.GetLocked()
		if l.GetHeld() || !strings.Contains(l.GetHolder(), "needs a name") {
			t.Errorf("answer = %+v; want a refusal that says why", l)
		}
	default:
		t.Fatal("a lock with no name was not answered at all")
	}
}

// TestASessionWithNoAgentDescribesItselfAnyway: a session logging in has
// nothing to report but its name, and a client asking must get an answer
// saying so rather than a panic or a session that looks fine.
func TestASessionWithNoAgentDescribesItselfAnyway(t *testing.T) {
	t.Parallel()

	h := &Hosted{Name: "example"}
	info := h.info()
	if info.GetName() != "example" || info.GetState() != pb.AgentInfo_CONNECTING {
		t.Errorf("info = %+v, want a CONNECTING example", info)
	}
	if info.GetConnected() {
		t.Error("a session with no agent reported itself connected")
	}
	if info.GetDetail() != "logging in" {
		t.Errorf("detail = %q", info.GetDetail())
	}
}

// TestASessionThatFellOverIsStillConnecting: down but not stopped is
// the supervisor's business, not the client's, so it reports as
// CONNECTING with the reason rather than as a session to give up on.
func TestASessionThatFellOverIsStillConnecting(t *testing.T) {
	r := newRig(t, agent.Caps{})
	h, _ := r.srv.Agent("example")

	h.Agent().Close()
	waitFor(t, 5*time.Second, "the session to end", func() bool {
		state, _ := h.state()
		return state == pb.AgentInfo_CONNECTING
	})
	if h.info().GetConnected() {
		t.Error("a session with no circuit reported itself connected")
	}
}

// TestNeighboursAreAskedAboutAndTurnedOverThroughTheOneCall: the call
// has three requests in it and the field's presence is what tells them
// apart -- ask, turn on, turn off -- so each is made here and the answer
// is read for what it says about the session afterwards.
//
// The circuit is opened by the session on its own, out of an offer the
// simulator makes.  Nothing here dials anything: the offer names a
// second simulator, and the whole of what this test does about it is
// turn the flag on and wait.
func TestNeighboursAreAskedAboutAndTurnedOverThroughTheOneCall(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()

	// Asked without being changed, on a session that was started
	// without them.
	got, err := r.srv.Neighbours(ctx, &pb.NeighboursRequest{Agent: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetOn() || len(got.GetNeighbours()) != 0 {
		t.Fatalf("neighbours = %v, on=%v before anything asked for them",
			got.GetNeighbours(), got.GetOn())
	}

	// The neighbour, which is an ordinary simulator that answers a
	// circuit: a child needs no event queue and no capability.
	next := newSim(t)
	t.Cleanup(next.close)
	handle := msg.RegionHandle(43521, 43520)

	on := true
	got, err = r.srv.Neighbours(ctx, &pb.NeighboursRequest{Agent: "example", Set: &on})
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetOn() {
		t.Fatal("the session was not turned on")
	}
	// On with nothing held is the ordinary answer to the request that
	// turned them on: the circuit follows the next offer.
	if len(got.GetNeighbours()) != 0 {
		t.Errorf("neighbours = %v the instant they were turned on", got.GetNeighbours())
	}

	offer := &msg.EnableSimulator{}
	offer.SimulatorInfo.Handle = handle
	offer.SimulatorInfo.IP = msg.IPAddr{127, 0, 0, 1}
	offer.SimulatorInfo.Port = msg.IPPort(next.addr().Port)
	r.sim.send(offer, 0)

	waitFor(t, 5*time.Second, "the neighbour to be answered", func() bool {
		got, err = r.srv.Neighbours(ctx, &pb.NeighboursRequest{Agent: "example"})
		return err == nil && len(got.GetNeighbours()) == 1 && got.GetNeighbours()[0].GetHandshook()
	})
	n := got.GetNeighbours()[0]
	if n.GetHandle() != handle {
		t.Errorf("handle = %d, want the one the offer named, %d", n.GetHandle(), handle)
	}
	if n.GetAddress() != next.addr().String() {
		t.Errorf("address = %q, want %q", n.GetAddress(), next.addr())
	}
	if n.GetName() != "Testville" {
		t.Errorf("name = %q, want what the handshake carried", n.GetName())
	}
	if n.GetHeard() == 0 {
		t.Error("nothing was counted as heard on a circuit that answered")
	}

	// And off, which drops what is held rather than only refusing what
	// comes next -- so the same answer says both halves.
	off := false
	got, err = r.srv.Neighbours(ctx, &pb.NeighboursRequest{Agent: "example", Set: &off})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetOn() || len(got.GetNeighbours()) != 0 {
		t.Errorf("neighbours = %v, on=%v after being turned off",
			got.GetNeighbours(), got.GetOn())
	}
}
