package sl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

func testScriptDialog() *msg.ScriptDialog {
	m := &msg.ScriptDialog{}
	m.Data.ObjectID = msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
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
		{OwnerID: msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")},
	}
	return m
}

func TestDialogDecoding(t *testing.T) {
	w := &Session{}
	w.dialog(testScriptDialog())

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
	w.dialog(testScriptDialog())
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
	w.dialog(testScriptDialog())
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
	w.dialog(testScriptDialog())
	if got.Message != "pick one" {
		t.Errorf("the callback saw %+v", got)
	}
}

// TestADialogPrintsAsSomethingAPersonCanAnswer: a dialog is put in front
// of somebody deciding, and a caller that had to reach into the struct
// to show it would show a different thing each time.
func TestADialogPrintsAsSomethingAPersonCanAnswer(t *testing.T) {
	w := &Session{}
	w.dialog(testScriptDialog())
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
