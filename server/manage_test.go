package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// loginServer answers logins, counting them, and can be told to refuse.
func loginServer(t *testing.T, sim *fakeSim, logins *atomic.Int64, refuse *atomic.Bool) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		if refuse != nil && refuse.Load() {
			fmt.Fprint(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
			  <member><name>login</name><value><string>false</string></value></member>
			  <member><name>reason</name><value><string>key</string></value></member>
			  <member><name>message</name><value><string>nope</string></value></member>
			</struct></value></param></params></methodResponse>`)
			return
		}
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-9eeb-1bd0e1ec6995</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-f4f4-19d29d124acf</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-d169-d9847afe641e</string></value></member>
		  <member><name>circuit_code</name><value><int>%d</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`,
			4000+logins.Load(), sim.addr().IP, sim.addr().Port)
	}))
	t.Cleanup(hs.Close)
	return hs
}

// startable is a server that can bring "example" up on request.
func startable(t *testing.T, hs *httptest.Server) *Server {
	t.Helper()
	srv := New()
	srv.SetProfiles(
		func() []string { return []string{"example", "other"} },
		func(name string) (agent.Login, agent.Options, error) {
			if name != "example" {
				return agent.Login{}, agent.Options{}, fmt.Errorf("no profile %q", name)
			}
			return agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
				agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1}, nil
		})
	return srv
}

// TestHostStartsOnDemand: an agent nobody named at startup can be
// brought up later, and asking twice does not log it in twice -- which
// would make the grid kick the session it already has.
func TestHostStartsOnDemand(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	srv := startable(t, loginServer(t, sim, &logins, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.SetBase(ctx, nil, nil)

	if _, ok := srv.Agent("example"); ok {
		t.Fatal("it is hosted before being asked for")
	}

	r, err := srv.Host(ctx, &pb.HostRequest{Agent: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if r.GetAlready() {
		t.Error("a fresh start reported itself as already up")
	}
	if _, ok := srv.Agent("example"); !ok {
		t.Fatal("it is not hosted after being started")
	}

	again, err := srv.Host(ctx, &pb.HostRequest{Agent: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if !again.GetAlready() {
		t.Error("starting an agent that was up did not say so")
	}
	if n := logins.Load(); n != 1 {
		t.Errorf("%d logins for two Host calls, want 1", n)
	}
}

// TestHostIsSingleFlight: several callers asking at once produce ONE
// login.  Two would race to the same account, and the grid would settle
// it by kicking one -- which looks exactly like the fault this daemon
// exists to avoid.
func TestHostIsSingleFlight(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	srv := startable(t, loginServer(t, sim, &logins, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.SetBase(ctx, nil, nil)

	const callers = 6
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = srv.Host(ctx, &pb.HostRequest{Agent: "example"})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("caller %d: %v", i, err)
		}
	}
	if n := logins.Load(); n != 1 {
		t.Errorf("%d logins for %d simultaneous callers, want 1", n, callers)
	}
}

// TestFailedLoginBacksOff: a refusal must not turn every ask into
// another login attempt.  A login server throttles a client that
// hammers it, and the throttle then presents as a different fault
// entirely.
func TestFailedLoginBacksOff(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	var refuse atomic.Bool
	refuse.Store(true)
	srv := startable(t, loginServer(t, sim, &logins, &refuse))

	saved := ReconnectDelays
	ReconnectDelays = []time.Duration{time.Minute}
	defer func() { ReconnectDelays = saved }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.SetBase(ctx, nil, nil)

	if _, err := srv.Host(ctx, &pb.HostRequest{Agent: "example"}); err == nil {
		t.Fatal("a refused login was reported as success")
	}
	first := logins.Load()

	// Asking again straight away must not reach the login server.
	_, err := srv.Host(ctx, &pb.HostRequest{Agent: "example"})
	if err == nil {
		t.Fatal("the second ask succeeded against a refusing login server")
	}
	if !strings.Contains(err.Error(), "not trying again") {
		t.Errorf("the second refusal does not say it is waiting: %v", err)
	}
	if n := logins.Load(); n != first {
		t.Errorf("%d further login attempt(s) during the backoff", n-first)
	}

	// And it is reported as FAILED rather than looking unheard of.
	list, err := srv.ListAgents(ctx, &pb.ListAgentsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	var found *pb.AgentInfo
	for _, a := range list.GetAgents() {
		if a.GetName() == "example" {
			found = a
		}
	}
	if found == nil {
		t.Fatal("a failed agent is not listed at all")
	}
	if found.GetState() != pb.AgentInfo_FAILED {
		t.Errorf("state is %v, want FAILED", found.GetState())
	}
}

// TestListAgentsShowsWhatCouldBeStarted: "I have never heard of qi" and
// "qi is not running" are different answers, and only one is an error.
func TestListAgentsShowsWhatCouldBeStarted(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	srv := startable(t, loginServer(t, sim, &logins, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.SetBase(ctx, nil, nil)

	list, err := srv.ListAgents(ctx, &pb.ListAgentsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]pb.AgentInfo_State{}
	for _, a := range list.GetAgents() {
		got[a.GetName()] = a.GetState()
	}
	for _, name := range []string{"example", "other"} {
		if got[name] != pb.AgentInfo_CONFIGURED {
			t.Errorf("%s is %v, want CONFIGURED", name, got[name])
		}
	}

	if _, err := srv.Host(ctx, &pb.HostRequest{Agent: "example"}); err != nil {
		t.Fatal(err)
	}
	list, _ = srv.ListAgents(ctx, &pb.ListAgentsRequest{})
	for _, a := range list.GetAgents() {
		want := pb.AgentInfo_CONFIGURED
		if a.GetName() == "example" {
			want = pb.AgentInfo_HOSTED
		}
		if a.GetState() != want {
			t.Errorf("%s is %v, want %v", a.GetName(), a.GetState(), want)
		}
	}
}

// TestLogoutStaysOut is stage 6's whole point: a session told to stop
// stays stopped, so that somebody using that avatar in a viewer is not
// fighting the daemon for it.
func TestLogoutStaysOut(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	srv := startable(t, loginServer(t, sim, &logins, nil))

	saved := ReconnectDelays
	ReconnectDelays = []time.Duration{20 * time.Millisecond}
	defer func() { ReconnectDelays = saved }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.SetBase(ctx, nil, nil)

	if _, err := srv.Host(ctx, &pb.HostRequest{Agent: "example"}); err != nil {
		t.Fatal(err)
	}
	h, _ := srv.Agent("example")
	atLogin := logins.Load()

	if _, err := srv.Logout(ctx, &pb.LogoutRequest{Agent: "example"}); err != nil {
		t.Fatal(err)
	}
	if !h.Stopped() {
		t.Error("logout did not stop it")
	}

	// The supervisor must not bring it back.
	time.Sleep(500 * time.Millisecond)
	if n := logins.Load(); n != atLogin {
		t.Errorf("%d login(s) after logout; it came back on its own", n-atLogin)
	}

	// It is still listed, as STOPPED -- so that a person can ask why it
	// is down rather than being told the name is unknown.
	list, _ := srv.ListAgents(ctx, &pb.ListAgentsRequest{})
	var state pb.AgentInfo_State
	for _, a := range list.GetAgents() {
		if a.GetName() == "example" {
			state = a.GetState()
		}
	}
	if state != pb.AgentInfo_STOPPED {
		t.Errorf("state after logout is %v, want STOPPED", state)
	}

	// And it is not the default any more, however alone it is.
	if d, ok := srv.Default(); ok {
		t.Errorf("a logged-out session is still the default: %s", d.Name)
	}

	// Starting it again needs force, because something stopped it on
	// purpose.
	if _, err := srv.Host(ctx, &pb.HostRequest{Agent: "example"}); err == nil {
		t.Error("a stopped agent was restarted without force")
	}
	if _, err := srv.Host(ctx, &pb.HostRequest{Agent: "example", Force: true}); err != nil {
		t.Fatalf("force did not restart it: %v", err)
	}
	if h2, ok := srv.Agent("example"); !ok || h2.Stopped() {
		t.Error("it is not running after being forced back up")
	}
}
