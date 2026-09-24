package main

// Listening: what arrives at an avatar, and what is done about it.
//
// Everything an avatar is told arrives as an instant message, and
// ImprovedInstantMessage is a dozen different messages wearing one
// name.  Three of them matter here:
//
//	a conversation      somebody talking, which may be a command
//	an inventory offer  somebody handing over an item or a folder
//	everything else     logged, and left for a person to answer
//
// A command is a remark that begins with the prefix.  Anything else is
// a remark, and a daemon that answered one would be a daemon holding a
// conversation it cannot hold.
//
// The rule about who is obeyed is in one place, Config.Trusts, and it
// is asked before the line is even split into words.  That is
// deliberate: the check is not a step in running a command, it is the
// gate in front of the whole of it.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
	"github.com/quark-idlemind/slgo/sl"
)

// IMDepth is how many instant messages may be waiting to be looked at.
//
// Generous, because what fills it is not conversation: a script talking
// to the avatar, a group with notices turned on, or a burst of offers
// all arrive here, and a subscription that overflows drops the message
// rather than blocking the session's reader.  A dropped command is
// silence, which is the one failure this daemon cannot report.
const IMDepth = 256

// serve reads what arrives at one avatar until the session ends.
//
// The subscription is taken here and the reading is a call, rather than
// both being inside one goroutine, because the two have to happen in
// that order and only the caller can see that they did.  A subscription
// registered a moment after the reading started would miss whatever
// arrived in between, which is a command that was sent and never
// answered -- the one failure this daemon cannot report.  It is also
// what lets a test deliver a message and know it will be seen.
func (b *bot) serve(ctx context.Context, s *sl.Session) {
	ims := s.IMs(IMDepth)
	defer s.StopIMs(ims)

	// What slgod says about the connection itself, which only a hosted
	// session has.  Taken here and passed in for the reason the
	// subscription above is: read is then a function of what arrives
	// rather than of what it can reach, and a test can hand it either.
	var notices <-chan *pb.AgentEvent
	if h, ok := s.Backend().(*sl.Hosted); ok {
		notices = h.Conn().Notices()
	}
	// What was handed over while this daemon was not attached, which
	// slgod kept and the subscription above will never deliver: it is
	// history, not news.  The same rule as for one that arrives now,
	// because a trusted avatar's gift is no less wanted for having been
	// made while this process was restarting.
	b.takeKept(ctx, s)
	b.read(ctx, s, ims, notices)
}

// takeKept answers the item offers slgod kept from before this daemon
// attached, by the rule offered applies to one that arrives live.
func (b *bot) takeKept(ctx context.Context, s *sl.Session) {
	for _, o := range s.InventoryOffers() {
		if o.Recorded {
			b.take(ctx, o)
		}
	}
}

// read is the listening loop proper.
func (b *bot) read(ctx context.Context, s *sl.Session, ims <-chan *sl.IM,
	notices <-chan *pb.AgentEvent) {
	// Endable from inside as well as from outside.  slgod's stream
	// deliberately OUTLIVES the session under it -- a session that
	// drops is re-established and the client keeps its stream and its
	// subscriptions across that -- so a session which is never coming
	// back does not end this loop by itself.  drainNotices is what
	// notices, and this is how it says so.
	ctx, gone := context.WithCancel(ctx)
	defer gone()

	// Jobs get a context of their own so that the end of the session
	// stops them: a benchmark still running against a session that has
	// gone is a benchmark that will fail slowly rather than at once.
	jobCtx, stopJobs := context.WithCancel(ctx)
	var jobs sync.WaitGroup
	defer func() {
		stopJobs()
		jobs.Wait()
	}()

	go b.drainNotices(jobCtx, notices, gone)
	// An avatar that has just been logged in may be missing part of
	// its outfit: the simulator puts most attachments back, not all.
	// See dress.go.
	go b.keepDressed(jobCtx, s)

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.Done():
			return
		case im, ok := <-ims:
			if !ok {
				return
			}
			b.arrived(jobCtx, s, im, &jobs)
		}
	}
}

// drainNotices reads what slgod says about the connection itself.
//
// It has to be read by somebody.  The relay's notice channel is small
// and a client that never reads it starts counting drops, and the
// notices are worth having anyway: a kick, a region change and a
// reconnect all arrive here and are the only warning a log gets that
// the session under an attendant has been replaced.
func (b *bot) drainNotices(ctx context.Context, notices <-chan *pb.AgentEvent, gone func()) {
	if notices == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case n, ok := <-notices:
			if !ok {
				return
			}
			detail := n.GetDetail()
			if detail == "" {
				detail = n.GetRegion()
			}
			b.logf("slgod reports %s %s", strings.ToLower(n.GetKind().String()), detail)

			// A session that ended may be coming back -- slgod
			// re-establishes one that dropped, under this same stream
			// -- or may not, if somebody logged the avatar out on
			// purpose.  The two look identical from here and the
			// stream ends for neither, so the attendant would sit
			// holding a session that no longer exists, believing it
			// was attached, until the daemon was restarted.  That is
			// exactly what it did.
			//
			// Which it is comes from asking slgod rather than from
			// reading the sentence it sent.  The words are for a
			// person; matching on them would be one more thing to be
			// wrong about when they change.
			switch n.GetKind() {
			case pb.AgentEvent_DISCONNECTED, pb.AgentEvent_KICKED:
				if b.d.stoppedAt(ctx, b.name) {
					b.logf("that session is not coming back; letting go of it")
					gone()
					return
				}
			}
		}
	}
}

// arrived decides what one message is and what happens to it.
func (b *bot) arrived(ctx context.Context, s *sl.Session, im *sl.IM, jobs *sync.WaitGroup) {
	// Something this avatar said through another client of the same
	// session.  It is kept, so that one avatar has one memory of what
	// it said whoever was driving, and it is never acted on: a daemon
	// that answered its own speech would be talking to itself, and
	// with "chat = *" it would not stop.
	if im.Mine {
		b.remember(s, im)
		return
	}

	switch im.Dialog {
	case sl.DialogTypingStart, sl.DialogTypingStop:
		// Two of these arrive for every remark anybody types.  There is
		// nothing to do about them and a log full of them is a log
		// nobody reads.
		return
	case sl.DialogInventoryOffered:
		b.offered(ctx, s, im)
		return
	}

	if !im.Conversation() {
		b.chatf("%s from %s: %s", sl.DialogName(im.Dialog), b.whoSaid(s, im), im.Text)
		return
	}

	text := strings.TrimSpace(im.Text)
	prefix := b.d.cfg.Prefix

	// Before anything else this remark leads to, and before the model
	// is asked, since the whole value of it is being told without
	// having asked.  It goes as its own message rather than on the
	// front of the reply: the reply is in character and paced to the
	// speed of somebody typing, and this is neither.
	b.reportTrouble(ctx, s, im)

	if !strings.HasPrefix(text, prefix) {
		b.chatf("%s says: %s", b.whoSaid(s, im), text)
		b.converse(ctx, s, im, jobs)
		return
	}

	line := strings.TrimSpace(strings.TrimPrefix(text, prefix))
	who := b.whoSaid(s, im)
	if !b.d.cfg.Trusts(im.From, im.FromName) {
		b.logf("refused a command from %s: %s", who, line)
		if b.d.cfg.AnswerStrangers {
			b.say(ctx, s, im.From, "I only take commands from people I have been told to trust.")
		}
		return
	}

	// The slot is taken here rather than inside the job, so that a
	// sender who has already filled this avatar is told so at once.
	select {
	case b.jobs <- struct{}{}:
	default:
		b.logf("busy: refused %q from %s", line, who)
		b.say(ctx, s, im.From, fmt.Sprintf(
			"%s is already running %d commands; try again when one has finished.",
			b.name, cap(b.jobs)))
		return
	}

	b.logf("%s: %s", who, text)
	jobs.Add(1)
	go func() {
		defer jobs.Done()
		defer func() { <-b.jobs }()
		b.obey(ctx, s, im, line)
	}()
}

// obey runs one command and sends back what it said.
//
// The timeout is the configuration's, and it is on the command rather
// than on the answer: a listing that took a minute to assemble is still
// worth sending, so the deadline is not carried into the sending.
func (b *bot) obey(ctx context.Context, s *sl.Session, im *sl.IM, line string) {
	run, cancel := context.WithTimeout(ctx, b.d.cfg.Timeout)
	defer cancel()

	r := &req{d: b.d, bot: b, from: im.From, who: im.FromName, base: ctx}
	var out bytes.Buffer
	err := r.Run(run, &out, line)

	text := out.String()
	if err != nil {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += err.Error()
	} else if strings.TrimSpace(text) == "" {
		// A command that did what it was asked and had nothing to
		// print still has to answer.  Silence from a daemon is
		// indistinguishable from a daemon that never heard.
		text = "ok"
	}

	// Sent on the session's context rather than the command's, so that
	// an answer is not lost to the deadline that produced it: a listing
	// that took a minute to assemble is still worth sending.  It is
	// still the session's context and not a detached one, because a
	// daemon being shut down should stop rather than finish delivering
	// a listing nobody is waiting for.
	send, cancel2 := context.WithTimeout(ctx, 60*time.Second)
	defer cancel2()
	if err := sendReply(send, s, im.From, text, b.d.cfg.ReplyLimit); err != nil {
		b.errf("could not answer %s: %v", b.whoSaid(s, im), err)
	}
}

// offered answers an inventory offer.
//
// Accepting is the point of the setting: a trusted avatar handing over a
// notecard, a script or a folder of them should not need anybody at a
// keyboard, and an offer nobody answers stays pending for ever.  An
// offer from somebody else is left exactly where it is rather than
// declined -- declining is a statement, and this daemon has nothing to
// say to a stranger about a gift it was not watching for.
//
// The offer is taken from the session rather than built from the
// message.  Both carry the same fields, but only the session's copy is
// tied to the session that can answer it, and the transaction id in it
// is the only thing the simulator will match an acceptance against.
func (b *bot) offered(ctx context.Context, s *sl.Session, im *sl.IM) {
	var offer *sl.InventoryOffer
	for _, o := range s.InventoryOffers() {
		if o.Transaction == im.ID {
			offer = o
			break
		}
	}
	if offer == nil {
		// The session keeps every offer it has been sent, so this is
		// not something that should happen -- unless another client
		// of this avatar answered it in the moment since it arrived,
		// which slgod says and the session acts on.  Saying so is
		// better than a gift that quietly went nowhere either way.
		what := im.Text
		if what == "" {
			what = "something"
		}
		b.logf("%s offered %q but the session is not holding the offer", b.whoSaid(s, im), what)
		return
	}
	b.take(ctx, offer)
}

// take is the rule itself: accept an offer from whoever the setting
// says, and leave the rest.
//
// Another client of the same avatar -- a person in slsh -- may have
// answered it first.  slgod says so, nothing is sent, and it is logged
// as that rather than as a failure: the offer was dealt with, just not
// here.
func (b *bot) take(ctx context.Context, offer *sl.InventoryOffer) {
	who := offer.FromName
	if who == "" {
		who = offer.From.String()
	}
	what := offer.Name
	if what == "" {
		what = "something"
	}

	take := false
	switch b.d.cfg.AcceptInventory {
	case AcceptAnyone:
		take = true
	case AcceptTrusted:
		take = b.d.cfg.Trusts(offer.From, offer.FromName)
	case AcceptNobody:
	}
	if !take {
		b.logf("%s offered %q; left waiting", who, what)
		return
	}

	// A zero folder is what a viewer sends when somebody clicks Accept
	// rather than dragging the item somewhere: the grid files it under
	// whatever kind of thing it is.
	accept, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := offer.Accept(accept, msg.UUID{}); err != nil {
		var already *sl.AnsweredError
		if errors.As(err, &already) {
			b.logf("%s", already)
			return
		}
		b.errf("could not accept %q from %s: %v", what, who, err)
		return
	}
	b.logf("accepted %q from %s", what, who)
}

// whoSaid is the best name there is for whoever sent a message.
func (b *bot) whoSaid(s *sl.Session, im *sl.IM) string {
	if im.FromName != "" {
		return im.FromName
	}
	if n := s.NameOr(im.From); n != "" {
		return n
	}
	return im.From.String()
}

// say sends one short remark and logs a failure rather than reporting
// it, for the places that are already handling something else.
func (b *bot) say(ctx context.Context, s *sl.Session, to msg.UUID, text string) {
	send, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.SendIM(send, to, text); err != nil {
		b.errf("could not send to %s: %v", to, err)
	}
}

// reportTrouble tells an admin, once, that things have gone wrong.
//
// Only somebody trusted, because it is a report about the daemon and
// means nothing to anybody else -- and because an avatar reciting its
// own faults to a stranger is both out of character and more than they
// should be told about the machine it runs on.
//
// Only on a FRESH approach: after a silence longer than error-gap, or
// the first thing this person has ever said to this avatar.  Somebody
// working with an avatar all afternoon has been told already.
//
// Whether it is fresh is decided for every remark, command or not, so
// that the reckoning of when somebody last spoke does not depend on
// what they said.
func (b *bot) reportTrouble(ctx context.Context, s *sl.Session, im *sl.IM) {
	quiet, known := b.sinceSeen(im.From, time.Now())
	if !b.d.cfg.Trusts(im.From, im.FromName) {
		return
	}
	if known && quiet < b.d.cfg.ErrorGap {
		return
	}
	if b.trouble.unreported() == 0 {
		return
	}
	line := troubleNotice(b.trouble.noteReported(), b.d.cfg.Prefix)
	if line == "" {
		return
	}
	b.logf("told %s about what has gone wrong", b.whoSaid(s, im))
	b.say(ctx, s, im.From, line)
}

// ------------------------------------------------------------ conversation

// converse decides whether to answer a remark, and sets about it.
//
// The deciding is Audience's and happens here, on the message handler's
// own goroutine, because it is cheap and because a decision not to
// speak should not cost a worker.  The answering is slow -- a model
// takes seconds where a command takes milliseconds -- so it goes to a
// goroutine with a budget of its own.
func (b *bot) converse(ctx context.Context, s *sl.Session, im *sl.IM, jobs *sync.WaitGroup) {
	if b.d.chat == nil {
		return
	}
	who := b.whoSaid(s, im)
	conv := b.d.chat.Store().Load(b.name, im.From, im.FromName)

	v := b.d.audience(ctx, &Approach{
		Avatar:     b.name,
		AvatarName: s.Info().AvatarName,
		From:       im.From,
		Name:       im.FromName,
		Text:       im.Text,
		Trusted:    b.d.cfg.Trusts(im.From, im.FromName),
		Known:      len(conv.Turns) > 0,
		Turns:      conv.Compacted + len(conv.Turns),
		Recent:     conv.Recent(time.Now(), b.d.cfg.ChatOwnRest),
	})
	if !v.Talk {
		// logf and not chatf: -q drops what other people said, which is
		// small talk, and this is a DECISION.  An avatar that stays
		// silent looks exactly like one that is broken, and the line
		// saying which is the only thing between the two -- so it is
		// the last thing that should be droppable.  It was chatf, and
		// the first time a bound refused a conversation the log said
		// nothing at all.
		b.logf("not answering %s: %s", who, v.Why)
		return
	}

	// A sender who has filled this avatar's conversation budget is
	// answered by silence rather than by a queue.  An instant message
	// is a conversation: an answer that arrives four minutes later,
	// behind two others, is worse than none.
	select {
	case b.chatJobs <- struct{}{}:
	default:
		b.logf("too busy talking to answer %s", who)
		return
	}

	jobs.Add(1)
	go func() {
		defer jobs.Done()
		defer func() { <-b.chatJobs }()
		b.answer(ctx, s, im, who)
	}()
}

// answer asks the model and says what it said.
//
// A failure is logged and nothing is sent.  There is no useful thing to
// tell somebody whose remark could not be answered -- they did not ask
// this daemon a question, they spoke to an avatar -- and "the model is
// down" said to a stranger is worse than the silence it replaces.
func (b *bot) answer(ctx context.Context, s *sl.Session, im *sl.IM, who string) {
	// Room beyond the model's own timeout for restoring the context
	// and writing it back, which are milliseconds, and for a server
	// that is thinking about it.
	run, cancel := context.WithTimeout(ctx, b.d.cfg.LLMTimeout+30*time.Second)
	defer cancel()

	started := time.Now()
	text, err := b.d.chat.Reply(run, b.name, im.From, im.FromName, im.Text)
	if err != nil {
		b.errf("could not answer %s: %v", who, err)
		return
	}
	b.logf("answered %s in %s: %s", who, time.Since(started).Round(time.Millisecond), text)

	// Held back until a person could have read the remark and written
	// that, counting the time the model already took.  See pace.go.
	arrived := im.At
	if arrived.IsZero() {
		arrived = started
	}
	b.wait(ctx, s, im.From, arrived, b.d.chat.pace(b.name, im.Text, text))

	send, cancel2 := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel2()
	if err := sendReply(send, s, im.From, text, b.d.cfg.ChatReply); err != nil {
		b.errf("could not send the answer to %s: %v", who, err)
	}
}

// remember keeps a line this avatar said through another client.
//
// Only conversation: the rest of what a client sends as this avatar --
// an inventory offer being accepted, a teleport answered, a typing
// notice -- is not something anybody said, and a memory of it would be
// noise in the note.  See Chatter.Remember for why it is kept at all
// and why it is never answered.
func (b *bot) remember(s *sl.Session, im *sl.IM) {
	if b.d.chat == nil || !im.Spoken() {
		return
	}
	name := s.NameOr(im.To)
	if name == "" {
		name = im.To.String()
	}
	via := im.Via
	if via == "" {
		via = "another client"
	}
	if b.d.chat.Remember(b.name, im.To, name, im.Text) {
		b.chatf("%s said to %s through %s: %s", b.name, name, via, im.Text)
	}
}
