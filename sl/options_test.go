package sl

import (
	"testing"
	"time"
)

// TestTheReadBackBoundsAreTheSessionsOptions: each is its default until
// it is set, and setting one leaves the others at theirs.
func TestTheReadBackBoundsAreTheSessionsOptions(t *testing.T) {
	w, _ := newFakeSession(t)
	if w.moveWait() != 15*time.Second || w.permissionsWait() != 15*time.Second || w.deleteWait() != 10*time.Second {
		t.Errorf("the defaults are %v, %v and %v; want 15s, 15s and 10s",
			w.moveWait(), w.permissionsWait(), w.deleteWait())
	}
	w.SetOptions(Options{MoveTimeout: 2 * time.Second})
	if w.moveWait() != 2*time.Second || w.permissionsWait() != DefaultPermissionsTimeout ||
		w.deleteWait() != DefaultDeleteTimeout {
		t.Errorf("with MoveTimeout set, the bounds are %v, %v and %v",
			w.moveWait(), w.permissionsWait(), w.deleteWait())
	}
	if got := w.Options(); got != (Options{MoveTimeout: 2 * time.Second}) {
		t.Errorf("Options = %+v, want what was set", got)
	}
}

// TestTheObjectWaitIsTheSessionsOption: Faces, SetFace and FacePicture
// wait for a name as long as Options.ObjectTimeout says, 30 s until it
// is set.
func TestTheObjectWaitIsTheSessionsOption(t *testing.T) {
	w, _ := newFakeSession(t)
	if w.objectWait() != 30*time.Second {
		t.Errorf("the default is %v, want 30s", w.objectWait())
	}
	w.SetOptions(Options{ObjectTimeout: 2 * time.Second})
	if w.objectWait() != 2*time.Second {
		t.Errorf("with ObjectTimeout set, the bound is %v", w.objectWait())
	}
}

// TestTheWindowForAskingEveryNameAgainIsTheSessionsOption: 30 s until
// it is set.
func TestTheWindowForAskingEveryNameAgainIsTheSessionsOption(t *testing.T) {
	w, _ := newFakeSession(t)
	if w.namesAskedAgainEvery() != 30*time.Second {
		t.Errorf("the default is %v, want 30s", w.namesAskedAgainEvery())
	}
	w.SetOptions(Options{NamesAskedAgainEvery: 2 * time.Second})
	if w.namesAskedAgainEvery() != 2*time.Second {
		t.Errorf("with NamesAskedAgainEvery set, the window is %v", w.namesAskedAgainEvery())
	}
}
