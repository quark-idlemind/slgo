package sl

// Changing inventory, as opposed to reading it.
//
// The thing worth holding these to is the thing inventory_ops.go was
// written for: none of these operations is confirmed by a reply to
// itself.  Creating a folder answers nothing, so the folder is looked
// for; a copy is given an id by the grid, so the copy is found by name;
// a rename over AIS answers 200 whatever happened, so the item is read
// back.  Every one of those is a loop that can be made to hang, to give
// up, or to declare success on an answer that was already true before
// the request went out -- which is what these tests are for, and none of
// them can be reached without something to be the far end.
//
// Two loops are not tested for giving up, because reaching the giving up
// costs twenty and fifteen seconds of real time.

import (
	"context"
	"errors"
	"io"
	"maps"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// ais answers the inventory capability for both of the things this file
// does with it: reading folders back, and the PATCH and DELETE that
// change something.
//
// One handler rather than two because it is one capability -- a call
// that changes an item and then reads it to see whether the change took
// goes to the same place twice.
type ais struct {
	mu       sync.Mutex
	calls    []aisCall
	status   int // what a change answers with; 0 is success
	contents func(msg.UUID) ([]*Folder, []*Item)
}

type aisCall struct {
	Method string
	Path   string
	Body   []byte
}

func serveAIS(t *testing.T, f *fakeBackend, contents func(msg.UUID) ([]*Folder, []*Item)) *ais {
	t.Helper()
	if contents == nil {
		contents = func(msg.UUID) ([]*Folder, []*Item) { return nil, nil }
	}
	a := &ais{contents: contents}
	f.ServeCap(t, agent.InventoryCap, func(w http.ResponseWriter, r *http.Request) {
		body, _ := readAllBody(r)
		a.mu.Lock()
		a.calls = append(a.calls, aisCall{r.Method, r.URL.Path, body})
		status, contents := a.status, a.contents
		a.mu.Unlock()

		if r.Method != http.MethodGet {
			if status != 0 {
				http.Error(w, "the grid said no", status)
				return
			}
			w.Header().Set("Content-Type", "application/llsd+xml")
			io.WriteString(w, `<llsd><map/></llsd>`)
			return
		}
		id := capFolderID(r.URL.Path)
		folders, items := contents(id)
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, folderLLSDTree(id, folders, items))
	})
	return a
}

// changes is every request that was not a read, which is the only
// evidence a delete happened: AIS answers one with an empty map.
func (a *ais) changes() []aisCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []aisCall
	for _, c := range a.calls {
		if c.Method != http.MethodGet {
			out = append(out, c)
		}
	}
	return out
}

// only insists on exactly one change having been made.
func (a *ais) only(t *testing.T) aisCall {
	t.Helper()
	got := a.changes()
	if len(got) != 1 {
		t.Fatalf("%d requests changed something, want 1: %+v", len(got), got)
	}
	return got[0]
}

func (a *ais) refuse(status int) {
	a.mu.Lock()
	a.status = status
	a.mu.Unlock()
}

// TestANewFolderIsLookedForRatherThanAssumed: nothing answers
// CreateInventoryFolder.  The id is chosen here, which is what makes
// looking possible -- and what would make a version of this that simply
// returned the id it had picked look exactly like a working one.
func TestANewFolderIsLookedForRatherThanAssumed(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	// The root holds whatever has been asked for, which is how a
	// folder that has been created differs from one that has not.
	f.ServeInventoryTree(t, func(id msg.UUID) ([]*Folder, []*Item) {
		if id != testInvRoot {
			return nil, nil
		}
		var out []*Folder
		for _, m := range sentOf[*msg.CreateInventoryFolder](f) {
			out = append(out, &Folder{
				ID:       m.FolderData.FolderID,
				ParentID: m.FolderData.ParentID,
				Name:     trimNul(m.FolderData.Name),
			})
		}
		return out, nil
	})

	// A zero parent means the root, so a caller that has not looked
	// one up still gets a folder somewhere sensible.
	id, err := w.CreateFolder(context.Background(), msg.UUID{}, "slgo path probe")
	if err != nil {
		t.Fatalf("CreateFolder: %v", err)
	}
	m := onlySent[*msg.CreateInventoryFolder](t, f)
	if m.FolderData.FolderID != id {
		t.Errorf("made %s and reported %s", m.FolderData.FolderID, id)
	}
	if m.FolderData.ParentID != testInvRoot {
		t.Errorf("made it in %s, want the inventory root", m.FolderData.ParentID)
	}
	if m.FolderData.Type != -1 {
		t.Errorf("preferred type %d, want -1 for an ordinary folder", m.FolderData.Type)
	}
	// Unterminated, the simulator takes the last byte for the
	// terminator and the folder comes back a character short.
	if n := len(m.FolderData.Name); n == 0 || m.FolderData.Name[n-1] != 0 {
		t.Errorf("the name went out as %q, with no terminator", m.FolderData.Name)
	}
	if got := trimNul(m.FolderData.Name); got != "slgo path probe" {
		t.Errorf("asked for a folder called %q", got)
	}
}

// TestCreateFolderRefusesWhatItCouldNotAskFor: a folder with no name is
// not refused by the simulator, it is made and called nothing.
func TestCreateFolderRefusesWhatItCouldNotAskFor(t *testing.T) {
	t.Run("no name", func(t *testing.T) {
		w, f := newFakeSession(t)
		if _, err := w.CreateFolder(context.Background(), aFolder, ""); err == nil {
			t.Error("CreateFolder made a folder with no name")
		}
		if got := f.Sent(); len(got) != 0 {
			t.Errorf("something went out anyway: %s", f.describe())
		}
	})

	t.Run("the request never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.CreateFolder(context.Background(), aFolder, "probe"); err == nil {
			t.Error("CreateFolder waited for a folder it never asked for")
		}
	})
}

// TestCreateFolderStopsWhenTheCallerGivesUp: a caller that gives up is
// told so at once, not twenty seconds later as a timeout.  The folder
// may have been made by then, and a caller that is not told of it
// cannot take it away again.
//
// The caller gives up while the first look after the request is being
// answered, and that look finds nothing; where the folder is made at
// the same moment, only a look after the cancel can see it.
func TestCreateFolderStopsWhenTheCallerGivesUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		made bool
	}{
		{"before anything was made", false},
		{"as the folder was made", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			var mu sync.Mutex
			looked, made := false, false
			f.ServeInventoryTree(t, func(id msg.UUID) ([]*Folder, []*Item) {
				asked := sentOf[*msg.CreateInventoryFolder](f)
				if id != testInvRoot || len(asked) == 0 {
					return nil, nil
				}
				mu.Lock()
				defer mu.Unlock()
				var out []*Folder
				if made {
					out = append(out, &Folder{
						ID: asked[0].FolderData.FolderID, ParentID: testInvRoot, Name: "lanterns",
					})
				}
				if !looked {
					looked, made = true, tc.made
					cancel()
				}
				return out, nil
			})

			id, err := whenCancelled(t, ctx, func(ctx context.Context) (msg.UUID, error) {
				return w.CreateFolder(ctx, msg.UUID{}, "lanterns")
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("CreateFolder = %v, want the caller's cancel", err)
			}
			var want msg.UUID
			if tc.made {
				want = onlySent[*msg.CreateInventoryFolder](t, f).FolderData.FolderID
			}
			if id != want {
				t.Errorf("CreateFolder handed back %s, want %s", id, want)
			}
		})
	}
}

// TestDeletingGoesOverAISBecauseTheOtherWayDoesNothing: the UDP move
// into Trash is accepted and ignored, so this is a DELETE, and the
// difference between an item and a category is one word of the path.
func TestDeletingGoesOverAISBecauseTheOtherWayDoesNothing(t *testing.T) {
	w, f := newFakeSession(t)
	a := serveAIS(t, f, nil)

	if err := w.DeleteItem(context.Background(), theChild); err != nil {
		t.Fatalf("DeleteItem: %v", err)
	}
	if got := a.only(t); got.Method != http.MethodDelete ||
		got.Path != "/item/"+theChild.String() {
		t.Errorf("deleted with %s %s", got.Method, got.Path)
	}
	// Nothing goes out over UDP: a message here would be the path
	// that silently does nothing.
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("a delete put %s on the wire", f.describe())
	}

	a.refuse(http.StatusForbidden)
	if err := w.DeleteItem(context.Background(), theChild); err == nil {
		t.Error("DeleteItem reported a deletion the grid refused")
	}
}

// TestDeletingAFolderWillNotTakeTheRootWithIt: the root has no parent
// to be removed from, and an inventory without one is not something to
// find out about by trying.
func TestDeletingAFolderWillNotTakeTheRootWithIt(t *testing.T) {
	w, f := newFakeSession(t)
	a := serveAIS(t, f, nil)

	if err := w.DeleteFolder(context.Background(), w.InventoryRoot()); err == nil {
		t.Error("DeleteFolder deleted the whole inventory")
	}
	if got := a.changes(); len(got) != 0 {
		t.Errorf("the root was asked about anyway: %+v", got)
	}

	if err := w.DeleteFolder(context.Background(), aFolder); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	if got := a.only(t); got.Path != "/category/"+aFolder.String() {
		t.Errorf("deleted %s", got.Path)
	}
}

// TestEmptyingAFolderLeavesTheFolder: emptying the trash is this, and
// the trash is a folder that is not meant to be deleted.  The root is
// refused because emptying it is emptying inventory.
func TestEmptyingAFolderLeavesTheFolder(t *testing.T) {
	w, f := newFakeSession(t)
	a := serveAIS(t, f, nil)

	if err := w.PurgeFolder(context.Background(), msg.UUID{}); err == nil {
		t.Error("PurgeFolder emptied a folder that was not named")
	}
	if err := w.PurgeFolder(context.Background(), w.InventoryRoot()); err == nil {
		t.Error("PurgeFolder emptied the whole inventory")
	}
	if got := a.changes(); len(got) != 0 {
		t.Errorf("something was asked for anyway: %+v", got)
	}

	if err := w.PurgeFolder(context.Background(), aFolder); err != nil {
		t.Fatalf("PurgeFolder: %v", err)
	}
	// The children, not the category: the folder itself stays.
	if got := a.only(t); got.Path != "/category/"+aFolder.String()+"/children" {
		t.Errorf("emptied %s", got.Path)
	}

	a.refuse(http.StatusInternalServerError)
	if err := w.PurgeFolder(context.Background(), aFolder); err == nil {
		t.Error("PurgeFolder reported emptying a folder the grid refused to empty")
	}
}

// TestTheTrashIsFoundByItsPreferredTypeAndNotItsName: an account made
// through a viewer in another language never called it "Trash", and it
// can be renamed in any language.
func TestTheTrashIsFoundByItsPreferredTypeAndNotItsName(t *testing.T) {
	w, f := newFakeSession(t)
	serveFolders(t, f,
		&Folder{ID: aFolder, ParentID: testInvRoot, Name: "Objects", Type: 6},
		&Folder{ID: theOther, ParentID: testInvRoot, Name: "Papierkorb", Type: FolderTrash},
	)
	got, err := w.TrashFolder(context.Background())
	if err != nil {
		t.Fatalf("TrashFolder: %v", err)
	}
	if got != theOther {
		t.Errorf("the trash is %s, want the one with the preferred type", got)
	}
}

// TestTrashFolderSaysWhenThereIsNone: an inventory with no trash and an
// inventory that could not be read are different, and one of them is
// worth retrying.
func TestTrashFolderSaysWhenThereIsNone(t *testing.T) {
	t.Run("no folder has the type", func(t *testing.T) {
		w, f := newFakeSession(t)
		serveFolders(t, f, &Folder{ID: aFolder, ParentID: testInvRoot, Name: "Trash", Type: 6})
		if _, err := w.TrashFolder(context.Background()); err == nil {
			t.Error("a folder merely called Trash was taken for the trash")
		}
	})

	t.Run("the listing failed", func(t *testing.T) {
		w, _ := newFakeSession(t)
		if _, err := w.TrashFolder(context.Background()); err == nil {
			t.Error("TrashFolder found a trash without reading anything")
		}
	})
}

// TestACopyIsFoundByLookingBecauseTheGridNamesIt: unlike a folder, the
// copy's id is the grid's to choose, so there is nothing to wait for
// and nothing to confirm except an item in the folder that was not
// there before.
func TestACopyIsFoundByLookingBecauseTheGridNamesIt(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	before := anItem(theOther, "workbench")
	copied := anItem(theChild, "workbench spare")

	var mu sync.Mutex
	made := false
	f.ServeInventory(t, func(msg.UUID) []*Item {
		mu.Lock()
		defer mu.Unlock()
		if !made {
			return []*Item{before}
		}
		return []*Item{before, copied}
	})

	wait := aside(t, func() (*Item, error) {
		return w.CopyItem(context.Background(), theOther, aFolder, "workbench spare", 0)
	})

	m := waitSent[*msg.CopyInventoryItem](t, f)
	if len(m.InventoryData) != 1 {
		t.Fatalf("%d items were copied", len(m.InventoryData))
	}
	d := m.InventoryData[0]
	if d.OldAgentID != testAgentID || d.OldItemID != theOther || d.NewFolderID != aFolder {
		t.Errorf("copied %+v", d)
	}
	if got := trimNul(d.NewName); got != "workbench spare" {
		t.Errorf("named the copy %q", got)
	}

	mu.Lock()
	made = true
	mu.Unlock()

	got, err := wait()
	if err != nil {
		t.Fatalf("CopyItem: %v", err)
	}
	if got.ID != theChild {
		t.Errorf("the copy is %s, want the item that was not there before", got.ID)
	}
}

// TestCopyItemWithNoNameTakesAnythingNew: an empty name means the same
// name as the original, so there is nothing to match on and the only
// test left is that the item is new.
func TestCopyItemWithNoNameTakesAnythingNew(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	before := anItem(theOther, "workbench")
	f.ServeInventory(t, func(msg.UUID) []*Item {
		if len(sentOf[*msg.CopyInventoryItem](f)) == 0 {
			return []*Item{before}
		}
		return []*Item{before, anItem(theChild, "workbench")}
	})

	got, err := w.CopyItem(context.Background(), theOther, aFolder, "", 0)
	if err != nil {
		t.Fatalf("CopyItem: %v", err)
	}
	if got.ID != theChild {
		t.Errorf("the copy is %s", got.ID)
	}
}

// TestCopyItemReportsWhatDidNotHappen: a no-copy item is not refused,
// the grid simply never answers -- so a copy that never appears is the
// normal way this fails and the error has to say so.
func TestCopyItemReportsWhatDidNotHappen(t *testing.T) {
	t.Run("no folder to copy into", func(t *testing.T) {
		w, _ := newFakeSession(t)
		if _, err := w.CopyItem(context.Background(), theOther, msg.UUID{}, "spare", 0); err == nil {
			t.Error("CopyItem copied something into nowhere")
		}
	})

	t.Run("the folder could not be read", func(t *testing.T) {
		w, f := newFakeSession(t)
		if _, err := w.CopyItem(context.Background(), theOther, aFolder, "spare", 0); err == nil {
			t.Error("CopyItem went ahead without knowing what was in the folder")
		}
		if got := f.Sent(); len(got) != 0 {
			t.Errorf("it copied anyway: %s", f.describe())
		}
	})

	t.Run("the request never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.CopyItem(context.Background(), theOther, aFolder, "spare", 0); err == nil {
			t.Error("CopyItem looked for a copy it never asked for")
		}
	})

	t.Run("nothing new ever appeared", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.ServeInventory(t, func(msg.UUID) []*Item { return []*Item{anItem(theOther, "workbench")} })
		// One nanosecond, because the only thing being tested here is
		// the giving up and the real default is thirty seconds.
		_, err := w.CopyItem(context.Background(), theOther, aFolder, "spare", 1)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("CopyItem = %v, want a timeout", err)
		}
		if err != nil && !strings.Contains(err.Error(), "no-copy") {
			t.Errorf("CopyItem = %v, want it to name the usual cause", err)
		}
	})
}

// TestCopyItemStopsWhenTheCallerGivesUp: a caller that gives up is told
// so at once, not at the end of the wait it asked for.  The copy may
// have been made by then, and is handed back with the cancel so that
// the caller can delete it; see TestCreateFolderStopsWhenTheCallerGivesUp
// for how the two cases are staged.
func TestCopyItemStopsWhenTheCallerGivesUp(t *testing.T) {
	for _, tc := range []struct {
		name string
		made bool
	}{
		{"before anything was made", false},
		{"as the copy was made", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			before := anItem(theOther, "workbench")
			copied := anItem(theChild, "workbench spare")
			var mu sync.Mutex
			looked, made := false, false
			f.ServeInventory(t, func(msg.UUID) []*Item {
				if len(sentOf[*msg.CopyInventoryItem](f)) == 0 {
					return []*Item{before}
				}
				mu.Lock()
				defer mu.Unlock()
				out := []*Item{before}
				if made {
					out = append(out, copied)
				}
				if !looked {
					looked, made = true, tc.made
					cancel()
				}
				return out
			})

			got, err := whenCancelled(t, ctx, func(ctx context.Context) (*Item, error) {
				return w.CopyItem(ctx, theOther, aFolder, "workbench spare", time.Minute)
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("CopyItem = %v, want the caller's cancel", err)
			}
			switch {
			case tc.made && (got == nil || got.ID != theChild):
				t.Errorf("CopyItem handed back %v, want the copy that was made", got)
			case !tc.made && got != nil:
				t.Errorf("CopyItem handed back %s, and nothing was made", got.ID)
			}
		})
	}
}

// TestSetItemChangesOnlyWhatItWasGiven: an empty name or description
// means leave it alone, and a nil mask means leave the permissions
// alone -- so a caller changing a description does not have to know the
// name to avoid clearing it.
func TestSetItemChangesOnlyWhatItWasGiven(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	// The grid applies all three, because the read-back waits for all
	// three: a fake that changed only the name would be asking to be
	// told the change had landed when two thirds of it had not.
	next := uint32(PermAll)
	renamed := anItem(theChild, "workbench renamed")
	renamed.Desc = "a bench"
	renamed.NextOwnerMask = next
	a := serveAIS(t, f, func(id msg.UUID) ([]*Folder, []*Item) {
		if id != testInvRoot {
			return nil, nil
		}
		return nil, []*Item{renamed}
	})

	got, err := w.SetItem(context.Background(), theChild, "workbench renamed", "a bench", &next)
	if err != nil {
		t.Fatalf("SetItem: %v", err)
	}
	if got.Name != "workbench renamed" || got.Desc != "a bench" {
		t.Errorf("the item read back as %q / %q", got.Name, got.Desc)
	}

	call := a.only(t)
	if call.Method != http.MethodPatch || call.Path != "/item/"+theChild.String() {
		t.Errorf("changed it with %s %s", call.Method, call.Path)
	}
	for _, want := range []string{"workbench renamed", "a bench", "next_owner_mask"} {
		if !strings.Contains(string(call.Body), want) {
			t.Errorf("the request did not carry %q: %s", want, call.Body)
		}
	}
	// Nothing over UDP: UpdateInventoryItem is accepted and changes
	// nothing, which is the whole reason this goes over AIS.
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("a change put %s on the wire", f.describe())
	}
}

// TestSetItemAsksForNothingWhenThereIsNothingToAsk: a call with nothing
// in it is a round trip that can only fail, so it answers from the
// inventory it had already read.
func TestSetItemAsksForNothingWhenThereIsNothingToAsk(t *testing.T) {
	w, f := newFakeSession(t)
	it := anItem(theChild, "workbench")
	a := serveAIS(t, f, func(id msg.UUID) ([]*Folder, []*Item) {
		if id != testInvRoot {
			return nil, nil
		}
		return nil, []*Item{it}
	})

	got, err := w.SetItem(context.Background(), theChild, "", "", nil)
	if err != nil {
		t.Fatalf("SetItem: %v", err)
	}
	if got.Name != "workbench" {
		t.Errorf("read back %q", got.Name)
	}
	if changed := a.changes(); len(changed) != 0 {
		t.Errorf("a change with nothing in it went out anyway: %+v", changed)
	}
}

// TestSetItemSaysWhichStepFailed: it is a read, a change and a read
// back, and a caller told only that it failed cannot tell whether the
// item was changed.
func TestSetItemSaysWhichStepFailed(t *testing.T) {
	t.Run("the inventory could not be read", func(t *testing.T) {
		w, _ := newFakeSession(t)
		if _, err := w.SetItem(context.Background(), theChild, "renamed", "", nil); err == nil {
			t.Error("SetItem changed an item it could not find")
		}
	})

	t.Run("no such item", func(t *testing.T) {
		w, f := newFakeSession(t)
		serveAIS(t, f, nil)
		_, err := w.SetItem(context.Background(), theChild, "renamed", "", nil)
		if err == nil || !strings.Contains(err.Error(), theChild.String()) {
			t.Errorf("SetItem = %v, want it to name what was not there", err)
		}
	})

	t.Run("the grid refused the change", func(t *testing.T) {
		w, f := newFakeSession(t)
		it := anItem(theChild, "workbench")
		a := serveAIS(t, f, func(id msg.UUID) ([]*Folder, []*Item) {
			if id != testInvRoot {
				return nil, nil
			}
			return nil, []*Item{it}
		})
		a.refuse(http.StatusBadRequest)
		if _, err := w.SetItem(context.Background(), theChild, "renamed", "", nil); err == nil {
			t.Error("SetItem reported a change the grid refused")
		}
	})
}

// TestSetItemWaitsForADescriptionAsItWaitsForAName: a change is
// confirmed by reading back what was asked for, and a description is as
// much a thing that was asked for as a name is.
//
// The read-back used to test the name and nothing else.  A
// description-only change names nothing, so the condition was
// vacuously true and the first item that could be read was handed back
// -- the item as it was.  The grid takes a moment to show a change and
// can refuse one outright, and both came back here as a success
// carrying the old description.
//
// So the grid here shows the old one first, which is what a grid that
// has not caught up looks like, and is exactly the read the old code
// returned.
func TestSetItemWaitsForADescriptionAsItWaitsForAName(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	it := anItem(theChild, "workbench")
	it.Desc = "the old description"

	// Read 1 is SetItem looking the item up; read 2 is the first read
	// back, and still shows the old description; read 3 is where the
	// change has landed.  The old code returned read 2.
	var reads atomic.Int32
	serveAIS(t, f, func(id msg.UUID) ([]*Folder, []*Item) {
		if id != testInvRoot {
			return nil, nil
		}
		shown := *it
		if reads.Add(1) >= 3 {
			shown.Desc = "the new description"
		}
		return nil, []*Item{&shown}
	})

	got, err := w.SetItem(context.Background(), theChild, "", "the new description", nil)
	if err != nil {
		t.Fatalf("SetItem: %v", err)
	}
	if got.Desc != "the new description" {
		t.Errorf("SetItem confirmed a description of %q", got.Desc)
	}
}

// TestSetItemStopsWhenTheCallerGivesUp: the change never shows here, and
// the caller gives up while the first read back is being answered.  It
// is told so at once, rather than fifteen seconds later that the item
// did not change.
func TestSetItemStopsWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	it := anItem(theChild, "workbench")
	var a *ais
	a = serveAIS(t, f, func(id msg.UUID) ([]*Folder, []*Item) {
		if id != testInvRoot {
			return nil, nil
		}
		if len(a.changes()) > 0 {
			cancel()
		}
		return nil, []*Item{it}
	})

	got, err := whenCancelled(t, ctx, func(ctx context.Context) (*Item, error) {
		return w.SetItem(ctx, theChild, "workbench renamed", "", nil)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("SetItem = %v, want the caller's cancel", err)
	}
	if got != nil {
		t.Errorf("SetItem handed back %q, which never changed", got.Name)
	}
}

// TestPermissionsTakeTwoMessagesBecauseTheProtocolCannotAssign: the
// message turns bits on or off, so sending only the on half leaves a bit
// the caller cleared still set -- and the caller would read the mask
// back and see it.
func TestPermissionsTakeTwoMessagesBecauseTheProtocolCannotAssign(t *testing.T) {
	prim := func(w *Session) *Object { return foundHere(w, &Object{ID: thePrim, Local: 77}) }

	t.Run("some bits on and the rest off", func(t *testing.T) {
		w, f := newFakeSession(t)
		answerMasks(t, f, thePrim, 0, granted)
		if _, err := w.SetObjectPermissions(context.Background(), prim(w), WhoNextOwner, PermCopy); err != nil {
			t.Fatalf("SetObjectPermissions: %v", err)
		}
		got := sentOf[*msg.ObjectPermissions](f)
		if len(got) != 2 {
			t.Fatalf("%d messages went out, want one to set and one to clear", len(got))
		}
		on, off := got[0].ObjectData[0], got[1].ObjectData[0]
		if on.Set != 1 || on.Mask != PermCopy || on.Field != WhoNextOwner {
			t.Errorf("the on half was %+v", on)
		}
		if off.Set != 0 || off.Mask != PermAll&^PermCopy {
			t.Errorf("the off half was %+v", off)
		}
		if on.ObjectLocalID != 77 || got[0].HeaderData.Override {
			t.Errorf("set on local id %d with override %v",
				on.ObjectLocalID, got[0].HeaderData.Override)
		}
	})

	t.Run("everything on leaves nothing to clear", func(t *testing.T) {
		w, f := newFakeSession(t)
		answerMasks(t, f, thePrim, 0, granted)
		if _, err := w.SetObjectPermissions(context.Background(), prim(w), WhoOwner, PermAll); err != nil {
			t.Fatalf("SetObjectPermissions: %v", err)
		}
		if got := sentOf[*msg.ObjectPermissions](f); len(got) != 1 {
			t.Errorf("%d messages went out for a mask with no off half", len(got))
		}
	})

	t.Run("everything off leaves nothing to set", func(t *testing.T) {
		w, f := newFakeSession(t)
		answerMasks(t, f, thePrim, 0, granted)
		if _, err := w.SetObjectPermissions(context.Background(), prim(w), WhoEveryone, 0); err != nil {
			t.Fatalf("SetObjectPermissions: %v", err)
		}
		got := sentOf[*msg.ObjectPermissions](f)
		if len(got) != 1 || got[0].ObjectData[0].Set != 0 {
			t.Errorf("a mask of nothing sent %d messages: %+v", len(got), got)
		}
	})

	t.Run("the first message never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.SetObjectPermissions(context.Background(), prim(w), WhoOwner, PermCopy); err == nil {
			t.Error("SetObjectPermissions reported permissions it never sent")
		}
	})
}

// answerMasks plays the simulator's side of a permission change: an
// object's masks, changed by each ObjectPermissions into what grant makes
// of them, and sent in full each time the object is selected.  A nil
// grant is a simulator that ignores the change.  The first stale
// selections are answered with the masks as they were before any change,
// which is a read overtaking the write.
func answerMasks(t *testing.T, f *fakeBackend, id msg.UUID, stale int, grant func(who uint8, mask uint32) uint32) {
	t.Helper()
	before := map[uint8]uint32{WhoBase: PermAll, WhoOwner: PermAll, WhoNextOwner: PermAll}
	masks := maps.Clone(before)
	selects := 0
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		switch q := m.(type) {
		case *msg.ObjectPermissions:
			for _, d := range q.ObjectData {
				if grant == nil {
					continue
				}
				mask := masks[d.Field]
				if d.Set == 1 {
					mask |= d.Mask
				} else {
					mask &^= d.Mask
				}
				masks[d.Field] = grant(d.Field, mask)
			}
		case *msg.ObjectSelect:
			selects++
			now := masks
			if selects <= stale {
				now = before
			}
			p := propertiesOf(id)
			d := &p.ObjectData[0]
			d.BaseMask, d.OwnerMask, d.GroupMask = now[WhoBase], now[WhoOwner], now[WhoGroup]
			d.EveryoneMask, d.NextOwnerMask = now[WhoEveryone], now[WhoNextOwner]
			f.Relay(t, p)
		}
	}
}

// granted is a simulator that grants whatever it is asked.
func granted(_ uint8, mask uint32) uint32 { return mask }

// byTheRules is a simulator that applies the viewer's permission rules
// to an object whose base and owner masks are full: everyone is never
// given modify, and a next owner without copy is given transfer.
func byTheRules(who uint8, mask uint32) uint32 {
	switch who {
	case WhoEveryone:
		mask &^= PermModify
	case WhoNextOwner:
		if mask&PermCopy == 0 {
			mask |= PermTransfer
		}
	}
	return mask
}

// TestPermissionsAreReadBackUntilTheyHold: nothing answers
// ObjectPermissions, and a read straight after a write can overtake it
// -- measured for descriptions -- so a mask that still reads as it was
// is asked about again rather than reported.
func TestPermissionsAreReadBackUntilTheyHold(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	answerMasks(t, f, thePrim, 1, granted)

	o := foundHere(w, &Object{ID: thePrim, Local: 77})
	if _, err := w.SetObjectPermissions(context.Background(), o, WhoEveryone, PermCopy); err != nil {
		t.Fatalf("SetObjectPermissions: %v", err)
	}
	if n := len(sentOf[*msg.ObjectSelect](f)); n != 2 {
		t.Errorf("the masks were read %d times, want twice: once overtaken, once as set", n)
	}
}

// TestPermissionsTheRulesAdjustAreConfirmedAsAdjusted: the permission
// rules adjust a request rather than refuse it, so a mask that reads as
// the rules make what was sent has landed, and what it now allows is
// what is handed back -- a next owner asked to modify may transfer too.
func TestPermissionsTheRulesAdjustAreConfirmedAsAdjusted(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name       string
		who        uint8
		asked, now uint32
	}{
		{"a next owner without copy", WhoNextOwner, PermModify, PermModify | PermTransfer},
		{"everyone asked to modify", WhoEveryone, PermCopy | PermModify, PermCopy},
		{"a group given what was asked", WhoGroup, PermCopy | PermMove, PermCopy | PermMove},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			answerMasks(t, f, thePrim, 0, byTheRules)

			o := foundHere(w, &Object{ID: thePrim, Local: 77})
			got, err := w.SetObjectPermissions(context.Background(), o, c.who, c.asked)
			if err != nil {
				t.Fatalf("SetObjectPermissions: %v", err)
			}
			if got != c.now {
				t.Errorf("SetObjectPermissions = %s, want %s", PermWords(got), PermWords(c.now))
			}
		})
	}
}

// TestPermissionsTheRulesDoNotExplainAreNotReported: a mask that never
// reads as the rules make what was sent -- changed some other way, or
// not changed at all -- is an error that says what it allows, not a
// report of what was asked for.
func TestPermissionsTheRulesDoNotExplainAreNotReported(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name   string
		grant  func(uint8, uint32) uint32
		allows string
	}{
		{"narrowed otherwise", func(who uint8, mask uint32) uint32 {
			if who == WhoGroup {
				mask &^= PermCopy
			}
			return mask
		}, "it allows modify"},
		{"ignored", nil, "it allows nothing"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			answerMasks(t, f, thePrim, 0, c.grant)
			w.SetOptions(Options{PermissionsTimeout: time.Millisecond})

			o := foundHere(w, &Object{ID: thePrim, Local: 77})
			_, err := w.SetObjectPermissions(context.Background(), o, WhoGroup, PermCopy|PermModify)
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("SetObjectPermissions = %v, want a timeout", err)
			}
			if !strings.Contains(err.Error(), c.allows) || !strings.Contains(err.Error(), "make what was sent copy, modify") {
				t.Errorf("SetObjectPermissions = %v, want what the rules make of it and %q", err, c.allows)
			}
		})
	}
}

// TestTheExpectedMaskIsTheViewersRules: what the rules make of a mask
// sent, from the viewer's llpermissions.cpp, and the two bits the source
// leaves unsettled going uncompared.
func TestTheExpectedMaskIsTheViewersRules(t *testing.T) {
	const noTransfer = PermCopy | PermModify | PermMove
	for _, c := range []struct {
		name        string
		who         uint8
		sent        uint32
		base, owner uint32
		want        uint32
		uncompared  uint32
	}{
		{"an owner within the base", WhoOwner, PermAll, noTransfer, PermAll, noTransfer, 0},
		{"a group within the owner", WhoGroup, PermCopy | PermModify, PermAll, PermCopy, PermCopy, 0},
		{"everyone never modifies", WhoEveryone, PermAll, PermAll, PermAll, PermAll &^ PermModify, 0},
		{"everyone's copy with no transfer in the base", WhoEveryone, PermCopy, noTransfer, noTransfer, 0, PermCopy},
		{"a group's copy with no transfer in the base", WhoGroup, PermCopy | PermMove, noTransfer, noTransfer, PermMove, PermCopy},
		{"a next owner who may copy", WhoNextOwner, PermCopy, PermAll, PermAll, PermCopy, PermMove},
		{"a next owner who may not copy transfers", WhoNextOwner, PermModify, PermAll, PermAll, PermModify | PermTransfer, PermMove},
		{"a next owner given nothing transfers", WhoNextOwner, 0, PermAll, PermAll, PermTransfer, PermMove},
		{"a next owner within the base", WhoNextOwner, PermModify, noTransfer, noTransfer, PermModify, PermMove},
		{"a next owner left with nothing", WhoNextOwner, PermModify, PermMove, PermMove, 0, 0},
		{"a base as sent", WhoBase, PermCopy, PermAll, PermAll, PermCopy, 0},
	} {
		p := &Properties{BaseMask: c.base, OwnerMask: c.owner}
		want, compared := expectMask(c.who, c.sent, p)
		if want != c.want || compared != PermAll&^c.uncompared {
			t.Errorf("%s: expected %s comparing %s, want %s comparing %s", c.name,
				PermWords(want), PermWords(compared), PermWords(c.want), PermWords(PermAll&^c.uncompared))
		}
	}
}

// TestPermissionsStopWhenTheCallerGivesUp: given up on while the masks
// are being read back, it says so at once rather than fifteen seconds
// later that they never changed.
func TestPermissionsStopWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.mu.Lock()
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.ObjectSelect); ok {
			cancel()
		}
	}
	f.mu.Unlock()

	o := foundHere(w, &Object{ID: thePrim, Local: 77})
	_, err := whenCancelled(t, ctx, func(ctx context.Context) (struct{}, error) {
		_, err := w.SetObjectPermissions(ctx, o, WhoEveryone, PermCopy)
		return struct{}{}, err
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("SetObjectPermissions = %v, want the caller's cancel", err)
	}
}

// TestPermissionsForNobodyAreRefused: a who-value that names no mask has
// no mask to read back, so nothing is sent for it.
func TestPermissionsForNobodyAreRefused(t *testing.T) {
	w, f := newFakeSession(t)
	o := foundHere(w, &Object{ID: thePrim, Local: 77})
	if _, err := w.SetObjectPermissions(context.Background(), o, WhoOwner|WhoGroup, PermCopy); err == nil {
		t.Error("SetObjectPermissions took two masks at once")
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("sent %s for a mask nobody has", f.describe())
	}
}

// TestPermWordsSaysAMaskInWords, so that what was set is legible to
// somebody who does not know the letters.
func TestPermWordsSaysAMaskInWords(t *testing.T) {
	for _, c := range []struct {
		mask uint32
		want string
	}{
		{0, "nothing"},
		{PermCopy, "copy"},
		{PermCopy | PermTransfer, "copy, transfer"},
		{PermAll, "copy, modify, transfer, move"},
		{0x7ffffff0 &^ PermAll, "nothing"},
	} {
		if got := PermWords(c.mask); got != c.want {
			t.Errorf("PermWords(%#x) = %q, want %q", c.mask, got, c.want)
		}
	}
}

// seenAt is an object the region has described, already named so that
// looking at the region does not turn into a round of name requests.
func seenAt(id msg.UUID, local uint32, at msg.Vector3) *Seen {
	return &Seen{
		Object:   Object{ID: id, Local: local, Name: "workbench"},
		Owner:    testAgentID,
		Position: at,
	}
}

// TestRezFromInventoryConfirmsByPositionAndNotByNovelty: an object that
// is new to this session is not necessarily one we just made -- the
// entry for something taken a moment ago lingers until KillObject is
// processed, and it was the object that had GONE that got reported as
// the one that had arrived.  Where it was asked for is the one thing
// about it that cannot be stale.
func TestRezFromInventoryConfirmsByPositionAndNotByNovelty(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	at := msg.Vector3{X: 130, Y: 128, Z: 25}
	it := anItem(theChild, "workbench")
	it.Flags, it.GroupMask, it.EveryoneMask, it.NextOwnerMask = 1, PermCopy, PermMove, PermTransfer
	it.Created = 1700000000

	// What is already there: one of ours at the spot, which is the
	// one a novelty test would fall for.
	f.objects = []*Seen{seenAt(theOther, 5, at)}

	wait := aside(t, func() (*Object, error) {
		return w.RezFromInventory(context.Background(), it, at, testRegionID, 0)
	})

	m := waitSent[*msg.RezObject](t, f)
	if m.AgentData.GroupID != testRegionID {
		t.Errorf("rezzed as group %s; a parcel grants building to a group, "+
			"and zero is a stranger", m.AgentData.GroupID)
	}
	if m.RezData.BypassRaycast != 1 {
		t.Error("without BypassRaycast the position is ignored and the prim lands on whatever a ray hits")
	}
	if m.RezData.RayStart != at || m.RezData.RayEnd != at {
		t.Errorf("rezzed along %v to %v", m.RezData.RayStart, m.RezData.RayEnd)
	}
	if m.InventoryData.ItemID != it.ID || m.InventoryData.CRC != itemCRC(it) {
		t.Errorf("rezzed %s with checksum %d", m.InventoryData.ItemID, m.InventoryData.CRC)
	}

	// Everything that arrives afterwards, only one of which is the
	// object that was asked for: a child prim, somebody else's, one
	// too far away, and two of ours -- of which the nearer wins.
	f.mu.Lock()
	f.objects = append(f.objects,
		&Seen{Object: Object{ID: thePrim, Local: 8, Name: "linked"}, Owner: testAgentID,
			Position: at, Parent: 5},
		&Seen{Object: Object{ID: mustUUID("14ff7e57-7e57-c0de-3d5f-1eb0c71e7610"), Local: 9,
			Name: "somebody else's"}, Owner: theOther, Position: at},
		seenAt(mustUUID("1ff07e57-7e57-c0de-5b88-0f61d0ade479"), 10,
			msg.Vector3{X: at.X + 40, Y: at.Y, Z: at.Z}),
		seenAt(mustUUID("35aa7e57-7e57-c0de-288f-71993ab0962d"), 11,
			msg.Vector3{X: at.X + 3, Y: at.Y, Z: at.Z}),
		seenAt(thePrim, 12, msg.Vector3{X: at.X + 1, Y: at.Y, Z: at.Z}),
	)
	f.mu.Unlock()

	o, err := wait()
	if err != nil {
		t.Fatalf("RezFromInventory: %v", err)
	}
	if o.Local != 12 {
		t.Errorf("rezzed local id %d, want the nearest new one of ours", o.Local)
	}
}

// TestRezFromInventoryReportsWhatDidNotHappen: the request is not
// refused when it fails.  Nothing appears and nothing is said, so every
// failure here looks the same from the wire and has to be told apart
// before the send.
func TestRezFromInventoryReportsWhatDidNotHappen(t *testing.T) {
	at := msg.Vector3{X: 130, Y: 128, Z: 25}
	it := anItem(theChild, "workbench")

	t.Run("nothing to rez", func(t *testing.T) {
		w, _ := newFakeSession(t)
		if _, err := w.RezFromInventory(context.Background(), nil, at, msg.UUID{}, time.Second); err == nil {
			t.Error("RezFromInventory rezzed nothing at all")
		}
	})

	t.Run("out where nothing would be described", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.presence.DrawDistance = 64
		_, err := w.RezFromInventory(context.Background(), it,
			msg.Vector3{X: 1000, Y: 1000, Z: 25}, msg.UUID{}, time.Second)
		if !errors.Is(err, ErrOutOfRange) {
			t.Errorf("RezFromInventory = %v, want a refusal", err)
		}
	})

	t.Run("the region could not be looked at", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.objectsErr = errors.New("the daemon is not answering")
		if _, err := w.RezFromInventory(context.Background(), it, at, msg.UUID{}, time.Second); err == nil {
			t.Error("RezFromInventory rezzed without knowing what was there before")
		}
	})

	t.Run("the request never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.RezFromInventory(context.Background(), it, at, msg.UUID{}, time.Second); err == nil {
			t.Error("RezFromInventory looked for a prim it never asked for")
		}
	})

	t.Run("nothing appeared where it was asked for", func(t *testing.T) {
		w, f := newFakeSession(t)
		// A presence that cannot be had is not a reason to refuse a
		// rez: the range check is a courtesy, so it goes ahead.
		f.presenceErr = errors.New("nobody knows where we are")
		f.objects = []*Seen{seenAt(theOther, 5, msg.Vector3{X: 200, Y: 200, Z: 25})}
		// One nanosecond, because what is being tested is the giving
		// up and the real default is a minute.
		_, err := w.RezFromInventory(context.Background(), it, at, msg.UUID{}, 1)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("RezFromInventory = %v, want a timeout", err)
		}
	})
}

// TestRezFromInventoryStopsWhenTheCallerGivesUp: a caller that gives up
// is told so at once, not a minute later as a timeout.  The object may
// be standing there by then, and is handed back with the cancel so that
// the caller can take it; see TestCreateFolderStopsWhenTheCallerGivesUp
// for how the two cases are staged.
func TestRezFromInventoryStopsWhenTheCallerGivesUp(t *testing.T) {
	at := msg.Vector3{X: 130, Y: 128, Z: 25}
	for _, tc := range []struct {
		name string
		made bool
	}{
		{"before anything was made", false},
		{"as the object was made", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			looked := false
			f.mu.Lock()
			f.afterObjects = func() {
				if looked || !sentLocked[*msg.RezObject](f) {
					return
				}
				looked = true
				if tc.made {
					f.objects = append(f.objects, seenAt(thePrim, 12, at))
				}
				cancel()
			}
			f.mu.Unlock()

			o, err := whenCancelled(t, ctx, func(ctx context.Context) (*Object, error) {
				return w.RezFromInventory(ctx, anItem(theChild, "workbench"), at, msg.UUID{}, time.Minute)
			})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("RezFromInventory = %v, want the caller's cancel", err)
			}
			switch {
			case tc.made && (o == nil || o.Local != 12):
				t.Errorf("RezFromInventory handed back %v, want the object that was made", o)
			case !tc.made && o != nil:
				t.Errorf("RezFromInventory handed back %v, and nothing was made", o)
			}
		})
	}
}

// TestGivingSomethingAwayIsAnInstantMessage: there is no message for
// this.  The asset type and the id go in the bucket of an IM, and that
// is the whole protocol for handing something over.
func TestGivingSomethingAwayIsAnInstantMessage(t *testing.T) {
	w, f := newFakeSession(t)

	if err := w.GiveToAvatar(context.Background(), theOther, theChild,
		"workbench", int8(AssetObject)); err != nil {
		t.Fatalf("GiveToAvatar: %v", err)
	}
	m := onlySent[*msg.ImprovedInstantMessage](t, f)
	if m.MessageBlock.ToAgentID != theOther {
		t.Errorf("offered it to %s", m.MessageBlock.ToAgentID)
	}
	if m.MessageBlock.Dialog != 4 {
		t.Errorf("dialog %d, want the inventory offer", m.MessageBlock.Dialog)
	}
	if got := m.MessageBlock.BinaryBucket; len(got) != 17 ||
		got[0] != byte(AssetObject) || msg.UUID(got[1:17]) != theChild {
		t.Errorf("the bucket was %x", got)
	}
	if m.MessageBlock.Position != f.presence.Position {
		t.Errorf("offered from %v", m.MessageBlock.Position)
	}
	// Both strings terminated: without it the offer arrives naming
	// "workbenc", which is what the recipient is shown.
	if got := trimNul(m.MessageBlock.Message); got != "workbench" {
		t.Errorf("the offer named %q", got)
	}
	if n := len(m.MessageBlock.Message); m.MessageBlock.Message[n-1] != 0 {
		t.Errorf("the name went out unterminated: %q", m.MessageBlock.Message)
	}
	if got := trimNul(m.MessageBlock.FromAgentName); got != "Quark Idlemind" {
		t.Errorf("offered it as %q", got)
	}
}

// TestGiveToAvatarWillNotOfferFromNowhere: the offer carries the
// position it was made from, so a session that cannot say where it is
// has nothing to put there.
func TestGiveToAvatarWillNotOfferFromNowhere(t *testing.T) {
	w, f := newFakeSession(t)
	f.presenceErr = errors.New("nobody knows where we are")
	if err := w.GiveToAvatar(context.Background(), theOther, theChild, "workbench", 6); err == nil {
		t.Error("GiveToAvatar made an offer from a position it did not have")
	}
}

// TestActivatingAGroupWaitsToBeToldItTookEffect: a viewer remembers the
// active group across sessions and a fresh login has none, so an avatar
// that has always been able to build somewhere is refused with a message
// blaming the land.  Nothing answers this message, so the group is read
// back.
func TestActivatingAGroupWaitsToBeToldItTookEffect(t *testing.T) {
	w, f := newFakeSession(t)
	f.presence.ActiveGroup = testRegionID

	if err := w.ActivateGroup(context.Background(), testRegionID, 0); err != nil {
		t.Fatalf("ActivateGroup: %v", err)
	}
	m := onlySent[*msg.ActivateGroup](t, f)
	if m.AgentData.GroupID != testRegionID {
		t.Errorf("activated %s", m.AgentData.GroupID)
	}
}

// TestActivateGroupSaysWhatItUsuallyMeans: the group never becoming
// active almost always means this avatar is not in it, which is not
// something the grid ever says.
func TestActivateGroupSaysWhatItUsuallyMeans(t *testing.T) {
	t.Run("the request never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if err := w.ActivateGroup(context.Background(), testRegionID, time.Second); err == nil {
			t.Error("ActivateGroup waited for a group it never asked for")
		}
	})

	t.Run("it never became active", func(t *testing.T) {
		t.Parallel()
		w, _ := newFakeSession(t)
		// Long enough for one round of asking and the half second
		// wait between rounds, and no longer: the giving up is the
		// point, and the real default is fifteen seconds.
		err := w.ActivateGroup(context.Background(), testRegionID, 300*time.Millisecond)
		if !errors.Is(err, ErrTimeout) {
			t.Errorf("ActivateGroup = %v, want a timeout", err)
		}
		if err != nil && !strings.Contains(err.Error(), "member") {
			t.Errorf("ActivateGroup = %v, want it to name the usual cause", err)
		}
	})
}

// TestActivateGroupStopsWhenTheCallerGivesUp: the group is read back
// from the presence, which answers whatever the context, as Direct's
// does.  So only the wait can notice the caller giving up, and it has
// to, rather than read on to the end of the minute.
func TestActivateGroupStopsWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.mu.Lock()
	f.afterPresence = func() {
		if sentLocked[*msg.ActivateGroup](f) {
			cancel()
		}
	}
	f.mu.Unlock()

	_, err := whenCancelled(t, ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, w.ActivateGroup(ctx, testRegionID, time.Minute)
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("ActivateGroup = %v, want the caller's cancel", err)
	}
}

// TestTheItemChecksumCountsEveryFieldTheSimulatorCounts: sent as zero,
// an update is accepted and silently does nothing -- so a checksum that
// leaves a field out is a rename that returns no error and changes
// nothing, which is exactly how this came to be written.
func TestTheItemChecksumCountsEveryFieldTheSimulatorCounts(t *testing.T) {
	base := &Item{
		ID: theChild, ParentID: aFolder, AssetID: theOther,
		CreatorID: testAgentID, OwnerID: testAgentID, GroupID: testRegionID,
		BaseMask: PermAll, OwnerMask: PermAll, GroupMask: PermCopy,
		EveryoneMask: PermMove, NextOwnerMask: PermTransfer,
		Flags: 3, Type: 6, InvType: 6, Created: 1700000000,
		SaleType: 2, SalePrice: 11,
	}
	want := itemCRC(base)

	for _, c := range []struct {
		what   string
		change func(*Item)
	}{
		{"the asset", func(it *Item) { it.AssetID = testRegionID }},
		{"the item id", func(it *Item) { it.ID = testRegionID }},
		{"the folder", func(it *Item) { it.ParentID = testRegionID }},
		{"the creator", func(it *Item) { it.CreatorID = testRegionID }},
		{"the owner", func(it *Item) { it.OwnerID = theOther }},
		{"the group", func(it *Item) { it.GroupID = theOther }},
		{"the owner mask", func(it *Item) { it.OwnerMask ^= PermCopy }},
		{"the next owner mask", func(it *Item) { it.NextOwnerMask ^= PermCopy }},
		{"the everyone mask", func(it *Item) { it.EveryoneMask ^= PermCopy }},
		{"the group mask", func(it *Item) { it.GroupMask ^= PermMove }},
		{"the flags", func(it *Item) { it.Flags++ }},
		{"the inventory type", func(it *Item) { it.InvType++ }},
		{"the asset type", func(it *Item) { it.Type++ }},
		{"the creation date", func(it *Item) { it.Created++ }},
		{"the sale price", func(it *Item) { it.SalePrice++ }},
		{"the sale type", func(it *Item) { it.SaleType++ }},
	} {
		it := *base
		c.change(&it)
		if got := itemCRC(&it); got == want {
			t.Errorf("%s is not counted in the checksum", c.what)
		}
	}

	// And what is deliberately absent: the viewer's own version adds
	// the base mask, and the simulator evidently does not require it.
	it := *base
	it.BaseMask ^= PermCopy
	if got := itemCRC(&it); got != want {
		t.Errorf("the base mask changed the checksum from %d to %d", want, got)
	}
}

// TestAUUIDIsFoldedBackToFront: the four words are little endian, which
// is not what reading the printed form suggests -- and a checksum built
// the other way round is accepted and does nothing.
func TestAUUIDIsFoldedBackToFront(t *testing.T) {
	if got := uuidCRC(msg.UUID{1}); got != 1 {
		t.Errorf("uuidCRC of a leading 01 = %#x, want 1", got)
	}
	if got := uuidCRC(msg.UUID{0, 0, 0, 1}); got != 1<<24 {
		t.Errorf("uuidCRC of 00 00 00 01 = %#x, want %#x", got, 1<<24)
	}
	if got := uuidCRC(msg.UUID{}); got != 0 {
		t.Errorf("uuidCRC of nothing = %#x", got)
	}
}

// applyMoves serves an inventory in which every move that went out has
// happened, as a simulator that takes them: each item and folder a move
// named is listed in the last folder it was sent to, under the last name
// it was given, and the item keeps the name "workbench" until then.
func applyMoves(t *testing.T, f *fakeBackend) {
	t.Helper()
	f.ServeInventoryTree(t, func(folder msg.UUID) ([]*Folder, []*Item) {
		items := map[msg.UUID]*Item{}
		parents := map[msg.UUID]msg.UUID{}
		for _, s := range f.Sent() {
			switch m := s.Msg.(type) {
			case *msg.MoveInventoryItem:
				for _, d := range m.InventoryData {
					it, ok := items[d.ItemID]
					if !ok {
						it = anItem(d.ItemID, "workbench")
						items[d.ItemID] = it
					}
					it.ParentID = d.FolderID
					if name := trimNul(d.NewName); name != "" {
						it.Name = name
					}
				}
			case *msg.MoveInventoryFolder:
				for _, d := range m.InventoryData {
					parents[d.FolderID] = d.ParentID
				}
			}
		}
		var fs []*Folder
		for id, parent := range parents {
			if parent == folder {
				fs = append(fs, &Folder{ID: id, ParentID: parent, Name: "a folder", Type: -1})
			}
		}
		var is []*Item
		for _, it := range items {
			if it.ParentID == folder {
				is = append(is, it)
			}
		}
		return fs, is
	})
}

// TestMovingAnItemGoesOverUDPAndMayRenameOnTheWay: AIS answers a change
// of parent_id with "Cannot change parent_id.  Use MOVE method", so this
// is the viewer's message -- which carries a new name, making a move and
// a rename one round trip.
func TestMovingAnItemGoesOverUDPAndMayRenameOnTheWay(t *testing.T) {
	w, f := newFakeSession(t)
	applyMoves(t, f)

	if err := w.MoveItem(context.Background(), theChild, aFolder); err != nil {
		t.Fatalf("MoveItem: %v", err)
	}
	m := onlySent[*msg.MoveInventoryItem](t, f)
	if m.AgentData.Stamp {
		t.Error("the move re-dated the item")
	}
	d := m.InventoryData[0]
	if d.ItemID != theChild || d.FolderID != aFolder {
		t.Errorf("moved %s into %s", d.ItemID, d.FolderID)
	}
	if got := trimNul(d.NewName); got != "" {
		t.Errorf("a move with no name renamed it to %q", got)
	}

	f.Forget()
	if err := w.MoveItem(context.Background(), theChild, aFolder, "renamed"); err != nil {
		t.Fatalf("MoveItem: %v", err)
	}
	got := onlySent[*msg.MoveInventoryItem](t, f).InventoryData[0]
	if name := trimNul(got.NewName); name != "renamed" {
		t.Errorf("renamed it to %q on the way", name)
	}
}

// TestRenamingAFolderGoesOverAIS: the opposite of the item path, and
// worth saying why -- the refusal there was about parent_id and not
// about PATCH, so a category takes a new name this way.
func TestRenamingAFolderGoesOverAIS(t *testing.T) {
	w, f := newFakeSession(t)
	a := serveAIS(t, f, nil)

	if err := w.RenameFolder(context.Background(), aFolder, ""); err == nil {
		t.Error("RenameFolder renamed a folder to nothing")
	}
	if err := w.RenameFolder(context.Background(), w.InventoryRoot(), "mine"); err == nil {
		t.Error("RenameFolder renamed the inventory root")
	}
	if got := a.changes(); len(got) != 0 {
		t.Errorf("something went out anyway: %+v", got)
	}

	if err := w.RenameFolder(context.Background(), aFolder, "renamed"); err != nil {
		t.Fatalf("RenameFolder: %v", err)
	}
	call := a.only(t)
	if call.Method != http.MethodPatch || call.Path != "/category/"+aFolder.String() {
		t.Errorf("renamed with %s %s", call.Method, call.Path)
	}
	if !strings.Contains(string(call.Body), "renamed") {
		t.Errorf("the request did not carry the new name: %s", call.Body)
	}

	a.refuse(http.StatusBadRequest)
	if err := w.RenameFolder(context.Background(), aFolder, "again"); err == nil {
		t.Error("RenameFolder reported a rename the grid refused")
	}
}

// TestMovingAFolderGoesOverUDPForTheSameReason: AIS will not change a
// parent, whatever is being moved.
func TestMovingAFolderGoesOverUDPForTheSameReason(t *testing.T) {
	w, f := newFakeSession(t)
	applyMoves(t, f)
	if err := w.MoveFolder(context.Background(), aFolder, testInvRoot); err != nil {
		t.Fatalf("MoveFolder: %v", err)
	}
	m := onlySent[*msg.MoveInventoryFolder](t, f)
	if m.AgentData.Stamp {
		t.Error("the move re-dated the folder")
	}
	d := m.InventoryData[0]
	if d.FolderID != aFolder || d.ParentID != testInvRoot {
		t.Errorf("moved %s into %s", d.FolderID, d.ParentID)
	}
}

// TestAMoveIsListedWhereItWentBeforeItIsReported: nothing answers a
// move, and one into Trash is accepted and ignored, so a move is done
// when the destination lists it and not when the message has gone.
// One the grid ignores is an error saying it never arrived; one that
// arrives without the name it was given is an error saying what it is
// called.
func TestAMoveIsListedWhereItWentBeforeItIsReported(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		move func(context.Context, *Session) error
		want string
	}{
		{"an item the grid ignores", func(ctx context.Context, w *Session) error {
			return w.MoveItem(ctx, theChild, aFolder)
		}, "never arrived"},
		{"a folder the grid ignores", func(ctx context.Context, w *Session) error {
			return w.MoveFolder(ctx, aFolder, testInvRoot)
		}, "never arrived"},
		{"a rename the grid ignores", func(ctx context.Context, w *Session) error {
			return w.MoveItem(ctx, theChild, aFolder, "renamed")
		}, `arrived named "workbench"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			w.SetOptions(Options{MoveTimeout: time.Millisecond})
			// The item is in the destination already, under its old
			// name, only where the question is the name.
			f.ServeInventory(t, func(folder msg.UUID) []*Item {
				if strings.Contains(c.name, "rename") && folder == aFolder {
					return []*Item{anItem(theChild, "workbench")}
				}
				return nil
			})

			err := c.move(context.Background(), w)
			if !errors.Is(err, ErrTimeout) {
				t.Fatalf("the move = %v, want a timeout", err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the move = %v, want it to say %q", err, c.want)
			}
		})
	}
}

// TestAMoveStopsWhenTheCallerGivesUp: given up on while the destination
// is being listed, it says so at once rather than fifteen seconds later
// that nothing arrived.
func TestAMoveStopsWhenTheCallerGivesUp(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.ServeInventory(t, func(msg.UUID) []*Item {
		cancel()
		return nil
	})

	_, err := whenCancelled(t, ctx, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, w.MoveItem(ctx, theChild, aFolder)
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("MoveItem = %v, want the caller's cancel", err)
	}
}

// TestAMoveNeedsSomewhereToGo: there is no folder to list for a zero
// id, so nothing is sent for one.
func TestAMoveNeedsSomewhereToGo(t *testing.T) {
	w, f := newFakeSession(t)
	if err := w.MoveItem(context.Background(), theChild, msg.UUID{}); err == nil {
		t.Error("MoveItem moved an item into no folder")
	}
	if err := w.MoveFolder(context.Background(), aFolder, msg.UUID{}); err == nil {
		t.Error("MoveFolder moved a folder into no folder")
	}
	if got := f.Sent(); len(got) != 0 {
		t.Errorf("sent %s for a move with nowhere to go", f.describe())
	}
}
