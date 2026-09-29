package sl

// Conferences: an instant message with more than two people in it.
//
// It is the machinery of group chat (groupchat.go) under a session that
// is not a group's: started by a post to ChatSessionRequest rather than
// by a message, with a session id the grid chooses, and addressed to its
// "other participant" rather than to itself.  There is no way to remove
// anybody from one, and none is built.  What the viewer does, and what is
// read from its source rather than measured: doc/conference.md

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// ConferenceName is what the viewer calls a conference this avatar
// starts (the string conference-title).
const ConferenceName = "Multi-person chat"

// ErrNotInConference is acting in a conference this session has not
// joined or does not know of.
var ErrNotInConference = errors.New("sl: this session is not in that conference")

// ErrNotInvited is accepting a conference nobody has invited this
// session into.
var ErrNotInvited = errors.New("sl: no invitation to that conference is waiting")

// sessionKind is what a chat session is.  Unknown is one not yet told
// apart, which is treated as a group's, as it was before conferences.
type sessionKind uint8

const (
	sessionUnknown sessionKind = iota
	sessionGroup
	sessionConference
)

// confStart is a conference being started: who with, and the answer.
type confStart struct {
	ids     []msg.UUID
	reply   *groupChatReply // nil while waiting
	session msg.UUID
}

// Conference is one conference this session knows of.
type Conference struct {
	ID   msg.UUID
	Name string

	// Joined says the grid has told this session it is in it; Invited
	// that somebody invited it into one it has not joined.
	Joined, Invited bool

	// Members is who the grid last said is in it, and Guests everybody
	// this session invited into it, both in id order.
	Members, Guests []msg.UUID
}

// conferenceTitle is what the viewer calls an invitation's session: the
// name the grid gave it, else "Conference with" whoever invited.
// Why: doc/conference.md#how-an-invitation-reaches-an-avatar-that-is-not-in-it
func conferenceTitle(name, from string) string {
	if name != "" {
		return name
	}
	if from == "" {
		return "Conference"
	}
	return "Conference with " + from
}

// groupListed says whether the avatar's group list has the session, and
// whether there is a list to say so: an empty one is "not told yet" as
// well as "none".
func (w *Session) groupListed(id msg.UUID) (in, listed bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p, err := w.presence(ctx, 0)
	if err != nil || len(p.Groups) == 0 {
		return false, false
	}
	for _, g := range p.Groups {
		if g.ID == id {
			return true, true
		}
	}
	return false, true
}

// classify says what a session is: what this session already knows, else
// the group list's word, which is remembered.  Without a list it is
// unknown, which the callers take as a group.
// Why: doc/conference.md#hearing
func (w *Session) classify(id msg.UUID) sessionKind {
	w.mu.Lock()
	if st := w.gchat[id]; st != nil && st.kind != sessionUnknown {
		k := st.kind
		w.mu.Unlock()
		return k
	}
	w.mu.Unlock()
	in, listed := w.groupListed(id)
	if !listed {
		return sessionUnknown
	}
	k := sessionConference
	if in {
		k = sessionGroup
	}
	w.mu.Lock()
	w.chatState(id).kind = k
	w.mu.Unlock()
	return k
}

// isConference says the session is known to be a conference.
func (w *Session) isConference(id msg.UUID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.gchat[id]
	return st != nil && st.kind == sessionConference
}

// conferenceOther is where a conference's messages are addressed: its
// other participant, which for one this avatar was invited into is the
// session itself.  The caller holds w.mu.
func conferenceOther(session msg.UUID, st *groupChatState) msg.UUID {
	if st != nil && !st.other.IsZero() {
		return st.other
	}
	return session
}

// Conferences lists the conferences this session knows of, in the order
// it first heard of them.  A conference left stays listed, unjoined, so
// that the numbers a caller gave them do not move.
func (w *Session) Conferences() []Conference {
	w.mu.Lock()
	defer w.mu.Unlock()
	type row struct {
		seq int
		c   Conference
	}
	var rows []row
	for id, st := range w.gchat {
		if st.kind != sessionConference {
			continue
		}
		c := Conference{ID: id, Name: st.name, Joined: st.joined, Invited: st.invited,
			Members: sortedIDs(st.members), Guests: sortedIDs(st.guests)}
		rows = append(rows, row{st.seq, c})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].seq < rows[j].seq })
	out := make([]Conference, len(rows))
	for i, r := range rows {
		out[i] = r.c
	}
	return out
}

func sortedIDs(set map[msg.UUID]bool) []msg.UUID {
	out := make([]msg.UUID, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i][:]) < string(out[j][:]) })
	return out
}

// guestList checks the people to put in a conference: somebody, not this
// avatar, nobody twice.
func (w *Session) guestList(people []msg.UUID) ([]msg.UUID, error) {
	seen := map[msg.UUID]bool{}
	for _, p := range people {
		switch {
		case p.IsZero():
			return nil, fmt.Errorf("sl: no person to put in the conference")
		case p == w.me:
			return nil, fmt.Errorf("sl: this avatar is in every conference it starts; name the others")
		case seen[p]:
			return nil, fmt.Errorf("sl: %s is named twice", w.NameOr(p))
		}
		seen[p] = true
	}
	return people, nil
}

// StartConference starts a conference with people, in the order given,
// and waits up to Options.GroupChatTimeout for the grid to say it has.
// It returns the session's id, which the grid chooses.
//
// It needs at least two people besides this avatar.  A conference this
// session started with the same people and still holds is returned and
// nothing is sent, as the viewer does.  Otherwise it posts the start to
// ChatSessionRequest, and to the older message when the grid answers 400.
// A refusal is a *GroupChatError with Conference set.
func (w *Session) StartConference(ctx context.Context, people ...msg.UUID) (msg.UUID, error) {
	ids, err := w.guestList(people)
	if err != nil {
		return msg.UUID{}, err
	}
	if len(ids) < 2 {
		return msg.UUID{}, fmt.Errorf("sl: a conference needs at least two other people; " +
			"an instant message is what one person is")
	}
	if id, ok := w.sameConference(ids); ok {
		return id, nil
	}
	return w.startConference(ctx, ids)
}

// ConferenceFromIM turns an instant message into a conference by adding
// people to it, as the viewer does: it leaves the message's session,
// then starts a conference with the other person first and the added
// people after.  The leave goes first, so a start that fails leaves the
// instant message all the same.
// Why: doc/conference.md#adding-people-to-a-conference
func (w *Session) ConferenceFromIM(ctx context.Context, with msg.UUID, add ...msg.UUID) (msg.UUID, error) {
	ids, err := w.guestList(append([]msg.UUID{with}, add...))
	if err != nil {
		return msg.UUID{}, err
	}
	if len(ids) < 2 {
		return msg.UUID{}, fmt.Errorf("sl: nobody was added to the instant message")
	}
	if err := w.Send(ctx, w.sessionIM(with, imSessionID(w.me, with), DialogSessionLeave, "")); err != nil {
		return msg.UUID{}, err
	}
	if id, ok := w.sameConference(ids); ok {
		return id, nil
	}
	return w.startConference(ctx, ids)
}

// sameConference finds a conference this session started with exactly
// these people, still joined.
func (w *Session) sameConference(ids []msg.UUID) (msg.UUID, bool) {
	want := map[msg.UUID]bool{}
	for _, id := range ids {
		want[id] = true
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var best msg.UUID
	var bestSeq int
	for id, st := range w.gchat {
		if st.kind != sessionConference || !st.joined || len(st.initial) != len(ids) {
			continue
		}
		same := true
		for _, p := range st.initial {
			same = same && want[p]
		}
		if same && (best.IsZero() || st.seq < bestSeq) {
			best, bestSeq = id, st.seq
		}
	}
	return best, !best.IsZero()
}

func (w *Session) startConference(ctx context.Context, ids []msg.UUID) (msg.UUID, error) {
	temp := randomUUID()
	cs := &confStart{ids: append([]msg.UUID(nil), ids...)}
	w.mu.Lock()
	w.confStarts[temp] = cs
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.confStarts, temp)
		w.mu.Unlock()
	}()

	if err := w.postConferenceStart(ctx, temp, ids); err != nil {
		return msg.UUID{}, err
	}
	var reply *groupChatReply
	err := w.await(ctx, w.groupChatWait(), "the grid's answer to starting the conference", func() bool {
		reply = cs.reply
		return reply != nil
	})
	if err != nil {
		return msg.UUID{}, err
	}
	if !reply.ok {
		return msg.UUID{}, reply.err
	}
	return cs.session, nil
}

// postConferenceStart asks for the conference: a post to
// ChatSessionRequest, and when the grid answers 400 the message the
// viewer falls back on.
// Why: doc/conference.md#starting-one
func (w *Session) postConferenceStart(ctx context.Context, temp msg.UUID, ids []msg.UUID) error {
	if !w.b.HasCap(ChatSessionCap) {
		return fmt.Errorf("sl: this region offers no %s", ChatSessionCap)
	}
	body, err := llsd.Encode(map[string]any{
		"method":     "start conference",
		"session-id": llsd.UUID(temp.String()),
		"params":     uuidArray(ids),
		"alt_params": map[string]any{"voice_server_type": w.voiceServerType(ctx)},
	})
	if err != nil {
		return err
	}
	_, err = w.capDo(ctx, agent.CapRequest{
		Cap: ChatSessionCap, Method: "POST", Body: body, Type: "application/llsd+xml",
	})
	var ce *CapError
	if errors.As(err, &ce) && ce.Status == 400 {
		return w.Send(ctx, w.deprecatedConferenceStart(temp, ids))
	}
	return err
}

func uuidArray(ids []msg.UUID) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = llsd.UUID(id.String())
	}
	return out
}

// voiceServerType is what the viewer sends when its own setting is
// empty: the region's SimulatorFeatures VoiceServerType, "vivox" when it
// names none.
func (w *Session) voiceServerType(ctx context.Context) string {
	if w.b.HasCap("SimulatorFeatures") {
		if f, err := w.Features(ctx); err == nil {
			if s := llsd.String(f.Raw, "VoiceServerType"); s != "" {
				return s
			}
		}
	}
	return "vivox"
}

// deprecatedConferenceStart is the start the viewer sends when the post
// is refused with 400: dialog 16 to the first person, the temporary id as
// the session, and the people's ids one after another as the bucket.
func (w *Session) deprecatedConferenceStart(temp msg.UUID, ids []msg.UUID) *msg.ImprovedInstantMessage {
	m := w.sessionIM(ids[0], temp, DialogSessionConferenceStart, "")
	m.MessageBlock.BinaryBucket = nil
	for _, id := range ids {
		m.MessageBlock.BinaryBucket = append(m.MessageBlock.BinaryBucket, id[:]...)
	}
	if where, err := w.Where(context.Background()); err == nil {
		m.MessageBlock.Position = where.Position
	}
	return m
}

// conferenceStartReply reads a ChatterBoxSessionStartReply that answers
// a conference start, and says it did.
func (w *Session) conferenceStartReply(m map[string]any) bool {
	temp := parseUUIDOrZero(llsd.String(m, "temp_session_id"))
	w.mu.Lock()
	cs := w.confStarts[temp]
	w.mu.Unlock()
	if cs == nil {
		return false
	}
	if !llsd.Bool(m, "success") {
		key := llsd.String(m, "error")
		if key == "" {
			key = llsd.String(m, "reason")
		}
		w.mu.Lock()
		cs.reply = &groupChatReply{err: &GroupChatError{Group: temp, Conference: true,
			Key: key, Reason: chatReason(key)}}
		w.mu.Unlock()
		return true
	}
	session := parseUUIDOrZero(llsd.String(m, "session_id"))
	if session.IsZero() {
		session = temp
	}
	w.noteChatMembers(session, m)
	w.mu.Lock()
	st := w.chatState(session)
	st.kind = sessionConference
	st.joined, st.invited = true, false
	st.name = ConferenceName
	st.other = cs.ids[0]
	st.initial = cs.ids
	if st.guests == nil {
		st.guests = map[msg.UUID]bool{}
	}
	for _, id := range cs.ids {
		st.guests[id] = true
	}
	cs.session = session
	cs.reply = &groupChatReply{ok: true}
	w.mu.Unlock()
	w.deliverGroupChat(&GroupChat{At: time.Now(), Kind: GroupChatJoined, Group: session,
		GroupName: ConferenceName, Conference: true})
	return true
}

// AddToConference invites people into a conference this session is in,
// as the viewer does: a post to ChatSessionRequest.  It leaves out
// anybody already in it or already invited, and refuses when that is
// everybody.  Nothing answers the post but its status, and the grid
// says who came in as an event.
func (w *Session) AddToConference(ctx context.Context, session msg.UUID, people ...msg.UUID) error {
	ids, err := w.guestList(people)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("sl: nobody to add to the conference")
	}
	w.mu.Lock()
	st := w.gchat[session]
	if st == nil || st.kind != sessionConference || !st.joined {
		w.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotInConference, session)
	}
	var fresh []msg.UUID
	for _, id := range ids {
		if !st.members[id] && !st.guests[id] {
			fresh = append(fresh, id)
		}
	}
	w.mu.Unlock()
	if len(fresh) == 0 {
		return fmt.Errorf("sl: everybody named is already in the conference or invited to it")
	}
	if !w.b.HasCap(ChatSessionCap) {
		return fmt.Errorf("sl: this region offers no %s", ChatSessionCap)
	}
	body, err := llsd.Encode(map[string]any{
		"method":     "invite",
		"session-id": llsd.UUID(session.String()),
		"params":     uuidArray(fresh),
	})
	if err != nil {
		return err
	}
	if _, err := w.capDo(ctx, agent.CapRequest{
		Cap: ChatSessionCap, Method: "POST", Body: body, Type: "application/llsd+xml",
	}); err != nil {
		return err
	}
	w.mu.Lock()
	if st := w.gchat[session]; st != nil {
		if st.guests == nil {
			st.guests = map[msg.UUID]bool{}
		}
		for _, id := range fresh {
			st.guests[id] = true
		}
	}
	w.mu.Unlock()
	return nil
}

// SayToConference speaks in a conference this session has joined,
// splitting text as the viewer does.  The messages go to the
// conference's other participant with the session as their id.
func (w *Session) SayToConference(ctx context.Context, session msg.UUID, text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("sl: nothing to say")
	}
	w.mu.Lock()
	st := w.gchat[session]
	if st == nil || st.kind != sessionConference || !st.joined {
		w.mu.Unlock()
		return fmt.Errorf("%w: %s; join it first", ErrNotInConference, session)
	}
	to := conferenceOther(session, st)
	w.mu.Unlock()
	for _, part := range splitForIM(text) {
		if err := w.Send(ctx, w.sessionIM(to, session, DialogSessionSend, part)); err != nil {
			return err
		}
	}
	return nil
}

// LeaveConference ends this avatar's part in a conference, and declines
// an invitation to one by the same message.  It is sent whether or not
// this session thinks it joined.  Nothing answers it.
func (w *Session) LeaveConference(ctx context.Context, session msg.UUID) error {
	if session.IsZero() {
		return fmt.Errorf("sl: no conference to leave")
	}
	w.mu.Lock()
	to := conferenceOther(session, w.gchat[session])
	w.mu.Unlock()
	if err := w.Send(ctx, w.sessionIM(to, session, DialogSessionLeave, "")); err != nil {
		return err
	}
	w.mu.Lock()
	if st := w.gchat[session]; st != nil {
		st.joined, st.invited = false, false
		clear(st.members)
	}
	w.mu.Unlock()
	return nil
}

// AcceptConference answers an invitation into a conference by joining,
// as GroupChat.Accept does for the event that announced it.
func (w *Session) AcceptConference(ctx context.Context, session msg.UUID) error {
	w.mu.Lock()
	st := w.gchat[session]
	ok := st != nil && st.kind == sessionConference && st.invited
	w.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotInvited, session)
	}
	return w.acceptGroupChat(ctx, session)
}

// noteConferenceInvitation records an invitation to a session found to
// be a conference: its name, and that it is waiting.  Its messages are
// addressed to the session itself, which conferenceOther does when no
// other participant is recorded.
func (w *Session) noteConferenceInvitation(session msg.UUID, name string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.chatState(session)
	st.kind = sessionConference
	if name != "" {
		st.name = name
	}
	if !st.joined {
		st.invited = true
	}
}

// conferenceInvitedEvent reads the "immediate" form of an invitation,
// which is a conference's alone, and carries no text.  One for a group
// this avatar belongs to is that group's voice call and is not a
// conference.
func (w *Session) conferenceInvitedEvent(m map[string]any) {
	session := parseUUIDOrZero(llsd.String(m, "session_id"))
	from := parseUUIDOrZero(llsd.String(m, "from_id"))
	if session.IsZero() || from == w.me {
		return
	}
	if in, listed := w.groupListed(session); listed && in {
		return
	}
	fromName := llsd.String(m, "from_name")
	w.learn(from, fromName)
	name := conferenceTitle(llsd.String(m, "session_name"), fromName)
	w.noteConferenceInvitation(session, name)
	g := &GroupChat{
		At: time.Now(), Kind: GroupChatInvited, Group: session, GroupName: name, Conference: true,
		From: from, FromName: fromName, w: w,
	}
	g.speaker = (&IM{Dialog: DialogSessionSend, From: from, FromName: fromName}).Sender()
	w.deliverGroupChat(g)
}
