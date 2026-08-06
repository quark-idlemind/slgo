package sl

import (
	"strings"
	"testing"

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
