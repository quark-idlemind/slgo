package sl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

func testScriptDialog() *msg.ScriptDialog {
	m := &msg.ScriptDialog{}
	m.Data.ObjectID = msg.MustParseUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")
	m.Data.ObjectName = []byte("Test Object\x00")
	m.Data.FirstName = []byte("Quark\x00")
	m.Data.LastName = []byte("Idlemind\x00")
	m.Data.Message = []byte("pick one\x00")
	m.Data.ChatChannel = -4242
	m.Buttons = []msg.ScriptDialog_Buttons{
		{ButtonLabel: []byte("Yes\x00")},
		{ButtonLabel: []byte("No\x00")},
		{ButtonLabel: []byte("Maybe\x00")},
	}
	m.OwnerData = []msg.ScriptDialog_OwnerData{
		{OwnerID: msg.MustParseUUID("72427e57-7e57-c0de-39d2-e78c47465eb6")},
	}
	return m
}

func TestDialogDecoding(t *testing.T) {
	w := &Session{}
	w.dialog(nil, testScriptDialog())

	ds := w.Dialogs()
	if len(ds) != 1 {
		t.Fatalf("kept %d dialogs", len(ds))
	}
	d := ds[0]
	if d.ObjectName != "Test Object" || d.Message != "pick one" {
		t.Errorf("dialog = %+v", d)
	}
	if d.OwnerName != "Quark Idlemind" {
		t.Errorf("owner name = %q", d.OwnerName)
	}
	if d.Owner.IsZero() {
		t.Error("the owner id was dropped")
	}
	// The channel is the script's choice and is usually negative.
	if d.Channel != -4242 {
		t.Errorf("channel = %d", d.Channel)
	}
	if strings.Join(d.Buttons, ",") != "Yes,No,Maybe" {
		t.Errorf("buttons = %v", d.Buttons)
	}
}

func TestDialogButtonLookup(t *testing.T) {
	w := &Session{}
	w.dialog(nil, testScriptDialog())
	d := w.Dialogs()[0]

	for _, c := range []struct {
		in   string
		want int
	}{{"Yes", 0}, {"no", 1}, {"  MAYBE  ", 2}} {
		got, ok := d.Button(c.in)
		if !ok || got != c.want {
			t.Errorf("Button(%q) = %d, %v; want %d, true", c.in, got, ok, c.want)
		}
	}
	if _, ok := d.Button("Perhaps"); ok {
		t.Error("a button that was never offered was found")
	}
}

// TestAnswerRefusesUnknownButtons: the simulator passes the label to
// the script as well as the index, so answering with a label the dialog
// never showed would tell the script something impossible.
func TestAnswerRefusesUnknownButtons(t *testing.T) {
	w := &Session{}
	w.dialog(nil, testScriptDialog())
	d := w.Dialogs()[0]

	if err := w.Answer(nil, d, "Perhaps"); err == nil {
		t.Error("expected a refusal for a button that does not exist")
	} else if !strings.Contains(err.Error(), "Perhaps") {
		t.Errorf("the error should name the button: %v", err)
	}
	if err := w.AnswerIndex(nil, d, 3); err == nil {
		t.Error("expected a refusal for an index past the end")
	}
	if err := w.AnswerIndex(nil, d, -1); err == nil {
		t.Error("expected a refusal for a negative index")
	}
}

// TestOnDialogIsCalled: the callback is how a program hears about one
// without polling.
func TestOnDialogIsCalled(t *testing.T) {
	var got Dialog
	w := &Session{}
	w.OnDialog = func(d Dialog) { got = d }
	w.dialog(nil, testScriptDialog())
	if got.Message != "pick one" {
		t.Errorf("the callback saw %+v", got)
	}
}

// TestADialogPrintsAsSomethingAPersonCanAnswer: a dialog is put in front
// of somebody deciding, and a caller that had to reach into the struct
// to show it would show a different thing each time.
func TestADialogPrintsAsSomethingAPersonCanAnswer(t *testing.T) {
	w := &Session{}
	w.dialog(nil, testScriptDialog())
	d := w.Dialogs()[0]

	got := d.String()
	for _, want := range []string{"Test Object", "-4242", `"pick one"`, "Yes"} {
		if !strings.Contains(got, want) {
			t.Errorf("String = %q, want it to carry %q", got, want)
		}
	}
}

// TestWaitDialogCountsTheOnesAlreadySeen: a script that opens a dialog
// the instant it is rezzed would otherwise be a race nobody can win --
// the dialog arrives before anything has had a chance to wait for it.
func TestWaitDialogCountsTheOnesAlreadySeen(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	f.Relay(t, testScriptDialog())
	d, err := w.WaitDialog(context.Background(), time.Second, nil)
	if err != nil {
		t.Fatalf("WaitDialog: %v", err)
	}
	if d.Message != "pick one" {
		t.Errorf("WaitDialog = %+v", d)
	}

	// A match that picks one out of several, since a probe usually
	// cares about one particular script.
	other := testScriptDialog()
	other.Data.Message = []byte("something else\x00")
	f.Relay(t, other)
	d, err = w.WaitDialog(context.Background(), time.Second, func(d Dialog) bool {
		return d.Message == "something else"
	})
	if err != nil || d.Message != "something else" {
		t.Errorf("WaitDialog = %+v, %v", d, err)
	}
}

// TestWaitDialogGivesUpRatherThanHangs: a dialog cannot be declined --
// there is no message for the close box -- so one nobody answers simply
// expires, and a wait for the wrong thing has to end by itself.
func TestWaitDialogGivesUpRatherThanHangs(t *testing.T) {
	t.Parallel()
	w, _ := newFakeSession(t)

	if _, err := w.WaitDialog(context.Background(), 200*time.Millisecond, nil); !errors.Is(err, ErrTimeout) {
		t.Errorf("WaitDialog = %v, want a timeout", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := w.WaitDialog(ctx, time.Hour, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("WaitDialog = %v, want the context's reason", err)
	}
}

// TestAnsweringPressesTheButtonTheDialogOffered: the simulator passes
// both the index and the label to the script, so a script that switches
// on the label would be told something it never displayed.
func TestAnsweringPressesTheButtonTheDialogOffered(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, testScriptDialog())
	d := w.Dialogs()[0]

	if err := w.Answer(context.Background(), d, " maybe "); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	m := onlySent[*msg.ScriptDialogReply](t, f)
	if m.Data.ButtonIndex != 2 || trimNul(m.Data.ButtonLabel) != "Maybe" {
		t.Errorf("answered %+v, want the label as the dialog spelled it", m.Data)
	}
	// The answer goes on whichever channel the script chose, which is
	// usually negative precisely so an avatar cannot fake it by typing.
	if m.Data.ChatChannel != -4242 || m.Data.ObjectID != d.Object {
		t.Errorf("answered on %+v", m.Data)
	}

	f.Forget()
	if err := w.AnswerIndex(context.Background(), d, 0); err != nil {
		t.Fatalf("AnswerIndex: %v", err)
	}
	if got := onlySent[*msg.ScriptDialogReply](t, f); trimNul(got.Data.ButtonLabel) != "Yes" {
		t.Errorf("AnswerIndex pressed %q", trimNul(got.Data.ButtonLabel))
	}

	f.Forget()
	f.FailSends(errors.New("the circuit is gone"))
	if err := w.AnswerIndex(context.Background(), d, 1); err == nil {
		t.Error("AnswerIndex reported an answer that never went out")
	}
}

// TestAnsweringRefusesAButtonThatIsNotThere: a dialog has at most twelve
// buttons and the simulator matches the reply against them, so an index
// off the end would be a reply the script cannot make sense of.
func TestAnsweringRefusesAButtonThatIsNotThere(t *testing.T) {
	w, f := newFakeSession(t)
	f.Relay(t, testScriptDialog())
	d := w.Dialogs()[0]

	for _, i := range []int{-1, 3} {
		if err := w.AnswerIndex(context.Background(), d, i); err == nil {
			t.Errorf("AnswerIndex pressed button %d of three", i)
		}
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("a reply went out for a button that does not exist: %s", f.describe())
	}
}

// TestADialogWhoseAnswerNeverWentIsStillWaiting: forgotten only after
// the send, the same shape a permission request keeps.  A dialog whose
// answer never left is one the script is still waiting on.
func TestADialogWhoseAnswerNeverWentIsStillWaiting(t *testing.T) {
	w := &Session{}
	down := errors.New("the circuit is down")
	w.sendFn = func(msg.Message) error { return down }
	w.dialog(nil, testScriptDialog())
	d := w.Dialogs()[0]

	if err := w.Answer(context.Background(), d, "Yes"); !errors.Is(err, down) {
		t.Fatalf("Answer = %v, want the send's failure", err)
	}
	if n := len(w.Dialogs()); n != 1 {
		t.Fatalf("a dialog whose answer never went out was forgotten: %d left", n)
	}

	w.sendFn = func(msg.Message) error { return nil }
	if err := w.Answer(context.Background(), d, "Yes"); err != nil {
		t.Fatal(err)
	}
	if n := len(w.Dialogs()); n != 0 {
		t.Errorf("an answered dialog is still waiting: %d", n)
	}
}

// handledSink collects what a session hands OnHandled.
type handledSink struct {
	mu   sync.Mutex
	told []Handled
}

func (h *handledSink) on(w *Session) {
	w.mu.Lock()
	w.OnHandled = func(x Handled) {
		h.mu.Lock()
		h.told = append(h.told, x)
		h.mu.Unlock()
	}
	w.mu.Unlock()
}

func (h *handledSink) take() []Handled {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.told
	h.told = nil
	return out
}

// TestAnUnansweredDialogIsForgottenAfterAnHour: a dialog cannot be
// declined and is often never answered, so one is forgotten after
// UnansweredFor -- when the next arrives, or when the list is read --
// and whoever counts what is waiting is told.
func TestAnUnansweredDialogIsForgottenAfterAnHour(t *testing.T) {
	w := &Session{}
	var told handledSink
	told.on(w)
	age := func(i int) {
		w.mu.Lock()
		w.dialogs[i].At = time.Now().Add(-UnansweredFor - time.Minute)
		w.mu.Unlock()
	}

	w.dialog(nil, testScriptDialog())
	age(0)
	w.dialog(nil, testScriptDialog())
	if ds := w.Dialogs(); len(ds) != 1 || time.Since(ds[0].At) > time.Minute {
		t.Fatalf("Dialogs = %+v, want only the one that has just arrived", ds)
	}
	got := told.take()
	if len(got) != 1 {
		t.Fatalf("told %+v, want the one forgotten", got)
	}
	if want := "the dialog from [Object] Test Object was forgotten after 1h unanswered by this session"; got[0].String() != want {
		t.Errorf("told %q, want %q", got[0], want)
	}

	// And when nothing new comes, on reading.
	age(0)
	if ds := w.Dialogs(); len(ds) != 0 {
		t.Errorf("Dialogs = %+v, want the overdue one forgotten", ds)
	}
	if got := told.take(); len(got) != 1 {
		t.Errorf("told %+v, want the one forgotten on reading", got)
	}
}

// TestNoMoreThanMaxUnansweredDialogsAreKept: a script that puts up a
// dialog a second would fill the list inside the hour, so the oldest
// gives way to the newest, and is said to.
func TestNoMoreThanMaxUnansweredDialogsAreKept(t *testing.T) {
	w := &Session{}
	var told handledSink
	told.on(w)

	for i := range MaxUnanswered + 1 {
		m := testScriptDialog()
		m.Data.Message = []byte(fmt.Sprintf("dialog %d\x00", i))
		w.dialog(nil, m)
	}
	ds := w.Dialogs()
	if len(ds) != MaxUnanswered {
		t.Fatalf("kept %d dialogs, want %d", len(ds), MaxUnanswered)
	}
	if ds[0].Message != "dialog 1" || ds[len(ds)-1].Message != fmt.Sprintf("dialog %d", MaxUnanswered) {
		t.Errorf("kept %q to %q, want the oldest gone", ds[0].Message, ds[len(ds)-1].Message)
	}
	got := told.take()
	if len(got) != 1 || !strings.Contains(got[0].String(), "dropped as the oldest of more than 32 waiting") {
		t.Errorf("told %+v, want the oldest dropped", got)
	}
}
