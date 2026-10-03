package agent

import (
	"context"
	"fmt"
	"net/http"

	"github.com/quark-idlemind/slgo/msg"
)

// An attachment that takes itself off -- a script's llDetachFromAvatar --
// leaves its link in the Current Outfit folder, and the folder is what
// is worn at the next login. The viewer takes the link out whenever an
// attachment leaves the avatar (LLAppearanceMgr::unregisterAttachment,
// Firestorm 885631b93a llappearancemgr.cpp:4904-4917, called from
// LLVOAvatarSelf::detachObject, llvoavatarself.cpp:1742-1771), so with
// no viewer attached nothing does.
//
// The region says an attachment went back into inventory with
// SaveAssetIntoInventory naming the item. A teleport sends none.
// Why: doc/outfit.md#an-attachment-that-takes-itself-off
func (a *Agent) keepOutfit() {
	a.Disp.MustHandle("SaveAssetIntoInventory", func(p *msg.Packet) {
		m := p.Message.(*msg.SaveAssetIntoInventory)
		item := m.InventoryData.ItemID
		if item.IsZero() {
			return
		}
		// Off the dispatch goroutine: this reads and writes over HTTP.
		a.spawn(func() error {
			a.forgetDetached(a.sessionContext(), item)
			return nil
		})
	}, msg.Inline())
}

// sessionContext is the session's lifetime, for work that outlives the
// message that started it.
func (a *Agent) sessionContext() context.Context {
	if a.runCtx != nil {
		return a.runCtx
	}
	return context.Background()
}

// ending reports a session that is logging out or over. The viewer
// keeps its outfit when it shuts down by the same guard
// (mAttachmentInvLinkEnabled, llappviewer.cpp:6840).
func (a *Agent) ending() bool {
	if a.loggingOut.Load() {
		return true
	}
	select {
	case <-a.done:
		return true
	case <-a.loggedOut.wait():
		return true
	default:
		return false
	}
}

// forgetDetached takes the links to an item out of the Current Outfit
// folder, unless the item is worn again or the session is ending, and
// says what it did.
func (a *Agent) forgetDetached(ctx context.Context, item msg.UUID) {
	if a.ending() {
		a.logf("outfit: item %s left the avatar as the session ended; its links stay", item)
		return
	}
	for _, o := range a.Objects().Attachments() {
		if o.AttachItem == item {
			a.logf("outfit: item %s saved while still described worn; its links stay", item)
			return
		}
	}
	n, gone, err := a.ForgetOutfitLinks(ctx, item)
	switch {
	case err != nil:
		a.logf("outfit: item %s left the avatar; removing its links failed after %d: %v", item, n, err)
	case n == 0 && gone == 0:
		a.logf("outfit: item %s left the avatar; no link to it in the outfit", item)
	default:
		a.logf("outfit: item %s left the avatar; removed %d link(s) from the outfit, %d already gone", item, n, gone)
	}
}

// ForgetOutfitLinks deletes every link to an item from the Current
// Outfit folder over AIS and says how many it removed and how many were
// already gone (a 404): a viewer attached through the daemon, or slsh's
// own detach, may have taken it out first, and that is not an error.
func (a *Agent) ForgetOutfitLinks(ctx context.Context, item msg.UUID) (removed, gone int, err error) {
	return ForgetOutfitLinks(ctx, a, a.Inventory, item)
}

// FolderCurrentOutfit is the preferred type of the Current Outfit
// folder.
const FolderCurrentOutfit = 46

// ForgetOutfitLinks is Agent.ForgetOutfitLinks for any CapDoer and the
// tree it keeps. The folder is found among the root's children by its
// type and read fresh, so a link another client has just removed is not
// asked for twice.
func ForgetOutfitLinks(ctx context.Context, d CapDoer, inv *Inventory, item msg.UUID) (removed, gone int, err error) {
	cof, ok := currentOutfit(inv)
	if !ok {
		if err := FetchFolder(ctx, d, inv, inv.Root()); err != nil {
			return 0, 0, err
		}
		if cof, ok = currentOutfit(inv); !ok {
			return 0, 0, fmt.Errorf("agent: this inventory has no Current Outfit folder")
		}
	}
	if err := FetchFolder(ctx, d, inv, cof); err != nil {
		return 0, 0, err
	}
	for _, it := range inv.Contents(cof) {
		if !it.IsLink || it.AssetID != item {
			continue
		}
		resp, err := d.DoCap(ctx, CapRequest{
			Cap:    InventoryCap,
			Method: http.MethodDelete,
			Path:   "/item/" + it.ID.String(),
		})
		if err != nil {
			return removed, gone, err
		}
		switch {
		case resp.OK():
			removed++
		case resp.Status == http.StatusNotFound:
			gone++
		default:
			return removed, gone, fmt.Errorf("agent: deleting outfit link %s: status %d", it.ID, resp.Status)
		}
	}
	return removed, gone, nil
}

func currentOutfit(inv *Inventory) (msg.UUID, bool) {
	for _, f := range inv.Children(inv.Root()) {
		if f.Type == FolderCurrentOutfit {
			return f.ID, true
		}
	}
	return msg.UUID{}, false
}
