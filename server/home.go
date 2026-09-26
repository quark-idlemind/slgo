package server

// Getting home, and giving up trying when somebody else has the wheel.
//
// A profile may say "start = home", which asks the login server to put
// the avatar at its home position.  That is a request and not a
// promise: if the home REGION is down when the login happens, the grid
// puts the avatar somewhere else entirely and says nothing about it
// afterwards.  What comes up is a session in the wrong place, with the
// wrong land under it -- and for a lab whose avatars are meant to be
// standing on their own parcel that is a working day spent wondering
// why nothing can rez.
//
// So a session that asked to start at home keeps asking to GO home
// until it gets there, once a minute, for up to an hour.  A region that
// was down comes back and the avatar walks in on the next attempt with
// nobody watching.
//
// # How anything here knows whether it is home already
//
// It does not, and cannot: nothing in the protocol answers "where is
// home", and the daemon has never been told.  What it can do is ask to
// go there and read what the grid says, which is exactly what the first
// attempt is for.  Four answers, all of them measured in sl against
// Agni -- the access refusal on a teleport to somewhere other than
// home, and read here as meaning the same on the way home, which has
// not been seen:
//
//   - a TeleportFinish, or a TeleportLocal: the avatar moved, so it is
//     home now and there is nothing more to do.
//   - a TeleportFailed carrying CouldntTPCloser: the grid will not
//     shorten a teleport that arrives where it started, which is the
//     grid's way of saying the avatar is already there.  Done.
//   - a TeleportFailed carrying RegionTPAccessBlocked: not home, and
//     never going to be by asking.  See below.
//   - anything else, or silence: not home and not going, so wait a
//     minute and ask again.
//
// So the cost is one teleport request per login, and what the grid does
// with it depends on how exactly the login placed the avatar.  Measured
// on Agni on 2026-09-02, the first time this ran in service: three
// sessions that had logged in at home were each answered with a
// TeleportLocal -- a move inside the region -- and logged "home, after
// one attempt", finishing within seconds and leaving all three at the
// coordinates they had started at.  The CouldntTPCloser refusal is
// real and was measured the same day from the shell, going home while
// standing exactly on the home point; which of the two answers comes
// back is the grid's arithmetic and not something to depend on.
//
// Either way the avatar ends up at home and the loop stops, which is
// all this needs, and it buys not having to know where home is.
//
// # When it stops without getting there
//
// Asking again is worth doing only for an obstacle that goes away, and
// the one this loop was built for does: a region that is down comes
// back, in minutes.  Not every refusal is that.
//
// RegionTPAccessBlocked is the grid's key for a region this avatar may
// not enter.  It was measured for a maturity rating above what the
// avatar may be shown, and the name says access in general; either way
// it is the same answer every time it is asked, because nothing about
// the avatar or the region changes by asking.  So that refusal stops
// the loop at once, with one line naming the key and the grid's
// sentence, and it is remembered: a reconnect does not ask again.  What
// clears it is a new home being set through this daemon (see
// forgetHome), since the refusal was about the old one, or the daemon
// restarting.
//
// Anything else -- no_host, which is a region that is down, or silence,
// or a key nobody here has seen -- is asked about once a minute for an
// hour, and then given up on with one line saying so and naming the
// last answer.  An hour is well past how long a region takes to come
// back; a refusal that has not changed in that time is not a region on
// its way back up.
//
// The hour is an hour of ASKING, counted across reconnects until the
// avatar gets home.  A session that reconnects every half hour would
// otherwise start a fresh hour each time and never give up -- the same
// loop for ever, by the back door.  Time between loops, while there is
// no session to ask with, is not counted, so a session that was down
// for a day does not come back having spent its hour.  Once the hour
// is spent, each reconnect asks once more -- a fresh login is a fresh
// question, and one request per login is what this has always cost --
// and gives up again at once if that is refused.  Getting home resets
// it all.
//
// What it says while it tries is the first answer, then any answer
// that differs from the one before, then the line that gives up.  A
// refusal that never changes is two lines in all: one saying it is not
// home and why, and one an hour later saying it has stopped asking.
// Saying it every tenth attempt, as this once did, was too often to be
// quiet and too rare to notice.
//
// # Why a client teleporting stops it
//
// Because the point is to put an avatar back where it belongs, not to
// keep it there.  Somebody who types "tp" has taken the wheel, and a
// daemon that dragged the avatar home a minute later would be a poltergeist:
// the shell would report an arrival and the avatar would leave again by
// itself, with nothing on the screen to say why.
//
// So any teleport a CLIENT asks for stops the loop for the rest of the
// session, and so does one asked for at a viewer.  They arrive by two
// roads: a client's messages through sendMessage, and a viewer's down
// its own circuit straight to the session, with the circuit telling
// ViewerSent of each one it passes on.  It stops on the request rather
// than on an arrival, so a teleport that is refused stops it too: the
// person meant to be somewhere else, and finding out they cannot is
// their business.
//
// A viewer being handed the session stops it as well, before the viewer
// has asked for anything: a person at a viewer has the wheel.  See
// ViewerAttached.
//
// A reconnect starts it again, and that is deliberate.  A reconnect is
// a fresh login with "start = home" in it, so the same question is
// being asked again by the same means, and whatever a client did with
// the previous session was about a session that no longer exists.
// What the GRID said is another matter, and is kept: the hour of asking
// and an access refusal both outlive a reconnect, for the reasons in
// the section above.

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// HomeRetry is how long to wait before asking to go home again.
//
// A minute, because what is being waited for is a region coming back
// up, which takes minutes rather than seconds, and because an avatar
// that is not where it should be is not usually urgent -- it is simply
// wrong, and it stays wrong quietly.
var HomeRetry = time.Minute

// homeSettle is how long after a session comes up before the first
// attempt.
//
// The login has just finished and the simulator is still sending the
// avatar its own region; a teleport request in the middle of that is
// answered as often as not with silence, which would cost a minute for
// nothing.
var homeSettle = 5 * time.Second

// homeAnswerWait is how long one attempt waits for the grid to say
// anything at all.
//
// Generous, for the reason sl.DefaultTeleportTimeout is: a teleport
// that works is answered in a fraction of a second, but everything
// after the answer is this daemon's -- a circuit at the new simulator,
// a capability fetch, a fresh event queue -- and an attempt that gave
// up early would call a teleport that was working a failure and ask
// for it again on top of itself.
var homeAnswerWait = 45 * time.Second

// HomeLimit is how long to go on asking to go home, in all, before
// giving up on it.
//
// An hour, because a region that is down is back well inside one, and
// a refusal that has lasted longer than that is not one that the next
// minute will change.  Counted across reconnects; see the head of this
// file.
var HomeLimit = time.Hour

// homeClock is what the loop reads the time from.  A variable only so
// that a test can spend an hour without waiting for one.
var homeClock = time.Now

// homeTried is what the grid has said about getting this avatar home,
// kept on the Hosted rather than in one run of the loop because a
// reconnect starts a new run and what the grid said still stands.  See
// the head of this file.
type homeTried struct {
	// spent is how long has gone on asking without getting there,
	// across every run of the loop since the avatar was last home.
	spent time.Duration

	// blocked is a refusal of access to the home region, which asking
	// again will not change.  Zero when there has been none.
	blocked agent.Refusal
}

// homeAnswer is what the grid said about the request to go home.
type homeAnswer struct {
	// arrived is a teleport that happened: the avatar is home.
	arrived bool

	// refusal is a TeleportFailed, both voices of it, read by the agent
	// package's reader so that the key here is the same field sl reads
	// its key from.  Zero for an arrival, and for silence.
	//
	// The key is what the loop decides on and the words are only for
	// the log.  Deciding on the words is what this used to do, by
	// searching them for a copy of the key kept in this file; the key
	// is the grid's, and it is written down once, in agent.
	refusal agent.Refusal
}

// already is the refusal that means the avatar is home and the grid
// will not move it a shorter distance to prove it.
func (a homeAnswer) already() bool {
	return a.refusal.Key == agent.KeyCouldntTPCloser
}

// blocked is the refusal that means the avatar may not go into its home
// region at all, and will be told so however many times it asks.
func (a homeAnswer) blocked() bool {
	return a.refusal.Key == agent.KeyRegionTPAccessBlocked
}

// keepHome starts the loop that asks to go home until it gets there,
// for a session whose profile asked to start there.
//
// Called for every session as it comes up, including one that has just
// reconnected: a reconnect is a fresh login with the same "start =
// home" in it, so it is the same question being asked again.
//
// A session that asked for anything else is left alone.  "start = last"
// means where the avatar was, and dragging that avatar home would be
// this daemon overruling the profile rather than honouring it.
func (h *Hosted) keepHome(ctx context.Context) {
	if !strings.EqualFold(strings.TrimSpace(h.login.Start), "home") {
		return
	}

	ctx, cancel := context.WithCancel(ctx)
	h.mu.Lock()
	if h.homing != nil {
		h.homing()
	}
	h.homing = cancel
	h.homingID++
	id := h.homingID
	h.mu.Unlock()

	// The pace is read once, here, and carried into the loop.  A loop
	// that read them each time round would have its own timings change
	// under it while it was running, which is a thing nobody would ever
	// intend and which the race detector is right to object to.
	go h.homeward(ctx, id, homePace{settle: homeSettle, retry: HomeRetry, answer: homeAnswerWait,
		limit: HomeLimit, now: homeClock})
}

// finishedHoming forgets a loop that has ended of its own accord, so
// that a client teleporting the avatar an hour later has nothing to
// stop and says nothing about it.
//
// By id, because a session that reconnected has a NEWER loop running
// and clearing the field blindly would leave that one unstoppable.
func (h *Hosted) finishedHoming(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.homingID == id {
		h.homing = nil
	}
}

// homePace is how long one run of the loop waits at each of the three
// points it waits at, how long it may go on asking in all, and the clock
// it measures that by.  See keepHome for why it is a value and not
// globals read as it goes.
type homePace struct {
	settle time.Duration
	retry  time.Duration
	answer time.Duration
	limit  time.Duration
	now    func() time.Time
}

// triedFor adds to the time spent asking, and says how much that is now
// in all.
//
// Only for the run of the loop that is current: one a reconnect has
// replaced is on its way out, and a count it added to would be counted
// twice.
func (h *Hosted) triedFor(id uint64, d time.Duration) time.Duration {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.homingID == id {
		h.homeTried.spent += d
	}
	return h.homeTried.spent
}

// cameHome forgets what the grid said on the way, because the avatar is
// there: whatever kept it out has gone, and a later outage is a new one
// with its own hour.
func (h *Hosted) cameHome(id uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.homingID == id {
		h.homeTried = homeTried{}
	}
}

// refusedHome remembers a refusal of access, so that no later run of the
// loop asks again to be told the same thing.
func (h *Hosted) refusedHome(id uint64, r agent.Refusal) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.homingID == id {
		h.homeTried.blocked = r
	}
}

// forgetHome forgets everything the grid has said about getting home,
// because home is somewhere else now: a client or a viewer has sent
// SetStartLocationRequest.  A refusal of the old home says nothing
// about the new one, and the next login asks the question afresh.
//
// A viewer that sets home through the HomeLocation capability instead
// talks to the grid directly and is not seen here.
func (h *Hosted) forgetHome() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.homeTried = homeTried{}
}

// stopHoming ends the loop for the rest of this session, because
// somebody else has decided where the avatar should be.
//
// why is logged only when there was a loop to stop, so that the
// ordinary case -- a client teleporting an avatar that was never
// homing, or one that reached home an hour ago -- says nothing.
func (h *Hosted) stopHoming(why string) {
	h.mu.Lock()
	cancel := h.homing
	h.homing = nil
	h.mu.Unlock()

	if cancel == nil {
		return
	}
	cancel()
	h.logf("no longer trying to get home: %s", why)
}

// noteRequest is what a message somebody else sent means for getting
// home: a teleport stops the loop, and a new home forgets what the grid
// said about the old one.  who is "a client" or "a viewer", for the log.
//
// On the REQUEST rather than on an arrival, so that a teleport which is
// refused stops it too.  See the head of this file.
func (h *Hosted) noteRequest(id msg.ID, who string) {
	if teleportRequest(id) {
		h.stopHoming(who + " teleported this avatar")
	}
	if id == msg.IDOf(&msg.SetStartLocationRequest{}) {
		h.forgetHome()
	}
}

// homeward asks to go home until it does, until something stops it, or
// until the grid has said no for long enough, or in a way, that asking
// again is pointless.  See the head of this file for which.
func (h *Hosted) homeward(ctx context.Context, id uint64, pace homePace) {
	defer h.finishedHoming(id)

	h.mu.RLock()
	blocked := h.homeTried.blocked
	h.mu.RUnlock()
	if blocked.Key != "" {
		// Said on every reconnect, because the reconnect is said and a
		// session that comes up and does not go home would otherwise
		// look like this loop having gone missing.
		h.logf("not asking to go home: refused earlier for access (%s); not asking again until a client "+
			"sets a new home or slgod restarts", blocked.Key)
		return
	}

	if !sleepFor(ctx, pace.settle) {
		return
	}

	// said is the answer last logged, so that the log carries the first
	// answer and each change of answer rather than every one.
	var said string
	since := pace.now()
	for attempt := 0; ; attempt++ {
		answer, err := h.askForHome(ctx, pace.answer)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			// Nothing to send it down.  The session is going or gone,
			// and the supervisor is the one that answers for that; a
			// loop that kept asking would fill the log with the same
			// sentence once a minute.
			return
		case answer.already():
			// The ordinary case, and it says nothing: an avatar that
			// came up at home is not news.
			h.cameHome(id)
			return
		case answer.arrived:
			h.cameHome(id)
			h.logf("home, after %s", attempts(attempt+1))
			return
		case answer.blocked():
			h.refusedHome(id, answer.refusal)
			h.logf("giving up on getting home: the grid will not let this avatar into its home region (%s), "+
				"a refusal of access that asking again will not change; not asking again until a client "+
				"sets a new home or slgod restarts", answer.inFull())
			return
		}

		// The time is counted from before the request went out, so the
		// wait for an answer is counted too: it is part of the asking.
		now := pace.now()
		spent := h.triedFor(id, now.Sub(since))
		since = now
		if spent >= pace.limit {
			h.logf("giving up on getting home: refused for %s of asking, most recently %s; "+
				"a reconnect will ask once more", spent.Round(time.Second), answer.saidOrSilence())
			return
		}

		if this := answer.saidOrSilence(); this != said {
			h.logf("not home (%s); asking again every %s, for up to %s more",
				this, pace.retry, (pace.limit - spent).Round(time.Second))
			said = this
		}
		if !sleepFor(ctx, pace.retry) {
			return
		}
	}
}

// askForHome sends the one request and waits for the grid to answer it.
//
// The request is TeleportLandmarkRequest carrying the null id, which the
// grid reads as home rather than as an error -- the same message
// sl.GoHome sends, and the reason there is no home position in it.
//
// The answer is listened for BEFORE the request goes out, for the reason
// sl/teleport.go gives at length: the answer is about a third of a
// second behind the request, which is a race a listener registered
// afterwards loses often enough to matter and silently when it does.
func (h *Hosted) askForHome(ctx context.Context, wait time.Duration) (homeAnswer, error) {
	a := h.Agent()
	if a == nil {
		return homeAnswer{}, errNoSession
	}

	answers := make(chan homeAnswer, 1)
	h.mu.Lock()
	h.homeAnswers = answers
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if h.homeAnswers == answers {
			h.homeAnswers = nil
		}
		h.mu.Unlock()
	}()

	m := &msg.TeleportLandmarkRequest{}
	m.Info.AgentID = a.Account.AgentID
	m.Info.SessionID = a.Account.SessionID
	// The null id, which is home.  Nothing else goes in it: an id this
	// daemon invented would be a landmark nobody owns, and the grid
	// answers one of those with perfect silence.
	if err := a.Send.Send(ctx, m); err != nil {
		return homeAnswer{}, err
	}

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case answer := <-answers:
		return answer, nil
	case <-timer.C:
		// Silence is not success.  It is what the grid answers a
		// teleport asked for while one is already under way, and what
		// a request that never arrived looks like.
		return homeAnswer{}, nil
	case <-ctx.Done():
		return homeAnswer{}, ctx.Err()
	}
}

// noteTeleportAnswer hands the grid's answer to whatever attempt is
// waiting for one, and is called for every teleport answer the session
// sees whether this loop asked for it or not.
//
// Nothing in the protocol says which request an answer belongs to, so
// an answer provoked by a client's or a viewer's teleport is
// indistinguishable from an answer to ours -- which is why the loop
// stops on their REQUEST, before any answer, rather than trying to tell
// the answers apart.  See the head of this file.
func (h *Hosted) noteTeleportAnswer(answer homeAnswer) {
	h.mu.RLock()
	waiting := h.homeAnswers
	h.mu.RUnlock()
	if waiting == nil {
		return
	}
	select {
	case waiting <- answer:
	default:
	}
}

// noteTeleportMessage reads a teleport answer off the circuit, where
// TeleportLocal arrives and where TeleportFailed can.
func (h *Hosted) noteTeleportMessage(p *msg.Packet) {
	h.mu.RLock()
	waiting := h.homeAnswers != nil
	h.mu.RUnlock()
	if !waiting {
		return
	}

	switch p.ID {
	case msg.IDOf(&msg.TeleportLocal{}), msg.IDOf(&msg.TeleportFinish{}):
		h.noteTeleportAnswer(homeAnswer{arrived: true})
	case msg.IDOf(&msg.TeleportFailed{}):
		var m msg.TeleportFailed
		body := p.Body
		if body == nil && p.Message != nil {
			if b, err := p.Message.Encode(); err == nil {
				body = b
			}
		}
		if err := m.Decode(body); err != nil {
			return
		}
		h.noteTeleportAnswer(homeAnswer{refusal: agent.ReadTeleportFailed(&m)})
	}
}

// noteTeleportEvent reads the same answers off the event queue, which
// is where Second Life actually sends them: measured on Agni, both the
// finish and the failure arrive there and not on the circuit, which the
// message template does not say.
func (h *Hosted) noteTeleportEvent(name string, body []byte) {
	h.mu.RLock()
	waiting := h.homeAnswers != nil
	h.mu.RUnlock()
	if !waiting {
		return
	}

	switch name {
	case "TeleportFinish":
		h.noteTeleportAnswer(homeAnswer{arrived: true})
	case "TeleportFailed":
		// A body that will not decode is still a refusal, and one with
		// nothing to say: it is answered as one rather than dropped, so
		// the attempt waiting for it is not left waiting for an answer
		// that has already come.
		var refusal agent.Refusal
		if v, err := llsd.Decode(bytes.NewReader(body)); err == nil {
			refusal = agent.ReadTeleportFailedEvent(v)
		}
		h.noteTeleportAnswer(homeAnswer{refusal: refusal})
	}
}

// saidOrSilence is the grid's words for a log line, or a description of
// there having been none -- which is an answer of its own and a common
// one.
//
// The key when there is one and the reason when there is not, which is
// what this line has always said: the key is short and is the name
// somebody would search for.
func (a homeAnswer) saidOrSilence() string {
	switch {
	case a.refusal.Key != "":
		return string(a.refusal.Key)
	case a.refusal.Reason != "":
		return a.refusal.Reason
	}
	return "the grid said nothing"
}

// inFull is the grid's key and its sentence both, for the line that
// says the loop has stopped for good: that line is read once, by
// somebody deciding what to do about it, and the sentence is what says
// which of the causes behind one key it was.
func (a homeAnswer) inFull() string {
	r := a.refusal
	switch {
	case r.Key == "":
		return a.saidOrSilence()
	case r.Reason == "" || r.Reason == string(r.Key):
		return string(r.Key)
	}
	return fmt.Sprintf("%s: %q", r.Key, r.Reason)
}

// attempts is "3 attempts" or "one attempt", for a line a person reads.
func attempts(n int) string {
	if n == 1 {
		return "one attempt"
	}
	return fmt.Sprintf("%d attempts", n)
}

// sleepFor waits, and says whether the wait finished rather than the
// context ending under it.
func sleepFor(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}
