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
// AgentIsNowWearing (Low 383), which the protocol alone makes look like
// the way to do this, is dead: the viewer sends it only for
// compatibility, with four fixed ids that mean nothing, and a live
// simulator answered a question about this avatar's wearables with
// exactly those four.
// Why: doc/outfit.md#the-message-that-looks-like-the-answer
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
const FolderCurrentOutfit = agent.FolderCurrentOutfit

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

	// Parent is the folder the thing at the far end lives in, which is
	// where to fetch the whole item from when one is needed.
	Parent msg.UUID

	// Found is whether the thing at the far end was reached at all.
	// A link outlives what it points at, and one that has been left
	// behind says nothing about its kind.
	Found bool

	// InvType is the inventory type the link itself carries, which is
	// its target's: an object link says object (InvTypeObject) without
	// the target being found.  Outfit walks only a few folders deep, so
	// a thing kept deeper than that is not Found, and this is all there
	// is to tell an attachment's link from a wearable's.
	InvType int
}

// InvTypeObject is the inventory type of an object, an attachment's
// item (IT_OBJECT, llinventorytype.h).
const InvTypeObject = 6

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
		l := OutfitLink{Link: e.ID, Item: e.Asset, Name: e.Name, InvType: e.InvType}
		if to, ok := byID[e.Asset]; ok {
			l.Found, l.Folder, l.Parent = true, to.Folder, to.Parent
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

// Putting an outfit back on.
//
// The simulator puts most of an avatar's attachments back by itself at
// login, but where a point held several it was measured to put back
// only one of them.  A viewer covers the gap by putting on whatever the
// Current Outfit folder names and is not on
// (LLAppearanceMgr::updateAppearanceFromCOF), and this is the same
// catching up.
// Why: doc/outfit.md#putting-an-outfit-back-on
//
// # Why it adds rather than replaces
//
// Several attachments on one point are an ordinary outfit, and putting
// them on with replace makes each knock the last one off.  So this adds,
// as the viewer does down the same path (userAttachMultipleAttachments,
// llagentwearables.cpp:1610).  What that risks is a second copy of
// something whose description was missing, and a duplicate is visible
// and costs a detach where a lost garment is neither: so it is reported
// rather than prevented -- after the wait, anything the region
// describes twice is named, because nothing else here will mention it.
// Why: doc/outfit.md#why-it-adds-rather-than-replaces
//
// # Why it asks for everything at once
//
// Waiting for each in turn is forty seconds apiece, and a dozen
// attachments is eight minutes of waiting for confirmations that were
// all going to arrive together.  So the requests go out in a batch and
// the waiting happens once, which is the shape a viewer uses too.

// OutfitReport is what a restore did, by name, so that a caller can say
// what happened rather than that something did.
type OutfitReport struct {
	// Already were on before this started, Worn went on because of it,
	// Missing were asked for and never confirmed, and Doubled are worn
	// more than once.
	//
	// Missing is not the same as failed.  The confirmation is the
	// region describing the new object, and that description is the
	// thing most likely to be lost -- so a name here means "asked for,
	// and nothing came back about it", which is a weaker statement
	// than "not worn".
	Already []string
	Worn    []string
	Missing []string

	// Undescribed is how many attachments the simulator lists that the
	// region has still not described when the restore finishes: on,
	// by the simulator's account, and not matched to anything in the
	// outfit.  HUDs are never in that list, so this counts body
	// attachments only.  Unknown says the list has not been heard or
	// describes an outfit that has since changed, so there was nothing
	// to count against.
	Undescribed int
	Unknown     bool

	// Unbaked is why the bake after putting things on failed, if it
	// did.  What went on is on; the simulator's list will not show it
	// until the next bake.
	Unbaked error

	// Doubled is what the region describes more than one attachment
	// of.  Adding is what makes that possible -- see the head of this
	// section -- and it is named here because it is a thing to go and
	// undo, and nothing else will mention it: two attachments from one
	// item agree in every field a person could name one by.
	Doubled []string
}

// RestoreOutfit puts on everything in the Current Outfit folder that is
// not on already.
//
// Only the attachments.  Clothing and body parts are not attached and
// do not go missing at a login: the folder is what the baking service
// reads, and it has been read by the time the avatar is standing up.
//
// A zero timeout is DefaultRestoreWait.
func (w *Session) RestoreOutfit(ctx context.Context, timeout time.Duration) (*OutfitReport, error) {
	if timeout == 0 {
		timeout = DefaultRestoreWait
	}
	outfit, err := w.Outfit(ctx)
	if err != nil {
		return nil, err
	}

	// The simulator puts most of an outfit back by itself at login, and
	// the region describes each piece as it arrives.  While the
	// simulator lists attachments nothing has described, something
	// named in the outfit and not described may be one of them, so
	// this waits for the descriptions before deciding anything is
	// missing -- the wait a viewer spends drawing the avatar as a
	// cloud.  Bounded: a description that was lost is not sent again,
	// and the grid has been seen not to answer a restore at all.
	on, acct, err := w.takeStock(ctx)
	if err != nil {
		return nil, err
	}
	// Without a list baked from the outfit as it is now there is
	// nothing to wait on, and at a login there is none: measured on
	// Agni, an avatar arriving was sent the others' appearances at
	// once and its own not at all in the twenty seconds before
	// anything asked.  A viewer asks for a bake as soon as the outfit
	// folder has loaded, and the appearance that answers it lists
	// what is attached at that moment -- so this asks too.
	if !acct.Known || !acct.Current {
		if w.bakeAndHear(ctx, min(timeout, bakeHearWait)) {
			if on, acct, err = w.takeStock(ctx); err != nil {
				return nil, err
			}
		}
	}
	deadline := time.Now().Add(timeout)
	for acct.Waiting() && time.Now().Before(deadline) {
		if !nap(ctx, restorePoll) {
			break
		}
		if on, acct, err = w.takeStock(ctx); err != nil {
			return nil, err
		}
	}

	report := &OutfitReport{}
	defer func() {
		report.Undescribed, report.Unknown = len(acct.Undescribed), !acct.Known || !acct.Current
	}()
	var asked []OutfitLink
	for _, l := range outfit {
		if l.Folder || !l.Found || l.Kind != AssetObject {
			continue
		}
		if on[l.Item] > 0 {
			report.Already = append(report.Already, l.Name)
			continue
		}
		it, err := w.itemIn(ctx, l.Parent, l.Item)
		if err != nil {
			report.Missing = append(report.Missing, l.Name)
			continue
		}
		// The point the object carries, with the add bit: see the head
		// of this section for why adding is the one to have.
		if err := w.AskToWear(ctx, it, AttachAdd); err != nil {
			return report, err
		}
		asked = append(asked, l)
	}
	if len(asked) == 0 {
		return report, nil
	}

	// One wait for the lot.  Ended early when everything asked for has
	// been described, which is the ordinary case and takes a second or
	// two.
	deadline = time.Now().Add(timeout)
	for {
		now, a, err := w.takeStock(ctx)
		if err != nil {
			break
		}
		on, acct = now, a
		done := true
		for _, l := range asked {
			if on[l.Item] == 0 {
				done = false
				break
			}
		}
		if done || !time.Now().Before(deadline) {
			break
		}
		if !nap(ctx, restorePoll) {
			break
		}
	}
	for _, l := range asked {
		if on[l.Item] > 0 {
			report.Worn = append(report.Worn, l.Name)
		} else {
			report.Missing = append(report.Missing, l.Name)
		}
	}
	// Baked, as a viewer bakes once it has put an outfit back on, so
	// that the simulator's list of what is worn includes what just went
	// on.  After the waiting rather than before: a bake lists what is
	// attached when it runs.
	report.Unbaked = w.UpdateAppearance(ctx)
	for _, l := range outfit {
		if on[l.Item] > 1 {
			report.Doubled = append(report.Doubled, l.Name)
		}
	}
	return report, nil
}

// DefaultRestoreWait is how long a restore gives the region to describe
// what it was asked to rez, and restorePoll how often it looks.
//
// The value is not promised and may change in any release: refer to it by name. (restorePoll is not exported.)
const (
	DefaultRestoreWait = 20 * time.Second
	restorePoll        = time.Second
)

// bakeHearWait bounds the wait for the appearance a bake produces.
// Measured on Agni it arrives within a second of the request.
const bakeHearWait = 5 * time.Second

// bakeAndHear asks for a bake and waits for the appearance that answers
// it, saying whether one arrived.  A failure of either is not an error
// to the caller: the list is a help to a restore, not a condition of it.
func (w *Session) bakeAndHear(ctx context.Context, wait time.Duration) bool {
	before := time.Time{}
	if list, err := w.SimAttachments(ctx, msg.UUID{}); err == nil && list != nil {
		before = list.Heard
	}
	if w.UpdateAppearance(ctx) != nil {
		return false
	}
	deadline := time.Now().Add(wait)
	for {
		if list, err := w.SimAttachments(ctx, msg.UUID{}); err == nil && list != nil &&
			list.Heard.After(before) {
			return true
		}
		if !time.Now().Before(deadline) || !nap(ctx, bakeHearPoll) {
			return false
		}
	}
}

const bakeHearPoll = 250 * time.Millisecond

// takeStock is how many attachments this avatar has from each
// inventory item, as the region has described them, and the
// simulator's account set against that -- read together so that the
// two describe the same moment.
//
// Counted rather than merely noted, because more than one is a state
// worth reporting: it is what adding can leave behind, and two
// attachments from one item agree in every field a person could name
// one by.
func (w *Session) takeStock(ctx context.Context) (map[msg.UUID]int, *AttachmentAccount, error) {
	worn, err := w.WornObjects(ctx)
	if err != nil {
		return nil, nil, err
	}
	on := make(map[msg.UUID]int, len(worn))
	for _, a := range worn {
		on[a.Item]++
	}
	acct, err := w.AccountForAttachments(ctx, worn)
	if err != nil {
		return nil, nil, err
	}
	return on, acct, nil
}

// itemIn is one item out of a folder, by id.
//
// By id and not by name: a folder may hold several things of one name,
// and what is wanted here is the one the outfit points at.
func (w *Session) itemIn(ctx context.Context, folder, item msg.UUID) (*Item, error) {
	items, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		if it.ID == item {
			return it, nil
		}
	}
	return nil, fmt.Errorf("sl: item %s is not in folder %s", item, folder)
}
