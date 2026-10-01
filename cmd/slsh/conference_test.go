package main

// Conferences from the shell: what each word sends and posts, and what is
// printed for what arrives.  The events are the shapes the viewer's
// handlers read; none of it has been seen on a grid.  See
// doc/conference.md.

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// confStranger is somebody the shell has never heard of, whom the grid
// lists by key and names when asked.
var confStranger = msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000004")

var confSessions = []msg.UUID{
	msg.MustParseUUID("52ec7e57-7e57-c0de-f0fd-975a9e215f24"),
	msg.MustParseUUID("72747e57-7e57-c0de-6488-8c360b21eafd"),
}

// confPosts is what the fake's ChatSessionRequest was sent.
type confPosts struct {
	mu     sync.Mutex
	bodies []string
}

func (p *confPosts) all() []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []map[string]any
	for _, b := range p.bodies {
		v, _ := llsd.Decode(bytes.NewReader([]byte(b)))
		out = append(out, llsd.Map(v))
	}
	return out
}

func (p *confPosts) methods() []string {
	var out []string
	for _, m := range p.all() {
		out = append(out, llsd.String(m, "method"))
	}
	return out
}

// conferenceShell has a shell whose grid answers a conference start as
// the viewer's handler reads it, with the next of confSessions, and an
// acceptance with Example Resident in the session.  People are the first
// three of the last listing, and named.
func conferenceShell(t *testing.T) (*testShell, *confPosts) {
	t.Helper()
	x := newTestShell(t)
	posts := &confPosts{}
	var next int
	x.grid.ServeCap(t, sl.ChatSessionCap, func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		posts.mu.Lock()
		posts.bodies = append(posts.bodies, string(b))
		posts.mu.Unlock()
		v, _ := llsd.Decode(bytes.NewReader(b))
		m := llsd.Map(v)
		rw.Header().Set("Content-Type", "application/llsd+xml")
		switch llsd.String(m, "method") {
		case "start conference":
			posts.mu.Lock()
			session := confSessions[next%len(confSessions)]
			next++
			posts.mu.Unlock()
			x.grid.RelayEvent(t, "ChatterBoxSessionStartReply", fmt.Sprintf(
				`<?xml version="1.0" ?><llsd><map><key>success</key><boolean>1</boolean>`+
					`<key>temp_session_id</key><uuid>%s</uuid><key>session_id</key><uuid>%s</uuid></map></llsd>`,
				llsd.String(m, "session-id"), session))
		case "accept invitation":
			fmt.Fprintf(rw, `<?xml version="1.0" ?><llsd><map><key>agent_info</key><map>`+
				`<key>%s</key><map/><key>%s</key><map/><key>%s</key><map/></map></map></llsd>`,
				testSomebody, confStranger, x.s.Me())
			return
		}
		fmt.Fprint(rw, `<?xml version="1.0" ?><llsd><map/></llsd>`)
	})
	x.grid.AnswerNames(t, map[msg.UUID]string{
		testSomebody: "Example Resident", testFriend: "Another Resident", testOther: "Third Resident",
		chatTalker: "Example Friend", confStranger: "Fourth Resident",
	})
	// Names are heard the way a session hears them, off a message.
	x.grid.Relay(t, imFrom(testSomebody, "Example Resident", sl.DialogMessage, "hello"))
	x.grid.Relay(t, imFrom(testFriend, "Another Resident", sl.DialogMessage, "hello"))
	x.grid.Relay(t, imFrom(testOther, "Third Resident", sl.DialogMessage, "hello"))
	x.setListed([]person{
		{ID: testSomebody, Name: "Example Resident"},
		{ID: testFriend, Name: "Another Resident"},
		{ID: testOther, Name: "Third Resident"},
	})
	return x, posts
}

func confSent(x *testShell) []*msg.ImprovedInstantMessage {
	var out []*msg.ImprovedInstantMessage
	for _, m := range x.grid.Sent() {
		if im, ok := m.(*msg.ImprovedInstantMessage); ok {
			out = append(out, im)
		}
	}
	return out
}

// TestAConferenceIsStartedWithPeopleSeparatedByCommas: each is read as im
// reads a person, the first is first in the post, and the conference is
// numbered and says who is in it.
func TestAConferenceIsStartedWithPeopleSeparatedByCommas(t *testing.T) {
	x, posts := conferenceShell(t)

	got := x.do(t, "conference start 1, another resident, 3")
	for _, want := range []string{
		"started [Conference] Multi-person chat #1",
		"with Another Resident, Example Resident, Third Resident",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printed %q, want %q", got, want)
		}
	}
	all := posts.all()
	if len(all) != 1 || llsd.String(all[0], "method") != "start conference" {
		t.Fatalf("posts %v", posts.methods())
	}
	params, _ := all[0]["params"].([]any)
	if len(params) != 3 || params[0] != testSomebody.String() || params[1] != testFriend.String() ||
		params[2] != testOther.String() {
		t.Errorf("params %v, want the three people in the order typed", params)
	}
	if cs := x.s.Conferences(); len(cs) != 1 || cs[0].ID != confSessions[0] || !cs[0].Joined {
		t.Errorf("Conferences() = %+v", cs)
	}

	// The same people again is the same conference.
	got = x.do(t, "conference start 3, 2, 1")
	if !strings.Contains(got, "already in [Conference] Multi-person chat #1") || len(posts.all()) != 1 {
		t.Errorf("starting it again printed %q after %d posts", got, len(posts.all()))
	}
}

// TestAConferenceNeedsTwoPeopleAndTheCommasRight: nothing is posted for a
// line that could not make one.
func TestAConferenceNeedsTwoPeopleAndTheCommasRight(t *testing.T) {
	x, posts := conferenceShell(t)
	for line, want := range map[string]string{
		"conference start":          "who to put in it, separated by commas",
		"conference start 1":        "two people or more",
		"conference start 1,":       "a name is missing between the commas",
		"conference start 1,,2":     "a name is missing between the commas",
		"conference start 1, 9":     "no 9 in the last listing",
		"conference start 1, 1":     "named twice",
		"conference from-im":        "who to put in it",
		"conference from-im 1":      "two people or more",
		"conference add":            "which conference",
		"conference add 1 2":        "there is no conference 1",
		"conference say":            "which conference",
		"conference join":           "which invitation",
		"conference leave":          "which conference",
		"conference nonsense":       "not one of start, from-im, add, say, join, leave",
		"conference leave nonsense": "not the number of a conference",
	} {
		got := x.do(t, line)
		if !strings.Contains(got, want) {
			t.Errorf("%q printed %q, want it to say %q", line, got, want)
		}
	}
	if len(posts.all()) != 0 || len(confSent(x)) != 0 {
		t.Errorf("posted %v and sent %d messages for lines that make no conference", posts.methods(), len(confSent(x)))
	}
}

// TestAnInstantMessageBecomesAConference: the message is left, and then
// the start has the first person first.
func TestAnInstantMessageBecomesAConference(t *testing.T) {
	x, posts := conferenceShell(t)

	got := x.do(t, "conference from-im 2, 1, 3")
	for _, want := range []string{
		"left the message with Another Resident",
		"started [Conference] Multi-person chat #1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printed %q, want %q", got, want)
		}
	}
	sent := confSent(x)
	if len(sent) != 1 || sent[0].MessageBlock.Dialog != sl.DialogSessionLeave || sent[0].MessageBlock.ToAgentID != testFriend {
		t.Fatalf("sent %+v, want the leave of the message with the first person", sent)
	}
	params, _ := posts.all()[0]["params"].([]any)
	if len(params) != 3 || params[0] != testFriend.String() {
		t.Errorf("params %v, want the message's person first", params)
	}
}

// TestSayingAddingAndLeavingAConference: one run through the words and what
// each puts on the wire.
func TestSayingAddingAndLeavingAConference(t *testing.T) {
	x, posts := conferenceShell(t)
	x.do(t, "conference start 1, 2")

	got := x.do(t, "conference say 1 good evening, both")
	if !strings.Contains(got, "> [Conference] Multi-person chat #1: good evening, both") {
		t.Errorf("saying printed %q", got)
	}
	sent := confSent(x)
	said := sent[len(sent)-1].MessageBlock
	if said.Dialog != sl.DialogSessionSend || said.ToAgentID != testSomebody || said.ID != confSessions[0] ||
		strings.TrimRight(string(said.Message), "\x00") != "good evening, both" {
		t.Errorf("saying sent %+v", said)
	}

	got = x.do(t, "conference add 1 3")
	if !strings.Contains(got, "invited Third Resident into [Conference] Multi-person chat #1") {
		t.Errorf("adding printed %q", got)
	}
	if m := posts.methods(); len(m) != 2 || m[1] != "invite" {
		t.Errorf("posts %v", m)
	}
	invite := posts.all()[1]
	if p, _ := invite["params"].([]any); len(p) != 1 || p[0] != testOther.String() ||
		llsd.String(invite, "session-id") != confSessions[0].String() {
		t.Errorf("invite %v", invite)
	}

	got = x.do(t, "conference leave 1")
	if !strings.Contains(got, "left [Conference] Multi-person chat #1") {
		t.Errorf("leaving printed %q", got)
	}
	sent = confSent(x)
	left := sent[len(sent)-1].MessageBlock
	if left.Dialog != sl.DialogSessionLeave || left.ToAgentID != testSomebody || left.ID != confSessions[0] {
		t.Errorf("leaving sent %+v", left)
	}

	got = x.do(t, "conference say 1 once more")
	if !strings.Contains(got, "is not in [Conference] Multi-person chat #1") {
		t.Errorf("speaking after leaving printed %q", got)
	}
	got = x.do(t, "conference add 1 3")
	if !strings.Contains(got, "is not in [Conference] Multi-person chat #1") {
		t.Errorf("adding after leaving printed %q", got)
	}
	got = x.do(t, "conference say 5 hello")
	if !strings.Contains(got, "there is no conference 5") {
		t.Errorf("a conference nobody has printed %q", got)
	}
}

// TestTheListingNumbersConferencesAndSaysWhereEachStands.
func TestTheListingNumbersConferencesAndSaysWhereEachStands(t *testing.T) {
	x, _ := conferenceShell(t)
	if got := x.do(t, "conference"); !strings.Contains(got, "no conferences") {
		t.Errorf("an empty listing printed %q", got)
	}
	x.do(t, "conference start 1, 2")
	x.do(t, "conference start 2, 3")
	x.do(t, "conference leave 1")

	got := x.do(t, "conference")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("listing %q", got)
	}
	for i, want := range []string{
		" 1  left    [Conference] Multi-person chat  with Another Resident, Example Resident  52ec7e57-…",
		" 2  joined  [Conference] Multi-person chat  with Another Resident, Third Resident  72747e57-…",
	} {
		if lines[i] != want {
			t.Errorf("line %d = %q, want %q", i+1, lines[i], want)
		}
	}
	if strings.Contains(got, confSessions[0].String()) {
		t.Errorf("the whole key is printed:\n%s", got)
	}
}

// TestAConferencesSpeechIsPrintedWithTheConferenceAndTheSpeaker: once, the
// conference behind its label and numbered, our own words not again, and
// who comes and goes said.
func TestAConferencesSpeechIsPrintedWithTheConferenceAndTheSpeaker(t *testing.T) {
	x, _ := conferenceShell(t)
	x.do(t, "conference start 1, 2")
	watching(t, x)

	x.grid.Relay(t, groupSaid(chatTalker, "Example Friend", confSessions[0], "shall we start?"))
	got := waits(t, x, "shall we start?")
	if !strings.Contains(got, "< Example Friend in [Conference] Multi-person chat #1: shall we start?") {
		t.Errorf("printed %q", got)
	}
	x.grid.Relay(t, imFrom(chatTalker, "Example Friend", sl.DialogMessage, "and a private word"))
	got = waits(t, x, "and a private word")
	if n := strings.Count(got, "shall we start?"); n != 1 {
		t.Errorf("printed %d times:\n%s", n, got)
	}

	x.grid.Relay(t, groupSaid(x.s.Me(), "Quark Idlemind", confSessions[0], "my own words"))
	x.grid.RelayEvent(t, "ChatterBoxSessionAgentListUpdates", fmt.Sprintf(
		`<?xml version="1.0" ?><llsd><map><key>session_id</key><uuid>%s</uuid><key>agent_updates</key><map>`+
			`<key>%s</key><map><key>transition</key><string>ENTER</string></map>`+
			`<key>%s</key><map><key>transition</key><string>ENTER</string></map></map></map></llsd>`,
		confSessions[0], x.s.Me(), testOther))
	got = waits(t, x, "came into")
	if !strings.Contains(got, "* (d22b7e57) came into [Conference] Multi-person chat #1") &&
		!strings.Contains(got, "* Third Resident came into [Conference] Multi-person chat #1") {
		t.Errorf("printed %q", got)
	}
	if strings.Contains(got, "my own words") || strings.Contains(got, "Quark Idlemind came into") {
		t.Errorf("this avatar's own words or entrance were printed:\n%s", got)
	}

	x.grid.RelayEvent(t, "ChatterBoxSessionAgentListUpdates", fmt.Sprintf(
		`<?xml version="1.0" ?><llsd><map><key>session_id</key><uuid>%s</uuid><key>updates</key><map>`+
			`<key>%s</key><string>LEAVE</string></map></map></llsd>`, confSessions[0], testOther))
	waits(t, x, "left [Conference] Multi-person chat #1")

	x.grid.RelayEvent(t, "ForceCloseChatterBoxSession", fmt.Sprintf(
		`<?xml version="1.0" ?><llsd><map><key>session_id</key><uuid>%s</uuid><key>reason</key><string>removed</string></map></llsd>`,
		confSessions[0]))
	waits(t, x, "out of [Conference] Multi-person chat #1")
}

// TestAConferenceNamedLikeAPersonIsLabelledAConference: an invitation names
// the conference, and that is whoever's choice.
func TestAConferenceNamedLikeAPersonIsLabelledAConference(t *testing.T) {
	x, _ := conferenceShell(t)
	joined(x, sl.Group{ID: testBuilders, Name: "Example Builders"})
	watching(t, x)

	x.grid.RelayEvent(t, "ChatterBoxInvitation", chatInvitationTo(confSessions[0], chatTalker, "Example Friend", "hello", "Example Resident"))
	got := waits(t, x, "conference join 1")
	if !strings.Contains(got, "in [Conference] Example Resident #1: hello") {
		t.Errorf("printed %q", got)
	}
}

func chatInvitationTo(session, from msg.UUID, name, text, sessionName string) string {
	return strings.Replace(chatInvitation(session, from, name, text),
		"RXhhbXBsZSBCdWlsZGVycwA=", base64.StdEncoding.EncodeToString([]byte(sessionName+"\x00")), 1)
}

// TestAnInvitationToAConferenceIsHeardAnnouncedOnceAndJoinedOnRequest: the
// line each time, the notice once, nothing posted until join, and then the
// acceptance and who is in it.
func TestAnInvitationToAConferenceIsHeardAnnouncedOnceAndJoinedOnRequest(t *testing.T) {
	x, posts := conferenceShell(t)
	joined(x, sl.Group{ID: testBuilders, Name: "Example Builders"})
	watching(t, x)

	x.grid.RelayEvent(t, "ChatterBoxInvitation", chatInvitationTo(confSessions[0], chatTalker, "Example Friend", "anyone about?", "Evening plans"))
	got := waits(t, x, "conference join 1")
	for _, want := range []string{
		"< Example Friend in [Conference] Evening plans #1: anyone about?",
		"* Example Friend invites this avatar into [Conference] Evening plans #1 -- \"conference join 1\" joins it, \"conference leave 1\" refuses",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the screen lacks %q:\n%s", want, got)
		}
	}
	x.grid.RelayEvent(t, "ChatterBoxInvitation", chatInvitationTo(confSessions[0], chatTalker, "Example Friend", "hello?", "Evening plans"))
	got = waits(t, x, "hello?")
	if n := strings.Count(got, "invites this avatar"); n != 1 {
		t.Errorf("announced %d times:\n%s", n, got)
	}
	if len(posts.all()) != 0 || len(confSent(x)) != 0 || x.s.InGroupChat(confSessions[0]) {
		t.Errorf("an invitation was answered: posts %v, sent %d", posts.methods(), len(confSent(x)))
	}
	if got := x.do(t, "conference"); !strings.Contains(got, " 1  invited ") {
		t.Errorf("the listing printed %q", got)
	}
	if got := x.do(t, "conference say 1 yes"); !strings.Contains(got, "\"conference join 1\" accepts the invitation") {
		t.Errorf("speaking before joining printed %q", got)
	}

	begun := time.Now()
	got = x.do(t, "conference join 1")
	// This avatar is listed among the people and nothing names it, so
	// asking after it would wait out the whole of the name timeout.
	if took := time.Since(begun); took > 2*time.Second {
		t.Errorf("joining took %s: it waited for a name this avatar does not need", took)
	}
	if !strings.Contains(got, "joined [Conference] Evening plans #1") || !strings.Contains(got, "with Example Resident, Fourth Resident") {
		t.Errorf("joining printed %q", got)
	}
	if m := posts.methods(); len(m) != 1 || m[0] != "accept invitation" {
		t.Errorf("posts %v", m)
	}
	if got := x.do(t, "conference join 1"); !strings.Contains(got, "nobody has invited this avatar into") {
		t.Errorf("joining twice printed %q", got)
	}
}

// TestTheImmediateInvitationIsAnnouncedToo.
func TestTheImmediateInvitationIsAnnouncedToo(t *testing.T) {
	x, posts := conferenceShell(t)
	watching(t, x)
	x.grid.RelayEvent(t, "ChatterBoxInvitation", fmt.Sprintf(
		`<?xml version="1.0" ?><llsd><map><key>immediate</key><boolean>1</boolean>`+
			`<key>session_id</key><uuid>%s</uuid><key>session_name</key><string></string>`+
			`<key>from_id</key><uuid>%s</uuid><key>from_name</key><string>Example Friend</string></map></llsd>`,
		confSessions[1], chatTalker))
	got := waits(t, x, "conference join 1")
	if !strings.Contains(got, "* Example Friend invites this avatar into [Conference] Conference with Example Friend #1") {
		t.Errorf("printed %q", got)
	}
	if strings.Contains(got, " < ") {
		t.Errorf("a line was printed for an invitation that carries no words:\n%s", got)
	}
	if len(posts.all()) != 0 {
		t.Error("answered")
	}
}

// TestGroupChatIsUntouchedByConferences: a session in the group list is
// still a group's, printed as one.
func TestGroupChatIsUntouchedByConferences(t *testing.T) {
	x := chatShell(t)
	watching(t, x)
	x.grid.Relay(t, groupSaid(chatTalker, "Example Friend", testBuilders, "shall we start?"))
	got := waits(t, x, "shall we start?")
	if !strings.Contains(got, "in [Group] Example Builders:") || strings.Contains(got, "[Conference]") {
		t.Errorf("printed %q", got)
	}
}
