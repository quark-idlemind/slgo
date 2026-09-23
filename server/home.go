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
// until it gets there, once a minute.  A region that was down comes
// back and the avatar walks in on the next attempt with nobody
// watching.
//
// # How anything here knows whether it is home already
//
// It does not, and cannot: nothing in the protocol answers "where is
// home", and the daemon has never been told.  What it can do is ask to
// go there and read what the grid says, which is exactly what the first
// attempt is for.  Three answers and they are all measured, in sl,
// against Agni:
//
//   - a TeleportFinish, or a TeleportLocal: the avatar moved, so it is
//     home now and there is nothing more to do.
//   - a TeleportFailed carrying CouldntTPCloser: the grid will not
//     shorten a teleport that arrives where it started, which is the
//     grid's way of saying the avatar is already there.  Done.
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
// # Why a client teleporting stops it
//
// Because the point is to put an avatar back where it belongs, not to
// keep it there.  Somebody who types "tp" has taken the wheel, and a
// daemon that dragged the avatar home a minute later would be a poltergeist:
// the shell would report an arrival and the avatar would leave again by
// itself, with nothing on the screen to say why.
//
// So any teleport a CLIENT asks for stops the loop for the rest of the
// session -- see sendMessage, which is the one place every client
// message goes through.  It stops on the request rather than on an
// arrival, so a teleport that is refused stops it too: the person meant
// to be somewhere else, and finding out they cannot is their business.
//
// A reconnect starts it again, and that is deliberate.  A reconnect is
// a fresh login with "start = home" in it, so the same question is
// being asked again by the same means, and whatever a client did with
// the previous session was about a session that no longer exists.

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
	// that read the three of them each time round would have its own
	// timings change under it while it was running, which is a thing
	// nobody would ever intend and which the race detector is right to
	// object to.
	go h.homeward(ctx, id, homePace{settle: homeSettle, retry: HomeRetry, answer: homeAnswerWait})
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
// points it waits at.  See keepHome for why it is a value and not three
// globals read as it goes.
type homePace struct {
	settle time.Duration
	retry  time.Duration
	answer time.Duration
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

// homeward asks to go home until it does, or until something stops it.
func (h *Hosted) homeward(ctx context.Context, id uint64, pace homePace) {
	defer h.finishedHoming(id)

	if !sleepFor(ctx, pace.settle) {
		return
	}

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
			return
		case answer.arrived:
			h.logf("home, after %s", attempts(attempt+1))
			return
		}

		// Said the first time and then rarely.  The first line is the
		// one that explains a session standing in the wrong place; a
		// line a minute after that would be the same sentence 1440
		// times a day.
		if attempt == 0 || attempt%10 == 0 {
			h.logf("not home (%s); trying again every %s", answer.saidOrSilence(), pace.retry)
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
// an answer provoked by a client's teleport is indistinguishable from
// an answer to ours -- which is harmless here and is why the loop stops
// on a client's REQUEST rather than trying to tell the answers apart.
// See the head of this file.
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

// trimNul drops the terminator the protocol puts on a variable string.
func trimNul(b []byte) string {
	if n := len(b); n > 0 && b[n-1] == 0 {
		b = b[:n-1]
	}
	return string(b)
}
