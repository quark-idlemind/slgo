package main

// A group's chat from the shell: what the three words send, what is
// printed for what arrives, and that nothing joins by itself.
//
// The events are the shapes the viewer's handlers read.  None of it has
// been seen on a grid; see doc/group-chat.md.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var chatTalker = msg.MustParseUUID("d3be7e57-7e57-c0de-b926-d58bbf0ca981")

// groupSaid is somebody speaking in a group's chat: dialog 17, with the
// group as the session.
func groupSaid(from msg.UUID, name string, group msg.UUID, text string) *msg.ImprovedInstantMessage {
	m := imFrom(from, name, sl.DialogSessionSend, text)
	m.MessageBlock.ID = group
	return m
}

func groupIMs(x *testShell) []*msg.ImprovedInstantMessage {
	var out []*msg.ImprovedInstantMessage
	for _, m := range x.grid.Sent() {
		if im, ok := m.(*msg.ImprovedInstantMessage); ok {
			out = append(out, im)
		}
	}
	return out
}

func chatShell(t *testing.T) *testShell {
	t.Helper()
	x := newTestShell(t)
	joined(x, sl.Group{ID: testBuilders, Name: "Example Builders"})
	x.groups.refresh(t.Context())
	return x
}

func chatInvitation(group, from msg.UUID, name, text string) string {
	return fmt.Sprintf(`<?xml version="1.0" ?><llsd><map><key>instantmessage</key><map><key>message_params</key><map>`+
		`<key>id</key><uuid>%s</uuid><key>from_id</key><uuid>%s</uuid><key>from_name</key><string>%s</string>`+
		`<key>message</key><string>%s</string><key>data</key><map><key>binary_bucket</key>`+
		`<binary encoding="base64">RXhhbXBsZSBCdWlsZGVycwA=</binary></map></map></map></map></llsd>`,
		group, from, name, text)
}

// TestGroupChatJoinsSpeaksAndLeavesAsTheViewerDoes: one run through the
// three words, and what each puts on the wire.
func TestGroupChatJoinsSpeaksAndLeavesAsTheViewerDoes(t *testing.T) {
	x := chatShell(t)
	x.grid.AnswerGroupChat(t, "")

	got := x.do(t, "group chat Example Builders")
	if !strings.Contains(got, "joined the chat of [Group] Example Builders") {
		t.Errorf("joining printed %q", got)
	}
	ims := groupIMs(x)
	if len(ims) != 1 || ims[0].MessageBlock.Dialog != sl.DialogSessionGroupStart ||
		ims[0].MessageBlock.ToAgentID != testBuilders || ims[0].MessageBlock.ID != testBuilders {
		t.Fatalf("joining sent %+v, want dialog 15 to the group", ims)
	}

	got = x.do(t, "group say Example Builders good evening, all")
	if !strings.Contains(got, "> [Group] Example Builders: good evening, all") {
		t.Errorf("saying printed %q", got)
	}
	ims = groupIMs(x)
	said := ims[len(ims)-1].MessageBlock
	if said.Dialog != sl.DialogSessionSend || said.ToAgentID != testBuilders ||
		strings.TrimRight(string(said.Message), "\x00") != "good evening, all" {
		t.Errorf("saying sent %+v", said)
	}

	got = x.do(t, "group leave-chat Example Builders")
	if !strings.Contains(got, "left the chat of [Group] Example Builders") {
		t.Errorf("leaving printed %q", got)
	}
	ims = groupIMs(x)
	left := ims[len(ims)-1].MessageBlock
	if left.Dialog != sl.DialogSessionLeave || left.ToAgentID != testBuilders || left.ID != testBuilders {
		t.Errorf("leaving sent %+v", left)
	}

	got = x.do(t, "group say Example Builders once more")
	if !strings.Contains(got, "has not joined the chat") || !strings.Contains(got, "group chat Example Builders") {
		t.Errorf("speaking after leaving printed %q", got)
	}
}

// TestGroupChatSaysWhyTheGridRefused: the reason is the viewer's words.
func TestGroupChatSaysWhyTheGridRefused(t *testing.T) {
	x := chatShell(t)
	x.grid.AnswerGroupChat(t, "insufficient_perms_error")
	got := x.do(t, "group chat Example Builders")
	if !strings.Contains(got, "you do not have sufficient permissions") {
		t.Errorf("a refused join printed %q", got)
	}
}

// TestGroupChatWordsNeedAGroup: nothing is sent for a line with no group,
// or with "none", or with nothing to say.
func TestGroupChatWordsNeedAGroup(t *testing.T) {
	x := chatShell(t)
	for _, line := range []string{
		"group chat", "group say", "group leave-chat",
		"group chat none", "group chat Nowhere At All",
		"group say Example Builders",
	} {
		got := x.do(t, line)
		if !strings.Contains(got, "usage") && !strings.Contains(got, "no group") && !strings.Contains(got, "joined no group") {
			t.Errorf("%q printed %q, want a refusal", line, got)
		}
	}
	if ims := groupIMs(x); len(ims) != 0 {
		t.Errorf("sent %d messages for lines that name no group", len(ims))
	}
}

// TestAGroupNamedLikeTheWordsStillActivates: a line that is the whole
// name of a group keeps meaning it.
func TestAGroupNamedLikeTheWordsStillActivates(t *testing.T) {
	x := newTestShell(t)
	joined(x, sl.Group{ID: testExplorers, Name: "Chat Lounge"})
	x.grid.AnswerActivateGroup()

	got := x.do(t, "group Chat Lounge")
	if !strings.Contains(got, "acting as Chat Lounge") {
		t.Errorf("printed %q", got)
	}
	if a := activations(x); len(a) != 1 || a[0] != testExplorers {
		t.Errorf("activations %v", a)
	}
	if ims := groupIMs(x); len(ims) != 0 {
		t.Error("a chat was started for a group name")
	}
}

// TestTheLongestGroupNameIsTheGroup: "Example Builders hello" is the
// group Example Builders and a word to say, where a shorter group is
// called Example.
func TestTheLongestGroupNameIsTheGroup(t *testing.T) {
	p := &sl.Presence{Groups: []sl.Group{
		{ID: testExplorers, Name: "Example"},
		{ID: testBuilders, Name: "Example Builders"},
	}}
	id, name, rest, err := groupAndRest(p, strings.Fields("Example Builders hello there"))
	if err != nil || id != testBuilders || name != "Example Builders" || strings.Join(rest, " ") != "hello there" {
		t.Errorf("read %s %q %q, %v", id, name, rest, err)
	}
	id, _, rest, err = groupAndRest(p, strings.Fields("example builders"))
	if err != nil || id != testBuilders || len(rest) != 0 {
		t.Errorf("the whole line being a name: %s %q, %v", id, rest, err)
	}
	id, _, rest, err = groupAndRest(p, []string{testBuilders.String(), "hi"})
	if err != nil || id != testBuilders || len(rest) != 1 {
		t.Errorf("a key: %s %q, %v", id, rest, err)
	}
}

// TestAGroupsSpeechIsPrintedWithTheGroupAndTheSpeaker: once, and with the
// group behind its label.
func TestAGroupsSpeechIsPrintedWithTheGroupAndTheSpeaker(t *testing.T) {
	x := chatShell(t)
	watching(t, x)

	x.grid.Relay(t, groupSaid(chatTalker, "Example Friend", testBuilders, "shall we start?"))
	got := waits(t, x, "shall we start?")
	if !strings.Contains(got, "< Example Friend in [Group] Example Builders: shall we start?") {
		t.Errorf("printed %q", got)
	}
	// Once: the same message is an instant message too, and printing it
	// there would say it twice.
	x.grid.Relay(t, imFrom(chatTalker, "Example Friend", sl.DialogMessage, "and a private word"))
	got = waits(t, x, "and a private word")
	if n := strings.Count(got, "shall we start?"); n != 1 {
		t.Errorf("the message was printed %d times:\n%s", n, got)
	}
}

// TestAGroupNamedLikeAPersonIsLabelledAGroup: a group's name is its
// founder's choice, so a line naming one says what it is.
func TestAGroupNamedLikeAPersonIsLabelledAGroup(t *testing.T) {
	x := newTestShell(t)
	joined(x, sl.Group{ID: testBuilders, Name: "Example Friend"})
	x.groups.refresh(t.Context())
	watching(t, x)

	x.grid.Relay(t, groupSaid(chatTalker, "Another Person", testBuilders, "hello"))
	got := waits(t, x, "hello")
	if !strings.Contains(got, "in [Group] Example Friend:") {
		t.Errorf("printed %q", got)
	}
}

// TestOurOwnWordsSentBackAreNotPrintedTwice: the line was shown when it
// was sent.
func TestOurOwnWordsSentBackAreNotPrintedTwice(t *testing.T) {
	x := chatShell(t)
	watching(t, x)

	x.grid.Relay(t, groupSaid(x.s.Me(), "Quark Idlemind", testBuilders, "my own words"))
	x.grid.Relay(t, groupSaid(chatTalker, "Example Friend", testBuilders, "somebody else's"))
	got := waits(t, x, "somebody else's")
	if strings.Contains(got, "my own words") {
		t.Errorf("our own words were printed again:\n%s", got)
	}
}

// TestAnInvitationIsHeardAndAnnouncedOnce: the line each time, the notice
// once, and nothing is sent or joined.
func TestAnInvitationIsHeardAndAnnouncedOnce(t *testing.T) {
	x := chatShell(t)
	watching(t, x)

	x.grid.RelayEvent(t, "ChatterBoxInvitation", chatInvitation(testBuilders, chatTalker, "Example Friend", "anyone about?"))
	got := waits(t, x, "group chat Example Builders")
	for _, want := range []string{
		"< Example Friend in [Group] Example Builders: anyone about?",
		"* [Group] Example Builders is talking and this avatar is not in its chat",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the screen lacks %q:\n%s", want, got)
		}
	}

	x.grid.RelayEvent(t, "ChatterBoxInvitation", chatInvitation(testBuilders, chatTalker, "Example Friend", "hello?"))
	got = waits(t, x, "hello?")
	if n := strings.Count(got, "is not in its chat"); n != 1 {
		t.Errorf("the group was announced %d times:\n%s", n, got)
	}
	if ims := groupIMs(x); len(ims) != 0 {
		t.Errorf("an invitation was answered by sending %d messages", len(ims))
	}
	if x.s.InGroupChat(testBuilders) {
		t.Error("joined a chat nobody asked to join")
	}
}

// TestBeingPutOutAndRefusedAreSaid.
func TestBeingPutOutAndRefusedAreSaid(t *testing.T) {
	x := chatShell(t)
	watching(t, x)

	x.grid.RelayEvent(t, "ForceCloseChatterBoxSession", fmt.Sprintf(
		`<?xml version="1.0" ?><llsd><map><key>session_id</key><uuid>%s</uuid><key>reason</key><string>removed</string></map></llsd>`, testBuilders))
	got := waits(t, x, "out of the chat of [Group] Example Builders")
	if !strings.Contains(got, "removed from the group") {
		t.Errorf("printed %q", got)
	}

	x.grid.RelayEvent(t, "ChatterBoxSessionEventReply", fmt.Sprintf(
		`<?xml version="1.0" ?><llsd><map><key>success</key><boolean>0</boolean><key>session_id</key><uuid>%s</uuid>`+
			`<key>error</key><string>muted</string></map></llsd>`, testBuilders))
	got = waits(t, x, "refused a message to [Group] Example Builders")
	if !strings.Contains(got, "moderator disabled") {
		t.Errorf("printed %q", got)
	}
}
