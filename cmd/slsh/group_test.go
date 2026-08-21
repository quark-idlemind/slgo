package main

// What the group command prints, and what it refuses to guess at.
//
// Almost all of this is formatting and matching, which is the kind of
// code that is never wrong until it is -- a marker that stopped
// appearing, an empty list that says nothing, a name matched by
// accident.  The one thing that is not formatting is the empty case:
// there is no way to tell "belongs to none" from "not told yet", and a
// test is what stops a later tidy-up from picking one and printing it
// as fact.

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

var (
	testBuilders  = msg.MustParseUUID("431c7e57-7e57-c0de-3436-9a8a7819a5e2")
	testExplorers = msg.MustParseUUID("5adb7e57-7e57-c0de-d129-851548d0e1c3")
)

// joined puts a membership list behind the fake, which is what the
// simulator sends unasked and nothing ever requests.
func joined(x *testShell, gs ...sl.Group) {
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	x.grid.presence.Groups = gs
}

// acting sets the group the fake says the avatar is already acting as.
func acting(x *testShell, id msg.UUID) {
	x.grid.mu.Lock()
	defer x.grid.mu.Unlock()
	x.grid.presence.ActiveGroup = id
}

// activations is how many groups were asked for, out of everything on
// the wire: reading the presence back is a call of its own, and only
// the request is worth counting.
func activations(x *testShell) []msg.UUID {
	var out []msg.UUID
	for _, m := range x.grid.Sent() {
		if a, ok := m.(*msg.ActivateGroup); ok {
			out = append(out, a.AgentData.GroupID)
		}
	}
	return out
}

// TestGroupMarksTheOneItIsActingAs: the whole point of the listing is
// which of them is in force, so a listing that named them all alike
// would answer the easy half of the question and not the half that
// decides whether this parcel will let the avatar build.
func TestGroupMarksTheOneItIsActingAs(t *testing.T) {
	x := newTestShell(t)
	joined(x,
		sl.Group{ID: testBuilders, Name: "Pelmar Reach Builders"},
		sl.Group{ID: testExplorers, Name: "Explorers"},
	)
	acting(x, testExplorers)

	got := x.do(t, "group")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("group printed %d lines, want one for each group: %q", len(lines), got)
	}
	if strings.HasPrefix(lines[0], "active") {
		t.Errorf("the group that is not active was marked active: %q", lines[0])
	}
	if !strings.HasPrefix(lines[1], "active") {
		t.Errorf("the active group was not marked: %q", lines[1])
	}
	// Both halves of every line: the name is what a person recognises
	// and the key is what everything else takes.
	for _, want := range []string{
		testBuilders.String(), "Pelmar Reach Builders",
		testExplorers.String(), "Explorers",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("group did not print %q: %q", want, got)
		}
	}
}

// TestGroupSaysWhenItIsActingAsNone, because that is the state a
// headless login starts in and the one that makes a parcel refuse to
// let it build -- and nothing is marked in the listing to say so.
func TestGroupSaysWhenItIsActingAsNone(t *testing.T) {
	x := newTestShell(t)
	joined(x, sl.Group{ID: testBuilders, Name: "Pelmar Reach Builders"})

	got := x.do(t, "group")
	if !strings.Contains(got, "acting as no group") {
		t.Errorf("group did not say it is acting as none: %q", got)
	}
	if !strings.Contains(got, "building") {
		t.Errorf("group did not say what acting as none costs: %q", got)
	}
}

// TestGroupWillNotCallAnEmptyListNoGroups: the membership list is not
// asked for -- it arrives on its own shortly after the handshake -- so
// nothing here can tell an avatar that has joined none from one that
// has only just logged in.  Saying "no groups" would be a claim this
// command cannot support, and saying nothing at all would read as a
// command that worked and found nothing.
func TestGroupWillNotCallAnEmptyListNoGroups(t *testing.T) {
	x := newTestShell(t)

	got := x.do(t, "group")
	if got == "" {
		t.Fatal("group with nothing to list printed nothing at all")
	}
	if !strings.Contains(got, "joined none") || !strings.Contains(got, "not been told") {
		t.Errorf("group should say an empty list means either, got %q", got)
	}

	// A group can be active while the list is still missing: the two
	// arrive in different messages, and the key is then all there is.
	acting(x, testBuilders)
	got = x.do(t, "group")
	if !strings.Contains(got, testBuilders.String()) {
		t.Errorf("group did not report the active group it cannot name: %q", got)
	}
	if !strings.Contains(got, "nothing here can name") {
		t.Errorf("group should say why it printed a key and no name: %q", got)
	}
}

// TestGroupActivatesByNameWhateverTheCase: a name is the only handle
// anybody has -- the key is not printable from anywhere until this
// command prints it -- and nobody types a group name with its capitals
// in the right places.
func TestGroupActivatesByNameWhateverTheCase(t *testing.T) {
	x := newTestShell(t)
	joined(x,
		sl.Group{ID: testBuilders, Name: "Pelmar Reach Builders"},
		sl.Group{ID: testExplorers, Name: "Explorers"},
	)
	x.grid.AnswerActivateGroup()

	got := x.do(t, "group pelmar reach BUILDERS")
	if !strings.Contains(got, "acting as Pelmar Reach Builders") {
		t.Errorf("group did not report what it activated: %q", got)
	}
	if !strings.Contains(got, testBuilders.String()) {
		t.Errorf("the report should keep the key: %q", got)
	}
	if want := []msg.UUID{testBuilders}; len(activations(x)) != 1 || activations(x)[0] != want[0] {
		t.Fatalf("activated %v, want %v", activations(x), want)
	}

	// Asking for the one already in force says so rather than sending
	// a request whose answer is already on the screen.
	got = x.do(t, "group Explorers")
	if !strings.Contains(got, "acting as Explorers") {
		t.Errorf("group did not activate the second one: %q", got)
	}
	got = x.do(t, "group Explorers")
	if !strings.Contains(got, "already acting as Explorers") {
		t.Errorf("group repeated should say it is already in force: %q", got)
	}
	if n := len(activations(x)); n != 2 {
		t.Errorf("%d groups were activated, want the two that were not already active", n)
	}
}

// TestGroupSendsAKeyItCannotName: the list may not have arrived, and
// refusing a key because a list that nobody asked for does not mention
// it would make the command useless in the state it exists for.  The
// grid answers a key that is not this avatar's by never activating it,
// which sl.ActivateGroup explains.
func TestGroupSendsAKeyItCannotName(t *testing.T) {
	x := newTestShell(t)
	x.grid.AnswerActivateGroup()

	got := x.do(t, "group "+testBuilders.String())
	if !strings.Contains(got, "acting as "+testBuilders.String()) {
		t.Errorf("group by key printed %q", got)
	}
	if a := activations(x); len(a) != 1 || a[0] != testBuilders {
		t.Errorf("activated %v, want the key as typed", a)
	}
}

// TestGroupClearsTheGroupInWords covers the way back out, which the
// null key is the only spelling of on the wire and the worst possible
// spelling of on the screen.
//
// Both halves have to hold.  "none" is the word a person has to be able
// to reach it by -- nothing in the shell asks anybody to type thirty-two
// zeros, and a listing that says "acting as no group" gives no hint
// that this is how to get back to it -- and what comes out has to be
// the sentence rather than the key, since a null key printed as a group
// is the mistake where used to make with a key nobody could read.
func TestGroupClearsTheGroupInWords(t *testing.T) {
	x := newTestShell(t)
	joined(x, sl.Group{ID: testBuilders, Name: "Pelmar Reach Builders"})
	acting(x, testBuilders)
	x.grid.AnswerActivateGroup()

	zero := msg.UUID{}
	got := x.do(t, "group none")
	if !strings.Contains(got, "acting as no group") {
		t.Errorf("clearing the group printed %q", got)
	}
	if strings.Contains(got, zero.String()) {
		t.Errorf("clearing the group printed the null key: %q", got)
	}
	// The same wording the listing uses for the state, so that the two
	// do not read as two different states.
	if !strings.Contains(got, "refuses") {
		t.Errorf("clearing the group did not say what it costs: %q", got)
	}
	if a := activations(x); len(a) != 1 || !a[0].IsZero() {
		t.Fatalf("activated %v, want the null key", a)
	}

	// Already clear is a sentence too, and sends nothing.
	got = x.do(t, "group none")
	if !strings.HasPrefix(got, "already acting as no group") {
		t.Errorf("clearing a group that is already clear printed %q", got)
	}
	if n := len(activations(x)); n != 1 {
		t.Errorf("%d activations, want the one that changed something", n)
	}

	// The null key typed out reaches the same place as the word, since
	// it is what a person who read the viewer's protocol would try.
	acting(x, testBuilders)
	got = x.do(t, "group "+zero.String())
	if !strings.Contains(got, "acting as no group") || strings.Contains(got, zero.String()) {
		t.Errorf("the null key printed %q", got)
	}
	if a := activations(x); len(a) != 2 || !a[1].IsZero() {
		t.Errorf("activated %v, want the null key again", a)
	}
}

// TestGroupRefusesANameItCannotSettle rather than picking one.  A group
// that activates and is the wrong one is a silent wrong answer: it says
// it worked, and the parcel goes on refusing to let you build for a
// reason that now looks impossible.
func TestGroupRefusesANameItCannotSettle(t *testing.T) {
	x := newTestShell(t)
	joined(x,
		sl.Group{ID: testBuilders, Name: "Builders"},
		sl.Group{ID: testExplorers, Name: "Builders"},
	)
	x.grid.AnswerActivateGroup()

	got := x.do(t, "group builders")
	if !strings.Contains(got, testBuilders.String()) || !strings.Contains(got, testExplorers.String()) {
		t.Errorf("an ambiguous name should print both keys, got %q", got)
	}
	if a := activations(x); len(a) != 0 {
		t.Errorf("an ambiguous name still activated %v", a)
	}

	// A name that matches nothing, with a list to match against.
	got = x.do(t, "group Explorers")
	if !strings.Contains(got, "no group called") {
		t.Errorf("an unknown name printed %q", got)
	}

	// And the same with no list, which is a different answer: the name
	// may be perfectly good and there is nothing here to check it
	// against yet.
	joined(x)
	got = x.do(t, "group Explorers")
	if !strings.Contains(got, "try again in a moment") {
		t.Errorf("an unknown name with no list should say the list may be missing, got %q", got)
	}
	if a := activations(x); len(a) != 0 {
		t.Errorf("a name that matched nothing activated %v", a)
	}
}

// TestTheGroupListReachesAShellThroughTheDaemon is the plumbing this
// command needed built: the list lives on slgod's side of the wire, in
// agent.Agent, and a hosted session can only be handed it.  Everything
// above is tested against a backend in this process, so the translation
// out of protobuf is only exercised here.
func TestTheGroupListReachesAShellThroughTheDaemon(t *testing.T) {
	x, d := newDaemonShell(t)
	d.presence = &pb.PresenceResponse{
		Region:      "Test Region",
		ActiveGroup: testBuilders.String(),
		Groups: []*pb.GroupMembership{
			{Id: testBuilders.String(), Name: "Pelmar Reach Builders", Powers: 0x101},
			{Id: testExplorers.String(), Name: "Explorers"},
		},
	}

	got := x.do(t, "group")
	if !strings.Contains(got, "Pelmar Reach Builders") || !strings.Contains(got, "Explorers") {
		t.Errorf("the daemon's list did not reach the shell: %q", got)
	}
	if !strings.HasPrefix(got, "active") {
		t.Errorf("the active group did not survive the crossing: %q", got)
	}

	// And where names it rather than printing a key nobody can look
	// up, which is the whole reason the list crossed.
	if got := x.do(t, "where"); !strings.Contains(got, "acting as group Pelmar Reach Builders") {
		t.Errorf("where printed %q", got)
	}
}
