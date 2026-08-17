package sl

// Who somebody is, when nothing said.
//
// A uuid is what arrives and a name is what a person can act on, and
// the two are joined only here.  The cache is what keeps that from
// costing a round trip every time somebody is mentioned, so the
// interesting failures are not "the wrong name" but "asked twice",
// "asked for a name it already had", and "waited for an answer that was
// never coming" -- none of which a live session would show up as
// anything worse than being slow.
//
// The other half is searching.  Two mechanisms answer it, the
// capability and the old whole-name message, and they are reached by
// asking the backend whether the capability is there; both are driven
// here, one over httptest and one over a relayed reply, so neither
// needs the grid.

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	nemo    = msg.MustParseUUID("0e497e57-7e57-c0de-3a9f-ac1c32e2e746")
	unnamed = msg.MustParseUUID("051b7e57-7e57-c0de-f8f9-771b98f00baa")
)

// namesReply is the answer a simulator sends to UUIDNameRequest.
func namesReply(names map[msg.UUID]string) *msg.UUIDNameReply {
	r := &msg.UUIDNameReply{}
	for id, n := range names {
		first, last, _ := strings.Cut(n, " ")
		r.UUIDNameBlock = append(r.UUIDNameBlock, msg.UUIDNameReply_UUIDNameBlock{
			ID:        id,
			FirstName: append([]byte(first), 0),
			LastName:  append([]byte(last), 0),
		})
	}
	return r
}

// TestANameIsAskedForOnceAndThenRemembered: a question already out is a
// round trip already spent, and a name already known is one that need
// never be spent again.  Asking every time something mentions somebody
// would be a request per line of chat.
func TestANameIsAskedForOnceAndThenRemembered(t *testing.T) {
	w, f := newFakeSession(t)
	ctx := context.Background()

	// Nothing worth asking about: one name is known already and the
	// other is nobody.
	w.learn(somebody, "Quark Idlemind")
	if err := w.AskNames(ctx, []msg.UUID{somebody, {}}); err != nil {
		t.Fatalf("AskNames: %v", err)
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("asking about a name we have sent %s", f.describe())
	}

	if err := w.AskNames(ctx, []msg.UUID{somebodyElse}); err != nil {
		t.Fatalf("AskNames: %v", err)
	}
	q := onlySent[*msg.UUIDNameRequest](t, f)
	if len(q.UUIDNameBlock) != 1 || q.UUIDNameBlock[0].ID != somebodyElse {
		t.Errorf("asked about %+v", q.UUIDNameBlock)
	}

	// The question is out, so a second caller asking the same thing
	// waits for the same answer rather than asking again.
	if err := w.AskNames(ctx, []msg.UUID{somebodyElse}); err != nil {
		t.Fatalf("AskNames: %v", err)
	}
	if got := len(sentOf[*msg.UUIDNameRequest](f)); got != 1 {
		t.Errorf("%d questions went out about one name", got)
	}

	f.Relay(t, namesReply(map[msg.UUID]string{somebodyElse: "Someone Else"}))
	if got := w.Name(somebodyElse); got != "Someone Else" {
		t.Errorf("the answer left the name as %q", got)
	}
	f.Forget()
	if err := w.AskNames(ctx, []msg.UUID{somebodyElse}); err != nil {
		t.Fatalf("AskNames: %v", err)
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("a name that has been answered was asked for again: %s", f.describe())
	}

	// A question that could not be sent is a failure and not a silent
	// wait for an answer nobody will send.
	f.FailSends(errors.New("the circuit is gone"))
	if err := w.AskNames(ctx, []msg.UUID{nemo}); err == nil {
		t.Error("AskNames reported success though nothing was sent")
	}
}

// TestTheFirstQuestionBuildsTheMapItIsNotedIn: the map of questions
// outstanding is made on the way past rather than by New, so a session
// that has been through none of New's setup must not drop the first
// question into a nil map.
func TestTheFirstQuestionBuildsTheMapItIsNotedIn(t *testing.T) {
	var asked []msg.UUID
	w := &Session{}
	w.sendFn = func(m msg.Message) error {
		q, ok := m.(*msg.UUIDNameRequest)
		if !ok {
			t.Errorf("asking a name sent %s", m.MsgInfo().Name)
			return nil
		}
		for _, b := range q.UUIDNameBlock {
			asked = append(asked, b.ID)
		}
		return nil
	}

	if err := w.AskNames(context.Background(), []msg.UUID{somebody}); err != nil {
		t.Fatalf("AskNames: %v", err)
	}
	if len(asked) != 1 || asked[0] != somebody {
		t.Errorf("asked about %v, want just %s", asked, somebody)
	}
}

// TestNamesGivesUpWithWhatItHas: a listing of thirty avatars must not
// be lost because the simulator will not name one of them.  Waiting for
// all of them or nothing turns one silent uuid into a failed command.
func TestNamesGivesUpWithWhatItHas(t *testing.T) {
	w, f := newFakeSession(t)
	f.AnswerNames(t, map[msg.UUID]string{somebody: "Quark Idlemind"})

	// A timeout of zero asks for the default, which is not reached
	// here: the answer is in before the first look.
	got := w.Names(context.Background(), []msg.UUID{somebody, {}}, 0)
	if len(got) != 1 || got[somebody] != "Quark Idlemind" {
		t.Errorf("Names = %v", got)
	}

	// Nobody will name this one, so the wait runs out and the answer
	// is what arrived, which is nothing.
	start := time.Now()
	if got := w.Names(context.Background(), []msg.UUID{unnamed}, 150*time.Millisecond); len(got) != 0 {
		t.Errorf("Names = %v, want nothing", got)
	}
	if waited := time.Since(start); waited < 100*time.Millisecond {
		t.Errorf("gave up after %s, before the timeout it was given", waited)
	}

	// A caller in a hurry cuts the wait short with the context, and
	// still gets what had arrived by then.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := w.Names(ctx, []msg.UUID{somebody, unnamed}, 30*time.Second); len(got) != 1 {
		t.Errorf("a cancelled wait answered %v, want the one name already known", got)
	}
}

// TestFindMatchesTheWayAPersonTypes: a person naming somebody types
// part of a name, and every match has to come back rather than a guess,
// so that an ambiguous one can be refused instead of acted on.
func TestFindMatchesTheWayAPersonTypes(t *testing.T) {
	var (
		quark   = somebody
		quarkJ  = somebodyElse
		someone = unnamed
	)
	// Nemo has no surname, which is not a curiosity: the grid's older
	// accounts are one word, and splitting on a space that is not
	// there must not match everybody.
	w := &Session{names: map[msg.UUID]string{
		quark:   "Quark Idlemind",
		quarkJ:  "Quark Idlemindful",
		someone: "Someone Else",
		nemo:    "Nemo",
	}}

	cases := []struct {
		name  string
		want  string
		found []msg.UUID
	}{
		{"nothing typed", "", nil},
		{"only spaces", "   ", nil},
		{"the whole name", "Quark Idlemind", []msg.UUID{quark}},
		{"the whole name in any case", "qUARK iDLEMIND", []msg.UUID{quark}},
		// The start of the whole name matches both, in name order.
		{"the start of it", "quark i", []msg.UUID{quark, quarkJ}},
		{"the start of the surname", "idlemindf", []msg.UUID{quarkJ}},
		{"the start of the first name", "someo", []msg.UUID{someone}},
		{"a one word name", "nem", []msg.UUID{nemo}},
		{"the middle of a name", "mind", nil},
		{"somebody nobody has heard of", "trousers", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := w.Find(c.want)
			if len(got) != len(c.found) {
				t.Fatalf("Find(%q) found %d, want %d", c.want, len(got), len(c.found))
			}
			for i := range got {
				if got[i] != c.found[i] {
					t.Errorf("Find(%q)[%d] = %s, want %s",
						c.want, i, w.names[got[i]], w.names[c.found[i]])
				}
			}
		})
	}
}

// avatarAt is one avatar in the region, as the backend describes it.
func avatarAt(id msg.UUID, x, y, z float32) *Seen {
	return &Seen{
		Object:   Object{ID: id, Local: 1},
		PCode:    pcodeAvatar,
		Position: msg.Vector3{X: x, Y: y, Z: z},
	}
}

// TestNearbyIsEverybodyButUs: the avatars come from what the backend
// has been listening to since before this program started, so this is
// the only way to see somebody who was already standing there.  We are
// not "nearby" to ourselves, and a prim is not somebody.
func TestNearbyIsEverybodyButUs(t *testing.T) {
	w, f := newFakeSession(t)

	// The avatar is at 128,128,25; these are 3, 4 and 4 metres off.
	far := avatarAt(somebodyElse, 128, 132, 25)
	near := avatarAt(somebody, 128, 128, 28)
	alsoFar := avatarAt(nemo, 132, 128, 25)
	f.objects = []*Seen{
		far, near, alsoFar,
		avatarAt(testAgentID, 128, 128, 25),                       // us
		{Object: Object{ID: unnamed, Local: 9}, PCode: pcodePrim}, // a prim
	}
	f.AnswerNames(t, map[msg.UUID]string{
		somebody:     "Quark Idlemind",
		somebodyElse: "Someone Else",
	})

	// Nemo is never named, and waiting the full three seconds for a
	// name is not what is being tested here; the context cuts it short
	// the way a caller in a hurry would.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	all, err := w.Nearby(ctx)
	if err != nil {
		t.Fatalf("Nearby: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("Nearby found %d, want 3: %+v", len(all), all)
	}
	if all[0].ID != somebody || all[0].Name != "Quark Idlemind" {
		t.Errorf("nearest is %+v, want the one three metres up", all[0])
	}
	if got := all[0].Distance; got < 2.9 || got > 3.1 {
		t.Errorf("distance = %v, want 3", got)
	}
	// The two at the same distance are in name order, and the one
	// nobody named still appears, shortened.
	if all[1].ID != nemo || all[2].ID != somebodyElse {
		t.Errorf("the two at four metres came back %s then %s", all[1].Name, all[2].Name)
	}
	if !strings.HasPrefix(all[1].Name, "(") {
		t.Errorf("the one nobody named is %q, want the id standing in", all[1].Name)
	}
}

// TestNearbyKeepsWhereEverybodyIsStanding, and not only how far off they
// are.
//
// A distance is what "who" prints and it is the wrong thing to draw
// with: twenty metres says nothing about which way, and the position it
// was worked out from cannot be got back out of it.  It is the region's
// own metres, so that a caller can draw the region as well as the
// ground around this avatar.
func TestNearbyKeepsWhereEverybodyIsStanding(t *testing.T) {
	w, f := newFakeSession(t)
	f.objects = []*Seen{avatarAt(somebody, 140, 100, 61)}
	f.AnswerNames(t, map[msg.UUID]string{somebody: "Ozu Brantwick"})

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	all, err := w.Nearby(ctx)
	if err != nil {
		t.Fatalf("Nearby: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("Nearby found %d, want the one avatar: %+v", len(all), all)
	}
	if want := (msg.Vector3{X: 140, Y: 100, Z: 61}); all[0].Position != want {
		t.Errorf("position = %v, want %v, the region's own metres", all[0].Position, want)
	}
}

// TestNearbySaysWhenItCannotTell: an empty region and a broken backend
// look the same to a caller that is only handed a list, and one of them
// means the answer is worth nothing.
func TestNearbySaysWhenItCannotTell(t *testing.T) {
	t.Run("nothing knows what is in the region", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.objectsErr = errors.New("the daemon is not answering")
		if _, err := w.Nearby(context.Background()); err == nil {
			t.Error("Nearby reported an empty region rather than a failure")
		}
	})

	t.Run("nothing knows where we are", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.objects = []*Seen{avatarAt(somebody, 1, 2, 3)}
		f.presenceErr = errors.New("no presence")
		if _, err := w.Nearby(context.Background()); err == nil {
			t.Error("Nearby measured distances from nowhere")
		}
	})
}

// pickerLLSD is what AvatarPickerSearch answers with: a row per person
// under one key.
func pickerLLSD(rows ...string) string {
	return `<?xml version="1.0" ?><llsd><map><key>agents</key><array>` +
		strings.Join(rows, "") + `</array></map></llsd>`
}

func pickerRow(id, first, last, username, display string) string {
	return `<map>` +
		`<key>id</key><string>` + id + `</string>` +
		`<key>legacy_first_name</key><string>` + first + `</string>` +
		`<key>legacy_last_name</key><string>` + last + `</string>` +
		`<key>username</key><string>` + username + `</string>` +
		`<key>display_name</key><string>` + display + `</string>` +
		`</map>`
}

// TestLookupSearchesThroughTheCapability: the capability is the only
// one of the two that searches on part of a name, which is what anybody
// asking means, so it is used whenever the session has it.
func TestLookupSearchesThroughTheCapability(t *testing.T) {
	w, f := newFakeSession(t)

	var query string
	f.ServeCap(t, PickerCap, func(rw http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("names")
		rw.Header().Set("Content-Type", "application/llsd+xml")
		rw.Write([]byte(pickerLLSD(
			pickerRow(somebodyElse.String(), "Someone", "Else", "someone.else", "Kit"),
			pickerRow(somebody.String(), "Quark", "Idlemind", "quark.idlemind", "Quark"),
			// A resident with no legacy name is known by the login
			// name instead, which is at least something to call them.
			pickerRow(nemo.String(), "", "", "nemo", "Nemo"),
			// Rows that say nobody: an unreadable id, a zero one, and
			// something that is not a row at all.
			pickerRow("not a uuid", "Bad", "Row", "bad", ""),
			pickerRow(msg.UUID{}.String(), "Zero", "Row", "zero", ""),
			`<string>rubbish</string>`,
		)))
	})

	found, err := w.Lookup(context.Background(), "quark.idlemind")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	// The viewer turns dots into spaces so a username typed as
	// first.last searches as a name; without it the search finds
	// nobody.
	if query != "quark idlemind" {
		t.Errorf("searched for %q", query)
	}
	if len(found) != 3 {
		t.Fatalf("found %d, want 3: %+v", len(found), found)
	}
	if found[0].Name != "nemo" || found[1].Name != "Quark Idlemind" || found[2].Name != "Someone Else" {
		t.Errorf("came back in the order %q, %q, %q; want them in name order",
			found[0].Name, found[1].Name, found[2].Name)
	}
	if found[0].Username != "nemo" || found[0].ID != nemo {
		t.Errorf("the one with no legacy name is %+v, want the login name standing in", found[0])
	}
	if found[1].Display != "Quark" || found[1].Username != "quark.idlemind" {
		t.Errorf("found %+v", found[1])
	}
	// What a search turns up is worth keeping: the name is the thing
	// everything else here needs, and it has just been paid for.
	if got := w.Name(somebody); got != "Quark Idlemind" {
		t.Errorf("the name a search found was not learned: %q", got)
	}
}

// TestLookupPutsTwoOfTheSameNameInAFixedOrder: two accounts differing
// only in case sort the same by name, and a search that came back in a
// different order every time would be a listing that never looks twice
// alike.
func TestLookupPutsTwoOfTheSameNameInAFixedOrder(t *testing.T) {
	w, f := newFakeSession(t)
	f.ServeCap(t, PickerCap, func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(pickerLLSD(
			pickerRow(somebody.String(), "quark", "idlemind", "a", ""),
			pickerRow(somebodyElse.String(), "Quark", "Idlemind", "b", ""),
		)))
	})

	for range asking {
		found, err := w.Lookup(context.Background(), "quark")
		if err != nil {
			t.Fatalf("Lookup: %v", err)
		}
		if len(found) != 2 || found[0].Name != "Quark Idlemind" {
			t.Fatalf("came back %+v", found)
		}
	}
}

// TestLookupSaysWhenTheSearchFailed: nobody found and the search not
// working are different answers, and a caller told the first when the
// second happened stops looking.
func TestLookupSaysWhenTheSearchFailed(t *testing.T) {
	t.Run("nothing to look up", func(t *testing.T) {
		w, _ := newFakeSession(t)
		if _, err := w.Lookup(context.Background(), "  "); err == nil {
			t.Error("Lookup searched for nothing at all")
		}
	})

	t.Run("the search refused", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.ServeCap(t, PickerCap, func(rw http.ResponseWriter, r *http.Request) {
			http.Error(rw, "no", http.StatusInternalServerError)
		})
		if _, err := w.Lookup(context.Background(), "quark"); err == nil {
			t.Error("a refused search reported nobody found")
		}
	})

	t.Run("the search answered with something unreadable", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.ServeCap(t, PickerCap, func(rw http.ResponseWriter, r *http.Request) {
			rw.Write([]byte("this is not llsd"))
		})
		if _, err := w.Lookup(context.Background(), "quark"); err == nil {
			t.Error("an unreadable answer reported nobody found")
		}
	})
}

// TestLookupFallsBackToTheWholeNameQuestion: without the capability the
// old message is all there is.  It only ever matches a whole name, so
// an exact match beats refusing to look, and a row with a zero uuid is
// how the simulator says nothing matched rather than naming somebody.
func TestLookupFallsBackToTheWholeNameQuestion(t *testing.T) {
	w, f := newFakeSession(t) // no picker capability

	wait := aside(t, func() ([]Found, error) {
		return w.Lookup(context.Background(), "Quark Idlemind")
	})
	q := waitSent[*msg.AvatarPickerRequest](t, f)
	if got := trimNul(q.Data.Name); got != "Quark Idlemind" {
		t.Errorf("asked about %q", got)
	}
	if q.AgentData.AgentID != testAgentID || q.AgentData.SessionID != testSessionID {
		t.Errorf("the question came from %+v", q.AgentData)
	}

	r := &msg.AvatarPickerReply{}
	r.AgentData.QueryID = q.AgentData.QueryID
	r.Data = []msg.AvatarPickerReply_Data{
		{AvatarID: somebody, FirstName: []byte("Quark\x00"), LastName: []byte("Idlemind\x00")},
		{}, // "nothing matched", which is not somebody called ""
	}
	f.Relay(t, r)

	found, err := wait()
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(found) != 1 || found[0].ID != somebody || found[0].Name != "Quark Idlemind" {
		t.Fatalf("found %+v", found)
	}
	if got := w.Name(somebody); got != "Quark Idlemind" {
		t.Errorf("the name the search found was not learned: %q", got)
	}
}

// TestTheWholeNameQuestionCanFail: it is a message and a wait, so it
// has both a message that may not go and a wait a caller may end.
func TestTheWholeNameQuestionCanFail(t *testing.T) {
	t.Run("the question never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.Lookup(context.Background(), "Quark Idlemind"); err == nil {
			t.Error("Lookup waited for an answer to a question that was not sent")
		}
	})

	t.Run("the caller gave up", func(t *testing.T) {
		w, f := newFakeSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		wait := aside(t, func() ([]Found, error) { return w.Lookup(ctx, "Quark Idlemind") })
		waitSent[*msg.AvatarPickerRequest](t, f)
		cancel()
		if _, err := wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("gave up with %v, want the context's reason", err)
		}
	})
}

// TestTheFirstSearchBuildsTheMapTheAnswerComesBackThrough: the map of
// searches outstanding is made on the way past, like the map of names
// asked for, so a session New never touched must not lose the first
// search into a nil map.
func TestTheFirstSearchBuildsTheMapTheAnswerComesBackThrough(t *testing.T) {
	w := &Session{b: newFake(t)}
	w.sendFn = func(m msg.Message) error {
		q, ok := m.(*msg.AvatarPickerRequest)
		if !ok {
			return nil
		}
		// The simulator's answer, quoting the query it was asked.
		r := &msg.AvatarPickerReply{}
		r.AgentData.QueryID = q.AgentData.QueryID
		r.Data = []msg.AvatarPickerReply_Data{
			{AvatarID: somebody, FirstName: []byte("Quark\x00"), LastName: []byte("Idlemind\x00")},
		}
		w.pickerReply(r)
		return nil
	}

	found, err := w.Lookup(context.Background(), "Quark Idlemind")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if len(found) != 1 || found[0].ID != somebody {
		t.Errorf("found %+v", found)
	}
}

// TestAnAnswerNobodyIsWaitingFor: a reply may arrive after whoever
// asked has given up, or for a query this session never made, and
// neither is a reason to block the reader goroutine or to panic.
func TestAnAnswerNobodyIsWaitingFor(t *testing.T) {
	query := msg.MustParseUUID("51547e57-7e57-c0de-e62e-4f09d89d3041")
	// A channel nobody will ever read from, which is what a caller
	// that has given up leaves behind.
	w := &Session{pickers: map[msg.UUID]chan []Found{query: make(chan []Found)}}

	r := &msg.AvatarPickerReply{}
	r.AgentData.QueryID = query
	r.Data = []msg.AvatarPickerReply_Data{
		{AvatarID: somebody, FirstName: []byte("Quark\x00"), LastName: []byte("Idlemind\x00")},
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.pickerReply(r)
		// And one for a search nobody here made at all.
		r.AgentData.QueryID = msg.UUID{9}
		w.pickerReply(r)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("an answer nobody was waiting for stopped the reader")
	}

	// It was still worth keeping the name it carried.
	if got := w.Name(somebody); got != "Quark Idlemind" {
		t.Errorf("the name in an unwanted answer was thrown away: %q", got)
	}
}

// TestAPickerSearchEscapesWhatItIsGiven exists because the query goes
// into a URL: a name with a space in it is the ordinary case, and one
// that is not escaped is a request the capability answers with nothing.
func TestAPickerSearchEscapesWhatItIsGiven(t *testing.T) {
	w, f := newFakeSession(t)
	var raw string
	f.ServeCap(t, PickerCap, func(rw http.ResponseWriter, r *http.Request) {
		raw = r.URL.RawQuery
		rw.Write([]byte(pickerLLSD()))
	})
	if _, err := w.Lookup(context.Background(), "Quark Idlemind"); err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if !strings.Contains(raw, "names="+url.QueryEscape("Quark Idlemind")) {
		t.Errorf("the search asked for %q", raw)
	}
}
