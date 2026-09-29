package sl

// A conference from the library: what each call sends, and what is made
// of what the grid says.  The bodies are written as the viewer's handlers
// read them, not as a grid was seen to send them; see doc/conference.md.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

var (
	confSession = msg.MustParseUUID("52ec7e57-7e57-c0de-f0fd-975a9e215f24")
	confAlice   = msg.MustParseUUID("a81f7e57-7e57-c0de-7928-a6c6088f8922")
	confBob     = msg.MustParseUUID("bdea7e57-7e57-c0de-adb5-e240030c289f")
	confCarol   = msg.MustParseUUID("cc797e57-7e57-c0de-5bb4-419055b1d2cd")
)

// chatSessionPosts records what is posted to ChatSessionRequest, and
// answers each with the status it was given.
type chatSessionPosts struct {
	mu     sync.Mutex
	bodies []string
	status int
}

func (p *chatSessionPosts) handler(rw http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.bodies = append(p.bodies, string(b))
	status := p.status
	p.mu.Unlock()
	if status != 0 {
		http.Error(rw, "no", status)
		return
	}
	rw.Header().Set("Content-Type", "application/llsd+xml")
	fmt.Fprint(rw, `<?xml version="1.0" ?><llsd><map/></llsd>`)
}

func (p *chatSessionPosts) all() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.bodies...)
}

func (p *chatSessionPosts) count() int { return len(p.all()) }

// post decodes the nth post.
func (p *chatSessionPosts) post(t *testing.T, n int) map[string]any {
	t.Helper()
	all := p.all()
	if n >= len(all) {
		t.Fatalf("%d posts, want a %dth", len(all), n+1)
	}
	v, err := llsd.Decode(bytes.NewReader([]byte(all[n])))
	if err != nil {
		t.Fatalf("a post that does not decode: %v: %s", err, all[n])
	}
	return llsd.Map(v)
}

func serveChatSession(t *testing.T, f *fakeBackend) *chatSessionPosts {
	t.Helper()
	p := &chatSessionPosts{}
	f.ServeCap(t, ChatSessionCap, p.handler)
	return p
}

func conferenceReplyBody(temp, session msg.UUID, ok bool, extra string) string {
	b := "0"
	if ok {
		b = "1"
	}
	return llsdEvent(fmt.Sprintf(`<key>success</key><boolean>%s</boolean>`+
		`<key>temp_session_id</key><uuid>%s</uuid><key>session_id</key><uuid>%s</uuid>%s`,
		b, temp, session, extra))
}

// startedConference has a fake session start a conference with people and
// answers as the grid does: with a session id that is not the temporary
// one.  It returns the session and the temporary id the start used.
func startedConference(t *testing.T, w *Session, f *fakeBackend, posts *chatSessionPosts, people ...msg.UUID) (session, temp msg.UUID) {
	t.Helper()
	return startedConferenceAs(t, w, f, posts, confSession, people...)
}

func startedConferenceAs(t *testing.T, w *Session, f *fakeBackend, posts *chatSessionPosts, as msg.UUID, people ...msg.UUID) (session, temp msg.UUID) {
	t.Helper()
	before := posts.count()
	wait := aside(t, func() (msg.UUID, error) { return w.StartConference(context.Background(), people...) })
	waitFor(t, "the start's post", func() bool { return posts.count() > before })
	temp = parseUUIDOrZero(llsd.String(posts.post(t, before), "session-id"))
	f.RelayEvent(t, "ChatterBoxSessionStartReply", conferenceReplyBody(temp, as, true, ""))
	session, err := wait()
	if err != nil {
		t.Fatalf("StartConference: %v", err)
	}
	f.Forget()
	return session, temp
}

// TestStartingAConferencePostsWhatTheViewerPosts: {"method": "start
// conference", "session-id": a temporary id, "params": the people in
// order as uuids, "alt_params": the voice server type} to
// ChatSessionRequest; the session is the one the reply names, which is not
// the temporary id, and it is called what the viewer calls it.
func TestStartingAConferencePostsWhatTheViewerPosts(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	subs := w.GroupChats(8)

	wait := aside(t, func() (msg.UUID, error) {
		return w.StartConference(context.Background(), confAlice, confBob, confCarol)
	})
	waitFor(t, "the post", func() bool { return posts.count() == 1 })
	body := posts.all()[0]
	m := posts.post(t, 0)
	if llsd.String(m, "method") != "start conference" {
		t.Errorf("method %q in %s", llsd.String(m, "method"), body)
	}
	temp := parseUUIDOrZero(llsd.String(m, "session-id"))
	if temp.IsZero() || temp == confSession {
		t.Fatalf("session-id %s in %s, want a fresh temporary id", temp, body)
	}
	if !strings.Contains(body, fmt.Sprintf("<uuid>%s</uuid>", temp)) {
		t.Errorf("the temporary id is not a uuid in %s", body)
	}
	params, _ := m["params"].([]any)
	want := []msg.UUID{confAlice, confBob, confCarol}
	if len(params) != len(want) {
		t.Fatalf("params %v, want the three people", params)
	}
	for i, id := range want {
		if params[i] != id.String() {
			t.Errorf("params[%d] = %v, want %s (the order is the caller's)", i, params[i], id)
		}
	}
	if strings.Count(body, "<uuid>") != 4 {
		t.Errorf("%s: want the session and the three people as uuids", body)
	}
	if alt := llsd.Map(m["alt_params"]); llsd.String(alt, "voice_server_type") != "vivox" {
		t.Errorf("alt_params %v, want the viewer's default of vivox", m["alt_params"])
	}
	if got := len(f.Sent()); got != 0 {
		t.Errorf("sent %s on the circuit as well", f.describe())
	}

	f.RelayEvent(t, "ChatterBoxSessionStartReply", conferenceReplyBody(temp, confSession, true, ""))
	session, err := wait()
	if err != nil {
		t.Fatal(err)
	}
	if session != confSession {
		t.Errorf("session %s, want the one the reply named", session)
	}
	g := nextChat(t, subs)
	if g.Kind != GroupChatJoined || !g.Conference || g.Group != confSession || g.GroupName != "Multi-person chat" {
		t.Errorf("event %+v, want a conference joined", g)
	}
	cs := w.Conferences()
	if len(cs) != 1 || cs[0].ID != confSession || !cs[0].Joined || cs[0].Name != ConferenceName ||
		len(cs[0].Guests) != 3 {
		t.Errorf("Conferences() = %+v", cs)
	}
	if w.InGroupChat(temp) {
		t.Error("the temporary id was kept as a chat")
	}
}

// TestAConferenceStartTakesTheRegionsVoiceServerType: what the viewer's
// module reports follows SimulatorFeatures.
func TestAConferenceStartTakesTheRegionsVoiceServerType(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	f.ServeCap(t, "SimulatorFeatures", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprint(rw, `<?xml version="1.0" ?><llsd><map><key>VoiceServerType</key><string>webrtc</string></map></llsd>`)
	})
	startedConference(t, w, f, posts, confAlice, confBob)
	alt := llsd.Map(posts.post(t, 0)["alt_params"])
	if got := llsd.String(alt, "voice_server_type"); got != "webrtc" {
		t.Errorf("voice_server_type %q, want webrtc", got)
	}
}

// TestARefusedStartFallsBackToTheOlderMessage: on a 400 the viewer sends
// dialog 16 to the first person, the temporary id as the session, and the
// people's ids in order as the bucket.
func TestARefusedStartFallsBackToTheOlderMessage(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	posts.status = 400

	wait := aside(t, func() (msg.UUID, error) {
		return w.StartConference(context.Background(), confAlice, confBob)
	})
	m := waitSent[*msg.ImprovedInstantMessage](t, f)
	b := m.MessageBlock
	temp := parseUUIDOrZero(llsd.String(posts.post(t, 0), "session-id"))
	if b.Dialog != DialogSessionConferenceStart || b.ToAgentID != confAlice || b.ID != temp || temp.IsZero() {
		t.Errorf("dialog %d to %s id %s, want 16 to the first person with the post's temporary id %s",
			b.Dialog, b.ToAgentID, b.ID, temp)
	}
	wantBucket := append(append([]byte(nil), confAlice[:]...), confBob[:]...)
	if !bytes.Equal(b.BinaryBucket, wantBucket) {
		t.Errorf("bucket %x, want the two ids in order %x", b.BinaryBucket, wantBucket)
	}
	if b.Offline != 0 || trimNul(b.Message) != "" || b.Position != f.presence.Position {
		t.Errorf("offline %d message %q position %v", b.Offline, b.Message, b.Position)
	}
	f.RelayEvent(t, "ChatterBoxSessionStartReply", conferenceReplyBody(temp, confSession, true, ""))
	if id, err := wait(); err != nil || id != confSession {
		t.Fatalf("StartConference: %v, %v", id, err)
	}
}

// TestAnyOtherFailureOfTheStartIsNotRetried: only a 400 is answered with
// the older message.
func TestAnyOtherFailureOfTheStartIsNotRetried(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	posts.status = 403
	_, err := w.StartConference(context.Background(), confAlice, confBob)
	var ce *CapError
	if !errors.As(err, &ce) || ce.Status != 403 {
		t.Fatalf("error %v, want the 403", err)
	}
	if len(f.Sent()) != 0 {
		t.Errorf("sent %s", f.describe())
	}
}

// TestAConferenceNeedsTwoOtherPeopleAndNoOneTwice: nothing is sent or
// posted for a list the viewer would not have made.
func TestAConferenceNeedsTwoOtherPeopleAndNoOneTwice(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	for name, people := range map[string][]msg.UUID{
		"nobody":          nil,
		"one person":      {confAlice},
		"this avatar":     {confAlice, testAgentID},
		"somebody twice":  {confAlice, confBob, confAlice},
		"the zero person": {confAlice, {}},
	} {
		if _, err := w.StartConference(context.Background(), people...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if posts.count() != 0 || len(f.Sent()) != 0 {
		t.Errorf("%d posts, sent %s", posts.count(), f.describe())
	}
}

// TestARefusedConferenceSaysWhy: the reason is the viewer's words, the
// error says it was a conference, and nothing is joined.
func TestARefusedConferenceSaysWhy(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	subs := w.GroupChats(8)

	wait := aside(t, func() (msg.UUID, error) {
		return w.StartConference(context.Background(), confAlice, confBob)
	})
	waitFor(t, "the post", func() bool { return posts.count() == 1 })
	temp := parseUUIDOrZero(llsd.String(posts.post(t, 0), "session-id"))
	f.RelayEvent(t, "ChatterBoxSessionStartReply", conferenceReplyBody(temp, temp, false,
		`<key>error</key><string>insufficient_perms_error</string>`))
	_, err := wait()
	var ge *GroupChatError
	if !errors.As(err, &ge) || !ge.Conference || ge.Key != "insufficient_perms_error" ||
		!strings.Contains(err.Error(), "you do not have sufficient permissions") ||
		!strings.Contains(err.Error(), "conference") {
		t.Fatalf("error %v (%+v)", err, ge)
	}
	if len(w.Conferences()) != 0 {
		t.Errorf("Conferences() = %+v after a refusal", w.Conferences())
	}
	f.RelayEvent(t, "SomethingElse", llsdEvent(""))
	noChat(t, subs, "a refused conference start")
}

// TestAConferenceStartNobodyAnswersTimesOut: the wait is
// Options.GroupChatTimeout.
func TestAConferenceStartNobodyAnswersTimesOut(t *testing.T) {
	w, f := newFakeSession(t)
	serveChatSession(t, f)
	w.SetOptions(Options{GroupChatTimeout: 50 * time.Millisecond})
	_, err := w.StartConference(context.Background(), confAlice, confBob)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error %v, want a timeout", err)
	}
	if len(w.Conferences()) != 0 {
		t.Error("a conference nobody answered is listed")
	}
	if n := func() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.confStarts) }(); n != 0 {
		t.Errorf("%d starts still waiting", n)
	}
}

// TestStartingWithTheSamePeopleAgainReturnsTheSameConference: the viewer
// finds the ad-hoc session it already has and starts nothing.
func TestStartingWithTheSamePeopleAgainReturnsTheSameConference(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	session, _ := startedConference(t, w, f, posts, confAlice, confBob)

	again, err := w.StartConference(context.Background(), confBob, confAlice)
	if err != nil || again != session {
		t.Fatalf("StartConference again: %v, %v; want %s", again, err, session)
	}
	if posts.count() != 1 {
		t.Errorf("%d posts, want the one", posts.count())
	}

	// With somebody else it is another conference.
	other := msg.MustParseUUID("72747e57-7e57-c0de-6488-8c360b21eafd")
	got, _ := startedConferenceAs(t, w, f, posts, other, confAlice, confCarol)
	if got != other || len(w.Conferences()) != 2 {
		t.Errorf("a different set gave %s and %d conferences", got, len(w.Conferences()))
	}
}

// TestAnInstantMessageBecomesAConferenceByLeavingItAndStarting: the leave
// goes first, for the message's own session and to the other person, and
// then the start has the other person first and the added people after.
func TestAnInstantMessageBecomesAConferenceByLeavingItAndStarting(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)

	wait := aside(t, func() (msg.UUID, error) {
		return w.ConferenceFromIM(context.Background(), confAlice, confBob, confCarol)
	})
	waitFor(t, "the post", func() bool { return posts.count() == 1 })
	left := onlySent[*msg.ImprovedInstantMessage](t, f).MessageBlock
	if left.Dialog != DialogSessionLeave || left.ToAgentID != confAlice ||
		left.ID != imSessionID(testAgentID, confAlice) {
		t.Errorf("dialog %d to %s id %s, want 18 to the other person for the message's own session",
			left.Dialog, left.ToAgentID, left.ID)
	}
	if trimNul(left.Message) != "" || !left.RegionID.IsZero() || left.Position != (msg.Vector3{}) {
		t.Errorf("message %q region %s position %v", left.Message, left.RegionID, left.Position)
	}
	m := posts.post(t, 0)
	params, _ := m["params"].([]any)
	if len(params) != 3 || params[0] != confAlice.String() || params[1] != confBob.String() ||
		params[2] != confCarol.String() {
		t.Errorf("params %v, want the other person first and the added people after", params)
	}
	temp := parseUUIDOrZero(llsd.String(m, "session-id"))
	f.RelayEvent(t, "ChatterBoxSessionStartReply", conferenceReplyBody(temp, confSession, true, ""))
	if id, err := wait(); err != nil || id != confSession {
		t.Fatalf("ConferenceFromIM: %v, %v", id, err)
	}
	if _, err := w.ConferenceFromIM(context.Background(), confAlice); err == nil {
		t.Error("nobody added, and no error")
	}
}

// TestAddingToAConferencePostsTheInvite: {"method": "invite", "session-id":
// the session, "params": the new people}; people already in it or invited
// are left out, and a session this one is not in is refused before any
// post.
func TestAddingToAConferencePostsTheInvite(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	session, _ := startedConference(t, w, f, posts, confAlice, confBob)

	if err := w.AddToConference(context.Background(), session, confBob, confCarol); err != nil {
		t.Fatal(err)
	}
	m := posts.post(t, 1)
	if llsd.String(m, "method") != "invite" || llsd.String(m, "session-id") != session.String() {
		t.Errorf("post %v", m)
	}
	params, _ := m["params"].([]any)
	if len(params) != 1 || params[0] != confCarol.String() {
		t.Errorf("params %v, want only the one who was not in it", params)
	}
	if !strings.Contains(posts.all()[1], fmt.Sprintf("<uuid>%s</uuid>", confCarol)) {
		t.Errorf("the person is not a uuid in %s", posts.all()[1])
	}
	if g := w.Conferences()[0].Guests; len(g) != 3 {
		t.Errorf("guests %v, want the invited one remembered", g)
	}

	// Everybody named is in already: nothing more is posted.
	if err := w.AddToConference(context.Background(), session, confAlice, confCarol); err == nil {
		t.Error("adding people who are in already was not refused")
	}
	if err := w.AddToConference(context.Background(), msg.MustParseUUID("84c57e57-7e57-c0de-9279-b56cd7e8719d"), confCarol); !errors.Is(err, ErrNotInConference) {
		t.Errorf("error %v, want ErrNotInConference for a session not known", err)
	}
	if posts.count() != 2 || len(f.Sent()) != 0 {
		t.Errorf("%d posts, sent %s", posts.count(), f.describe())
	}

	// A conference that has been left is one this session is not in.
	if err := w.LeaveConference(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	if err := w.AddToConference(context.Background(), session, confBob, confCarol); !errors.Is(err, ErrNotInConference) {
		t.Errorf("adding to a conference that was left: %v", err)
	}
	if posts.count() != 2 {
		t.Errorf("%d posts, want the two", posts.count())
	}
}

// TestSpeakingInAConferenceGoesToTheOtherParticipant: dialog 17 to the
// first person invited, the session as the id, split as the viewer splits;
// and not before it is joined.
func TestSpeakingInAConferenceGoesToTheOtherParticipant(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	if err := w.SayToConference(context.Background(), confSession, "hello"); !errors.Is(err, ErrNotInConference) {
		t.Fatalf("spoke before joining: %v", err)
	}
	session, _ := startedConference(t, w, f, posts, confAlice, confBob)

	if err := w.SayToConference(context.Background(), session, "hello, both"); err != nil {
		t.Fatal(err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	b := m.MessageBlock
	if b.Dialog != DialogSessionSend || b.ToAgentID != confAlice || b.ID != confSession {
		t.Errorf("dialog %d to %s id %s, want 17 to the first person with the session as id", b.Dialog, b.ToAgentID, b.ID)
	}
	if trimNul(b.Message) != "hello, both" || b.Offline != 0 || b.FromGroup ||
		!b.RegionID.IsZero() || b.Position != (msg.Vector3{}) || len(b.BinaryBucket) != 1 {
		t.Errorf("message %+v", b)
	}
	if !f.Sent()[0].Reliable {
		t.Error("sent unreliably")
	}

	f.Forget()
	long := strings.Repeat("word ", 500)
	if err := w.SayToConference(context.Background(), session, long); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, im := range sentOf[*msg.ImprovedInstantMessage](f) {
		if im.MessageBlock.ToAgentID != confAlice {
			t.Errorf("a piece went to %s", im.MessageBlock.ToAgentID)
		}
		got = append(got, trimNul(im.MessageBlock.Message))
	}
	if len(got) < 3 || strings.Join(got, "") != long {
		t.Errorf("%d pieces, want the text split and whole", len(got))
	}
	if err := w.SayToConference(context.Background(), session, "  "); err == nil {
		t.Error("saying nothing was allowed")
	}
}

// TestLeavingAConferenceSendsWhatTheViewerSends: dialog 18 to the other
// participant with the session as id; the conference stays listed, not
// joined, so numbers given to conferences do not move.
func TestLeavingAConferenceSendsWhatTheViewerSends(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	session, _ := startedConference(t, w, f, posts, confAlice, confBob)

	if err := w.LeaveConference(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	b := onlySent[*msg.ImprovedInstantMessage](t, f).MessageBlock
	if b.Dialog != DialogSessionLeave || b.ToAgentID != confAlice || b.ID != session {
		t.Errorf("dialog %d to %s id %s, want 18 to the first person with the session as id", b.Dialog, b.ToAgentID, b.ID)
	}
	if trimNul(b.Message) != "" || !b.RegionID.IsZero() || b.Position != (msg.Vector3{}) || b.Offline != 0 {
		t.Errorf("message %q region %s position %v", b.Message, b.RegionID, b.Position)
	}
	cs := w.Conferences()
	if len(cs) != 1 || cs[0].Joined {
		t.Errorf("Conferences() = %+v, want it listed and not joined", cs)
	}
	if err := w.SayToConference(context.Background(), session, "hello"); !errors.Is(err, ErrNotInConference) {
		t.Errorf("spoke after leaving: %v", err)
	}
	if err := w.LeaveConference(context.Background(), msg.UUID{}); err == nil {
		t.Error("no conference was left without an error")
	}
}

// TestWhatAConferenceSaysIsMarkedAsOne: a dialog 17 message for a known
// conference is an event with Conference set and its name, and stays an
// instant message; who came and went, and being put out, are marked too.
func TestWhatAConferenceSaysIsMarkedAsOne(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	session, _ := startedConference(t, w, f, posts, confAlice, confBob)
	subs := w.GroupChats(8)
	ims := w.IMs(8)

	f.Relay(t, arrivingIM(confAlice, "Example Resident", DialogSessionSend, session, "good evening"))
	g := nextChat(t, subs)
	if g.Kind != GroupChatSaid || !g.Conference || g.Group != session || g.GroupName != ConferenceName ||
		g.From != confAlice || g.Text != "good evening" || g.Sender() != SenderPerson {
		t.Errorf("event %+v", g)
	}
	if im := <-ims; im.Dialog != DialogSessionSend {
		t.Errorf("dialog %d", im.Dialog)
	}

	f.RelayEvent(t, "ChatterBoxSessionAgentListUpdates", llsdEvent(fmt.Sprintf(
		`<key>session_id</key><uuid>%s</uuid><key>agent_updates</key><map><key>%s</key><map>`+
			`<key>transition</key><string>ENTER</string></map></map>`, session, confCarol)))
	if g := nextChat(t, subs); g.Kind != GroupChatEntered || !g.Conference || g.From != confCarol {
		t.Errorf("event %+v, want carol entering a conference", g)
	}
	if got := w.Conferences()[0].Members; len(got) != 1 || got[0] != confCarol {
		t.Errorf("members %v", got)
	}
	f.RelayEvent(t, "ChatterBoxSessionAgentListUpdates", llsdEvent(fmt.Sprintf(
		`<key>session_id</key><uuid>%s</uuid><key>updates</key><map><key>%s</key><string>LEAVE</string></map>`,
		session, confCarol)))
	if g := nextChat(t, subs); g.Kind != GroupChatLeft || !g.Conference {
		t.Errorf("event %+v, want a leave in a conference", g)
	}

	f.RelayEvent(t, "ChatterBoxSessionEventReply", llsdEvent(fmt.Sprintf(
		`<key>success</key><boolean>0</boolean><key>session_id</key><uuid>%s</uuid><key>error</key><string>muted</string>`, session)))
	if g := nextChat(t, subs); g.Kind != GroupChatRefused || !g.Conference {
		t.Errorf("event %+v, want a refusal in a conference", g)
	}

	f.RelayEvent(t, "ForceCloseChatterBoxSession", llsdEvent(fmt.Sprintf(
		`<key>session_id</key><uuid>%s</uuid><key>reason</key><string>removed</string>`, session)))
	if g := nextChat(t, subs); g.Kind != GroupChatClosed || !g.Conference {
		t.Errorf("event %+v, want being put out of a conference", g)
	}
	if w.Conferences()[0].Joined {
		t.Error("still joined after being put out")
	}
}

// TestASessionNotInTheGroupListIsAConference: with a list that lacks the
// session it is a conference's; with one that has it, a group's; with no
// list, as before conferences, a group's.
func TestASessionNotInTheGroupListIsAConference(t *testing.T) {
	for _, c := range []struct {
		name   string
		groups []Group
		want   bool
	}{
		{"a list without it", []Group{{ID: chatOther, Name: "Some Other Group"}}, true},
		{"a list with it", []Group{{ID: confSession, Name: "Example Club"}}, false},
		{"no list", nil, false},
	} {
		w, f := newFakeSession(t)
		f.presence.Groups = c.groups
		subs := w.GroupChats(8)
		f.Relay(t, arrivingIM(confAlice, "Example Resident", DialogSessionSend, confSession, "hi"))
		if g := nextChat(t, subs); g.Conference != c.want {
			t.Errorf("%s: Conference = %v, want %v", c.name, g.Conference, c.want)
		}
	}
}

// conferenceInvitation is the instantmessage shape for a session that
// is not one of the avatar's groups.
func conferenceInvitation(name string) string {
	return invitationBody(confSession, confAlice, "Example Resident", "are you there?", name)
}

// TestAConferenceInvitationIsSurfacedAndNotAnswered: the viewer answers
// this shape at once; this says it is a conference, who and what, and
// posts nothing until AcceptConference.
func TestAConferenceInvitationIsSurfacedAndNotAnswered(t *testing.T) {
	w, f := newFakeSession(t)
	f.presence.Groups = []Group{{ID: chatOther, Name: "Some Other Group"}}
	posts := serveChatSession(t, f)
	subs := w.GroupChats(8)

	f.RelayEvent(t, "ChatterBoxInvitation", conferenceInvitation("Evening plans"))

	g := nextChat(t, subs)
	if g.Kind != GroupChatInvited || !g.Conference || g.Group != confSession || g.GroupName != "Evening plans" ||
		g.From != confAlice || g.Text != "are you there?" || g.Sender() != SenderPerson {
		t.Errorf("event %+v", g)
	}
	if posts.count() != 0 || len(f.Sent()) != 0 {
		t.Errorf("answered an invitation nobody accepted: %d posts, sent %s", posts.count(), f.describe())
	}
	cs := w.Conferences()
	if len(cs) != 1 || !cs[0].Invited || cs[0].Joined || cs[0].Name != "Evening plans" {
		t.Errorf("Conferences() = %+v, want it listed as an invitation", cs)
	}
	if got := w.Name(confAlice); got != "Example Resident" {
		t.Errorf("the inviter is called %q", got)
	}
}

// TestAcceptingAConferenceInvitationPostsWhatTheViewerPosts: {"method":
// "accept invitation", "session-id": the session}; it is joined, its
// members are the reply's, and messages go to the session itself.
func TestAcceptingAConferenceInvitationPostsWhatTheViewerPosts(t *testing.T) {
	w, f := newFakeSession(t)
	f.presence.Groups = []Group{{ID: chatOther, Name: "Some Other Group"}}
	var mu sync.Mutex
	var bodies []string
	f.ServeCap(t, ChatSessionCap, func(rw http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		rw.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(rw, `<?xml version="1.0" ?><llsd><map><key>agent_info</key><map><key>%s</key><map/></map></map></llsd>`, confAlice)
	})
	subs := w.GroupChats(8)
	f.RelayEvent(t, "ChatterBoxInvitation", conferenceInvitation("Evening plans"))
	nextChat(t, subs)

	if err := w.AcceptConference(context.Background(), confSession); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), bodies...)
	mu.Unlock()
	if len(got) != 1 || !strings.Contains(got[0], `<key>method</key><string>accept invitation</string>`) ||
		!strings.Contains(got[0], fmt.Sprintf(`<key>session-id</key><uuid>%s</uuid>`, confSession)) {
		t.Fatalf("posts %v", got)
	}
	g := nextChat(t, subs)
	if g.Kind != GroupChatJoined || !g.Conference || g.Group != confSession || g.GroupName != "Evening plans" {
		t.Errorf("event %+v, want a conference joined", g)
	}
	cs := w.Conferences()
	if len(cs) != 1 || !cs[0].Joined || cs[0].Invited || len(cs[0].Members) != 1 || cs[0].Members[0] != confAlice {
		t.Errorf("Conferences() = %+v", cs)
	}

	// The session is the other participant of one invited into.
	f.Forget()
	if err := w.SayToConference(context.Background(), confSession, "yes"); err != nil {
		t.Fatal(err)
	}
	if b := onlySent[*msg.ImprovedInstantMessage](t, f).MessageBlock; b.ToAgentID != confSession || b.ID != confSession {
		t.Errorf("to %s id %s, want the session as both", b.ToAgentID, b.ID)
	}
	f.Forget()
	if err := w.LeaveConference(context.Background(), confSession); err != nil {
		t.Fatal(err)
	}
	if b := onlySent[*msg.ImprovedInstantMessage](t, f).MessageBlock; b.ToAgentID != confSession || b.Dialog != DialogSessionLeave {
		t.Errorf("leave to %s dialog %d, want the session itself", b.ToAgentID, b.Dialog)
	}
}

// TestAcceptingAnInvitationThatIsNotWaitingIsRefused: nothing is posted for
// a session nobody invited this one into, or one already joined.
func TestAcceptingAnInvitationThatIsNotWaitingIsRefused(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	if err := w.AcceptConference(context.Background(), confSession); !errors.Is(err, ErrNotInvited) {
		t.Errorf("error %v, want ErrNotInvited", err)
	}
	session, _ := startedConference(t, w, f, posts, confAlice, confBob)
	if err := w.AcceptConference(context.Background(), session); !errors.Is(err, ErrNotInvited) {
		t.Errorf("accepting one already joined: %v", err)
	}
	if posts.count() != 1 {
		t.Errorf("%d posts, want the start only", posts.count())
	}
}

// TestAGroupInvitationIsStillAGroupsInTheList: a session in the group list
// is not turned into a conference.
func TestAGroupInvitationIsStillAGroupsInTheList(t *testing.T) {
	w, f := newFakeSession(t)
	f.presence.Groups = []Group{{ID: chatGroup, Name: chatName}}
	subs := w.GroupChats(8)
	f.RelayEvent(t, "ChatterBoxInvitation", invitationBody(chatGroup, chatSpeaker, "Example Resident", "anyone here?", chatName))
	g := nextChat(t, subs)
	if g.Conference || g.Kind != GroupChatInvited || g.GroupName != chatName {
		t.Errorf("event %+v", g)
	}
	if len(w.Conferences()) != 0 {
		t.Errorf("Conferences() = %+v", w.Conferences())
	}
}

func immediateBody(session, from msg.UUID, fromName, sessionName string) string {
	return llsdEvent(fmt.Sprintf(`<key>immediate</key><boolean>1</boolean>`+
		`<key>session_id</key><uuid>%s</uuid><key>session_name</key><string>%s</string>`+
		`<key>from_id</key><uuid>%s</uuid><key>from_name</key><string>%s</string>`,
		session, sessionName, from, fromName))
}

// TestTheImmediateInvitationIsAConferences: it asks, in the viewer, and
// carries no text; here it is an event named by session_name, or by
// "Conference with" whoever invited when there is none; and for one of the
// avatar's groups it is a call and not an event.
func TestTheImmediateInvitationIsAConferences(t *testing.T) {
	w, f := newFakeSession(t)
	posts := serveChatSession(t, f)
	subs := w.GroupChats(8)

	f.RelayEvent(t, "ChatterBoxInvitation", immediateBody(confSession, confAlice, "Example Resident", "Evening plans"))
	g := nextChat(t, subs)
	if g.Kind != GroupChatInvited || !g.Conference || g.Group != confSession || g.GroupName != "Evening plans" ||
		g.From != confAlice || g.FromName != "Example Resident" || g.Text != "" {
		t.Errorf("event %+v", g)
	}
	if posts.count() != 0 {
		t.Error("answered an invitation nobody accepted")
	}

	other := msg.MustParseUUID("72747e57-7e57-c0de-6488-8c360b21eafd")
	f.RelayEvent(t, "ChatterBoxInvitation", immediateBody(other, confBob, "Another Resident", ""))
	if g := nextChat(t, subs); g.GroupName != "Conference with Another Resident" {
		t.Errorf("an unnamed invitation is called %q", g.GroupName)
	}

	f.presence.Groups = []Group{{ID: chatGroup, Name: chatName}}
	f.RelayEvent(t, "ChatterBoxInvitation", immediateBody(chatGroup, confBob, "Another Resident", chatName))
	f.RelayEvent(t, "SomethingElse", llsdEvent(""))
	noChat(t, subs, "the voice invitation of a group")
}

// TestAConferenceIsLabelledAndNeverBare.
func TestAConferenceIsLabelledAndNeverBare(t *testing.T) {
	if got := SenderConference.Label("Evening plans"); got != "[Conference] Evening plans" {
		t.Errorf("Label = %q", got)
	}
}
