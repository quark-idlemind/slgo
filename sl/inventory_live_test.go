package sl

// Inventory against a live simulator, end to end.
//
// Skipped unless SLGO_TEST_ADDR names a running slgod; see
// backend_test.go for why the live tests are guarded rather than run by
// default.
//
// It CHANGES THINGS: it makes a folder, a notecard and a script, rezzes
// a prim, puts the items inside it, and takes all of it away again.
// Everything it makes is named with a prefix and removed in a Cleanup,
// so a failure part way through still tidies up -- but a process killed
// mid-run can leave a folder or a prim behind.
//
// # Why this cannot be a unit test
//
// Almost nothing in this protocol confirms itself.  An item goes into
// an object with no acknowledgement; a name change is a request; a
// permission mask is set by a message that answers nothing.  A fake
// simulator would confirm whatever this code believed, which is exactly
// the belief worth testing.  So each step here READS BACK what it did,
// and the value is entirely in doing that against a real one.
//
// # Why the steps are one test rather than several
//
// They are a chain: there is no notecard to put in an object until one
// has been made, and nothing to take out until it has been put in.
// Subtests would either repeat the setup a dozen times -- minutes of
// grid round trips each -- or share state through package variables and
// break confusingly when run with -run.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

const liveNotecard = "Hello from slgo.\nSecond line.\n"

const liveScript = `default { state_entry() { llOwnerSay("slgo test"); } }`

// TestInventoryChain walks every inventory operation once, in the order
// they depend on each other, reading back after each.
func TestInventoryChain(t *testing.T) {
	s := liveSession(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	where, err := s.Where(ctx)
	if err != nil {
		t.Fatalf("where: %v", err)
	}
	if err := s.Settle(ctx, 10*time.Second); err != nil {
		t.Fatalf("settling: %v", err)
	}

	// ---- a folder

	root, err := s.ObjectsFolder(ctx)
	if err != nil {
		t.Fatalf("finding Objects: %v", err)
	}
	folder, err := s.CreateFolder(ctx, root, "slgo live test")
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.DeleteFolder(ctx, folder); err != nil {
			t.Logf("could not remove the test folder: %v", err)
		}
	})

	// ---- items, and their contents

	note, err := s.CreateItem(ctx, "slgo note", "a notecard", int8(AssetNotecard), int8(AssetNotecard))
	if err != nil {
		t.Fatalf("creating a notecard: %v", err)
	}
	if _, err := s.SaveNotecard(ctx, note.ID, liveNotecard); err != nil {
		t.Fatalf("uploading the notecard: %v", err)
	}

	script, up, err := s.NewScript(ctx, "slgo script", liveScript)
	if err != nil {
		t.Fatalf("creating a script: %v", err)
	}
	if !up.Compiled {
		t.Fatalf("the test script did not compile: %v", up.Errors)
	}

	// ---- renaming and permissions, read back

	next := uint32(PermAll)
	after, err := s.SetItem(ctx, note.ID, "slgo note renamed", "changed", &next)
	if err != nil {
		t.Fatalf("renaming: %v", err)
	}
	if after.Name != "slgo note renamed" {
		t.Errorf("name is %q after renaming", after.Name)
	}
	if after.NextOwnerMask != next {
		t.Errorf("next-owner mask is %#x, want %#x", after.NextOwnerMask, next)
	}

	// ---- an object to put them in

	at := where.Position
	at.X += 2
	obj, err := s.Rez(ctx, RezOptions{At: at})
	if err != nil {
		t.Fatalf("rezzing something to work with: %v\n"+
			"        (an avatar with no active group cannot rez on land that "+
			"grants building to one)", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := s.Delete(ctx, obj, msg.UUID{}); err != nil {
			t.Logf("could not clean up the test prim: %v", err)
		}
	})
	if err := s.SetName(ctx, obj, "slgo live test prim"); err != nil {
		t.Fatalf("naming it: %v", err)
	}

	for _, it := range []*Item{after, script} {
		if err := s.PutInObject(ctx, obj, it); err != nil {
			t.Fatalf("putting %q in: %v", it.Name, err)
		}
	}

	// Nothing acknowledges an item going into an object, and asking for
	// the contents straight away returns the contents from before it
	// arrived -- which reads exactly like the put having failed.
	var held []TaskItem
	deadline := time.Now().Add(60 * time.Second)
	for {
		held, err = s.TaskInventory(ctx, obj)
		if err == nil && len(held) >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the object holds %d items after putting 2 in (err %v)", len(held), err)
		}
		time.Sleep(2 * time.Second)
	}

	names := map[string]TaskItem{}
	for _, it := range held {
		names[it.Name] = it
	}
	for _, want := range []string{"slgo note renamed", "slgo script"} {
		if _, ok := names[want]; !ok {
			t.Errorf("the object does not hold %q; it holds %v", want, taskNames(held))
		}
	}

	// ---- and out again
	//
	// By the id the OBJECT knows it by, which is not the id of the
	// inventory item it was copied from -- a distinction that costs an
	// afternoon if it is got wrong.
	for _, it := range held {
		if !strings.HasPrefix(it.Name, "slgo ") {
			continue
		}
		if err := s.RemoveFromObject(ctx, obj, it.ID); err != nil {
			t.Errorf("removing %q from the object: %v", it.Name, err)
		}
	}

	// ---- and the items themselves

	for _, it := range []*Item{after, script} {
		if err := s.DeleteItem(ctx, it.ID); err != nil {
			t.Errorf("deleting %q: %v", it.Name, err)
		}
	}
}

func taskNames(items []TaskItem) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}
