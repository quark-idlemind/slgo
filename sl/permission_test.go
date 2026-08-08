package sl

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

func testScriptQuestion(wants Perms) *msg.ScriptQuestion {
	m := &msg.ScriptQuestion{}
	m.Data.TaskID = msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	m.Data.ItemID = msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	m.Data.ObjectName = []byte("Grabby Box\x00")
	m.Data.ObjectOwner = []byte("Quark Idlemind\x00")
	m.Data.Questions = int32(wants)
	return m
}

func TestPermsNames(t *testing.T) {
	got := (PermissionDebit | PermissionTriggerAnimation).String()
	if got != "debit, trigger animation" {
		t.Errorf("String = %q", got)
	}
	if Perms(0).String() != "nothing" {
		t.Errorf("zero = %q", Perms(0).String())
	}
	// A bit nobody here has heard of still prints, rather than
	// vanishing from the line that was supposed to explain the ask.
	if got := (PermissionDebit | 0x40000000).String(); !strings.Contains(got, "unknown 0x40000000") {
		t.Errorf("unknown bit = %q", got)
	}

	p := PermissionDebit | PermissionTeleport
	if !p.Has(PermissionDebit) || !p.Has(PermissionDebit|PermissionTeleport) {
		t.Error("Has should hold for bits that are set")
	}
	if p.Has(PermissionDebit | PermissionAttach) {
		t.Error("Has wants every bit, not any")
	}
	if !p.Any(PermissionAttach|PermissionTeleport) || p.Any(PermissionAttach) {
		t.Error("Any wants at least one")
	}
}

// TestPermissionsSubscription: a request reaches every listener, and
// the channel closes when the subscription stops.
func TestPermissionsSubscription(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()

	a := w.Permissions(4)
	b := w.Permissions(4)

	w.permission(testScriptQuestion(PermissionDebit | PermissionTriggerAnimation))

	for i, ch := range []<-chan *Permission{a, b} {
		select {
		case q := <-ch:
			if q.ObjectName != "Grabby Box" || q.OwnerName != "Quark Idlemind" {
				t.Errorf("listener %d got %+v", i, q)
			}
			if !q.Wants.Has(PermissionDebit | PermissionTriggerAnimation) {
				t.Errorf("listener %d wants = %s", i, q.Wants)
			}
			if q.Object.IsZero() || q.Item.IsZero() {
				t.Errorf("listener %d lost the ids: %+v", i, q)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("listener %d never heard the request", i)
		}
	}

	w.StopPermissions(a)
	if _, open := <-a; open {
		t.Error("the channel should be closed after StopPermissions")
	}
	// The other one is untouched.
	w.permission(testScriptQuestion(PermissionAttach))
	select {
	case q := <-b:
		if !q.Wants.Has(PermissionAttach) {
			t.Errorf("second request = %s", q.Wants)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stopping one subscription stopped the other")
	}
}

// TestPermissionsDropRatherThanBlock: the relay must not be held up by
// somebody who has stopped reading.
func TestPermissionsDropRatherThanBlock(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()

	ch := w.Permissions(2)
	for i := 0; i < 5; i++ {
		w.permission(testScriptQuestion(PermissionAttach))
	}
	if got := w.PermissionsDropped(ch); got != 3 {
		t.Errorf("dropped %d, want 3", got)
	}
	if len(w.Asked()) != 5 {
		t.Errorf("Asked kept %d, want all 5", len(w.Asked()))
	}
}

// TestGrantKeepsToWhatWasAsked: granting a bit the script never
// requested must not be reported as granted, since the simulator has
// nothing pending to match it against.
func TestGrantKeepsToWhatWasAsked(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()
	sent := make(chan *msg.ScriptAnswerYes, 4)
	w.sendFn = func(m msg.Message) error {
		sent <- m.(*msg.ScriptAnswerYes)
		return nil
	}

	w.permission(testScriptQuestion(PermissionDebit | PermissionTriggerAnimation))
	q := w.Asked()[0]

	// Granting one of the two, and something never asked for.
	if err := q.Grant(nil, PermissionTriggerAnimation|PermissionTeleport); err != nil {
		t.Fatal(err)
	}
	got := Perms((<-sent).Data.Questions)
	if got != PermissionTriggerAnimation {
		t.Errorf("granted %s, want only trigger animation", got)
	}

	// Deny is the same message with nothing set: there is no
	// ScriptAnswerNo.
	if err := q.Deny(nil); err != nil {
		t.Fatal(err)
	}
	if got := Perms((<-sent).Data.Questions); got != 0 {
		t.Errorf("Deny sent %s, want nothing", got)
	}

	if err := q.GrantAll(nil); err != nil {
		t.Fatal(err)
	}
	if got := Perms((<-sent).Data.Questions); got != PermissionDebit|PermissionTriggerAnimation {
		t.Errorf("GrantAll sent %s", got)
	}
}

// TestAnswerIdentifiesTheScript: the object and the script inside it
// both go back, because that pair is what the request is filed under.
func TestAnswerIdentifiesTheScript(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()
	sent := make(chan *msg.ScriptAnswerYes, 1)
	w.sendFn = func(m msg.Message) error {
		sent <- m.(*msg.ScriptAnswerYes)
		return nil
	}

	in := testScriptQuestion(PermissionAttach)
	w.permission(in)
	if err := w.Asked()[0].GrantAll(nil); err != nil {
		t.Fatal(err)
	}
	out := <-sent
	if out.Data.TaskID != in.Data.TaskID || out.Data.ItemID != in.Data.ItemID {
		t.Errorf("answer identifies %s/%s, asked by %s/%s",
			out.Data.TaskID, out.Data.ItemID, in.Data.TaskID, in.Data.ItemID)
	}
}

// TestAPermissionRequestPrintsAsSomethingWorthDeciding: some of these
// bits hand over real authority -- money, movement, the keyboard -- so
// whoever is being asked has to see what is being asked for rather than
// a number.
func TestAPermissionRequestPrintsAsSomethingWorthDeciding(t *testing.T) {
	w := &Session{}
	w.permission(testScriptQuestion(PermissionDebit | PermissionTakeControls))
	q := w.Asked()[0]

	got := q.String()
	for _, want := range []string{"Grabby Box", "Quark Idlemind", "debit", "take controls"} {
		if !strings.Contains(got, want) {
			t.Errorf("String = %q, want it to carry %q", got, want)
		}
	}
}

// TestAPermissionWithNoSessionCannotBeAnswered: a Permission built by
// hand, or one kept past the end of the session it came from, has
// nowhere to send the answer -- and a nil dereference here would be a
// panic in whatever was holding it.
func TestAPermissionWithNoSessionCannotBeAnswered(t *testing.T) {
	q := &Permission{Wants: PermissionDebit}
	if err := q.GrantAll(context.Background()); err == nil {
		t.Error("a request with no session reported that it had answered")
	}
	if err := q.Deny(context.Background()); err == nil {
		t.Error("a refusal with no session reported that it had gone out")
	}
}

// TestASubscriptionWithNoDepthGetsTheDefault: a caller that does not
// care how deep the buffer is still needs one, and a channel of zero
// would drop every request the moment the reader looked away.
func TestASubscriptionWithNoDepthGetsTheDefault(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()

	ch := w.Permissions(0)
	defer w.StopPermissions(ch)
	if got := cap(ch); got != DefaultPermissionDepth {
		t.Errorf("the buffer is %d deep, want the default of %d", got, DefaultPermissionDepth)
	}

	chat := w.Chat(ChatFilter{}, -1)
	defer w.StopChat(chat)
	if got := cap(chat); got != DefaultChatDepth {
		t.Errorf("the chat buffer is %d deep, want the default of %d", got, DefaultChatDepth)
	}
}

// TestDroppedIsZeroForSomethingNeverSubscribed: the answer is a count,
// so a channel nobody knows about has to read as none missed rather than
// as something the caller must handle.
func TestDroppedIsZeroForSomethingNeverSubscribed(t *testing.T) {
	w, stop := newTestSession(t)
	defer stop()
	ch := make(chan *Permission)
	if n := w.PermissionsDropped((<-chan *Permission)(ch)); n != 0 {
		t.Errorf("PermissionsDropped = %d for a channel never subscribed", n)
	}
}

// TestAScriptQuestionOffTheWireReachesWhoeverIsListening: the request
// arrives as an ordinary message on the relay, so the reader has to
// recognise it -- a subscription that only ever saw hand-built requests
// would pass while nothing real reached it.
func TestAScriptQuestionOffTheWireReachesWhoeverIsListening(t *testing.T) {
	w, f := newFakeSession(t)
	ch := w.Permissions(4)
	defer w.StopPermissions(ch)

	f.Relay(t, testScriptQuestion(PermissionTakeControls))
	select {
	case q := <-ch:
		if q.Wants != PermissionTakeControls || q.ObjectName != "Grabby Box" {
			t.Errorf("the request came out as %+v", q)
		}
	default:
		t.Fatal("a ScriptQuestion off the relay reached nobody")
	}
	if len(w.Asked()) != 1 {
		t.Errorf("Asked = %d", len(w.Asked()))
	}
}
