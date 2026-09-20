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
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/quark-idlemind/slgo/msg"
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
	b.read(ctx, s, ims)
}

// read is the listening loop proper.
func (b *bot) read(ctx context.Context, s *sl.Session, ims <-chan *sl.IM) {
	// Jobs get a context of their own so that the end of the session
	// stops them: a benchmark still running against a session that has
	// gone is a benchmark that will fail slowly rather than at once.
	jobCtx, stopJobs := context.WithCancel(ctx)
	var jobs sync.WaitGroup
	defer func() {
		stopJobs()
		jobs.Wait()
	}()

	go b.drainNotices(jobCtx, s)

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
func (b *bot) drainNotices(ctx context.Context, s *sl.Session) {
	h, ok := s.Backend().(*sl.Hosted)
	if !ok {
		return
	}
	notices := h.Conn().Notices()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.Done():
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
		b.logf("could not answer %s: %v", b.whoSaid(s, im), err)
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
	who := b.whoSaid(s, im)
	what := im.Text
	if what == "" {
		what = "something"
	}

	take := false
	switch b.d.cfg.AcceptInventory {
	case AcceptAnyone:
		take = true
	case AcceptTrusted:
		take = b.d.cfg.Trusts(im.From, im.FromName)
	case AcceptNobody:
	}
	if !take {
		b.logf("%s offered %q; left waiting", who, what)
		return
	}

	var offer *sl.InventoryOffer
	for _, o := range s.InventoryOffers() {
		if o.Transaction == im.ID {
			offer = o
			break
		}
	}
	if offer == nil {
		// The session keeps every offer it has been sent, so this is
		// not something that should happen; saying so is better than a
		// gift that quietly went nowhere.
		b.logf("%s offered %q but the session is not holding the offer", who, what)
		return
	}

	// A zero folder is what a viewer sends when somebody clicks Accept
	// rather than dragging the item somewhere: the grid files it under
	// whatever kind of thing it is.
	accept, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := offer.Accept(accept, msg.UUID{}); err != nil {
		b.logf("could not accept %q from %s: %v", what, who, err)
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
		b.logf("could not send to %s: %v", to, err)
	}
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
		Turns:      len(conv.Turns),
	})
	if !v.Talk {
		b.chatf("not answering %s: %s", who, v.Why)
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
		b.logf("could not answer %s: %v", who, err)
		return
	}
	b.logf("answered %s in %s: %s", who, time.Since(started).Round(time.Millisecond), text)

	send, cancel2 := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel2()
	if err := sendReply(send, s, im.From, text, b.d.cfg.ChatReply); err != nil {
		b.logf("could not send the answer to %s: %v", who, err)
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
