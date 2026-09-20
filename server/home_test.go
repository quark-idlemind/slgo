package server

// Getting an avatar home when the login could not.
//
// The fault this is about is quiet: "start = home" is a request, and a
// login whose home region is down puts the avatar somewhere else and
// says nothing.  So every test here is about what goes out on the wire
// and when it stops going out -- there is no reply to assert on, and an
// avatar in the wrong place looks exactly like one in the right place
// from the daemon's side.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// quickHoming makes the loop run in test time rather than in the
// minutes a person waiting for a region to come back would.
func quickHoming(t *testing.T) {
	t.Helper()
	settle, retry, answer := homeSettle, HomeRetry, homeAnswerWait
	homeSettle, HomeRetry, homeAnswerWait = 10*time.Millisecond, 40*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { homeSettle, HomeRetry, homeAnswerWait = settle, retry, answer })
}

// homeRig is a session against the fake sim, logged in with the start
// location a test wants to try.
func homeRig(t *testing.T, start string) (*Hosted, *fakeSim, context.CancelFunc) {
	t.Helper()
	sim := newSim(t)
	t.Cleanup(sim.close)

	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-9eeb-1bd0e1ec6995</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-f4f4-19d29d124acf</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-d169-d9847afe641e</string></value></member>
		  <member><name>circuit_code</name><value><int>4321</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`,
			sim.addr().IP, sim.addr().Port)
	}))
	t.Cleanup(hs.Close)

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL, Start: start},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}
	return h, sim, cancel
}

// refuse makes the fake sim answer every request to go home with the
// grid's own refusal, by key.
func refuse(t *testing.T, sim *fakeSim, key string) {
	t.Helper()
	go func() {
		for range time.Tick(5 * time.Millisecond) {
			if counted(sim, "TeleportLandmarkRequest") == 0 {
				continue
			}
			m := &msg.TeleportFailed{}
			m.Info.Reason = append([]byte(key), 0)
			m.AlertInfo = []msg.TeleportFailed_AlertInfo{{Message: append([]byte(key), 0)}}
			sim.send(m, 0)
		}
	}()
}

// asked is how many times this session has asked to go home.
func asked(sim *fakeSim) int { return counted(sim, "TeleportLandmarkRequest") }

// TestAHomeStartAsksToGoHome: "start = home" is a request the login
// server may not honour, and nothing afterwards says whether it did --
// so the session asks, which is the only question it can ask.
func TestAHomeStartAsksToGoHome(t *testing.T) {
	quickHoming(t)
	_, sim, _ := homeRig(t, "home")

	waitFor(t, 5*time.Second, "the session to ask to go home", func() bool {
		return asked(sim) >= 1
	})
}

// TestAnythingButHomeIsLeftAlone.
//
// "start = last" means where the avatar was, and an avatar dragged home
// from there is this daemon overruling the profile rather than
// honouring it.
func TestAnythingButHomeIsLeftAlone(t *testing.T) {
	quickHoming(t)
	for _, start := range []string{"last", "", "Pelmar Reach"} {
		_, sim, _ := homeRig(t, start)
		time.Sleep(200 * time.Millisecond)
		if n := asked(sim); n != 0 {
			t.Errorf("start = %q asked to go home %d times", start, n)
		}
	}
}

// TestAlreadyHomeAsksOnceAndStops.
//
// The grid will not shorten a teleport that arrives where it started,
// and that refusal is how this daemon learns the avatar is already
// home: there is no other way to ask.  So the ordinary case -- a login
// that went where it was asked to -- costs one refused teleport and
// then silence.
func TestAlreadyHomeAsksOnceAndStops(t *testing.T) {
	quickHoming(t)
	_, sim, _ := homeRig(t, "home")
	refuse(t, sim, tooCloseToGo)

	waitFor(t, 5*time.Second, "the one request", func() bool { return asked(sim) >= 1 })
	time.Sleep(10 * HomeRetry)
	if n := asked(sim); n != 1 {
		t.Errorf("asked %d times, and being told it is already there is an answer", n)
	}
}

// TestNotHomeKeepsAsking: a region that is down is a region that comes
// back, and the whole point is that nobody has to be watching when it
// does.
func TestNotHomeKeepsAsking(t *testing.T) {
	quickHoming(t)
	_, sim, _ := homeRig(t, "home")
	refuse(t, sim, "no_host")

	waitFor(t, 5*time.Second, "a second attempt", func() bool { return asked(sim) >= 2 })
}

// TestSilenceKeepsAsking: no answer at all is not success.  It is what
// a teleport asked for while one is under way is answered with, and
// what a request that never arrived looks like.
func TestSilenceKeepsAsking(t *testing.T) {
	quickHoming(t)
	_, sim, _ := homeRig(t, "home")

	waitFor(t, 5*time.Second, "a second attempt", func() bool { return asked(sim) >= 2 })
}

// TestArrivingStopsIt: a teleport that happened is the end of it, and a
// move inside the region counts -- home may be a few metres away rather
// than in another region, and TeleportLocal is how the grid says so.
func TestArrivingStopsIt(t *testing.T) {
	quickHoming(t)
	_, sim, _ := homeRig(t, "home")

	go func() {
		for range time.Tick(5 * time.Millisecond) {
			if asked(sim) > 0 {
				sim.send(&msg.TeleportLocal{}, 0)
			}
		}
	}()

	waitFor(t, 5*time.Second, "the first request", func() bool { return asked(sim) >= 1 })
	time.Sleep(10 * HomeRetry)
	if n := asked(sim); n != 1 {
		t.Errorf("asked %d times after arriving", n)
	}
}

// TestAClientTeleportStopsIt.
//
// Somebody who types "tp" has taken the wheel.  A daemon that dragged
// the avatar home a minute later would be a poltergeist: the shell
// reports an arrival and the avatar leaves again by itself, with
// nothing on the screen to say why.
func TestAClientTeleportStopsIt(t *testing.T) {
	quickHoming(t)
	h, sim, _ := homeRig(t, "home")
	refuse(t, sim, "no_host")

	waitFor(t, 5*time.Second, "the loop to be running", func() bool { return asked(sim) >= 2 })

	body, err := (&msg.TeleportLocationRequest{}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := sendMessage(context.Background(), h, nil, "test", &pb.OutboundMessage{
		Name: "TeleportLocationRequest", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	// Whatever was in flight may still land, so the count is taken
	// after a pause and compared with itself rather than with the
	// number at the moment of the teleport.
	time.Sleep(3 * HomeRetry)
	settled := asked(sim)
	time.Sleep(10 * HomeRetry)
	if n := asked(sim); n != settled {
		t.Errorf("asked %d more times after a client teleported the avatar", n-settled)
	}
}

// TestAClientSittingDoesNotStopIt: a sit moves the avatar up to ten
// metres and is not somebody saying where it should be.  Stopping on
// one would leave an avatar that sat on a chair on the way home never
// getting there.
func TestAClientSittingDoesNotStopIt(t *testing.T) {
	quickHoming(t)
	h, sim, _ := homeRig(t, "home")
	refuse(t, sim, "no_host")

	waitFor(t, 5*time.Second, "the loop to be running", func() bool { return asked(sim) >= 1 })
	body, err := (&msg.AgentRequestSit{}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := sendMessage(context.Background(), h, nil, "test", &pb.OutboundMessage{
		Name: "AgentRequestSit", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	was := asked(sim)
	waitFor(t, 5*time.Second, "the loop to carry on", func() bool { return asked(sim) > was })
}

// homing says whether a loop is running for this session.
func homing(h *Hosted) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.homing != nil
}

// TestALoopThatIsDoneForgetsItself.
//
// Otherwise a client teleporting the avatar an hour after it got home
// stops something that is not running, and says so in the log -- which
// reads as the daemon having been about to drag the avatar back.  It
// was not; it finished at login.
func TestALoopThatIsDoneForgetsItself(t *testing.T) {
	quickHoming(t)
	h, sim, _ := homeRig(t, "home")
	refuse(t, sim, tooCloseToGo)

	waitFor(t, 5*time.Second, "the loop to finish", func() bool {
		return asked(sim) >= 1 && !homing(h)
	})
}

// TestAReconnectStartsItAgain, because a reconnect is a fresh login
// with the same "start = home" in it -- and the region that was down
// when the first login happened may still be down now.
func TestAReconnectStartsItAgain(t *testing.T) {
	quickHoming(t)
	h, sim, _ := homeRig(t, "home")
	refuse(t, sim, tooCloseToGo)

	waitFor(t, 5*time.Second, "the first loop to finish", func() bool {
		return asked(sim) >= 1 && !homing(h)
	})

	saved := ReconnectDelays
	ReconnectDelays = []time.Duration{20 * time.Millisecond}
	defer func() { ReconnectDelays = saved }()

	was := asked(sim)
	first := h.Agent()
	first.Close()
	waitFor(t, 10*time.Second, "the session to come back", func() bool {
		a := h.Agent()
		return a != nil && a != first
	})
	waitFor(t, 5*time.Second, "it to ask again", func() bool { return asked(sim) > was })
}
