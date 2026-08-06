package world

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
func (w *World) dialog(m *msg.ScriptDialog) {
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
func (w *World) Dialogs() []Dialog {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]Dialog(nil), w.dialogs...)
}

// WaitDialog waits for a dialog that match accepts, and returns it.
//
// A nil match takes the next one.  Dialogs already seen count: a script
// that opens one the instant it is rezzed would otherwise be a race
// nobody can win.
func (w *World) WaitDialog(ctx context.Context, timeout time.Duration, match func(Dialog) bool) (Dialog, error) {
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

// Answer presses the button with this label.
//
// The label has to be one the dialog offered: the simulator passes both
// the index and the label to the script, and a script that switches on
// the label would be told something it never displayed.
func (w *World) Answer(ctx context.Context, d Dialog, label string) error {
	i, ok := d.Button(label)
	if !ok {
		return fmt.Errorf("world: %q is not one of the buttons: %v", label, d.Buttons)
	}
	return w.answer(ctx, d, i, d.Buttons[i])
}

// AnswerIndex presses the button at a position, counting from zero.
func (w *World) AnswerIndex(ctx context.Context, d Dialog, i int) error {
	if i < 0 || i >= len(d.Buttons) {
		return fmt.Errorf("world: there is no button %d; the dialog has %d", i, len(d.Buttons))
	}
	return w.answer(ctx, d, i, d.Buttons[i])
}

func (w *World) answer(ctx context.Context, d Dialog, index int, label string) error {
	m := &msg.ScriptDialogReply{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.Data.ObjectID = d.Object
	m.Data.ChatChannel = d.Channel
	m.Data.ButtonIndex = int32(index)
	m.Data.ButtonLabel = append([]byte(label), 0)
	return w.Send(ctx, m)
}
