# Wearing, and putting an outfit back on

`sl/wearable.go` wears system wearables through the Current Outfit
folder and puts an outfit's attachments back after a login, and
`sl/simattach.go` reads the simulator's own list of what an avatar is
wearing. Their comments say what the code does. This page is why: what
the viewer's source says, and what was measured on Agni.

## The message that looks like the answer

`AgentIsNowWearing` (Low 383) is what a viewer used to send to wear a
wearable, and reading the protocol alone would make it the obvious way
to do this. It is dead. The viewer that still sends it says so where it
does:

	// We no longer need this message in the current viewer, but send
	// it for now to maintain compatibility with release viewers.
	void LLAgentWearables::sendDummyAgentWearablesUpdate()
	  (llagentwearables.cpp:927-929)

and what it sends is four fixed ids that mean nothing at all -- "4
standardized nonsense item ids (same as returned by the modified sim,
not that it especially matters)". Asking a live simulator for this
avatar's wearables came back with exactly those four constants, which
is how the deadness was confirmed rather than assumed.

## The simulator's list

The list at the end of `AvatarAppearance` is the simulator's own
account of what an avatar is wearing: object ids and points, nothing
else.

Measured on Agni, one avatar wearing ten attachments: the list held
six, one per body attachment, and their ids were the attached objects'
-- the same ids `WornObjects` reports -- not the inventory items'. The
four HUDs were not in it, not even in the copy the avatar was sent
about itself. So the list can confirm a body attachment and count what
is missing a description, and it can say nothing about HUDs.

When it is sent was measured on Agni, and differs by who is being told.
The avatars around one that arrives hear its list at once. The avatar
itself does not: one logging in was sent its own list not at all in the
twenty seconds before anything asked for a bake, and was sent it within
a second of asking. So the way to have a list is to ask for a bake,
which is what a viewer does as soon as the outfit folder has loaded,
and again after every change to it, attachments included.

## A bake after every change to the folder

A viewer asks for a bake after it changes the Current Outfit folder,
whether the change put an attachment on or took one off. This is read
from Firestorm's source, not measured:

- Putting one on, the link goes into the folder with a callback that
  asks for the bake once it is there (`LLRegisterAttachmentCallback`,
  llattachmentsmgr.cpp:50-58 and 331-339, and the destructor of its
  parent, llappearancemgr.cpp:568-575).
- Taking one off, from inventory or from the object in world
  (llviewermenu.cpp:9442 and 9573), `removeItemsFromAvatar` takes the
  link out with a callback that runs `updateAppearanceFromCOF` once it
  has gone (llappearancemgr.cpp:4742-4744 and 543-560), and that asks
  for the bake (llappearancemgr.cpp:2892-2895).

slsh does the same on all three of its paths: `wear`, `detach`, and a
`detach` of something only the folder knows about (`detachFromOutfit`
in `cmd/slsh/wear.go`). That last went without a bake, on the reasoning
that an attachment is not baked into the avatar. That is true of the
textures and beside the point: the bake is also what has the simulator
send the avatar its own list of what is worn, as measured above, and
`worn` and `dress` check against that list.

## Putting an outfit back on

The simulator puts most of an avatar's attachments back by itself at
login, but not all of them. Measured on Agni over eight logins of two
avatars: wherever a point held several attachments, exactly one of
them came back. One avatar had three on its chest and got one of them
back each time -- a different one on different logins, as the
simulator's own attachment list confirmed before anything else was put
on. The other wore twelve HUDs on the eight HUD points and got eight
back, one on each, the same four missing every time. Every point with a
single attachment was restored every time. So it looks as though the
simulator restores one attachment per point, as it did when a point
could hold only one; that is inferred from the pattern, not something
the grid says.

A viewer covers the gap. Once the Current Outfit folder has loaded it
puts on whatever the folder names and is not on
(`LLAppearanceMgr::updateAppearanceFromCOF`), and nobody sees that
happen. Nothing here did, so an avatar came back from a login missing
part of its outfit and stayed that way. `RestoreOutfit` is the same
catching up.

### Why it adds rather than replaces

It was written the other way round first, on the reasoning that
replacing is self-limiting: `RestoreOutfit` cannot be sure what is
already on, since what says an attachment is worn is the region's
description of it and that description can be missing, so a restore
that added might put a second copy of something it could not see.

That reasoning is right about the risk and wrong about the cost. A
point holds more than one attachment and an ordinary outfit uses that
-- a mesh body, a dress and a pair of arms all sit on the chest -- so
restoring with replace puts them on one after another and each knocks
the last one off. Measured, and seen: an avatar restored that way came
back in her boots and her hair and nothing else, because the dress and
the arms had fought over one point.

A duplicate is visible and costs a detach. A garment silently lost is
neither. So it adds, which is also what the viewer does down the same
path -- `addAttachmentRequest(item, 0, add=true)` from
`userAttachMultipleAttachments`, `llagentwearables.cpp:1610`.

## An attachment that takes itself off

A worn attachment can take itself off: a script calls
`llDetachFromAvatar`. The item goes back into inventory, but its link
stays in the Current Outfit folder, and the folder is what a viewer
wears at the next login, so the object came back then. Seen on Agni on
2026-10-03 with a product box that handed over a folder and detached
itself.

### What the viewer does

Whenever an attachment object leaves the avatar, however it left,
`LLVOAvatarSelf::detachObject` calls
`LLAppearanceMgr::unregisterAttachment`, which takes the item's links
out of the folder (`removeCOFItemLinks`) if `mAttachmentInvLinkEnabled`
is set (Firestorm 885631b93a, llvoavatarself.cpp:1742-1771 and
llappearancemgr.cpp:4904-4917). The flag is false until inventory has
been fetched at login (llstartup.cpp:643 and 4067,
llagentwearablesfetch.cpp:146) and is set false again at shutdown, so
that destroying objects on the way out does not strip the outfit
(llappviewer.cpp:6840). The viewer never kills its own avatar
(`LLViewerObjectList::killObject`, llviewerobjectlist.cpp:1606-1611).
So a viewer cleans up after a self-detach; a daemon with no viewer
attached did not.

### What was measured

2026-10-03, the test avatar on a second daemon, an observer logging
every message:

- A self-detach: `KillObject` for the attachment's local id 7.2 s after
  the wear; then `SaveAssetIntoInventory`, naming the item
  (`InventoryData.ItemID`), 86 ms later; then a `BulkUpdateInventory`
  event 46 ms after that. The folder's link stayed. `WornFrom` still
  reported the item worn 15 s afterwards, because the session's record
  of worn objects ignored `KillObject`.
- A teleport to another region and back, wearing a box: no `KillObject`
  for it and no `SaveAssetIntoInventory`. It was not described on
  arrival, and was described again under a new local id about 0.2 s
  after `TeleportFinish`. The link must stay, and is not touched.
- A walk over a border and back, with neighbour circuits open, wearing
  one box on the chest and one on a HUD point: no `KillObject` for
  either and no `SaveAssetIntoInventory`, either way. The new region
  described both under new local ids about 0.1 s after
  `AgentMovementComplete`, before `CrossedRegion` reached the client.
  Both links stayed through 15 s in each region.
- Our own take-off (`DetachAttachmentIntoInv`): `KillObject` and
  `SaveAssetIntoInventory`, as for a self-detach.
- A logout wearing a box: no `KillObject` or `SaveAssetIntoInventory`
  reached a client through the daemon before the session ended. Whether
  the agent itself hears one at logout is not known.

### The signal

`SaveAssetIntoInventory` naming an item is the region saying that
item's attachment went back into inventory. It came for a self-detach
and for a take-off and never for a teleport or a walk over a border.
`KillObject` alone is not used: it names a local id, not an item, and
read alone it is the same message as any object leaving view, where the
save names the item and says where it went.

This is not the viewer's trigger. The viewer removes the links when its
own avatar object loses an attachment (`detachObject`, above), and its
handler for `SaveAssetIntoInventory` only refreshes the item and does
nothing with outfit links (`LLInventoryModel::processSaveAssetIntoInventory`,
Firestorm 885631b93a, llinventorymodel.cpp:4133). The agent keeps no
avatar object with attachments of its own to lose, and the save is the
one message that names the item, so slgo acts on that.

### What slgo does

In the agent (`agent/selfdetach.go`), so once per avatar in the daemon
and not once per attached client, on a `SaveAssetIntoInventory`:

1. nothing, if the session is logging out or over (`Agent.Logout` has
   begun, the reply came, or the session ended). That is the viewer's
   shutdown guard, and covers a save the agent hears at logout;
2. nothing, if an attachment from that item is still described in the
   object store, which covers a re-wear racing the save. The measured
   order was kill first, then save, so the store has already dropped a
   detached object when the save comes;
3. otherwise, off the dispatch goroutine, it reads the Current Outfit
   folder over AIS (found by preferred type 46 among the root's
   children) and deletes every link whose `linked_id` is the item. A
   link that answers 404 is already gone, which is success: a viewer
   attached through the daemon, or `detach` in slsh, may have removed
   it first. One log line says the item id and how many links went, or
   why nothing was done; the agent logs no item names.

`sl.Session` now drops a worn attachment from its record when the
region kills its local id, so `WornFrom` and `Attachments` stop
reporting it. A teleport or a crossing sends no kill, and the entry is
replaced when the attachment is described again under its new local id.

### What is not known

Whether the agent hears a `SaveAssetIntoInventory` at logout; the guard
is there so that the answer does not matter.

Measured again with the fix, the same day and the same way: the
self-detached box's link was gone within a few seconds, and the box worn
through a teleport away and back kept its link throughout, as did the
two boxes walked over a border and back (above, measured with the fix
running and logging nothing for them until they were taken off).  A take-off
asked for by slgo itself is saved the same way, so its link goes by
the same path; removing it a second time finds it already gone.
