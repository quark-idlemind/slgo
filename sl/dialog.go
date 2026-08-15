package sl

// Answering the dialogs a script puts on the screen.
//
// llDialog sends a ScriptDialog: a message, up to twelve buttons, and a
// channel to answer on.  Pressing a button sends ScriptDialogReply,
// which the script hears as a listen on that channel from this avatar.
//
// Two things are worth knowing before relying on it.  A dialog cannot
// be declined -- there is no message for the close box, and one nobody
// answers simply expires -- so a probe that waits for the wrong thing
// waits out the timeout.
//
// And the answer goes on whichever channel the script chose, which is
// usually negative precisely so that an avatar cannot fake it by
// typing.  This message reaches those channels: a script listening on
// -4242 heard "HEARD [Beta] on -4242" from a reply sent here, while
// ordinary chat from this client on a negative channel still goes
// nowhere.  Until that bug is found, this is the way to reach one.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// Dialog is a script asking somebody to press a button.
type Dialog struct {
	At time.Time

	// Object is what asked, ObjectName what it is called, and Owner
	// who owns it.  OwnerName is the owner's name as the simulator
	// sent it, which is there so a dialog can be recognised without
	// another round trip.
	Object     msg.UUID
	ObjectName string
	Owner      msg.UUID
	OwnerName  string

	Message string
	Buttons []string

	// Channel is where the answer goes.  It is the script's choice
	// and is usually negative.
	Channel int32
}

// TextBoxToken is the button label a text box arrives as.
//
// llTextBox is not a message of its own: the simulator sends an
// ordinary ScriptDialog whose single button carries this sentinel, and
// a viewer that recognises it draws a field to type in instead of a
// button to press (lllslconstants.h:213).  The answer goes back as the
// same ScriptDialogReply with the typed text where the button label
// would be (llviewermessage.cpp:8278).
const TextBoxToken = "!!llTextBox!!"

// IsTextBox says whether this is a text box rather than a set of
// buttons.
func (d Dialog) IsTextBox() bool {
	for _, b := range d.Buttons {
		if strings.TrimSpace(b) == TextBoxToken {
			return true
		}
	}
	return false
}

// Button reports whether the dialog offers a button with this label,
// ignoring the case and the spaces around it, and where it sits.
func (d Dialog) Button(label string) (int, bool) {
	want := strings.TrimSpace(strings.ToLower(label))
	for i, b := range d.Buttons {
		if strings.TrimSpace(strings.ToLower(b)) == want {
			return i, true
		}
	}
	return 0, false
}

func (d Dialog) String() string {
	return fmt.Sprintf("%s on channel %d: %q %v", d.ObjectName, d.Channel, d.Message, d.Buttons)
}

// dialog records one and tells whoever is waiting.
func (w *Session) dialog(m *msg.ScriptDialog) {
	d := Dialog{
		At:         time.Now(),
		Object:     m.Data.ObjectID,
		ObjectName: trimNul(m.Data.ObjectName),
		OwnerName:  strings.TrimSpace(trimNul(m.Data.FirstName) + " " + trimNul(m.Data.LastName)),
		Message:    trimNul(m.Data.Message),
		Channel:    m.Data.ChatChannel,
	}
	if len(m.OwnerData) > 0 {
		d.Owner = m.OwnerData[0].OwnerID
	}
	for _, b := range m.Buttons {
		d.Buttons = append(d.Buttons, trimNul(b.ButtonLabel))
	}

	w.mu.Lock()
	w.dialogs = append(w.dialogs, d)
	fn := w.OnDialog
	w.mu.Unlock()

	if fn != nil {
		fn(d)
	}
}

// Dialogs returns the dialogs seen so far, oldest first.
func (w *Session) Dialogs() []Dialog {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Dialog(nil), w.dialogs...)
}

// WaitDialog waits for a dialog that match accepts, and returns it.
//
// A nil match takes the next one.  Dialogs already seen count: a script
// that opens one the instant it is rezzed would otherwise be a race
// nobody can win.
func (w *Session) WaitDialog(ctx context.Context, timeout time.Duration, match func(Dialog) bool) (Dialog, error) {
	if match == nil {
		match = func(Dialog) bool { return true }
	}
	var found Dialog
	err := w.await(ctx, timeout, "a dialog", func() bool {
		for _, d := range w.dialogs {
			if match(d) {
				found = d
				return true
			}
		}
		return false
	})
	if err != nil {
		return Dialog{}, err
	}
	return found, nil
}

// AnswerText types into a text box.
//
// The reply is the same message a button press sends, with the typed
// text in place of the label -- which is why a text box cannot be told
// from a button by anything downstream, and why the script hears it on
// the same channel either way.
func (w *Session) AnswerText(ctx context.Context, d Dialog, text string) error {
	if !d.IsTextBox() {
		return fmt.Errorf("sl: %s is a dialog with buttons, not a text box", d.ObjectName)
	}
	if len(text) > maxDialogReply {
		return fmt.Errorf("sl: %d bytes is too long for a text box; the limit is %d", len(text), maxDialogReply)
	}
	return w.answer(ctx, d, 0, text)
}

// Forget drops a dialog from the list without answering it, which is
// what ignoring one on screen amounts to.
func (w *Session) ForgetDialog(d Dialog) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for i, x := range w.dialogs {
		if x.At.Equal(d.At) && x.Object == d.Object && x.Channel == d.Channel {
			w.dialogs = append(w.dialogs[:i], w.dialogs[i+1:]...)
			return
		}
	}
}

// Answer presses the button with this label.
//
// The label has to be one the dialog offered: the simulator passes both
// the index and the label to the script, and a script that switches on
// the label would be told something it never displayed.
func (w *Session) Answer(ctx context.Context, d Dialog, label string) error {
	i, ok := d.Button(label)
	if !ok {
		return fmt.Errorf("sl: %q is not one of the buttons: %v", label, d.Buttons)
	}
	return w.answer(ctx, d, i, d.Buttons[i])
}

// AnswerIndex presses the button at a position, counting from zero.
func (w *Session) AnswerIndex(ctx context.Context, d Dialog, i int) error {
	if i < 0 || i >= len(d.Buttons) {
		return fmt.Errorf("sl: there is no button %d; the dialog has %d", i, len(d.Buttons))
	}
	return w.answer(ctx, d, i, d.Buttons[i])
}

func (w *Session) answer(ctx context.Context, d Dialog, index int, label string) error {
	// Answered is done: a dialog that stayed on the list would be
	// offered again to whoever asks what is waiting.
	defer w.ForgetDialog(d)

	m := &msg.ScriptDialogReply{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.Data.ObjectID = d.Object
	m.Data.ChatChannel = d.Channel
	m.Data.ButtonIndex = int32(index)
	m.Data.ButtonLabel = append([]byte(label), 0)
	return w.Send(ctx, m)
}
