package sl

// The backend that holds the session in this process.
//
// Most of this file is one line per method handing a question to the
// agent underneath, and there is no honest way to reach those without an
// agent -- which means a login, a UDP circuit and a simulator at the far
// end.  What is here is everything that does not need one: the tap that
// turns a packet into what slgod would have relayed, the locks that are
// nothing to do here because a direct session is the only client there
// is, and the handful of methods that answer from state a test can put
// in place.
//
// The agent those last ones run against is built by hand rather than
// connected.  That is a real limit and worth stating: it exercises the
// conversion in this file and says nothing about whether the agent would
// have had that answer.  The suite that compares the two backends
// properly is backend_test.go, and it needs a grid.

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// aDirectSession is a Direct with an agent that was never connected.
//
// Everything the agent answers from its own fields works; everything
// that needs a circuit does not, and is not called here.
func aDirectSession(t *testing.T) *Direct {
	t.Helper()
	return &Direct{
		a:        &agent.Agent{},
		messages: make(chan *Message, relayDepth),
		info: &Info{
			Name: "direct", AgentID: testAgentID, SessionID: testSessionID,
			AvatarName: "Quark Idlemind", Region: "Test Region",
			InventoryRoot: testInvRoot, Channel: "slgo test 1.0",
		},
	}
}

// TestTheTapTurnsAPacketIntoWhatTheDaemonWouldHaveRelayed: a hosted
// session's clients read undecoded bodies off a wire, and a direct one
// has to hand over the same thing -- or every reader above would have to
// know which kind it got, which is the one thing this interface exists
// to prevent.
func TestTheTapTurnsAPacketIntoWhatTheDaemonWouldHaveRelayed(t *testing.T) {
	d := aDirectSession(t)

	say := &msg.ChatFromViewer{}
	say.ChatData.Message = append([]byte("hello"), 0)
	body, err := say.Encode()
	if err != nil {
		t.Fatal(err)
	}

	at := time.Now()
	d.tap(&msg.Packet{
		ID: msg.IDOf(say), Message: say, Body: body, At: at,
		Header: msg.Header{Sequence: 42, Flags: msg.FlagReliable},
	})

	select {
	case m := <-d.Messages():
		if m.ID != msg.IDOf(say) || m.Name != "ChatFromViewer" {
			t.Errorf("the tap relayed %+v", m)
		}
		if m.Sequence != 42 || m.Flags != uint32(msg.FlagReliable) || !m.At.Equal(at) {
			t.Errorf("the framing came out as %+v", m)
		}
		if string(m.Body) != string(body) {
			t.Error("the body was not relayed as it arrived")
		}
	default:
		t.Fatal("the tap relayed nothing")
	}
}

// TestTheTapEncodesWhatArrivedDecoded: keeping the undecoded body is
// what lets one tap feed the relay without the agent knowing what any of
// it means -- but a packet that was decoded and not kept still has to
// reach a client, so it is encoded again on the way past.
func TestTheTapEncodesWhatArrivedDecoded(t *testing.T) {
	d := aDirectSession(t)

	say := &msg.ChatFromViewer{}
	say.ChatData.Message = append([]byte("hello"), 0)
	d.tap(&msg.Packet{ID: msg.IDOf(say), Message: say, At: time.Now()})

	select {
	case m := <-d.Messages():
		if len(m.Body) == 0 {
			t.Error("a packet with no body was relayed with none")
		}
		if m.Name != "ChatFromViewer" {
			t.Errorf("the tap relayed %q", m.Name)
		}
	default:
		t.Fatal("the tap relayed nothing")
	}
}

// TestTheTapDropsWhatCarriesNothing: an acknowledgement is a packet with
// no message and no body, and relaying one would put an entry on every
// client's channel saying nothing at all.
func TestTheTapDropsWhatCarriesNothing(t *testing.T) {
	d := aDirectSession(t)
	d.tap(&msg.Packet{At: time.Now()})
	select {
	case m := <-d.Messages():
		t.Errorf("an acknowledgement was relayed as %+v", m)
	default:
	}
}

// TestTheTapRelaysANumberItCannotName: the server relays a message
// number that is not in its template to a client subscribed to "*",
// body and all, so a direct session has to as well -- a client with a
// newer template can still read it.
func TestTheTapRelaysANumberItCannotName(t *testing.T) {
	d := aDirectSession(t)
	d.tap(&msg.Packet{ID: barrierID, Body: []byte{1, 2, 3}, At: time.Now()})

	select {
	case m := <-d.Messages():
		if m.ID != barrierID || m.Name != "" || len(m.Body) != 3 {
			t.Errorf("the tap relayed %+v", m)
		}
	default:
		t.Fatal("a message the template does not have was dropped")
	}
}

// TestTheTapDropsRatherThanStallTheCircuit: it runs on the dispatch
// goroutine, so a reader that has stopped must not be able to stop the
// circuit -- a message nobody could keep up with is worth less than the
// session staying up.
func TestTheTapDropsRatherThanStallTheCircuit(t *testing.T) {
	d := aDirectSession(t)
	for i := range relayDepth + 10 {
		d.tap(&msg.Packet{ID: barrierID, Body: []byte{byte(i)}, At: time.Now()})
	}
	if got := len(d.messages); got != relayDepth {
		t.Errorf("the relay holds %d, want it capped at %d", got, relayDepth)
	}
}

// TestADirectSessionHasNobodyToContendWith: a lock exists so that two
// clients of one slgod do not use the same object at once, and Second
// Life will not have one avatar logged in twice -- so a session this
// process holds IS the only client, and there is nothing to wait for.
func TestADirectSessionHasNobodyToContendWith(t *testing.T) {
	d := aDirectSession(t)

	if err := d.Lock(context.Background(), "the workbench"); err != nil {
		t.Errorf("Lock: %v", err)
	}
	got, by, err := d.TryLock(context.Background(), "the workbench")
	if err != nil || !got || by != "" {
		t.Errorf("TryLock = %v, %q, %v", got, by, err)
	}
	if err := d.Unlock("the workbench"); err != nil {
		t.Errorf("Unlock: %v", err)
	}
}

// TestADirectSessionSaysWhoItIs: it is printed when something has to
// name which session it is talking about, and a program holding one of
// each should be able to tell them apart at a glance.
func TestADirectSessionSaysWhoItIs(t *testing.T) {
	d := aDirectSession(t)
	got := d.String()
	if !strings.Contains(got, "direct") || !strings.Contains(got, "Quark Idlemind") {
		t.Errorf("String = %q", got)
	}
	if d.Info().Name != "direct" {
		t.Errorf("Info = %+v", d.Info())
	}
}

// TestADirectPresenceComesFromTheAgentsOwnState: a hosted session asks
// the daemon where the avatar is; this one already knows, and the draw
// distance is set by changing the view the agent keeps sending.
func TestADirectPresenceComesFromTheAgentsOwnState(t *testing.T) {
	d := aDirectSession(t)

	p, err := d.Presence(context.Background(), 0)
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.Region != "" || p.RegionHandle != 0 {
		t.Errorf("an agent that has not arrived anywhere reports %+v", p)
	}
	// The membership list comes from the agent as well, and an agent
	// nothing has told about any groups has none to hand over -- which
	// a reader has to take as "not told yet" as much as "belongs to
	// none".
	if len(p.Groups) != 0 {
		t.Errorf("an agent told about no groups reports %+v", p.Groups)
	}

	// Setting it goes through the agent, because the view is what the
	// agent keeps telling the simulator about -- a draw distance held
	// anywhere else would be forgotten on the next update.
	if p, err = d.Presence(context.Background(), 96); err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.DrawDistance != 96 {
		t.Errorf("the draw distance came back as %v", p.DrawDistance)
	}
	if p.LookAt != (msg.Vector3{}) || p.Camera != (msg.Vector3{}) {
		t.Errorf("Presence = %+v", p)
	}
}

// TestADirectRegionSaysWhetherTheHandshakeHasArrived: the handshake
// happens once, and a session that has not had it must say so rather
// than describe a region called "".
func TestADirectRegionSaysWhetherTheHandshakeHasArrived(t *testing.T) {
	d := aDirectSession(t)
	r, known, err := d.Region(context.Background())
	if err != nil {
		t.Fatalf("Region: %v", err)
	}
	if known {
		t.Error("an agent that never handshook reports a known region")
	}
	if r == nil || r.Name != "" {
		t.Errorf("Region = %+v", r)
	}
}

// TestADirectSessionRemembersAFriendshipItWatchedForm: accepting an
// offer is the one case the grid never reports -- the side that accepts
// is told nothing whatsoever -- so this is how that side ever finds out.
func TestADirectSessionRemembersAFriendshipItWatchedForm(t *testing.T) {
	d := aDirectSession(t)

	got, err := d.Friends(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("Friends = %+v, %v", got, err)
	}

	if err := d.NoteFriend(context.Background(), theOther, true); err != nil {
		t.Fatalf("NoteFriend: %v", err)
	}
	if got, err = d.Friends(context.Background()); err != nil {
		t.Fatalf("Friends: %v", err)
	}
	if len(got) != 1 || got[0].ID != theOther || !got[0].Online {
		t.Errorf("Friends = %+v", got)
	}
}

// TestADirectObjectListIsWhateverTheAgentHasHeard: a region describes
// itself to whoever is connected, so the cache belongs to the agent and
// this only filters it -- an empty one is a session that has just
// arrived rather than an empty region.
func TestADirectObjectListIsWhateverTheAgentHasHeard(t *testing.T) {
	d := aDirectSession(t)

	got, err := d.Objects(context.Background(), "", "")
	if err != nil {
		t.Fatalf("Objects: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("an agent that has heard nothing described %d objects", len(got))
	}

	// Flushing says how much was thrown away, which is what makes a
	// second call answer zero.
	if n, err := d.Flush(context.Background()); err != nil || n != 0 {
		t.Errorf("Flush = %d, %v", n, err)
	}
}

// TestADirectCapabilityGoesStraightToTheSimulator: there is no daemon in
// between, so the URLs are the agent's and the request is this process's
// -- which is the whole difference between the two backends for
// inventory and asset upload.
func TestADirectCapabilityGoesStraightToTheSimulator(t *testing.T) {
	d := aDirectSession(t)
	if d.HasCap("SimulatorFeatures") {
		t.Error("HasCap found a capability the simulator never offered")
	}
	if _, err := d.DoCap(context.Background(), agent.CapRequest{Cap: "SimulatorFeatures"}); err == nil {
		t.Error("DoCap reached a capability the simulator never offered")
	}

	srv := newTestCapServer(t)
	d.a.SetCaps(agent.Caps{"SimulatorFeatures": srv})
	if !d.HasCap("SimulatorFeatures") {
		t.Error("HasCap missed a capability that was offered")
	}
	resp, err := d.DoCap(context.Background(), agent.CapRequest{
		Cap: "SimulatorFeatures", Method: "GET",
	})
	if err != nil {
		t.Fatalf("DoCap: %v", err)
	}
	if resp.Status != 200 || string(resp.Body) != "some llsd" {
		t.Errorf("DoCap = %+v", resp)
	}
}

// TestADirectRegionChangeBringsTheNewRegionsCapabilities: Info used to
// be what Login read and nothing after, so a direct session that had
// teleported went on listing the capabilities of the region it logged
// in to -- which is what `caps` under --direct printed.  The session
// follows a region change with Refresh, and that reads the agent again.
func TestADirectRegionChangeBringsTheNewRegionsCapabilities(t *testing.T) {
	d := aDirectSession(t)
	d.regions = make(chan *RegionChange, 32)
	d.info.Caps = []string{"EventQueueGet"} // what Login found
	w, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	was := w.Info()

	// The agent has the new region's set, and says the avatar moved the
	// way it does, from its dispatch goroutine.
	d.a.SetCaps(agent.Caps{"SimulatorFeatures": "https://sim.example.net/cap/features"})
	d.regionChanged("Example Region", 1)

	waitFor(t, "the new region's capabilities", func() bool {
		return w.Info().HasCap("SimulatorFeatures")
	})
	got := w.Info()
	if got.HasCap("EventQueueGet") {
		t.Errorf("the login region's capabilities are still listed: %v", got.Caps)
	}
	if got.Name != was.Name || got.AgentID != was.AgentID || got.SessionID != was.SessionID ||
		got.AvatarName != was.AvatarName || got.InventoryRoot != was.InventoryRoot {
		t.Errorf("the move changed who the session is: %+v, was %+v", got, was)
	}
	// Replaced rather than edited: a caller holding the old one holds
	// what was true when it asked.
	if !slices.Equal(was.Caps, []string{"EventQueueGet"}) {
		t.Errorf("the Info held from before the move was edited: %v", was.Caps)
	}
	if d.Info() != got {
		t.Error("the backend's Info is not the one the session was handed")
	}
}

// TestAMovedDirectSessionIsNamedForTheRegionItIsIn: the rest of what
// Refresh installs, which an agent that was never connected cannot
// supply -- the region's name and the build of its simulator.
func TestAMovedDirectSessionIsNamedForTheRegionItIsIn(t *testing.T) {
	d := aDirectSession(t)
	was := d.Info()

	got := d.moved("Example Region", "Example Server 2026.09.01",
		agent.Caps{"SimulatorFeatures": "https://sim.example.net/cap/features"})
	if got.Region != "Example Region" || got.Channel != "Example Server 2026.09.01" ||
		!slices.Equal(got.Caps, []string{"SimulatorFeatures"}) {
		t.Errorf("after the move Info = %+v", got)
	}
	if got.AvatarName != was.AvatarName || got.SessionID != was.SessionID {
		t.Errorf("the move changed who the session is: %+v, was %+v", got, was)
	}
	if was.Region != "Test Region" {
		t.Errorf("the Info held from before the move was edited: %+v", was)
	}
	if d.Info() != got {
		t.Error("Info does not hand back what the move installed")
	}
}

// TestADirectSessionEndsWhenTheAgentDoes: Done and Err come from the
// agent, because a direct session is the agent -- there is nobody else
// holding it.
func TestADirectSessionEndsWhenTheAgentDoes(t *testing.T) {
	d := aDirectSession(t)
	// An agent that was never connected has no channel to close, which
	// reads as a session that has not ended.
	select {
	case <-d.Done():
		t.Error("an agent that never connected says it has ended")
	default:
	}
	if err := d.Err(); err != nil {
		t.Errorf("Err = %v", err)
	}
}

// TestSettlingTheActiveGroupPicksNothingWhenThereIsAChoice: one group
// joined means there is nothing to choose; several means the answer is
// not derivable and picking one would act as the wrong group on land
// that grants building to the other.
func TestSettlingTheActiveGroupPicksNothingWhenThereIsAChoice(t *testing.T) {
	d := aDirectSession(t)
	// Nothing has told this agent about any groups, and a cancelled
	// context is what stops the wait being the whole ten seconds.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if name, ok := d.activeGroup(ctx); ok || name != "" {
		t.Errorf("activeGroup = %q, %v for an agent in no groups", name, ok)
	}
}

// TestLoginRefusesCredentialsItCannotUse: a login with no name is
// refused before anything is sent, since the login server would answer
// with something less clear about it -- and everything after the login
// needs a grid.
func TestLoginRefusesCredentialsItCannotUse(t *testing.T) {
	t.Parallel()
	if _, err := Login(context.Background(), agent.Login{}); err == nil {
		t.Error("Login logged in with no credentials at all")
	}
	if _, err := Login(context.Background(), agent.Login{First: "Quark"}); err == nil {
		t.Error("Login logged in with half a name")
	}
}

// TestADirectBackendIsWhatTheSessionRunsOn: the interface is the promise
// that a program works the same either way, and a Session built on one
// has to reach the grid only through it.
func TestADirectBackendIsWhatTheSessionRunsOn(t *testing.T) {
	d := aDirectSession(t)
	w, err := New(d)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if w.Me() != testAgentID {
		t.Errorf("the session is %s", w.Me())
	}
	if d.Agent() == nil {
		t.Error("Agent answered with nothing")
	}
	// The reader is running on the tap's channel, so a tapped packet
	// reaches the session the way one off a wire would.
	f := &msg.ObjectPropertiesFamily{}
	f.ObjectData.ObjectID = thePrim
	f.ObjectData.Name = append([]byte("workbench"), 0)
	body, err := f.Encode()
	if err != nil {
		t.Fatal(err)
	}
	d.tap(&msg.Packet{ID: msg.IDOf(f), Body: body, At: time.Now()})

	deadline := time.Now().Add(5 * time.Second)
	for {
		w.mu.Lock()
		name := w.objectNames[thePrim]
		w.mu.Unlock()
		if name == "workbench" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the session never saw what the tap relayed")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// newTestCapServer is a capability that answers anything, for the calls
// that only need somewhere to send a request.
func newTestCapServer(t *testing.T) string {
	t.Helper()
	f := newFake(t)
	s := f.ServeCap(t, "any", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("some llsd"))
	})
	return s.URL
}

// recordingWriter is a circuit that goes nowhere and keeps what was
// written to it, which is all a sender needs.
type recordingWriter struct {
	mu   sync.Mutex
	sent [][]byte
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sent = append(w.sent, append([]byte(nil), p...))
	return len(p), nil
}

func (w *recordingWriter) datagrams() [][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([][]byte(nil), w.sent...)
}

// TestReliableAndUnreliableAreDifferentPromises: a message that has to
// arrive and one that does not go out through different calls on the
// sender, and the difference shows up as a flag in the header -- so a
// backend that sent everything one way would either flood the circuit
// with retransmissions or lose the confirmations everything above waits
// for.
func TestReliableAndUnreliableAreDifferentPromises(t *testing.T) {
	d := aDirectSession(t)
	w := &recordingWriter{}
	d.a.Send = msg.NewSender(w)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.a.Send.Run(ctx)

	say := &msg.ChatFromViewer{}
	say.ChatData.Message = append([]byte("hello"), 0)
	if err := d.Send(ctx, say, true); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := d.Send(ctx, say, false); err != nil {
		t.Fatalf("Send: %v", err)
	}

	var got [][]byte
	deadline := time.Now().Add(5 * time.Second)
	for len(got) < 2 && time.Now().Before(deadline) {
		got = w.datagrams()
		time.Sleep(time.Millisecond)
	}
	if len(got) < 2 {
		t.Fatalf("%d datagrams reached the circuit, want 2", len(got))
	}
	if got[0][0]&msg.FlagReliable == 0 {
		t.Error("a message sent reliably went out without the flag")
	}
	if got[1][0]&msg.FlagReliable != 0 {
		t.Error("a message sent unreliably went out asking to be acknowledged")
	}
}
