package sl

import (
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

var asker = msg.MustParseUUID("f9f27e57-7e57-c0de-e18e-fdcf6427f6ee")

// A teleport request is kept the way a lure is, and replaced rather
// than piled up: asking twice is asking, not two teleports.
func TestATeleportRequestIsKeptAndReplaced(t *testing.T) {
	w := &Session{}
	w.noteTeleportRequest(&IM{
		At: time.Now(), From: asker, FromName: "Example Resident",
		Text: "may I come over", Dialog: DialogTeleportRequest,
	})
	got := w.TeleportRequests()
	if len(got) != 1 {
		t.Fatalf("kept %d requests", len(got))
	}
	if got[0].Name != "Example Resident" || got[0].Text != "may I come over" {
		t.Errorf("kept %+v", got[0])
	}

	w.noteTeleportRequest(&IM{
		At: time.Now(), From: asker, FromName: "Example Resident", Text: "still there?",
	})
	if got := w.TeleportRequests(); len(got) != 1 || got[0].Text != "still there?" {
		t.Errorf("a second ask did not replace the first: %+v", got)
	}

	w.ForgetTeleportRequest(w.TeleportRequests()[0])
	if got := w.TeleportRequests(); len(got) != 0 {
		t.Errorf("forgetting left %d", len(got))
	}
}

// It reads as a sentence with and without a note.
func TestATeleportRequestSaysWhoAsked(t *testing.T) {
	r := TeleportRequest{From: asker, Name: "Example Resident"}
	if got := r.String(); got != "Example Resident asks to be teleported here" {
		t.Errorf("got %q", got)
	}
	r.Text = "may I come over"
	if got := r.String(); !strings.Contains(got, "may I come over") {
		t.Errorf("got %q", got)
	}
	// With no name at all the id is the only handle there is.
	r = TeleportRequest{From: asker}
	if got := r.String(); !strings.Contains(got, asker.String()) {
		t.Errorf("got %q", got)
	}
}

// The dialogs around a teleport are named rather than printed as bare
// numbers, which is what sent somebody looking for "dialog 26".
func TestTheTeleportDialogsAreNamed(t *testing.T) {
	for d, want := range map[uint8]string{
		DialogTeleportLure:    "teleport lure",
		DialogLureAccepted:    "teleport offer accepted",
		DialogLureDeclined:    "teleport offer declined",
		DialogGodlikeLure:     "godlike teleport",
		DialogTeleportRequest: "teleport request",
	} {
		if got := DialogName(d); got != want {
			t.Errorf("DialogName(%d) = %q, want %q", d, got, want)
		}
	}
	// And an unknown one still says what it was.
	if got := DialogName(99); got != "dialog 99" {
		t.Errorf("got %q", got)
	}
}

// The offer goes out as StartLure, addressed to one person, carrying
// the note.  LureType is zero because the viewer sends zero and the
// simulator fills it in.
func TestOfferTeleportSendsAStartLure(t *testing.T) {
	w, f := newFakeSession(t)

	if err := w.OfferTeleport(t.Context(), somebody, "come and look at the dock"); err != nil {
		t.Fatalf("OfferTeleport: %v", err)
	}
	m := onlySent[*msg.StartLure](t, f)
	if len(m.TargetData) != 1 || m.TargetData[0].TargetID != somebody {
		t.Errorf("addressed to %+v", m.TargetData)
	}
	if m.Info.LureType != 0 {
		t.Errorf("LureType %d, want 0", m.Info.LureType)
	}
	// Terminated: without the NUL the simulator eats the last
	// character, which is a fault nothing here would otherwise notice.
	if n := len(m.Info.Message); n == 0 || m.Info.Message[n-1] != 0 {
		t.Errorf("the note is not NUL terminated: %q", m.Info.Message)
	}
	got := string(trimNul(m.Info.Message))
	if !strings.HasPrefix(got, "come and look at the dock") {
		t.Errorf("the note did not go: %q", got)
	}
	// And the position is on the end, whole: the last character of it
	// is what a missing terminator costs.
	if !strings.HasSuffix(got, "/128/128/25") {
		t.Errorf("the position did not survive: %q", got)
	}

	// Nobody is not somebody.
	if err := w.OfferTeleport(t.Context(), msg.UUID{}, "hello"); err == nil {
		t.Error("offered a teleport to nobody")
	}
}
