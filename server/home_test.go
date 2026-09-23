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
	"strings"
	"sync"
	"sync/atomic"
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
func refuse(t *testing.T, sim *fakeSim, key agent.RefusalKey) {
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
	refuse(t, sim, agent.KeyCouldntTPCloser)

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
	refuse(t, sim, agent.KeyNoHost)

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
	refuse(t, sim, agent.KeyNoHost)

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
	refuse(t, sim, agent.KeyNoHost)

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
	refuse(t, sim, agent.KeyCouldntTPCloser)

	waitFor(t, 5*time.Second, "the loop to finish", func() bool {
		return asked(sim) >= 1 && !homing(h)
	})
}

// reconnectNow ends the session under h and waits for the supervisor to
// bring it back, which is a fresh login and a fresh run of the loop.
func reconnectNow(t *testing.T, h *Hosted) {
	t.Helper()
	saved := ReconnectDelays
	ReconnectDelays = []time.Duration{20 * time.Millisecond}
	t.Cleanup(func() { ReconnectDelays = saved })

	first := h.Agent()
	first.Close()
	waitFor(t, 10*time.Second, "the session to come back", func() bool {
		a := h.Agent()
		return a != nil && a != first
	})
}

// TestAReconnectStartsItAgain, because a reconnect is a fresh login
// with the same "start = home" in it -- and the region that was down
// when the first login happened may still be down now.
func TestAReconnectStartsItAgain(t *testing.T) {
	quickHoming(t)
	h, sim, _ := homeRig(t, "home")
	refuse(t, sim, agent.KeyCouldntTPCloser)

	waitFor(t, 5*time.Second, "the first loop to finish", func() bool {
		return asked(sim) >= 1 && !homing(h)
	})

	was := asked(sim)
	reconnectNow(t, h)
	waitFor(t, 5*time.Second, "it to ask again", func() bool { return asked(sim) > was })
}

// accessBlockedEvent is a maturity refusal in the shape the event queue
// carries a TeleportFailed.  It is written for this test and was not
// captured: the key and the sentence are the ones sl/maturity.go quotes
// from Agni, and the blocks are the two every measured TeleportFailed
// has had, with only the fields read here.
const accessBlockedEvent = `<llsd><map>` +
	`<key>AlertInfo</key><array><map>` +
	`<key>Message</key><string>RegionTPAccessBlocked</string></map></array>` +
	`<key>Info</key><array><map>` +
	`<key>Reason</key><string>You aren't allowed in that Region due to ` +
	`your maturity Rating. You may need to validate your age and/or ` +
	`install the latest Viewer. Please go to the Knowledge Base for ` +
	`details on accessing areas with this maturity Rating.</string>` +
	`</map></array></map></llsd>`

// TestTheHomingLoopIsHandedTheGridsKeyAndNotItsWords: what the loop
// decides on has to be the key, as a value it can compare with one of
// the agent package's names for it.  The loop once had only the words
// -- the key when there was one, the sentence when there was not, in
// one string -- and found the key by searching them for a copy of it
// kept in this package.
func TestTheHomingLoopIsHandedTheGridsKeyAndNotItsWords(t *testing.T) {
	h := &Hosted{}
	answers := make(chan homeAnswer, 1)
	h.homeAnswers = answers
	h.noteTeleportEvent("TeleportFailed", []byte(accessBlockedEvent))

	select {
	case got := <-answers:
		if got.refusal.Key != agent.KeyRegionTPAccessBlocked {
			t.Errorf("key = %q, want %q", got.refusal.Key, agent.KeyRegionTPAccessBlocked)
		}
		if !strings.HasPrefix(got.refusal.Reason, "You aren't allowed") {
			t.Errorf("reason = %q, want the grid's sentence", got.refusal.Reason)
		}
		if !got.blocked() {
			t.Errorf("a refusal of access was not read as one: %+v", got)
		}
		if got.already() || got.arrived {
			t.Errorf("a region that will not have the avatar was read as home: %+v", got)
		}
		if said := got.saidOrSilence(); said != "RegionTPAccessBlocked" {
			t.Errorf("the log line says %q, want the key", said)
		}
		// The line that gives up for good carries the sentence too:
		// one key covers more than one cause, and the sentence is what
		// says which.
		if said := got.inFull(); !strings.HasPrefix(said, `RegionTPAccessBlocked: "You aren't allowed`) {
			t.Errorf("the giving-up line says %q, want the key and the sentence", said)
		}
	default:
		t.Fatal("a refusal off the event queue was not handed to the waiting attempt")
	}
}

// heard is what a session said to whoever runs the daemon.
type heard struct {
	mu    sync.Mutex
	lines []string
}

// listen starts collecting what h says.
//
// Under h's lock, because the loop reads Log from its own goroutine and
// takes that lock before it first says anything; and a test that
// listens should slow the loop's first attempt down with listenSlowly,
// so that nothing is said before the listening starts.
func listen(h *Hosted) *heard {
	l := &heard{}
	h.mu.Lock()
	h.Log = func(format string, v ...any) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.lines = append(l.lines, fmt.Sprintf(format, v...))
	}
	h.mu.Unlock()
	return l
}

// said is every line so far that has want in it.
func (l *heard) said(want string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var got []string
	for _, line := range l.lines {
		if strings.Contains(line, want) {
			got = append(got, line)
		}
	}
	return got
}

// all is every line so far, for a failure message.
func (l *heard) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

// listenSlowly makes the loop's first attempt late enough that a test
// is listening before the loop has anything to say.
func listenSlowly(t *testing.T) {
	t.Helper()
	saved := homeSettle
	homeSettle = 300 * time.Millisecond
	t.Cleanup(func() { homeSettle = saved })
}

// fakeClock is the time as the homing loop counts it, moved on by a
// test rather than by waiting: the limit is an hour, and the rest of
// the loop's timings are shortened to milliseconds, so the clock is the
// only way to spend one.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// stoppedClock is a clock that moves only when the test moves it.
func stoppedClock(t *testing.T) *fakeClock {
	t.Helper()
	c := &fakeClock{t: time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)}
	saved := homeClock
	homeClock = c.now
	t.Cleanup(func() { homeClock = saved })
	return c
}

// spentHome is how long this session has spent asking to go home.
func spentHome(h *Hosted) time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.homeTried.spent
}

// TestAnAccessRefusalStopsItAtOnce.
//
// RegionTPAccessBlocked is the same answer every time it is asked: the
// avatar may not go into that region, and asking again changes nothing
// about the avatar or the region.  A loop that took it for a region
// being down would ask once a minute for ever and be refused every
// time.  One line, naming the grid's key, is what a person needs to go
// and do something about it.
func TestAnAccessRefusalStopsItAtOnce(t *testing.T) {
	quickHoming(t)
	listenSlowly(t)
	h, sim, _ := homeRig(t, "home")
	log := listen(h)
	refuse(t, sim, agent.KeyRegionTPAccessBlocked)

	waitFor(t, 5*time.Second, "the loop to stop", func() bool {
		return asked(sim) >= 1 && !homing(h)
	})
	time.Sleep(10 * HomeRetry)
	if n := asked(sim); n != 1 {
		t.Errorf("asked %d times, and a refusal of access is the same answer every time", n)
	}
	if got := log.all(); len(got) != 1 || !strings.Contains(got[0], "RegionTPAccessBlocked") {
		t.Errorf("said %q; want one line naming the grid's key", got)
	}
}

// TestARefusalThatDoesNotChangeIsGivenUpOn.
//
// A region that is down comes back inside an hour.  One that has said
// no for an hour is not coming back by being asked, and a loop without
// a limit asks forty thousand times a month.  And while it asks, it
// says so once and not again until something changes: the answer being
// the same every minute is not news every minute.
func TestARefusalThatDoesNotChangeIsGivenUpOn(t *testing.T) {
	quickHoming(t)
	listenSlowly(t)
	clock := stoppedClock(t)
	h, sim, _ := homeRig(t, "home")
	log := listen(h)
	refuse(t, sim, agent.KeyNoHost)

	waitFor(t, 5*time.Second, "several attempts", func() bool { return asked(sim) >= 3 })
	if !homing(h) {
		t.Fatal("gave up before the hour was out")
	}
	clock.advance(HomeLimit)
	waitFor(t, 5*time.Second, "the loop to give up", func() bool { return !homing(h) })

	settled := asked(sim)
	time.Sleep(10 * HomeRetry)
	if n := asked(sim); n != settled {
		t.Errorf("asked %d more times after giving up", n-settled)
	}
	if got := log.said("not home (no_host)"); len(got) != 1 {
		t.Errorf("said it was not home %d times for one answer that never changed: %q", len(got), log.all())
	}
	if got := log.said("giving up on getting home"); len(got) != 1 || !strings.Contains(got[0], "no_host") {
		t.Errorf("said %q; want one line giving up that names the grid's key", log.all())
	}
}

// TestAReconnectDoesNotStartTheHourAgain.
//
// A session that reconnected every half hour and got a fresh hour each
// time would never give up: the loop without a limit, by the back door.
// The hour is of asking, and it is carried from one session to the
// next until the avatar gets home.
func TestAReconnectDoesNotStartTheHourAgain(t *testing.T) {
	quickHoming(t)
	clock := stoppedClock(t)
	h, sim, _ := homeRig(t, "home")
	refuse(t, sim, agent.KeyNoHost)

	waitFor(t, 5*time.Second, "the loop to be running", func() bool { return asked(sim) >= 1 })
	clock.advance(50 * time.Minute)
	waitFor(t, 5*time.Second, "fifty minutes to be counted", func() bool {
		return spentHome(h) >= 50*time.Minute
	})

	was := asked(sim)
	reconnectNow(t, h)
	waitFor(t, 5*time.Second, "the new session to ask", func() bool { return asked(sim) > was })
	clock.advance(20 * time.Minute)
	waitFor(t, 5*time.Second, "the loop to give up at seventy minutes, not at an hour and fifty", func() bool {
		return !homing(h)
	})
}

// TestAfterGivingUpAReconnectAsksOnce.
//
// A fresh login is a fresh question, and it is asked: the region may be
// back.  But it is asked once, and a refusal ends it -- the hour was
// spent before, and a session that reconnects often must not become one
// that asks once a minute for ever.
func TestAfterGivingUpAReconnectAsksOnce(t *testing.T) {
	quickHoming(t)
	clock := stoppedClock(t)
	h, sim, _ := homeRig(t, "home")
	refuse(t, sim, agent.KeyNoHost)

	waitFor(t, 5*time.Second, "the loop to be running", func() bool { return asked(sim) >= 1 })
	clock.advance(HomeLimit)
	waitFor(t, 5*time.Second, "the loop to give up", func() bool { return !homing(h) })

	was := asked(sim)
	reconnectNow(t, h)
	waitFor(t, 5*time.Second, "the new session to ask", func() bool { return asked(sim) > was })
	waitFor(t, 5*time.Second, "it to stop again", func() bool { return !homing(h) })
	time.Sleep(10 * HomeRetry)
	if n := asked(sim) - was; n != 1 {
		t.Errorf("asked %d times after reconnecting, having given up; want once", n)
	}
}

// TestAnAccessRefusalIsRememberedAcrossAReconnect.
//
// The refusal is about the avatar and the region, and neither is any
// different for the avatar having logged in again.  Asking on every
// reconnect is how a session that reconnects often would go on being
// refused for ever.
func TestAnAccessRefusalIsRememberedAcrossAReconnect(t *testing.T) {
	quickHoming(t)
	h, sim, _ := homeRig(t, "home")
	refuse(t, sim, agent.KeyRegionTPAccessBlocked)

	waitFor(t, 5*time.Second, "the loop to stop", func() bool {
		return asked(sim) >= 1 && !homing(h)
	})
	reconnectNow(t, h)
	time.Sleep(10 * HomeRetry)
	if n := asked(sim); n != 1 {
		t.Errorf("asked %d times; a reconnect asked again to be refused for access again", n)
	}
}

// TestSettingANewHomeForgetsTheRefusal.
//
// The refusal was about the old home.  A client that sets a new one has
// made it beside the point, and a daemon that went on remembering it
// would never try the new home at all.
func TestSettingANewHomeForgetsTheRefusal(t *testing.T) {
	quickHoming(t)
	h, sim, _ := homeRig(t, "home")
	refuse(t, sim, agent.KeyRegionTPAccessBlocked)

	waitFor(t, 5*time.Second, "the loop to stop", func() bool {
		return asked(sim) >= 1 && !homing(h)
	})
	body, err := (&msg.SetStartLocationRequest{}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := sendMessage(context.Background(), h, nil, "test", &pb.OutboundMessage{
		Name: "SetStartLocationRequest", Body: body,
	}); err != nil {
		t.Fatal(err)
	}

	was := asked(sim)
	reconnectNow(t, h)
	waitFor(t, 5*time.Second, "the new home to be asked for", func() bool { return asked(sim) > was })
}

// TestGettingHomeStartsTheCountAgain.
//
// An hour spent getting home on Monday is not an hour off a region
// being down on Friday.  The count is of one spell of not getting
// there, and arriving ends the spell.
func TestGettingHomeStartsTheCountAgain(t *testing.T) {
	quickHoming(t)
	clock := stoppedClock(t)
	h, sim, _ := homeRig(t, "home")

	var open atomic.Bool
	go func() {
		for range time.Tick(5 * time.Millisecond) {
			if asked(sim) == 0 {
				continue
			}
			if open.Load() {
				sim.send(&msg.TeleportLocal{}, 0)
				continue
			}
			m := &msg.TeleportFailed{}
			m.AlertInfo = []msg.TeleportFailed_AlertInfo{{Message: append([]byte(agent.KeyNoHost), 0)}}
			sim.send(m, 0)
		}
	}()

	waitFor(t, 5*time.Second, "the loop to be running", func() bool { return asked(sim) >= 1 })
	clock.advance(50 * time.Minute)
	waitFor(t, 5*time.Second, "fifty minutes to be counted", func() bool {
		return spentHome(h) >= 50*time.Minute
	})
	open.Store(true)
	waitFor(t, 5*time.Second, "the avatar to get home", func() bool { return !homing(h) })
	if spent := spentHome(h); spent != 0 {
		t.Errorf("still counting %s after getting home", spent)
	}
}
