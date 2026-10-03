package slate

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// testBridgeCfg is budgets short enough for a failing step to be waited out.
func testBridgeCfg() bridgeCfg {
	return bridgeCfg{rez: 600 * time.Millisecond, name: 1500 * time.Millisecond, take: 5 * time.Second, cleanup: 10 * time.Second}
}

func makeOn(t *testing.T, f *fakeGrid, cfg bridgeCfg) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	return makeBridge(ctx, f.session(t), cfg)
}

func TestMakeBridgeHappyPath(t *testing.T) {
	f := newGrid(t)
	b := withBridge(t, f)
	if err := makeOn(t, f, testBridgeCfg()); err != nil {
		t.Fatalf("MakeBridge: %v", err)
	}
	if got := b.inv.itemsNamed("slate bridge"); len(got) != 1 {
		t.Fatalf("items named slate bridge = %v, want one", got)
	}
	if got := b.inv.folderOf(idMadeItem); got != idObjectsFolder {
		t.Errorf("the item is in folder %s, want Objects %s", got, idObjectsFolder)
	}
	// One metre along +X from the body at (128, 128, 22), 0.1 m on a side.
	if want := (msg.Vector3{X: 129, Y: 128, Z: 22}); b.rezzedAt != want {
		t.Errorf("rezzed at %v, want %v", b.rezzedAt, want)
	}
	if want := (msg.Vector3{X: 0.1, Y: 0.1, Z: 0.1}); b.scale != want {
		t.Errorf("scale %v, want %v", b.scale, want)
	}
	if b.trashed {
		t.Error("the prim was sent to the Trash")
	}
}

func TestMakeBridgeExistingItemIsNotDuplicated(t *testing.T) {
	f := newGrid(t)
	b := withBridge(t, f)
	b.inv.add(idObjectsFolder, idOldCopy, "slate bridge", 6)
	err := makeOn(t, f, testBridgeCfg())
	if !errors.Is(err, ErrBridgeExists) {
		t.Fatalf("err = %v, want ErrBridgeExists", err)
	}
	if n := len(sentOf[*msg.ObjectAdd](f)); n != 0 {
		t.Errorf("%d rezzes sent; none should be", n)
	}
	if got := b.inv.itemsNamed("slate bridge"); len(got) != 1 {
		t.Errorf("items named slate bridge = %v, want the one it had", got)
	}
}

func TestMakeBridgePositionNotExact(t *testing.T) {
	f := newGrid(t)
	withBridge(t, f)
	f.disappear(1) // the body is not in the store
	err := makeOn(t, f, testBridgeCfg())
	if err == nil || !strings.Contains(err.Error(), "slate: make-bridge: the tester's exact position is not known") {
		t.Fatalf("err = %v", err)
	}
	if n := len(sentOf[*msg.ObjectAdd](f)); n != 0 {
		t.Errorf("%d rezzes sent; the position check comes first", n)
	}
}

func TestMakeBridgeRezFails(t *testing.T) {
	f := newGrid(t)
	b := withBridge(t, f)
	b.noRez = true
	err := makeOn(t, f, testBridgeCfg())
	if err == nil || !strings.HasPrefix(err.Error(), "slate: make-bridge: rez (600ms): ") {
		t.Fatalf("err = %v", err)
	}
	if n := len(sentOf[*msg.DeRezObject](f)); n != 0 {
		t.Errorf("%d derezzes sent; nothing was rezzed", n)
	}
}

func TestMakeBridgeRenameFailsDeletesThePrim(t *testing.T) {
	f := newGrid(t)
	b := withBridge(t, f)
	b.noName = true
	err := makeOn(t, f, testBridgeCfg())
	if err == nil || !strings.HasPrefix(err.Error(), "slate: make-bridge: rename (1.5s): ") {
		t.Fatalf("err = %v", err)
	}
	if !b.trashed {
		t.Error("the rezzed prim was not deleted to the Trash")
	}
	if got := b.inv.itemsNamed("slate bridge"); len(got) != 0 {
		t.Errorf("items named slate bridge = %v, want none", got)
	}
}

func TestMakeBridgeTakeFailsDeletesThePrim(t *testing.T) {
	f := newGrid(t)
	b := withBridge(t, f)
	b.noTake = true
	cfg := testBridgeCfg()
	cfg.take = 1500 * time.Millisecond
	err := makeOn(t, f, cfg)
	if err == nil || !strings.HasPrefix(err.Error(), "slate: make-bridge: take (1.5s): ") {
		t.Fatalf("err = %v", err)
	}
	if !b.trashed {
		t.Error("the rezzed prim was not deleted to the Trash")
	}
	for _, d := range sentOf[*msg.DeRezObject](f) {
		if d.AgentBlock.Destination == 6 && d.AgentBlock.DestinationID != idTrashFolder {
			t.Errorf("delete went to %s, want the Trash %s", d.AgentBlock.DestinationID, idTrashFolder)
		}
	}
}
