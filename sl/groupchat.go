package sl

// Group chat: joining a group's chat, hearing it, speaking in it and
// leaving it.
//
// Nothing here joins on its own.  The viewer answers every invitation it
// does not drop, so it is in a group's chat from the moment somebody
// first speaks; this package surfaces the invitation as an event and
// leaves joining to the caller, since an avatar in a busy group would
// otherwise be flooded.  What the viewer does, step by step, and what is
// read from its source rather than measured: doc/group-chat.md

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// ChatSessionCap is the capability an invitation is answered through.
const ChatSessionCap = "ChatSessionRequest"

// ErrNotInGroup is joining the chat of a group this avatar has not
// joined.  The viewer refuses the same way (llgroupactions.cpp:673-679).
var ErrNotInGroup = errors.New("sl: this avatar is not a member of that group")

// ErrNotInGroupChat is speaking in a group's chat this session has not
// joined, which the grid would answer with a refusal nobody is waiting
// for.
var ErrNotInGroupChat = errors.New("sl: this session has not joined that group's chat")

// GroupChatError is the grid refusing to start a group's chat or to let
// this avatar go on in it.
type GroupChatError struct {
	Group msg.UUID

	// Key is the name of the string the grid answered with, and Reason
	// what the viewer says for it, or Key again when it has no words.
	Key, Reason string
}

func (e *GroupChatError) Error() string {
	return fmt.Sprintf("sl: the grid refused group %s's chat: %s", e.Group, e.Reason)
}

// groupChatReasons are the viewer's words for the reasons the grid
// gives, which are the names of strings in its strings.xml.
// Why: doc/group-chat.md#the-events
var groupChatReasons = map[string]string{
	"generic":                      "please close and reopen the conversation, or relog and try again",
	"generic_request_error":        "please close and reopen the conversation, or relog and try again",
	"insufficient_perms_error":     "you do not have sufficient permissions",
	"session_does_not_exist_error": "the session no longer exists",
	"no_ability":                   "you do not have that ability",
	"no_ability_error":             "you do not have that ability",
	"not_a_mod_error":              "you are not a session moderator",
	"muted":                        "a group moderator disabled your text chat",
	"muted_error":                  "a group moderator disabled your text chat",
	"removed":                      "you have been removed from the group",
	"removed_from_group":           "you have been removed from the group",
	"close_on_no_ability":          "you no longer have the ability to be in the chat session",
	"message":                      "the message is still being processed; if it does not appear in a few minutes it may have been dropped",
	"message_session_event":        "the message is still being processed; if it does not appear in a few minutes it may have been dropped",
}

func chatReason(key string) string {
	if r, ok := groupChatReasons[key]; ok {
		return r
	}
	if key == "" {
		return "no reason given"
	}
	return key
}

// GroupChatKind says what a GroupChat event is.
type GroupChatKind uint8

const (
	// GroupChatSaid is somebody speaking, this avatar included when the
	// grid sends its words back or another client of it spoke.
	GroupChatSaid GroupChatKind = iota

	// GroupChatInvited is the grid opening a session because somebody
	// spoke, and asking whether to be in it.  Text is what was said,
	// which arrives inside the invitation and not again.  Nothing is
	// answered until Accept or JoinGroupChat.
	GroupChatInvited

	// GroupChatJoined is the grid saying this avatar is in the chat.
	GroupChatJoined

	// GroupChatEntered and GroupChatLeft are somebody else coming and
	// going; From is who.
	GroupChatEntered
	GroupChatLeft

	// GroupChatClosed is the grid ending this avatar's part in it; Text
	// is why.
	GroupChatClosed

	// GroupChatRefused is the grid refusing to take a message; Text is
	// why.  A refused start is not one: JoinGroupChat returns it.
	GroupChatRefused
)

func (k GroupChatKind) String() string {
	switch k {
	case GroupChatSaid:
		return "said"
	case GroupChatInvited:
		return "invited"
	case GroupChatJoined:
		return "joined"
	case GroupChatEntered:
		return "entered"
	case GroupChatLeft:
		return "left"
	case GroupChatClosed:
		return "closed"
	case GroupChatRefused:
		return "refused"
	}
	return fmt.Sprintf("kind %d", uint8(k))
}

// GroupChat is one thing that happened in a group's chat.
//
// Group is the group and From who spoke, invited or came; GroupName is
// what the event itself said the group was called, which only an
// invitation does, so a caller that prints it names the group from its
// own list first.  Both names are printed through Sender.Label and never
// bare: a group's name is whatever its founder chose.
type GroupChat struct {
	At        time.Time
	Kind      GroupChatKind
	Group     msg.UUID
	GroupName string
	From      msg.UUID
	FromName  string
	Text      string

	// Mine says this avatar spoke, through another client of the same
	// session, and Via names it; see IM.Mine.
	Mine bool
	Via  string

	speaker Sender
	w       *Session
}

// Sender says what FromName names: a person, or the grid for a message
// signed with its name.
func (g *GroupChat) Sender() Sender { return g.speaker }

// Accept answers an invitation by joining, as the viewer does: a POST to
// ChatSessionRequest.  It is for a GroupChatInvited event only.
func (g *GroupChat) Accept(ctx context.Context) error {
	if g.Kind != GroupChatInvited || g.w == nil {
		return fmt.Errorf("sl: only an invitation to a group's chat is accepted")
	}
	return g.w.acceptGroupChat(ctx, g.Group)
}

// groupChatState is what this session knows of one group's chat.
type groupChatState struct {
	joined  bool
	name    string
	reply   *groupChatReply // the answer to the last start; nil while waiting
	members map[msg.UUID]bool
}

type groupChatReply struct {
	ok  bool
	err *GroupChatError
}

// groupChatSub is a subscription to group chat.
type groupChatSub struct {
	ch      chan *GroupChat
	dropped atomic.Uint64
}

// DefaultGroupChatDepth is the buffer a subscription gets when none is
// asked for.
const DefaultGroupChatDepth = 64

// GroupChats returns a channel of what happens in group chats, closed
// when StopGroupChats is called or the session ends.
func (w *Session) GroupChats(depth int) <-chan *GroupChat {
	if depth <= 0 {
		depth = DefaultGroupChatDepth
	}
	sub := &groupChatSub{ch: make(chan *GroupChat, depth)}
	if !w.addSub(func() { w.gchatSubs[sub.ch] = sub }) {
		close(sub.ch)
	}
	return sub.ch
}

// StopGroupChats closes a subscription, and returns once it is closed.
func (w *Session) StopGroupChats(ch <-chan *GroupChat) {
	w.onReader(func() {
		if s := w.gchatSubs[ch]; s != nil {
			delete(w.gchatSubs, ch)
			close(s.ch)
		}
	})
}

// GroupChatsDropped is how many a subscription missed because its buffer
// was full.
func (w *Session) GroupChatsDropped(ch <-chan *GroupChat) uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s := w.gchatSubs[ch]; s != nil {
		return s.dropped.Load()
	}
	return 0
}

func (w *Session) deliverGroupChat(g *GroupChat) {
	w.mu.Lock()
	subs := make([]*groupChatSub, 0, len(w.gchatSubs))
	for _, s := range w.gchatSubs {
		subs = append(subs, s)
	}
	w.mu.Unlock()
	for _, s := range subs {
		select {
		case s.ch <- g:
		default:
			s.dropped.Add(1)
		}
	}
}

// chatState is the record of one group's chat, made on first use.  The
// caller holds w.mu.
func (w *Session) chatState(group msg.UUID) *groupChatState {
	if w.gchat == nil {
		w.gchat = map[msg.UUID]*groupChatState{}
	}
	st := w.gchat[group]
	if st == nil {
		st = &groupChatState{members: map[msg.UUID]bool{}}
		w.gchat[group] = st
	}
	return st
}

// InGroupChat says this session has been told it is in a group's chat.
func (w *Session) InGroupChat(group msg.UUID) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.gchat[group]
	return st != nil && st.joined
}

// GroupChatMembers is who the grid last said is in a group's chat, in id
// order.
func (w *Session) GroupChatMembers(group msg.UUID) []msg.UUID {
	w.mu.Lock()
	defer w.mu.Unlock()
	st := w.gchat[group]
	if st == nil {
		return nil
	}
	out := make([]msg.UUID, 0, len(st.members))
	for id := range st.members {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i][:]) < string(out[j][:]) })
	return out
}

// mayChat refuses a group this avatar has not joined, when the list says
// so.  An empty list is not read as "belongs to none": it arrives
// unasked, as mayInvite explains.
func (w *Session) mayChat(ctx context.Context, group msg.UUID) error {
	p, err := w.Where(ctx)
	if err != nil {
		return err
	}
	if len(p.Groups) == 0 {
		return nil
	}
	for _, g := range p.Groups {
		if g.ID == group {
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrNotInGroup, group)
}

// groupIM is the message the viewer sends for a group's session: the
// group as recipient and as session id, no region, no position, an
// empty one-byte bucket.
// Why: doc/group-chat.md#speaking
func (w *Session) groupIM(group msg.UUID, dialog uint8, text string) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	b := &m.MessageBlock
	b.ToAgentID = group
	b.Dialog = dialog
	b.Offline = 0 // IM_ONLINE
	b.ID = group
	b.FromAgentName = append([]byte(w.Info().AvatarName), 0)
	b.Message = append([]byte(text), 0)
	b.BinaryBucket = []byte{0}
	return m
}

// JoinGroupChat starts this avatar's part in a group's chat and waits
// for the grid to say it has, up to Options.GroupChatTimeout.
//
// It sends what the viewer sends to open a group's window, dialog 15 to
// the group, and the answer is a ChatterBoxSessionStartReply on the
// event queue.  A refusal is a *GroupChatError; a group this avatar
// has not joined is ErrNotInGroup.  Starting a chat already started is
// sent again, and what the grid makes of that is not known.
func (w *Session) JoinGroupChat(ctx context.Context, group msg.UUID) error {
	if group.IsZero() {
		return fmt.Errorf("sl: no group to join the chat of")
	}
	if err := w.mayChat(ctx, group); err != nil {
		return err
	}
	m := w.groupIM(group, DialogSessionGroupStart, "")
	// The start carries the avatar's position, which the others do not.
	if where, err := w.Where(ctx); err == nil {
		m.MessageBlock.Position = where.Position
	}

	w.mu.Lock()
	st := w.chatState(group)
	st.reply = nil
	w.mu.Unlock()

	if err := w.Send(ctx, m); err != nil {
		return err
	}
	var reply *groupChatReply
	err := w.await(ctx, w.groupChatWait(), "the grid's answer to joining the group's chat", func() bool {
		reply = st.reply
		return reply != nil
	})
	if err != nil {
		return err
	}
	if !reply.ok {
		return reply.err
	}
	return nil
}

// SayToGroup speaks in a group's chat, which this session must have
// joined.  Text over 1023 bytes goes as several messages, cut at a space
// where there is one, as the viewer does.
//
// Nothing answers a message that went.  One the grid refuses arrives
// later as a GroupChatRefused event.
func (w *Session) SayToGroup(ctx context.Context, group msg.UUID, text string) error {
	if group.IsZero() {
		return fmt.Errorf("sl: no group to speak to")
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("sl: nothing to say")
	}
	if !w.InGroupChat(group) {
		return fmt.Errorf("%w: %s; join it first", ErrNotInGroupChat, group)
	}
	for _, part := range splitForIM(text) {
		if err := w.Send(ctx, w.groupIM(group, DialogSessionSend, part)); err != nil {
			return err
		}
	}
	return nil
}

// LeaveGroupChat ends this avatar's part in a group's chat.  It is sent
// whether or not this session thinks it joined: another client of the
// avatar may have.  Nothing answers it.
func (w *Session) LeaveGroupChat(ctx context.Context, group msg.UUID) error {
	if group.IsZero() {
		return fmt.Errorf("sl: no group to leave the chat of")
	}
	if err := w.Send(ctx, w.groupIM(group, DialogSessionLeave, "")); err != nil {
		return err
	}
	w.mu.Lock()
	if st := w.gchat[group]; st != nil {
		st.joined = false
		clear(st.members)
	}
	w.mu.Unlock()
	return nil
}

// maxIMText is the longest piece the viewer sends as one message,
// MAX_MSG_BUF_SIZE - 1.
const maxIMText = 1023

// splitForIM cuts text into pieces of at most maxIMText bytes as
// LLIMModel::sendMessage does: at the last space before the limit,
// else at the limit backed off to the start of a character.
// Why: doc/group-chat.md#speaking
func splitForIM(text string) []string {
	var out []string
	for len(text) > maxIMText {
		cut := strings.LastIndexByte(text[:maxIMText+1], ' ')
		if cut <= 0 {
			cut = maxIMText
			for cut > 0 && !utf8.RuneStart(text[cut]) {
				cut--
			}
			if cut == 0 {
				cut = maxIMText
			}
		}
		out = append(out, text[:cut])
		text = text[cut:]
	}
	return append(out, text)
}

// groupChatWait is how long a start is waited for.
func (w *Session) groupChatWait() time.Duration {
	return orDefault(w.Options().GroupChatTimeout, DefaultGroupChatTimeout)
}

// acceptGroupChat answers an invitation the way the viewer does.
func (w *Session) acceptGroupChat(ctx context.Context, group msg.UUID) error {
	if !w.b.HasCap(ChatSessionCap) {
		return fmt.Errorf("sl: this region offers no %s", ChatSessionCap)
	}
	body, err := llsd.Encode(map[string]any{
		"method":     "accept invitation",
		"session-id": llsd.UUID(group.String()),
	})
	if err != nil {
		return err
	}
	out, err := w.capDo(ctx, agent.CapRequest{
		Cap: ChatSessionCap, Method: "POST", Body: body, Type: "application/llsd+xml",
	})
	if err != nil {
		var ce *CapError
		if errors.As(err, &ce) && ce.Status == 404 {
			return &GroupChatError{Group: group, Key: "session_does_not_exist_error",
				Reason: chatReason("session_does_not_exist_error")}
		}
		return err
	}
	w.mu.Lock()
	st := w.chatState(group)
	st.joined = true
	w.mu.Unlock()
	if v, err := llsd.Decode(bytes.NewReader(out)); err == nil {
		w.noteChatMembers(group, llsd.Map(v))
	}
	w.deliverGroupChat(&GroupChat{At: time.Now(), Kind: GroupChatJoined, Group: group})
	return nil
}

// noteChatMembers replaces the members with what a reply lists, in its
// newer form (agent_info, keyed by id) or the older (agents, an array).
func (w *Session) noteChatMembers(group msg.UUID, m map[string]any) {
	if m == nil {
		return
	}
	ids := map[msg.UUID]bool{}
	if info := llsd.Map(m["agent_info"]); info != nil {
		for k := range info {
			ids[parseUUIDOrZero(k)] = true
		}
	} else if list, ok := m["agents"].([]any); ok {
		for _, a := range list {
			if s, ok := a.(string); ok {
				ids[parseUUIDOrZero(s)] = true
			}
		}
	}
	delete(ids, msg.UUID{})
	w.mu.Lock()
	w.chatState(group).members = ids
	w.mu.Unlock()
}

// ------------------------------------------------------------- events

// heardGroupChat turns a dialog 17 message into an event.  The group is
// the session, which is the message's id.
func (w *Session) heardGroupChat(im *IM) {
	if im.ID.IsZero() {
		return
	}
	w.mu.Lock()
	name := w.chatState(im.ID).name
	w.mu.Unlock()
	w.deliverGroupChat(&GroupChat{
		At: im.At, Kind: GroupChatSaid, Group: im.ID, GroupName: name,
		From: im.From, FromName: im.FromName, Text: im.Text,
		Mine: im.Mine, Via: im.Via, speaker: im.Sender(), w: w,
	})
}

// chatInvitationEvent reads a ChatterBoxInvitation.  Only the form with
// an instantmessage in it is a group's; the others are voice.
func (w *Session) chatInvitationEvent(m map[string]any) {
	p := llsd.Map(llsd.Map(m["instantmessage"])["message_params"])
	if p == nil {
		return
	}
	group := parseUUIDOrZero(llsd.String(p, "id"))
	from := parseUUIDOrZero(llsd.String(p, "from_id"))
	if group.IsZero() || from == w.me {
		return
	}
	name := strings.TrimRight(string(llsd.Bytes(llsd.Map(p["data"]), "binary_bucket")), "\x00")
	if name == "" {
		name = llsd.String(m, "session_name")
	}
	fromName := llsd.String(p, "from_name")
	w.learn(from, fromName)

	w.mu.Lock()
	if name != "" {
		w.chatState(group).name = name
	}
	w.mu.Unlock()
	g := &GroupChat{
		At: time.Now(), Kind: GroupChatInvited, Group: group, GroupName: name,
		From: from, FromName: fromName, Text: llsd.String(p, "message"), w: w,
	}
	g.speaker = (&IM{Dialog: DialogSessionSend, From: from, FromName: fromName}).Sender()
	w.deliverGroupChat(g)
}

// chatStartReplyEvent reads a ChatterBoxSessionStartReply, which is the
// answer to a start.
func (w *Session) chatStartReplyEvent(m map[string]any) {
	group := parseUUIDOrZero(llsd.String(m, "temp_session_id"))
	if group.IsZero() {
		group = parseUUIDOrZero(llsd.String(m, "session_id"))
	}
	if group.IsZero() {
		return
	}
	if !llsd.Bool(m, "success") {
		key := llsd.String(m, "error")
		if key == "" {
			key = llsd.String(m, "reason")
		}
		err := &GroupChatError{Group: group, Key: key, Reason: chatReason(key)}
		w.mu.Lock()
		st := w.chatState(group)
		st.joined = false
		st.reply = &groupChatReply{err: err}
		w.mu.Unlock()
		return
	}
	w.noteChatMembers(group, m)
	w.mu.Lock()
	st := w.chatState(group)
	st.joined = true
	st.reply = &groupChatReply{ok: true}
	name := st.name
	w.mu.Unlock()
	w.deliverGroupChat(&GroupChat{At: time.Now(), Kind: GroupChatJoined, Group: group, GroupName: name})
}

// chatEventReplyEvent reads a ChatterBoxSessionEventReply, which the grid
// sends for a message or another event it would not take.  A success
// says nothing worth passing on.
func (w *Session) chatEventReplyEvent(m map[string]any) {
	if llsd.Bool(m, "success") {
		return
	}
	group := parseUUIDOrZero(llsd.String(m, "session_id"))
	key := llsd.String(m, "error")
	if key == "" {
		key = llsd.String(m, "event")
	}
	w.deliverGroupChat(&GroupChat{At: time.Now(), Kind: GroupChatRefused, Group: group, Text: chatReason(key)})
}

// chatAgentListEvent reads a ChatterBoxSessionAgentListUpdates: who came
// and who went.  It arrives in two forms, agent_updates with a
// transition for each agent, and updates with the bare transition.
func (w *Session) chatAgentListEvent(m map[string]any) {
	group := parseUUIDOrZero(llsd.String(m, "session_id"))
	if group.IsZero() {
		return
	}
	moves := map[msg.UUID]string{}
	if u := llsd.Map(m["agent_updates"]); u != nil {
		for k, v := range u {
			moves[parseUUIDOrZero(k)] = llsd.String(llsd.Map(v), "transition")
		}
	} else if u := llsd.Map(m["updates"]); u != nil {
		for k, v := range u {
			s, _ := v.(string)
			moves[parseUUIDOrZero(k)] = s
		}
	}
	delete(moves, msg.UUID{})

	ids := make([]msg.UUID, 0, len(moves))
	for id := range moves {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return string(ids[i][:]) < string(ids[j][:]) })
	for _, id := range ids {
		var kind GroupChatKind
		switch moves[id] {
		case "ENTER":
			kind = GroupChatEntered
		case "LEAVE":
			kind = GroupChatLeft
		default:
			continue
		}
		w.mu.Lock()
		st := w.chatState(group)
		if kind == GroupChatEntered {
			st.members[id] = true
		} else {
			delete(st.members, id)
		}
		w.mu.Unlock()
		w.deliverGroupChat(&GroupChat{At: time.Now(), Kind: kind, Group: group, From: id, FromName: w.Name(id)})
	}
}

// chatForceCloseEvent reads a ForceCloseChatterBoxSession: the grid has
// ended this avatar's part in the chat.
func (w *Session) chatForceCloseEvent(m map[string]any) {
	group := parseUUIDOrZero(llsd.String(m, "session_id"))
	if group.IsZero() {
		return
	}
	w.mu.Lock()
	if st := w.gchat[group]; st != nil {
		st.joined = false
		clear(st.members)
	}
	w.mu.Unlock()
	w.deliverGroupChat(&GroupChat{At: time.Now(), Kind: GroupChatClosed, Group: group,
		Text: chatReason(llsd.String(m, "reason"))})
}
