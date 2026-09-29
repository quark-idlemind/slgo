package sl

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/quark-idlemind/slgo/msg"
)

// The events here are written as the viewer's handlers read them, not as
// a grid was seen to send them: nothing about group chat has been
// measured.  See doc/group-chat.md.

var (
	chatGroup   = msg.MustParseUUID("82247e57-7e57-c0de-acf2-3e3632cfbd84")
	chatSpeaker = msg.MustParseUUID("d3be7e57-7e57-c0de-b926-d58bbf0ca981")
	chatOther   = msg.MustParseUUID("e9937e57-7e57-c0de-32d7-8f0c72db7456")
)

const chatName = "Example Club"

// llsdEvent wraps map entries as an event body.
func llsdEvent(entries string) string {
	return `<?xml version="1.0" ?><llsd><map>` + entries + `</map></llsd>`
}

func startReplyBody(group msg.UUID, ok bool, extra string) string {
	b := "0"
	if ok {
		b = "1"
	}
	return llsdEvent(fmt.Sprintf(`<key>success</key><boolean>%s</boolean>`+
		`<key>temp_session_id</key><uuid>%s</uuid><key>session_id</key><uuid>%s</uuid>%s`,
		b, group, group, extra))
}

func invitationBody(group, from msg.UUID, fromName, text, session string) string {
	bucket := base64.StdEncoding.EncodeToString([]byte(session + "\x00"))
	return llsdEvent(fmt.Sprintf(`<key>instantmessage</key><map><key>message_params</key><map>`+
		`<key>id</key><uuid>%s</uuid><key>from_id</key><uuid>%s</uuid>`+
		`<key>from_name</key><string>%s</string><key>message</key><string>%s</string>`+
		`<key>offline</key><integer>0</integer><key>timestamp</key><integer>0</integer>`+
		`<key>data</key><map><key>binary_bucket</key><binary encoding="base64">%s</binary></map>`+
		`</map></map>`, group, from, fromName, text, bucket))
}

// joined has a fake session join the group's chat, answering the start
// from the test goroutine as the grid would.
func joined(t *testing.T, w *Session, f *fakeBackend, group msg.UUID) {
	t.Helper()
	wait := asideErr(t, func() error { return w.JoinGroupChat(context.Background(), group) })
	waitSent[*msg.ImprovedInstantMessage](t, f)
	f.RelayEvent(t, "ChatterBoxSessionStartReply", startReplyBody(group, true, ""))
	if err := wait(); err != nil {
		t.Fatalf("JoinGroupChat: %v", err)
	}
	f.Forget()
}

func nextChat(t *testing.T, ch <-chan *GroupChat) *GroupChat {
	t.Helper()
	select {
	case g := <-ch:
		return g
	case <-time.After(2 * time.Second):
		t.Fatal("no group chat event arrived")
		return nil
	}
}

func noChat(t *testing.T, ch <-chan *GroupChat, what string) {
	t.Helper()
	select {
	case g := <-ch:
		t.Fatalf("%s: got %+v", what, g)
	default:
	}
}

// TestJoiningSendsWhatTheViewerSends: dialog 15 to the group, with the
// group as the session, no region, an empty bucket and the avatar's
// position; and the call returns when the start's reply says so.
func TestJoiningSendsWhatTheViewerSends(t *testing.T) {
	w, f := newFakeSession(t)
	f.presence.Groups = []Group{{ID: chatGroup, Name: chatName}}
	subs := w.GroupChats(8)

	wait := asideErr(t, func() error { return w.JoinGroupChat(context.Background(), chatGroup) })
	m := waitSent[*msg.ImprovedInstantMessage](t, f)
	b := m.MessageBlock
	if b.Dialog != DialogSessionGroupStart || b.ToAgentID != chatGroup || b.ID != chatGroup {
		t.Errorf("dialog %d to %s id %s, want 15 to the group with the group as id", b.Dialog, b.ToAgentID, b.ID)
	}
	if b.Offline != 0 || b.FromGroup || b.Timestamp != 0 || b.ParentEstateID != 0 || !b.RegionID.IsZero() {
		t.Errorf("fields %+v, want online, not from a group, no time, no estate, no region", b)
	}
	if trimNul(b.Message) != "" || len(b.BinaryBucket) != 1 || b.BinaryBucket[0] != 0 {
		t.Errorf("message %q bucket %v, want empty and the one-byte empty bucket", b.Message, b.BinaryBucket)
	}
	if want := f.presence.Position; b.Position != want {
		t.Errorf("position %v, want the avatar's %v", b.Position, want)
	}
	if trimNul(b.FromAgentName) != "Quark Idlemind" {
		t.Errorf("from %q", b.FromAgentName)
	}
	if w.InGroupChat(chatGroup) {
		t.Error("in the chat before the grid said so")
	}

	f.RelayEvent(t, "ChatterBoxSessionStartReply", startReplyBody(chatGroup, true,
		fmt.Sprintf(`<key>agent_info</key><map><key>%s</key><map><key>is_moderator</key><boolean>0</boolean></map></map>`, chatSpeaker)))
	if err := wait(); err != nil {
		t.Fatalf("JoinGroupChat: %v", err)
	}
	if !w.InGroupChat(chatGroup) {
		t.Error("not in the chat after the grid said so")
	}
	if got := w.GroupChatMembers(chatGroup); len(got) != 1 || got[0] != chatSpeaker {
		t.Errorf("members %v, want the one the reply listed", got)
	}
	if g := nextChat(t, subs); g.Kind != GroupChatJoined || g.Group != chatGroup {
		t.Errorf("event %+v, want joined", g)
	}
}

// TestTheOlderReplyListsAgentsAsAnArray: the form the viewer still reads.
func TestTheOlderReplyListsAgentsAsAnArray(t *testing.T) {
	w, f := newFakeSession(t)
	wait := asideErr(t, func() error { return w.JoinGroupChat(context.Background(), chatGroup) })
	waitSent[*msg.ImprovedInstantMessage](t, f)
	f.RelayEvent(t, "ChatterBoxSessionStartReply", startReplyBody(chatGroup, true,
		fmt.Sprintf(`<key>agents</key><array><uuid>%s</uuid></array>`, chatOther)))
	if err := wait(); err != nil {
		t.Fatal(err)
	}
	if got := w.GroupChatMembers(chatGroup); len(got) != 1 || got[0] != chatOther {
		t.Errorf("members %v", got)
	}
}

// TestARefusedStartSaysWhy: the grid names a string and the viewer's words
// for it are the error; the chat is not joined, and the refusal is the
// call's and not an event.
func TestARefusedStartSaysWhy(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)
	wait := asideErr(t, func() error { return w.JoinGroupChat(context.Background(), chatGroup) })
	waitSent[*msg.ImprovedInstantMessage](t, f)
	f.RelayEvent(t, "ChatterBoxSessionStartReply", startReplyBody(chatGroup, false,
		`<key>error</key><string>muted</string>`))

	err := wait()
	var ge *GroupChatError
	if !errors.As(err, &ge) || ge.Key != "muted" || !strings.Contains(ge.Reason, "moderator disabled your text chat") {
		t.Fatalf("error %v, want the group chat refusal for muted", err)
	}
	if w.InGroupChat(chatGroup) {
		t.Error("in a chat the grid refused")
	}
	// The caller has the refusal; an event as well would say it twice.
	// The relay's barrier says the reader has finished with the reply.
	f.RelayEvent(t, "SomethingElse", llsdEvent(""))
	noChat(t, subs, "a refused start")
}

// TestAStartNobodyAnswersTimesOut: the wait is Options.GroupChatTimeout.
func TestAStartNobodyAnswersTimesOut(t *testing.T) {
	w, _ := newFakeSession(t)
	w.SetOptions(Options{GroupChatTimeout: 50 * time.Millisecond})
	err := w.JoinGroupChat(context.Background(), chatGroup)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error %v, want a timeout", err)
	}
	if w.InGroupChat(chatGroup) {
		t.Error("in a chat nobody answered")
	}
}

// TestAGroupNotJoinedIsRefusedBeforeAnythingIsSent: the viewer will not
// start the chat of a group it does not belong to either.
func TestAGroupNotJoinedIsRefusedBeforeAnythingIsSent(t *testing.T) {
	w, f := newFakeSession(t)
	f.presence.Groups = []Group{{ID: chatOther, Name: "Some Other Group"}}
	err := w.JoinGroupChat(context.Background(), chatGroup)
	if !errors.Is(err, ErrNotInGroup) {
		t.Fatalf("error %v, want ErrNotInGroup", err)
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("sent %s", f.describe())
	}
}

// TestSpeakingSendsWhatTheViewerSends: dialog 17 to the group, the group
// as the session, the text, no region and no position.
func TestSpeakingSendsWhatTheViewerSends(t *testing.T) {
	w, f := newFakeSession(t)
	joined(t, w, f, chatGroup)

	if err := w.SayToGroup(context.Background(), chatGroup, "hello everybody"); err != nil {
		t.Fatal(err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	b := m.MessageBlock
	if b.Dialog != DialogSessionSend || b.ToAgentID != chatGroup || b.ID != chatGroup {
		t.Errorf("dialog %d to %s id %s", b.Dialog, b.ToAgentID, b.ID)
	}
	if trimNul(b.Message) != "hello everybody" || b.Offline != 0 || b.FromGroup {
		t.Errorf("message %q offline %d from group %v", b.Message, b.Offline, b.FromGroup)
	}
	if !b.RegionID.IsZero() || b.Position != (msg.Vector3{}) || b.Timestamp != 0 || len(b.BinaryBucket) != 1 {
		t.Errorf("region %s position %v time %d bucket %v, want none of them", b.RegionID, b.Position, b.Timestamp, b.BinaryBucket)
	}
	if m.AgentData.AgentID != testAgentID || m.AgentData.SessionID != testSessionID {
		t.Error("the agent block is not this session's")
	}
	if sent := f.Sent(); !sent[0].Reliable {
		t.Error("sent unreliably")
	}
}

// TestSpeakingBeforeJoiningIsRefused: the grid would answer a refusal
// nobody is waiting for.
func TestSpeakingBeforeJoiningIsRefused(t *testing.T) {
	w, f := newFakeSession(t)
	err := w.SayToGroup(context.Background(), chatGroup, "hello")
	if !errors.Is(err, ErrNotInGroupChat) {
		t.Fatalf("error %v, want ErrNotInGroupChat", err)
	}
	if len(f.Sent()) != 0 {
		t.Errorf("sent %s", f.describe())
	}
}

// TestALongMessageIsSplitAsTheViewerSplitsIt.
func TestALongMessageIsSplitAsTheViewerSplitsIt(t *testing.T) {
	w, f := newFakeSession(t)
	joined(t, w, f, chatGroup)

	long := strings.Repeat("word ", 500) // 2500 bytes
	if err := w.SayToGroup(context.Background(), chatGroup, long); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range sentOf[*msg.ImprovedInstantMessage](f) {
		got = append(got, trimNul(m.MessageBlock.Message))
	}
	if len(got) < 3 {
		t.Fatalf("%d messages, want the text split", len(got))
	}
	for _, p := range got {
		if len(p) > 1023 {
			t.Errorf("a piece of %d bytes", len(p))
		}
	}
	if strings.Join(got, "") != long {
		t.Error("what was sent is not what was said")
	}
}

func TestSplitForIM(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []int // piece lengths
	}{
		{"short", "hello", []int{5}},
		{"exactly the limit", strings.Repeat("a", 1023), []int{1023}},
		{"no space", strings.Repeat("a", 1500), []int{1023, 477}},
		{"at the last space", strings.Repeat("a", 1000) + " " + strings.Repeat("b", 100), []int{1000, 101}},
	}
	for _, c := range cases {
		var got []int
		for _, p := range splitForIM(c.in) {
			got = append(got, len(p))
		}
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: pieces %v, want %v", c.name, got, c.want)
		}
	}

	// Never inside a character.
	in := strings.Repeat("é", 800) // two bytes each
	for _, p := range splitForIM(in) {
		if !utf8.ValidString(p) {
			t.Fatal("a piece ends inside a character")
		}
	}
}

// TestSayingNothingIsRefused.
func TestSayingNothingIsRefused(t *testing.T) {
	w, f := newFakeSession(t)
	joined(t, w, f, chatGroup)
	if err := w.SayToGroup(context.Background(), chatGroup, "   "); err == nil {
		t.Error("nothing was refused")
	}
	if len(f.Sent()) != 0 {
		t.Errorf("sent %s", f.describe())
	}
}

// TestLeavingSendsWhatTheViewerSends: dialog 18 to the group, the group as
// the session, an empty message; and the chat is no longer this session's
// even where it never thought it had joined.
func TestLeavingSendsWhatTheViewerSends(t *testing.T) {
	w, f := newFakeSession(t)
	joined(t, w, f, chatGroup)

	if err := w.LeaveGroupChat(context.Background(), chatGroup); err != nil {
		t.Fatal(err)
	}
	b := onlySent[*msg.ImprovedInstantMessage](t, f).MessageBlock
	if b.Dialog != DialogSessionLeave || b.ToAgentID != chatGroup || b.ID != chatGroup {
		t.Errorf("dialog %d to %s id %s", b.Dialog, b.ToAgentID, b.ID)
	}
	if trimNul(b.Message) != "" || !b.RegionID.IsZero() || b.Position != (msg.Vector3{}) || b.Offline != 0 {
		t.Errorf("message %q region %s position %v", b.Message, b.RegionID, b.Position)
	}
	if w.InGroupChat(chatGroup) {
		t.Error("still in the chat after leaving it")
	}
	if err := w.SayToGroup(context.Background(), chatGroup, "hello"); !errors.Is(err, ErrNotInGroupChat) {
		t.Errorf("spoke after leaving: %v", err)
	}

	f.Forget()
	if err := w.LeaveGroupChat(context.Background(), chatGroup); err != nil {
		t.Fatal(err)
	}
	if len(f.Sent()) != 1 {
		t.Errorf("a leave for a chat this session never joined sent %s, want the leave", f.describe())
	}
}

// TestAGroupMessageSaysWhichGroupAndWhoSpoke: dialog 17 arrives as an event
// with the group, from the message's id, and the speaker; and it is still
// an instant message for whoever reads those.
func TestAGroupMessageSaysWhichGroupAndWhoSpoke(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)
	ims := w.IMs(8)

	f.Relay(t, arrivingIM(chatSpeaker, "Example Resident", DialogSessionSend, chatGroup, "good evening"))

	g := nextChat(t, subs)
	if g.Kind != GroupChatSaid || g.Group != chatGroup || g.From != chatSpeaker ||
		g.FromName != "Example Resident" || g.Text != "good evening" || g.Mine {
		t.Errorf("event %+v", g)
	}
	if g.Sender() != SenderPerson {
		t.Errorf("the speaker is a %v", g.Sender())
	}
	if got := SenderGroup.Label(chatName); got != "[Group] Example Club" {
		t.Errorf("the group is labelled %q", got)
	}
	select {
	case im := <-ims:
		if im.Dialog != DialogSessionSend {
			t.Errorf("dialog %d", im.Dialog)
		}
	case <-time.After(2 * time.Second):
		t.Error("the IM subscription lost the message")
	}
}

// TestAGroupMessageSignedByTheGridIsTheGrids: a speaker whose name is the
// grid's own is labelled so and not as a person.
func TestAGroupMessageSignedByTheGridIsTheGrids(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)
	f.Relay(t, arrivingIM(chatSpeaker, SystemName, DialogSessionSend, chatGroup, "closing"))
	if g := nextChat(t, subs); g.Sender() != SenderGrid {
		t.Errorf("sender %v, want the grid", g.Sender())
	}
}

// TestAnEchoFromAnotherClientIsMine: what another client of this avatar said
// arrives marked, as an IM does, and is not news of somebody else.
func TestAnEchoFromAnotherClientIsMine(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)

	m := arrivingIM(testAgentID, "Quark Idlemind", DialogSessionSend, chatGroup, "from the other shell")
	m.MessageBlock.ToAgentID = chatGroup
	f.RelayRaw(t, &Message{
		ID: msg.IDOf(m), Name: m.MsgInfo().Name, Body: mustEncode(t, m), At: time.Now(),
		FromClient: "the other shell",
	})
	g := nextChat(t, subs)
	if !g.Mine || g.Via != "the other shell" || g.Group != chatGroup || g.Text != "from the other shell" {
		t.Errorf("event %+v", g)
	}
}

func mustEncode(t *testing.T, m msg.Message) []byte {
	t.Helper()
	b, err := m.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAnInvitationIsSurfacedAndNotAnswered: the viewer answers at once; this
// says who spoke, in what group and what, and sends nothing and posts
// nothing until told.
func TestAnInvitationIsSurfacedAndNotAnswered(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)
	var mu sync.Mutex
	var posts int
	f.ServeCap(t, ChatSessionCap, func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		posts++
		mu.Unlock()
	})

	f.RelayEvent(t, "ChatterBoxInvitation", invitationBody(chatGroup, chatSpeaker, "Example Resident", "anyone here?", chatName))

	g := nextChat(t, subs)
	if g.Kind != GroupChatInvited || g.Group != chatGroup || g.GroupName != chatName ||
		g.From != chatSpeaker || g.FromName != "Example Resident" || g.Text != "anyone here?" {
		t.Errorf("event %+v", g)
	}
	if g.Sender() != SenderPerson {
		t.Errorf("the inviter is a %v", g.Sender())
	}
	mu.Lock()
	defer mu.Unlock()
	if posts != 0 || len(f.Sent()) != 0 {
		t.Errorf("answered an invitation nobody accepted: %d posts, sent %s", posts, f.describe())
	}
	if w.InGroupChat(chatGroup) {
		t.Error("in a chat nobody joined")
	}
	if got := w.Name(chatSpeaker); got != "Example Resident" {
		t.Errorf("the inviter is called %q", got)
	}
}

// TestAcceptingPostsWhatTheViewerPosts: {"method": "accept invitation",
// "session-id": the group} to ChatSessionRequest; the answer's agents are the
// members.
func TestAcceptingPostsWhatTheViewerPosts(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)
	var mu sync.Mutex
	var body, method, ctype string
	f.ServeCap(t, ChatSessionCap, func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		body, method, ctype = string(b), r.Method, r.Header.Get("Content-Type")
		mu.Unlock()
		rw.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(rw, `<?xml version="1.0" ?><llsd><map><key>agent_info</key><map><key>%s</key><map/></map></map></llsd>`, chatSpeaker)
	})
	f.RelayEvent(t, "ChatterBoxInvitation", invitationBody(chatGroup, chatSpeaker, "Example Resident", "anyone here?", chatName))
	inv := nextChat(t, subs)

	if err := inv.Accept(context.Background()); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if method != "POST" || !strings.Contains(ctype, "llsd+xml") {
		t.Errorf("%s as %q", method, ctype)
	}
	for _, want := range []string{
		`<key>method</key><string>accept invitation</string>`,
		fmt.Sprintf(`<key>session-id</key><uuid>%s</uuid>`, chatGroup),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the request %s lacks %s", body, want)
		}
	}
	if !w.InGroupChat(chatGroup) {
		t.Error("not in the chat after accepting")
	}
	if got := w.GroupChatMembers(chatGroup); len(got) != 1 || got[0] != chatSpeaker {
		t.Errorf("members %v", got)
	}
	if g := nextChat(t, subs); g.Kind != GroupChatJoined {
		t.Errorf("event %+v, want joined", g)
	}
}

// TestAcceptingAnEndedSessionSaysSo: the viewer shows a 404 as the session
// no longer existing.
func TestAcceptingAnEndedSessionSaysSo(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)
	f.ServeCap(t, ChatSessionCap, func(rw http.ResponseWriter, r *http.Request) {
		http.Error(rw, "gone", http.StatusNotFound)
	})
	f.RelayEvent(t, "ChatterBoxInvitation", invitationBody(chatGroup, chatSpeaker, "Example Resident", "hi", chatName))
	err := nextChat(t, subs).Accept(context.Background())
	var ge *GroupChatError
	if !errors.As(err, &ge) || !strings.Contains(ge.Reason, "no longer exists") {
		t.Fatalf("error %v", err)
	}
	if w.InGroupChat(chatGroup) {
		t.Error("in a chat that no longer exists")
	}
}

// TestOnlyAnInvitationCanBeAccepted.
func TestOnlyAnInvitationCanBeAccepted(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)
	f.Relay(t, arrivingIM(chatSpeaker, "Example Resident", DialogSessionSend, chatGroup, "hi"))
	if err := nextChat(t, subs).Accept(context.Background()); err == nil {
		t.Error("a message was accepted")
	}
}

// TestInvitationsThatAreNotAGroupsAreIgnored: the viewer's other two shapes
// are voice and conference calls, and its own invitation is dropped.
func TestInvitationsThatAreNotAGroupsAreIgnored(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)

	f.RelayEvent(t, "ChatterBoxInvitation", llsdEvent(fmt.Sprintf(
		`<key>session_id</key><uuid>%s</uuid><key>voice</key><map/>`, chatGroup)))
	f.RelayEvent(t, "ChatterBoxInvitation", llsdEvent(fmt.Sprintf(
		`<key>session_id</key><uuid>%s</uuid><key>immediate</key><boolean>1</boolean>`, chatGroup)))
	f.RelayEvent(t, "ChatterBoxInvitation", invitationBody(chatGroup, testAgentID, "Quark Idlemind", "me", chatName))
	noChat(t, subs, "an invitation that is not for this avatar to hear")
}

// TestAnInvitationNamesTheGroupInLaterMessages.
func TestAnInvitationNamesTheGroupInLaterMessages(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)
	f.RelayEvent(t, "ChatterBoxInvitation", invitationBody(chatGroup, chatSpeaker, "Example Resident", "hi", chatName))
	nextChat(t, subs)
	f.Relay(t, arrivingIM(chatSpeaker, "Example Resident", DialogSessionSend, chatGroup, "again"))
	if g := nextChat(t, subs); g.GroupName != chatName {
		t.Errorf("group name %q", g.GroupName)
	}
}

// TestPeopleComingAndGoing: both forms of ChatterBoxSessionAgentListUpdates.
func TestPeopleComingAndGoing(t *testing.T) {
	w, f := newFakeSession(t)
	joined(t, w, f, chatGroup)
	subs := w.GroupChats(8)

	f.RelayEvent(t, "ChatterBoxSessionAgentListUpdates", llsdEvent(fmt.Sprintf(
		`<key>session_id</key><uuid>%s</uuid><key>agent_updates</key><map><key>%s</key><map>`+
			`<key>transition</key><string>ENTER</string></map></map>`, chatGroup, chatSpeaker)))
	g := nextChat(t, subs)
	if g.Kind != GroupChatEntered || g.Group != chatGroup || g.From != chatSpeaker {
		t.Errorf("event %+v", g)
	}
	if got := w.GroupChatMembers(chatGroup); len(got) != 1 {
		t.Errorf("members %v", got)
	}

	f.RelayEvent(t, "ChatterBoxSessionAgentListUpdates", llsdEvent(fmt.Sprintf(
		`<key>session_id</key><uuid>%s</uuid><key>updates</key><map><key>%s</key><string>LEAVE</string></map>`,
		chatGroup, chatSpeaker)))
	g = nextChat(t, subs)
	if g.Kind != GroupChatLeft || g.From != chatSpeaker {
		t.Errorf("event %+v", g)
	}
	if got := w.GroupChatMembers(chatGroup); len(got) != 0 {
		t.Errorf("members %v", got)
	}
}

// TestBeingPutOut: ForceCloseChatterBoxSession ends the chat here.
func TestBeingPutOut(t *testing.T) {
	w, f := newFakeSession(t)
	joined(t, w, f, chatGroup)
	subs := w.GroupChats(8)

	f.RelayEvent(t, "ForceCloseChatterBoxSession", llsdEvent(fmt.Sprintf(
		`<key>session_id</key><uuid>%s</uuid><key>reason</key><string>removed</string>`, chatGroup)))
	g := nextChat(t, subs)
	if g.Kind != GroupChatClosed || g.Group != chatGroup || !strings.Contains(g.Text, "removed from the group") {
		t.Errorf("event %+v", g)
	}
	if w.InGroupChat(chatGroup) {
		t.Error("still in a chat the grid closed")
	}
}

// TestAMessageTheGridRefusesArrivesAsARefusal: ChatterBoxSessionEventReply
// with success false; a success is not news.
func TestAMessageTheGridRefusesArrivesAsARefusal(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(8)

	f.RelayEvent(t, "ChatterBoxSessionEventReply", llsdEvent(fmt.Sprintf(
		`<key>success</key><boolean>1</boolean><key>session_id</key><uuid>%s</uuid>`, chatGroup)))
	noChat(t, subs, "a success")

	f.RelayEvent(t, "ChatterBoxSessionEventReply", llsdEvent(fmt.Sprintf(
		`<key>success</key><boolean>0</boolean><key>session_id</key><uuid>%s</uuid>`+
			`<key>event</key><string>message</string><key>error</key><string>muted_error</string>`, chatGroup)))
	g := nextChat(t, subs)
	if g.Kind != GroupChatRefused || g.Group != chatGroup || !strings.Contains(g.Text, "text chat") {
		t.Errorf("event %+v", g)
	}
}

// TestGroupChatEventsAreRelayedByADaemon: a hosted session only hears the
// events it subscribed to, by name.
func TestGroupChatEventsAreRelayedByADaemon(t *testing.T) {
	for name := range map[string]bool{
		"ChatterBoxInvitation": true, "ChatterBoxSessionStartReply": true,
		"ChatterBoxSessionEventReply": true, "ChatterBoxSessionAgentListUpdates": true,
		"ForceCloseChatterBoxSession": true,
	} {
		found := false
		for _, s := range Subscriptions {
			found = found || s == name
		}
		if !found {
			t.Errorf("%s is not in Subscriptions", name)
		}
		if eventHandlers[name] == nil {
			t.Errorf("%s has no handler", name)
		}
	}
}

// TestGroupChatSubscriptionsCloseWhenTheSessionEnds.
func TestGroupChatSubscriptionsCloseWhenTheSessionEnds(t *testing.T) {
	w, f := newFakeSession(t)
	subs := w.GroupChats(4)
	f.Close()
	select {
	case _, open := <-subs:
		if open {
			t.Error("delivered an event, want closed")
		}
	case <-time.After(2 * time.Second):
		t.Error("the subscription was not closed")
	}
	late := w.GroupChats(4)
	select {
	case _, open := <-late:
		if open {
			t.Error("a late subscription delivered an event")
		}
	case <-time.After(2 * time.Second):
		t.Error("a subscription taken after the end was not closed")
	}
}

// TestAGroupChatIsKeptAcrossARegionChange: the chat is the grid's and not
// the region's.
func TestAGroupChatIsKeptAcrossARegionChange(t *testing.T) {
	w, f := newFakeSession(t)
	joined(t, w, f, chatGroup)
	f.RelayRegion(t, "Elsewhere", 1)
	if !w.InGroupChat(chatGroup) {
		t.Error("a region change ended the chat")
	}
}
