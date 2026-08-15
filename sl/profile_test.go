package sl

// Reading somebody's profile, which is one question with three answers.
//
// The ordering is the whole of what can go wrong here and none of it
// shows up as a wrong message on the wire.  The groups arrive BEFORE the
// properties everything else hangs off, so a call that waited for the
// properties and then listened for groups would lose them every time; an
// avatar with no groups sends one row of zeros rather than no rows, so a
// caller that trusted the count would print a group that is not one; and
// a key the grid has never heard of is answered by that empty row and
// nothing else, so the deadline is an answer rather than a failure.  All
// three were measured on Agni; see profile.go.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var (
	theStranger  = msg.MustParseUUID("1b627e57-7e57-c0de-3614-68104e5fa9d8")
	theirPartner = msg.MustParseUUID("2bc17e57-7e57-c0de-6fcd-0fd10f67b8a1")
	theirGroup   = msg.MustParseUUID("3b357e57-7e57-c0de-bd0f-62d50ece68da")
)

// onWire is a string field as the wire carries one: NUL terminated.
func onWire(s string) []byte { return append([]byte(s), 0) }

// avatarProperties is the reply that carries the profile itself.
func avatarProperties(who msg.UUID, born, about string, partner msg.UUID, flags uint32) *msg.AvatarPropertiesReply {
	m := &msg.AvatarPropertiesReply{}
	m.AgentData.AgentID = testAgentID
	m.AgentData.AvatarID = who
	m.PropertiesData.BornOn = onWire(born)
	m.PropertiesData.AboutText = onWire(about)
	m.PropertiesData.PartnerID = partner
	m.PropertiesData.ProfileURL = onWire("")
	m.PropertiesData.FLAboutText = onWire("")
	// One byte, which is the caption index and is what an ordinary
	// avatar's reply carries.
	m.PropertiesData.CharterMember = []byte{0}
	m.PropertiesData.Flags = flags
	return m
}

// avatarGroups is the reply that carries the groups, and with no rows at
// all is the empty row the grid really sends: see emptyGroups.
func avatarGroups(who msg.UUID, rows ...msg.AvatarGroupsReply_GroupData) *msg.AvatarGroupsReply {
	m := &msg.AvatarGroupsReply{}
	m.AgentData.AgentID = testAgentID
	m.AgentData.AvatarID = who
	m.GroupData = rows
	return m
}

// emptyGroups is what an avatar with no groups to show sends, measured
// on Agni: one row of all zeros rather than a reply with no rows.
func emptyGroups(who msg.UUID) *msg.AvatarGroupsReply {
	return avatarGroups(who, msg.AvatarGroupsReply_GroupData{
		GroupTitle: onWire(""), GroupName: onWire(""),
	})
}

func aGroup(id msg.UUID, name, title string, powers uint64) msg.AvatarGroupsReply_GroupData {
	return msg.AvatarGroupsReply_GroupData{
		GroupID: id, GroupName: onWire(name), GroupTitle: onWire(title),
		GroupPowers: powers, AcceptNotices: true,
	}
}

func avatarInterests(who msg.UUID, wantTo, skills, languages string) *msg.AvatarInterestsReply {
	m := &msg.AvatarInterestsReply{}
	m.AgentData.AgentID = testAgentID
	m.AgentData.AvatarID = who
	m.PropertiesData.WantToText = onWire(wantTo)
	m.PropertiesData.SkillsText = onWire(skills)
	m.PropertiesData.LanguagesText = onWire(languages)
	return m
}

// answerProfile makes the fake answer an AvatarPropertiesRequest with
// the three replies, in the order Agni sent them: groups first.
func answerProfile(t *testing.T, f *fakeBackend, replies ...msg.Message) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.AvatarPropertiesRequest); !ok {
			return
		}
		for _, r := range replies {
			f.Relay(t, r)
		}
	}
}

// TestAProfileIsListeningBeforeItAsks.
//
// The three replies are messages like any other and can be handled
// before Send has returned, and the groups are the FIRST of them --
// measured on Agni twenty milliseconds ahead of the properties.  A call
// that subscribed after asking would miss the lot, and one that
// subscribed for groups only after the properties arrived would miss
// exactly the groups, every time, and report an avatar in no groups.
//
// The fake answers from inside Send, which is the sharpest form of the
// same race: nothing here has returned from asking yet.
func TestAProfileIsListeningBeforeItAsks(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	answerProfile(t, f,
		avatarGroups(theStranger, aGroup(theirGroup, "Lorn Rangers", "Officer", 0x800)),
		avatarProperties(theStranger, "5/21/2010", "I build things.", theirPartner, 0x1d),
		avatarInterests(theStranger, "", "", "English"))

	p, err := w.Profile(context.Background(), theStranger, 5*time.Second)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if !p.Known {
		t.Fatal("the properties arrived and the profile said the grid had not described this avatar")
	}
	if p.BornOn != "5/21/2010" || p.About != "I build things." || p.Partner != theirPartner {
		t.Errorf("Profile = %+v", p)
	}
	if p.Languages != "English" {
		t.Errorf("the interests arrived as %+v", p)
	}
	if len(p.Groups) != 1 {
		t.Fatalf("the groups arrived as %v, and they are the reply that comes first", p.Groups)
	}
	if g := p.Groups[0]; g.ID != theirGroup || g.Name != "Lorn Rangers" ||
		g.Title != "Officer" || g.Powers != 0x800 || !g.Notices {
		t.Errorf("the group is %+v", g)
	}

	// The request names the avatar asked about as well as this session,
	// since the reply is matched by the avatar and nothing else.
	q := onlySent[*msg.AvatarPropertiesRequest](t, f)
	if q.AgentData.AgentID != testAgentID || q.AgentData.SessionID != testSessionID ||
		q.AgentData.AvatarID != theStranger {
		t.Errorf("asked %+v", q.AgentData)
	}
}

// TestTheProfileRepliesAreAskedForAtAll.
//
// Nothing else here would notice if they were not.  The fake hands a
// message straight to the reader, where a hosted session is given only
// what it asked slgod to relay -- so a profile that assembles perfectly
// in every test above would come back empty against the daemon, and
// look exactly like a grid that had stopped answering.  Subscriptions is
// where the asking happens.
func TestTheProfileRepliesAreAskedForAtAll(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"AvatarPropertiesReply", "AvatarInterestsReply", "AvatarGroupsReply",
	} {
		if !slices.Contains(Subscriptions, name) {
			t.Errorf("%s is not relayed to this package, so no profile can arrive", name)
		}
	}
}

// TestAnEmptyRowIsNotAGroup.
//
// Measured on Agni against an avatar who lists none: the reply carries
// one row of all zeros rather than no rows at all.  A caller handed that
// row would print a group with no name and no key, and a caller counting
// rows would say one where the answer is none.
func TestAnEmptyRowIsNotAGroup(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	answerProfile(t, f,
		emptyGroups(theStranger),
		avatarProperties(theStranger, "5/21/2010", "", msg.UUID{}, 0x11),
		avatarInterests(theStranger, "", "", ""))

	p, err := w.Profile(context.Background(), theStranger, 5*time.Second)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if !p.Known {
		t.Fatal("the properties arrived and the profile says otherwise")
	}
	if len(p.Groups) != 0 {
		t.Errorf("an avatar who lists no groups came back with %+v", p.Groups)
	}
}

// TestAKeyTheGridHasNeverHeardOfIsAnsweredByTheGroupsAlone.
//
// Measured on Agni against a key made up on the spot: the empty group
// row arrives and the properties reply never does.  That is the only way
// the grid says it does not know a key, so the deadline is the answer
// and must not be reported as a failure -- and a caller must be able to
// tell it from a profile that is simply empty, which is what Known is
// for.
func TestAKeyTheGridHasNeverHeardOfIsAnsweredByTheGroupsAlone(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	answerProfile(t, f, emptyGroups(theStranger))

	p, err := w.Profile(context.Background(), theStranger, 300*time.Millisecond)
	if err != nil {
		t.Fatalf("a key the grid does not know was reported as an error: %v", err)
	}
	if p.Known {
		t.Error("a key with no properties reply was reported as an avatar the grid described")
	}
	if len(p.Groups) != 0 || p.BornOn != "" {
		t.Errorf("Profile = %+v", p)
	}
}

// TestAProfileNobodyAnsweredAtAllIsATimeout.
//
// The other silence.  A grid that has never heard of a key still answers
// with the group reply, so hearing NOTHING is a question that went
// unanswered -- and reporting that as "the grid does not know them"
// would turn a circuit that had stopped carrying replies into a claim
// about somebody's existence.
func TestAProfileNobodyAnsweredAtAllIsATimeout(t *testing.T) {
	t.Parallel()
	w, _ := newFakeSession(t)

	p, err := w.Profile(context.Background(), theStranger, 200*time.Millisecond)
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Profile = %+v, %v; want a timeout", p, err)
	}
	if !strings.Contains(err.Error(), theStranger.String()) {
		t.Errorf("the timeout does not say who it was about: %v", err)
	}
}

// TestAProfileIgnoresRepliesAboutSomebodyElse.
//
// Which avatar a reply is about is in its own block and not in anything
// the request left behind, so the sifting is all there is: a second
// client on this avatar's circuit asking about somebody else has its
// answers relayed here too, and a profile that took them would be
// somebody else's.
func TestAProfileIgnoresRepliesAboutSomebodyElse(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	wait := aside(t, func() (*Profile, error) {
		return w.Profile(context.Background(), theStranger, 30*time.Second)
	})
	waitSent[*msg.AvatarPropertiesRequest](t, f)

	f.Relay(t, avatarGroups(theOther, aGroup(theirGroup, "Somebody Else's Group", "", 0)))
	f.Relay(t, avatarProperties(theOther, "1/1/2003", "not this profile", theirPartner, 0x1d))
	f.Relay(t, avatarInterests(theOther, "", "", "Klingon"))

	// Now the real ones, which is what says the sifting drops rather
	// than derails.
	f.Relay(t, emptyGroups(theStranger))
	f.Relay(t, avatarProperties(theStranger, "5/21/2010", "mine", msg.UUID{}, 0x11))
	f.Relay(t, avatarInterests(theStranger, "", "", ""))

	p, err := wait()
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if p.About != "mine" || p.BornOn != "5/21/2010" || len(p.Groups) != 0 ||
		p.Languages != "" || !p.Partner.IsZero() {
		t.Errorf("a profile about somebody else got in: %+v", p)
	}
}

// TestAProfileAnswersWithWhatArrivedWhenTheRestDoesNot.
//
// The three replies came 22 milliseconds apart, so once the properties
// are in hand whatever is still out is a millisecond behind them or is
// not coming.  Waiting out the whole timeout for it would turn a profile
// that is all there into a fifteen second pause, and giving up on the
// profile because one of the three went missing would throw away the
// part that arrived.
func TestAProfileAnswersWithWhatArrivedWhenTheRestDoesNot(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	answerProfile(t, f,
		avatarProperties(theStranger, "5/21/2010", "", msg.UUID{}, 0x11))

	start := time.Now()
	p, err := w.Profile(context.Background(), theStranger, time.Minute)
	if err != nil {
		t.Fatalf("Profile: %v", err)
	}
	if !p.Known || p.BornOn != "5/21/2010" {
		t.Errorf("Profile = %+v", p)
	}
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("a profile whose groups never came waited %s, which is the whole timeout", took)
	}
}

// TestPaymentIsFourAnswersAndNotTwo.
//
// Two flags make three answers between them, and an account whose
// profile carries a caption has its payment information withheld rather
// than absent -- so reading the flags alone would print "none on file"
// for a Linden, which is a claim about somebody's money that the message
// never made.  The two ordinary values are the ones measured on Agni.
func TestPaymentIsFourAnswersAndNotTwo(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		what    string
		profile Profile
		want    Payment
	}{
		{"measured, 0x11", Profile{Flags: 0x11}, PaymentNone},
		{"measured, 0x1d", Profile{Flags: 0x1d}, PaymentUsed},
		{"on file and unused", Profile{Flags: ProfileIdentified}, PaymentOnFile},
		{"nothing set at all", Profile{}, PaymentNone},
		{"a captioned account", Profile{Caption: "Linden Lab Employee"}, PaymentNotRevealed},
		{"an employee by index", Profile{CaptionIndex: captionLinden, Flags: 0x11}, PaymentNotRevealed},
	} {
		if got := c.profile.Payment(); got != c.want {
			t.Errorf("%s: payment is %q, want %q", c.what, got, c.want)
		}
	}
	if fmt.Sprint(PaymentUsed) == fmt.Sprint(PaymentOnFile) {
		t.Error("payment used and payment on file print the same words")
	}
}

// TestAProfileReadsTheCaptionByItsLength.
//
// The field is one byte for an index into the viewer's list of captions
// and a string for the caption itself, which is what the template means
// by "special - usually U8" -- so a reader that took it for one of the
// two would print an index as text or lose the words entirely.
func TestAProfileReadsTheCaptionByItsLength(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		what   string
		member []byte
		index  uint8
		text   string
	}{
		{"an ordinary avatar", []byte{0}, 0, ""},
		{"an index of its own", []byte{captionLinden}, captionLinden, ""},
		{"the words themselves", onWire("Linden Lab Employee"), 0, "Linden Lab Employee"},
		{"nothing at all", nil, 0, ""},
	} {
		var p Profile
		d := &msg.AvatarPropertiesReply_PropertiesData{CharterMember: c.member}
		readProperties(&p, d)
		if p.CaptionIndex != c.index || p.Caption != c.text {
			t.Errorf("%s: caption %q, index %d", c.what, p.Caption, p.CaptionIndex)
		}
	}
}

// TestProfileRefusesWhatItCannotAsk.
//
// The null key is the one argument that would send a question about
// nobody and then wait out the whole timeout for the answer nobody
// sends, and a request that never went out is not one to wait on.
func TestProfileRefusesWhatItCannotAsk(t *testing.T) {
	t.Parallel()

	t.Run("nobody to ask about", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		if _, err := w.Profile(context.Background(), msg.UUID{}, time.Second); err == nil {
			t.Error("a profile was asked for about the null key")
		}
		if got := f.Sent(); len(got) != 0 {
			t.Errorf("a question about nobody still sent %s", f.describe())
		}
	})

	t.Run("the question never went out", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is down"))
		if _, err := w.Profile(context.Background(), theStranger, time.Second); err == nil {
			t.Error("a question that failed to send was waited on anyway")
		}
	})

	t.Run("the caller gave up waiting", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		wait := aside(t, func() (*Profile, error) {
			return w.Profile(ctx, theStranger, time.Minute)
		})
		waitSent[*msg.AvatarPropertiesRequest](t, f)
		cancel()
		if _, err := wait(); !errors.Is(err, context.Canceled) {
			t.Errorf("Profile = %v; want the context's reason", err)
		}
	})
}
