package sl

// Wearing something that is not an object.
//
// A shirt, a skin, a shape, a pair of eyes, an alpha: these are system
// wearables, and none of them is an object.  They do not rez, they do
// not go on an attachment point, and RezSingleAttachmentFromInv does
// not put one on -- the simulator answers a request to attach one with
// silence, which is the same silence any id it has no object for gets.
//
// # The message that looks like the answer is not
//
// AgentIsNowWearing (Low 383) is what a viewer used to send, and
// reading the protocol alone would make it the obvious way to do this.
// It is dead.  The viewer that still sends it says so where it does:
//
//	// We no longer need this message in the current viewer, but send
//	// it for now to maintain compatibility with release viewers.
//	void LLAgentWearables::sendDummyAgentWearablesUpdate()
//	  (llagentwearables.cpp:927-929)
//
// and what it sends is four fixed ids that mean nothing at all --
// "4 standardized nonsense item ids (same as returned by the modified
// sim, not that it especially matters)".  Asking a live simulator for
// this avatar's wearables came back with exactly those four constants,
// which is how the deadness was confirmed rather than assumed.
//
// # What actually wears one
//
// The Current Outfit folder.  It is an ordinary inventory folder full
// of links, one per worn thing, and it is the record the baking service
// reads to decide what an avatar looks like.  A viewer wears a wearable
// by putting a link to it in there and then asking for a rebake
// (llappearancemgr.cpp:1638-1703, ending at link_inventory_array).
//
// So the folder is maintained entirely by the client.  Nothing on the
// far side writes it, which is why an attachment put on from here
// without touching it is on the avatar now and gone at the next login.
//
// # Replacing
//
// A body part always replaces: an avatar cannot wear two skins, and the
// viewer removes the links of that slot before adding one
// (removeCOFLinksOfType, llappearancemgr.cpp:1671).  Clothing layers,
// so it adds unless replacing was asked for -- the same default, and
// for the same reason, as wearing an object.
//
// Which slot a wearable occupies lives in the low byte of the item's
// flags and nowhere else (LLWearableType::inventoryFlagsToWearableType,
// llwearabletype.cpp:133).  A skin and a shape are both "bodypart" and
// are told apart by that byte alone.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// WearableType is the slot a system wearable occupies.
//
// The numbers are the grid's (LLWearableType::EType,
// llwearabletype.h:44-61) and are what travels in an item's flags.
type WearableType uint8

const (
	WearableShape WearableType = iota
	WearableSkin
	WearableHair
	WearableEyes
	WearableShirt
	WearablePants
	WearableShoes
	WearableSocks
	WearableJacket
	WearableGloves
	WearableUndershirt
	WearableUnderpants
	WearableSkirt
	WearableAlpha
	WearableTattoo
	WearablePhysics
	WearableUniversal
	wearableCount
)

var wearableNames = [wearableCount]string{
	"shape", "skin", "hair", "eyes", "shirt", "pants", "shoes",
	"socks", "jacket", "gloves", "undershirt", "underpants", "skirt",
	"alpha", "tattoo", "physics", "universal",
}

func (t WearableType) String() string {
	if t < wearableCount {
		return wearableNames[t]
	}
	return fmt.Sprintf("wearable%d", uint8(t))
}

// WearableSlotMask is the part of an item's flags that is the slot.
const WearableSlotMask = 0xff

// FolderCurrentOutfit is the preferred type the grid gives the Current
// Outfit folder.
//
// By type and not by name, for the reason the trash is found by type:
// the folder can be renamed, and an account made through a viewer in
// another language never called it "Current Outfit" in the first place.
const FolderCurrentOutfit = 46

// IsWearable says whether an asset type is a system wearable, which is
// the thing that cannot be attached.
func IsWearable(t AssetType) bool {
	return t == AssetClothing || t == AssetBodypart
}

// AlwaysReplaces says whether wearing one of these replaces whatever
// occupies the slot, whether or not replacing was asked for.
//
// Body parts do: there is no such thing as wearing two skins, and an
// avatar that managed it would not be showing both.  Clothing layers.
func AlwaysReplaces(t AssetType) bool { return t == AssetBodypart }

// SlotOf is the slot an entry occupies, and whether it is a system
// wearable at all.
func SlotOf(kind AssetType, flags uint32) (WearableType, bool) {
	if !IsWearable(kind) {
		return 0, false
	}
	return WearableType(flags & WearableSlotMask), true
}

// CurrentOutfit finds the Current Outfit folder.
func (w *Session) CurrentOutfit(ctx context.Context) (msg.UUID, error) {
	es, err := w.ListFolder(ctx, w.InventoryRoot(), 0)
	if err != nil {
		return msg.UUID{}, err
	}
	for _, e := range es {
		if e.Folder && e.Type == FolderCurrentOutfit {
			return e.ID, nil
		}
	}
	return msg.UUID{}, fmt.Errorf("sl: this inventory has no Current Outfit folder")
}

// LinkItem puts a link to an item into a folder.
//
// Over AIS and not over the UDP message that exists for it.  Sending
// LinkInventoryItem (Low 426) was tried against a live simulator and
// answered "Cannot create requested inventory." -- a refusal, not a
// silence, which at least says so.  The viewer only packs that message
// where AIS is unavailable (llviewerinventory.cpp:1489-1514), and this
// tree had already learned the same thing for deleting and renaming:
// the UDP messages for those are accepted and ignored.
//
// The link is confirmed by reading the folder back rather than by
// trusting the answer, which is the habit the inventory PATCH here was
// given for the same reason: a request that was refused can otherwise
// come back looking like a success.
func (w *Session) LinkItem(ctx context.Context, folder msg.UUID, it *Item) error {
	if folder.IsZero() {
		return fmt.Errorf("sl: no folder to link into")
	}
	body, err := llsd.Encode(map[string]any{
		"links": []any{map[string]any{
			"linked_id": llsd.UUID(it.ID.String()),
			"type":      int(AssetLink),
			"inv_type":  it.InvType,
			"name":      it.Name,
			"desc":      it.Desc,
		}},
	})
	if err != nil {
		return err
	}
	// The transaction id is the caller's to invent and is what makes
	// the POST safe to repeat; the viewer generates a fresh one per
	// request (llaisapi.cpp:117-120).
	path := "/category/" + folder.String() + "?tid=" + randomUUID().String()
	if _, err := w.capDo(ctx, agent.CapRequest{
		Cap: agent.InventoryCap, Method: "POST",
		Path: path, Body: body, Type: "application/llsd+xml",
	}); err != nil {
		return err
	}

	es, err := w.ListFolder(ctx, folder, 0)
	if err != nil {
		return fmt.Errorf("sl: reading back the link to %s: %w", it.Name, err)
	}
	for _, e := range es {
		if e.IsLink && e.Asset == it.ID {
			return nil
		}
	}
	return fmt.Errorf("sl: the link to %s was accepted and is not in the folder", it.Name)
}

// OutfitLink is one link in the Current Outfit folder, with what it
// points at already worked out.
type OutfitLink struct {
	// Link is the link itself, which is what to delete to take the
	// thing off.  Item is what it points at.
	Link msg.UUID
	Item msg.UUID
	Name string

	// Kind is the asset type of the thing at the far end, and Slot is
	// its wearable slot where it has one.
	Kind     AssetType
	Slot     WearableType
	Wearable bool

	// Folder is a link to a folder rather than to an item.  The
	// Current Outfit folder holds one of these naming the outfit that
	// was put on, which is bookkeeping rather than a worn thing.
	Folder bool

	// Found is whether the thing at the far end was reached at all.
	// A link outlives what it points at, and one that has been left
	// behind says nothing about its kind.
	Found bool
}

// Outfit reads the Current Outfit folder and follows every link in it.
//
// One walk of inventory for the lot.  The folder is a dozen or more
// links and each has to be resolved to know what it is: a link carries
// the name and the inventory type of its target but not the target's
// asset type or flags, and the slot a wearable occupies lives in those
// flags.
func (w *Session) Outfit(ctx context.Context) ([]OutfitLink, error) {
	cof, err := w.CurrentOutfit(ctx)
	if err != nil {
		return nil, err
	}
	return w.outfitIn(ctx, cof)
}

// outfitIn is Outfit for a folder already found, so that a caller doing
// several of these reads the root once.
func (w *Session) outfitIn(ctx context.Context, cof msg.UUID) ([]OutfitLink, error) {
	here, err := w.ListFolder(ctx, cof, 0)
	if err != nil {
		return nil, err
	}
	all, err := w.ListInventory(ctx, "", 4)
	if err != nil {
		return nil, err
	}
	byID := make(map[msg.UUID]Entry, len(all))
	for _, e := range all {
		byID[e.ID] = e
	}

	out := make([]OutfitLink, 0, len(here))
	for _, e := range here {
		if !e.IsLink || e.Asset.IsZero() {
			continue
		}
		l := OutfitLink{Link: e.ID, Item: e.Asset, Name: e.Name}
		if to, ok := byID[e.Asset]; ok {
			l.Found, l.Folder = true, to.Folder
			l.Kind = AssetType(to.Type)
			l.Slot, l.Wearable = SlotOf(l.Kind, to.Flags)
		}
		out = append(out, l)
	}
	return out, nil
}

// ErrAlreadyWorn is a wearable that the Current Outfit folder already
// holds a link to.  Wearing it again would put a second link in for one
// item, which is a state nothing here could then unpick: the two links
// agree in every field a person could name one by.
var ErrAlreadyWorn = errors.New("sl: already worn")

// WearWearable puts a system wearable on: a link into the Current
// Outfit folder, and a rebake asked for afterwards.
//
// replace removes what is already in the slot.  A body part ignores it
// and always replaces, because two of one body part is not a state an
// avatar can be in.  The names of whatever came off are returned, since
// a replace that says nothing about what it displaced is the complaint
// this shell's wear command was rewritten over.
func (w *Session) WearWearable(ctx context.Context, it *Item, replace bool) ([]string, error) {
	slot, ok := SlotOf(AssetType(it.Type), it.Flags)
	if !ok {
		return nil, fmt.Errorf("sl: %s is not a system wearable", it.Name)
	}
	cof, err := w.CurrentOutfit(ctx)
	if err != nil {
		return nil, err
	}

	worn, err := w.outfitIn(ctx, cof)
	if err != nil {
		return nil, err
	}
	for _, l := range worn {
		if l.Item == it.ID {
			return nil, ErrAlreadyWorn
		}
	}

	var off []string
	if replace || AlwaysReplaces(AssetType(it.Type)) {
		for _, l := range worn {
			if !l.Wearable || l.Slot != slot {
				continue
			}
			if err := w.DeleteItem(ctx, l.Link); err != nil {
				return nil, fmt.Errorf("sl: taking off %s: %w", l.Name, err)
			}
			off = append(off, l.Name)
		}
	}

	if err := w.LinkItem(ctx, cof, it); err != nil {
		return off, err
	}
	// The link is the wearing; the rebake is only what makes it
	// visible.  So a failure here is not a failure to wear, and the
	// message has to say so -- the folder has already been changed, and
	// reporting this as though nothing had happened would send somebody
	// to do it again.
	if err := w.UpdateAppearance(ctx); err != nil {
		return off, fmt.Errorf("%s is worn -- the Current Outfit folder has it -- but the "+
			"region would not rebuild the appearance: %w", it.Name, err)
	}
	return off, nil
}

// RememberWorn puts a link to an item in the Current Outfit folder, if
// there is not one there already.
//
// This is what makes an attachment survive a login.  The folder is the
// record of what an avatar has on and it is the client's to write:
// attaching an object tells the simulator to rez it now and tells the
// folder nothing, so an object put on without this is on the avatar
// until the session ends and then gone.
//
// Already-linked is not an error.  The caller has just put something
// on and the folder agreeing with that is the whole point, so a second
// link would be the only wrong answer.
func (w *Session) RememberWorn(ctx context.Context, it *Item) error {
	cof, err := w.CurrentOutfit(ctx)
	if err != nil {
		return err
	}
	worn, err := w.outfitIn(ctx, cof)
	if err != nil {
		return err
	}
	for _, l := range worn {
		if l.Item == it.ID {
			return nil
		}
	}
	return w.LinkItem(ctx, cof, it)
}

// ForgetWorn takes every link to an item out of the Current Outfit
// folder, and says how many it removed.
//
// Every, not the first: two links to one item is a state a viewer can
// leave behind, and taking a thing off should not need doing twice.
func (w *Session) ForgetWorn(ctx context.Context, item msg.UUID) (int, error) {
	worn, err := w.Outfit(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range worn {
		if l.Item != item {
			continue
		}
		if err := w.DeleteItem(ctx, l.Link); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// TakeOffWearable removes a wearable's link from the Current Outfit
// folder and asks for a rebake.
func (w *Session) TakeOffWearable(ctx context.Context, link msg.UUID) error {
	if err := w.DeleteItem(ctx, link); err != nil {
		return err
	}
	return w.UpdateAppearance(ctx)
}

// UpdateAppearance asks the region to rebuild this avatar's appearance
// from the Current Outfit folder.
//
// The folder's version goes with the request and is required: a POST
// without one is answered "Bad Request: InvalidArguments".  It is read
// fresh on every attempt rather than passed in, because the simulator
// bumps it and uses it to throw away a request it has already answered
// (llappearancemgr.cpp:4343-4352).
//
// Retried, because the first request after a change to the folder is
// quite likely to fail: measured here, a link added and a bake asked
// for immediately came back 500 with an asset_error, and the same
// request a few seconds later came back success with the whole texture
// set.  The baking service is fetching the assets the folder now names
// and has not got them yet.
//
// The viewer retries five times with 2^n - 1 seconds between, which
// runs to nearly a minute.  This stops at three, because it is a person
// waiting at a prompt rather than a viewer painting an avatar, and
// because what was measured came right on the first retry.  A bake that
// needs longer than this will be asked for again by the next thing that
// changes the folder.
func (w *Session) UpdateAppearance(ctx context.Context) error {
	var last error
	for attempt := 0; attempt < bakeAttempts; attempt++ {
		if attempt > 0 {
			// The viewer's own backoff: 1 second, then 3.
			if !nap(ctx, time.Duration(1<<attempt-1)*time.Second) {
				return ctx.Err()
			}
		}
		if last = w.askToBake(ctx); last == nil {
			return nil
		}
		// A refusal that is not the service having a bad moment will
		// not improve by being asked again.
		var ce *CapError
		if errors.As(last, &ce) && ce.Status < 500 {
			return last
		}
	}
	return last
}

// bakeAttempts is how many times a rebake is asked for before giving
// up.  See UpdateAppearance for why it is fewer than the viewer's.
const bakeAttempts = 3

func (w *Session) askToBake(ctx context.Context) error {
	cof, err := w.CurrentOutfit(ctx)
	if err != nil {
		return err
	}
	version, err := w.folderVersion(ctx, cof)
	if err != nil {
		return err
	}
	body, err := llsd.Encode(map[string]any{"cof_version": version})
	if err != nil {
		return err
	}
	_, err = w.capDo(ctx, agent.CapRequest{
		Cap: "UpdateAvatarAppearance", Method: "POST",
		Body: body, Type: "application/llsd+xml",
	})
	return err
}

// nap waits, and says whether it got to the end.
func nap(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// folderVersion is what the grid thinks a folder's version is now.
func (w *Session) folderVersion(ctx context.Context, folder msg.UUID) (int, error) {
	inv := agent.NewInventory(w.invRoot)
	if err := agent.FetchFolder(ctx, w.b, inv, folder); err != nil {
		return 0, fmt.Errorf("sl: reading folder %s: %w", folder, err)
	}
	f, ok := inv.Folder(folder)
	if !ok {
		return 0, fmt.Errorf("sl: folder %s was not in its own listing", folder)
	}
	return f.Version, nil
}
