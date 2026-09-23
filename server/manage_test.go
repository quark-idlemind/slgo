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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

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

// TestACallerThatGivesUpWaitingLeavesTheLoginRunning: the single
// flight belongs to the daemon, not to the caller that happened to
// start it.  One client pressing ctrl-c must not take the login away
// from the others waiting on it, and must not be told it succeeded
// either.
func TestACallerThatGivesUpWaitingLeavesTheLoginRunning(t *testing.T) {
	sim := newSim(t)
	defer sim.close()

	// A login server slow enough that the second caller is certain to
	// arrive while the first is still waiting.
	var logins atomic.Int64
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		logins.Add(1)
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-8597-66b760a8cb5f</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-3bf6-2277c65663be</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-2a9a-c37f17e64c61</string></value></member>
		  <member><name>circuit_code</name><value><int>4242</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>Example</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`, sim.addr().IP, sim.addr().Port)
	}))
	defer slow.Close()
	srv := startable(t, slow)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.SetBase(ctx, nil, nil)

	first := make(chan error, 1)
	go func() {
		_, err := srv.Host(ctx, &pb.HostRequest{Agent: "example"})
		first <- err
	}()
	time.Sleep(100 * time.Millisecond)

	impatient, giveUp := context.WithTimeout(ctx, 50*time.Millisecond)
	defer giveUp()
	if _, err := srv.Host(impatient, &pb.HostRequest{Agent: "example"}); err == nil {
		t.Error("a caller that gave up waiting was told the session was up")
	}

	if err := <-first; err != nil {
		t.Fatalf("the login the first caller started: %v", err)
	}
	if _, ok := srv.Agent("example"); !ok {
		t.Error("the session was abandoned when one of the callers left")
	}
	if n := logins.Load(); n != 1 {
		t.Errorf("%d logins, want the one both callers shared", n)
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

// TestStartingAndStoppingNeedAName: there is a sensible default for
// which session to TALK to and deliberately none for which to start or
// stop, because both are visible on the grid and should follow from
// somebody meaning them.
func TestStartingAndStoppingNeedAName(t *testing.T) {
	t.Parallel()

	srv := New()
	ctx := context.Background()

	if _, err := srv.Host(ctx, &pb.HostRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("starting with no name = %v; want InvalidArgument", err)
	}
	if _, err := srv.Logout(ctx, &pb.LogoutRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("logging out with no name = %v; want InvalidArgument", err)
	}

	// A name nobody has heard of is not the same fault, and says so.
	if _, err := srv.Logout(ctx, &pb.LogoutRequest{Agent: "qi"}); status.Code(err) != codes.NotFound {
		t.Errorf("logging out an unknown agent = %v; want NotFound", err)
	}

	// A server given no way to start anything says that rather than
	// pretending the profile is missing: naming them on its command
	// line is the fix, and it is a different one.
	if _, err := srv.Host(ctx, &pb.HostRequest{Agent: "example"}); status.Code(err) != codes.Unimplemented {
		t.Errorf("starting on a server with no profiles = %v; want Unimplemented", err)
	}
}

// TestLoggingOutTwiceIsNotAnError: the second ask is somebody making
// sure, and the state they wanted is the state it is in.
func TestLoggingOutTwiceIsNotAnError(t *testing.T) {
	t.Parallel()

	srv := New()
	if _, err := srv.Add("example", nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := srv.Logout(ctx, &pb.LogoutRequest{Agent: "example"}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.Logout(ctx, &pb.LogoutRequest{Agent: "example"}); err != nil {
		t.Errorf("logging out a session that was already down: %v", err)
	}
}

// TestAProfileThatIsNotThereIsNotFound: "qi is down" and "there is no
// qi" are different answers to the same request, and only the second is
// a typo.
func TestAProfileThatIsNotThereIsNotFound(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	srv := startable(t, loginServer(t, sim, &logins, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv.SetBase(ctx, nil, nil)

	// "other" is listed as a profile and has no login behind it, which
	// is what a config file naming something that was deleted looks
	// like.
	_, err := srv.Host(ctx, &pb.HostRequest{Agent: "other"})
	if status.Code(err) != codes.NotFound {
		t.Errorf("starting a profile that will not load = %v; want NotFound", err)
	}
	if n := logins.Load(); n != 0 {
		t.Errorf("%d login attempts for a profile that would not load", n)
	}
}

// TestASessionStartedOnRequestIsSettledLikeTheRest is why the daemon
// hands the server a callback at all: a session brought up by a client
// needs the same settling as one named on the command line -- the
// active group above all -- or an avatar can build or not depending on
// how its session happened to come into being.
func TestASessionStartedOnRequestIsSettledLikeTheRest(t *testing.T) {
	sim := newSim(t)
	defer sim.close()
	var logins atomic.Int64
	srv := startable(t, loginServer(t, sim, &logins, nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	settled := make(chan string, 1)
	var mu sync.Mutex
	var logged []string
	srv.SetBase(ctx, func(format string, v ...any) {
		mu.Lock()
		logged = append(logged, fmt.Sprintf(format, v...))
		mu.Unlock()
	}, func(h *Hosted) { settled <- h.Name })

	if _, err := srv.Host(ctx, &pb.HostRequest{Agent: "example"}); err != nil {
		t.Fatal(err)
	}
	select {
	case name := <-settled:
		if name != "example" {
			t.Errorf("settled %q", name)
		}
	default:
		t.Fatal("a session started on request was not handed to the daemon to settle")
	}

	// And it was given somewhere to say things, so that what happens to
	// it afterwards is not silent.
	h, _ := srv.Agent("example")
	if h.Log == nil {
		t.Fatal("a session started on request has nowhere to log")
	}
	h.logf("something happened")
	mu.Lock()
	defer mu.Unlock()
	if len(logged) != 1 || !strings.Contains(logged[0], "example: something happened") {
		t.Errorf("logged %v, want the session's name in front of it", logged)
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
