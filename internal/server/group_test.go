package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// counted is how many times the sim saw a message of that name.
func counted(sim *fakeSim, name string) int {
	n := 0
	for _, s := range sim.got() {
		if s == name {
			n++
		}
	}
	return n
}

// waitFor polls until f is true, and fails the test if it never is.
func waitFor(t *testing.T, within time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestTheGroupListCrossesToAClient: which groups an avatar has joined
// is known here and nowhere else.  Nothing asks for it -- the simulator
// volunteers it shortly after the handshake and again whenever it
// changes -- so a client that attached later has no way to fetch it,
// exactly as with the active group.
//
// Without it a client holds no group key at all, and the only thing it
// could do with ActivateGroup is send a key it has no way of knowing.
func TestTheGroupListCrossesToAClient(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()
	h, _ := r.srv.Agent("example")

	// Before the simulator says anything, which is not the same as
	// belonging to none.  The response carries an empty list either
	// way and leaves the difference to whoever reads it.
	p, err := r.srv.Presence(ctx, &pb.PresenceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.GetGroups()) != 0 {
		t.Errorf("groups = %v before the simulator said anything", p.GetGroups())
	}

	builders := msg.MustParseUUID("488f7e57-7e57-c0de-054d-127bee068aa2")
	explorers := msg.MustParseUUID("4baf7e57-7e57-c0de-23d9-34c5d2bd3ec7")
	m := &msg.AgentGroupDataUpdate{}
	m.AgentData.AgentID = h.Agent().Account.AgentID
	m.GroupData = []msg.AgentGroupDataUpdate_GroupData{
		{GroupID: builders, GroupName: []byte("Pelmar Reach Builders\x00"), GroupPowers: 0x101},
		{GroupID: explorers, GroupName: []byte("Explorers\x00")},
	}
	r.sim.send(m, 0)
	waitFor(t, 5*time.Second, "the membership list to arrive", func() bool {
		return len(h.Agent().Groups()) == 2
	})

	p, err = r.srv.Presence(ctx, &pb.PresenceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	gs := p.GetGroups()
	if len(gs) != 2 {
		t.Fatalf("groups = %v, want the two the simulator sent", gs)
	}
	// The name as a person would type it: the wire carries it
	// nul-terminated, and a nul that crossed would make every name
	// this is matched against fail to match.
	if gs[0].GetId() != builders.String() || gs[0].GetName() != "Pelmar Reach Builders" {
		t.Errorf("first group = %v", gs[0])
	}
	if gs[0].GetPowers() != 0x101 {
		t.Errorf("powers = %#x, want the bitfield the simulator sent", gs[0].GetPowers())
	}
	if gs[1].GetId() != explorers.String() || gs[1].GetName() != "Explorers" {
		t.Errorf("second group = %v", gs[1])
	}
}

// TestReconnectRestoresTheActiveGroup is the regression test for a
// fault found in the field: an avatar that could build stopped being
// able to after its session was re-established.
//
// A reconnect is a fresh login, and a fresh login has NO active group,
// so a group settled once at startup is silently lost -- and the
// symptom is the simulator refusing to let the avatar rez, blaming the
// parcel.  Nothing says the daemon dropped something.
func TestReconnectRestoresTheActiveGroup(t *testing.T) {
	sim := newSim(t)
	defer sim.close()

	var logins atomic.Int64
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-8597-66b760a8cb5f</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-3bf6-2277c65663be</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-2a9a-c37f17e64c61</string></value></member>
		  <member><name>circuit_code</name><value><int>%d</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`,
			4000+logins.Load(), sim.addr().IP, sim.addr().Port)
	}))
	defer hs.Close()

	saved := ReconnectDelays
	ReconnectDelays = []time.Duration{20 * time.Millisecond}
	defer func() { ReconnectDelays = saved }()

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}

	group := msg.MustParseUUID("33a57e57-7e57-c0de-da54-ed9b5d7d8f09")
	if err := h.SetGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the group to be activated", func() bool {
		return counted(sim, "ActivateGroup") >= 1
	})
	if got := h.Group(); got != group {
		t.Errorf("Group() = %v, want %v", got, group)
	}

	// The session dies the way a lost circuit does.
	first := h.Agent()
	first.Close()

	waitFor(t, 10*time.Second, "the session to be re-established", func() bool {
		a := h.Agent()
		return a != nil && a != first
	})

	// The whole point: the new session is acting as the group too.
	waitFor(t, 5*time.Second, "the group to be activated again", func() bool {
		return counted(sim, "ActivateGroup") >= 2
	})
	if got := h.Group(); got != group {
		t.Errorf("after reconnecting, Group() = %v, want %v", got, group)
	}
}

// TestNoGroupSendsNothing: no group is a legitimate answer -- it is
// what a login starts with -- and must not put an ActivateGroup for the
// zero uuid on the wire, which would be a request to leave whatever
// group the avatar is in.
func TestNoGroupSendsNothing(t *testing.T) {
	sim := newSim(t)
	defer sim.close()

	var logins atomic.Int64
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-8597-66b760a8cb5f</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-3bf6-2277c65663be</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-2a9a-c37f17e64c61</string></value></member>
		  <member><name>circuit_code</name><value><int>4321</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`,
			sim.addr().IP, sim.addr().Port)
	}))
	defer hs.Close()

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.SetGroup(ctx, msg.UUID{}); err != nil {
		t.Fatal(err)
	}
	// Nothing to wait for, so give it a moment to be wrong in.
	time.Sleep(200 * time.Millisecond)
	if n := counted(sim, "ActivateGroup"); n != 0 {
		t.Errorf("%d ActivateGroup sent for no group, want none", n)
	}
}

// TestLosingTheGroupIsSaidOutLoud: putting the group back can fail --
// the reconnect may have produced a session that is already gone -- and
// the one thing that must not happen then is silence, because a silent
// loss is the exact fault this machinery was built for.  The session
// stays up: it is usable for everything that does not need land
// rights, and taking it down would trade a building problem for no
// session at all.
func TestLosingTheGroupIsSaidOutLoud(t *testing.T) {
	t.Parallel()

	var said []string
	h := &Hosted{
		Name: "example",
		Log:  func(format string, v ...any) { said = append(said, fmt.Sprintf(format, v...)) },
	}
	h.group = msg.MustParseUUID("33a57e57-7e57-c0de-da54-ed9b5d7d8f09")

	// No session to send it on, which is what activating a group
	// without one amounts to.
	h.restoreGroup(context.Background(), nil)
	if len(said) != 1 || !strings.Contains(said[0], "could not put the active group back") {
		t.Errorf("said %v; want one complaint about the group", said)
	}

	// And a session that never had a group has nothing to put back, so
	// it says nothing at all rather than reporting the zero uuid.
	said = nil
	h.group = msg.UUID{}
	h.restoreGroup(context.Background(), nil)
	if len(said) != 0 {
		t.Errorf("said %v for a session that was never acting as a group", said)
	}
	if err := activateGroup(context.Background(), nil, msg.MustParseUUID("33a57e57-7e57-c0de-da54-ed9b5d7d8f09")); err == nil {
		t.Error("activating a group with no session was reported as success")
	}
}

// TestDefaultIsTheOldestSession pins the rule a bare command relies on:
// of the sessions hosted, the default is the one hosted LONGEST, and it
// changes only when that one goes away.
//
// The property matters more than the mechanism.  A default that moved
// when an avatar was added would silently point an unchanged script at
// a different avatar, and a benchmark run against the wrong avatar is
// not an error -- it is a plausible number.
func TestDefaultIsTheOldestSession(t *testing.T) {
	srv := New()

	if _, ok := srv.Default(); ok {
		t.Fatal("an empty server has a default")
	}

	// Hosted directly rather than logged in: this is about ranking.
	add := func(name string) *Hosted {
		t.Helper()
		h, err := srv.Add(name, nil)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	first := add("example")
	if d, _ := srv.Default(); d != first {
		t.Fatal("the only session is not the default")
	}

	second := add("qi")
	third := add("helper")

	// Adding never moves it, however the names sort -- "helper"
	// sorts first alphabetically and must not win.
	if d, _ := srv.Default(); d != first {
		t.Errorf("default moved to %q when sessions were added", d.Name)
	}

	// Nor does a reconnect: the rank is the Hosted's, and the agent
	// underneath is replaced on every reconnect.
	first.setAgent(nil)
	if d, _ := srv.Default(); d != first {
		t.Errorf("default moved to %q after the agent underneath changed", d.Name)
	}

	// It moves when the default itself goes, and then to the next
	// oldest rather than to whatever sorts first.
	_, _ = srv.Remove("example")
	if d, _ := srv.Default(); d != second {
		t.Errorf("after the default went, default is %v, want qi", d)
	}

	_, _ = srv.Remove("qi")
	if d, _ := srv.Default(); d != third {
		t.Errorf("after two went, default is %v, want helper", d)
	}

	_, _ = srv.Remove("helper")
	if _, ok := srv.Default(); ok {
		t.Error("a server holding nothing still has a default")
	}
}

// TestKickedSessionStaysDown is the rule that keeps the daemon out of a
// fight with its own operator.
//
// Log an avatar slgod is holding into a viewer and the grid ends
// slgod's session.  A supervisor that treats every ending as something
// to recover from waits a few seconds and takes the session back --
// throwing the operator out of the viewer they just opened, and doing
// it again every time they try.  The daemon would win, repeatedly.
//
// So a session the grid ended DELIBERATELY is not re-established.  The
// viewer always wins.
func TestKickedSessionStaysDown(t *testing.T) {
	sim := newSim(t)
	defer sim.close()

	var logins atomic.Int64
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-8597-66b760a8cb5f</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-3bf6-2277c65663be</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-2a9a-c37f17e64c61</string></value></member>
		  <member><name>circuit_code</name><value><int>%d</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`,
			4000+logins.Load(), sim.addr().IP, sim.addr().Port)
	}))
	defer hs.Close()

	// Retry fast, so that a supervisor which is going to reconnect has
	// every chance to do so before this test concludes it did not.
	saved := ReconnectDelays
	ReconnectDelays = []time.Duration{20 * time.Millisecond}
	defer func() { ReconnectDelays = saved }()

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}
	first := h.Agent()
	atLogin := logins.Load()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serving := make(chan struct{})
	go func() { defer close(serving); srv.Serve(ctx, ln) }()
	defer func() { cancel(); <-serving }()

	// A client attached before the kick, whose stream has to end with it.
	watching, err := client.Dial(context.Background(), ln.Addr().String(), plaintext())
	if err != nil {
		t.Fatal(err)
	}
	defer watching.Close()
	if _, err := watching.Attach(context.Background(), "example"); err != nil {
		t.Fatal(err)
	}

	// The grid ejects the avatar, the way it does when the same
	// account logs in from somewhere else.
	kick := &msg.KickUser{}
	kick.UserInfo.Reason = []byte("You have been logged out because you logged in from another location.\x00")
	sim.send(kick, msg.FlagReliable)

	waitFor(t, 5*time.Second, "the session to end", func() bool {
		select {
		case <-first.Done():
			return true
		default:
			return false
		}
	})

	// The reason survives, because it is the only explanation there is
	// and a person reading a log needs it.
	if reason, ok := first.Kicked(); !ok || !strings.Contains(reason, "another location") {
		t.Errorf("Kicked() = %q, %v; want the grid's reason", reason, ok)
	}
	if agent.Retryable(first.Err()) {
		t.Errorf("a kick reported itself as retryable: %v", first.Err())
	}

	// And now the whole point: it must NOT come back.  Long enough for
	// many attempts at the 20ms retry above.
	time.Sleep(time.Second)

	if n := logins.Load(); n != atLogin {
		t.Errorf("%d further login(s) after being kicked; the daemon took the session back", n-atLogin)
	}
	if a := h.Agent(); a != first {
		t.Error("the session was re-established after a kick")
	}
	if !h.stopped.Load() {
		t.Error("a kicked session was not marked stopped, so something may bring it back")
	}

	// Staying down is not enough on its own: a session that ended still
	// answers out of what it last heard and sends into nothing, so a
	// client must not be handed one.
	if why := h.Down(); why == "" || !strings.Contains(why, "another location") {
		t.Errorf("Down() = %q; want the grid's reason", why)
	}
	if d, ok := srv.Default(); ok {
		t.Errorf("a stopped session is still the default: %s", d.Name)
	}

	c, err := client.Dial(context.Background(), ln.Addr().String(), plaintext())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, refused := c.Attach(context.Background(), "example")
	if refused == nil {
		t.Error("attaching to a session the grid ended was allowed")
	} else if !strings.Contains(refused.Error(), "not connected") {
		t.Errorf("attach refused, but not helpfully: %v", refused)
	}

	// And the client that was attached is told the same, the same way:
	// its stream ends rather than staying on a session that is not
	// coming back.
	select {
	case <-watching.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived a session the grid ended")
	}
	if got, want := status.Convert(watching.Err()), status.Convert(refused); got.Code() != codes.FailedPrecondition ||
		got.Message() != want.Message() {
		t.Errorf("the stream ended with %v, and an attach is refused with %v", watching.Err(), refused)
	}
}

// TestASessionTheGridEndedGivesUpItsPlace: a session thrown off by the
// grid is stopped as a logout is, and gives up its place as a logout
// does.  Kept at the head of Ranked, it would lead a listing whose head
// means "the default" while Default passed over it.
func TestASessionTheGridEndedGivesUpItsPlace(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	hs := loginServer(t, sim, &logins, nil)

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}
	helper, err := srv.Add("helper", nil)
	if err != nil {
		t.Fatal(err)
	}
	if r := srv.Ranked(); len(r) != 2 || r[0] != h {
		t.Fatal("the session hosted first is not at the head, so this tests nothing")
	}

	kick := &msg.KickUser{}
	kick.UserInfo.Reason = []byte("You have been logged out because you logged in from another location.\x00")
	sim.send(kick, msg.FlagReliable)
	waitFor(t, 5*time.Second, "the session to stop", h.Stopped)

	var order []string
	for _, x := range srv.Ranked() {
		order = append(order, x.Name)
	}
	if len(order) != 2 || order[0] != "helper" || order[1] != "example" {
		t.Errorf("Ranked() = %v, want [helper example]: the session the grid ended kept its place", order)
	}
	if d, ok := srv.Default(); !ok || d != helper {
		t.Errorf("Default() = %v, %v; want helper, the head of Ranked", d, ok)
	}
}
