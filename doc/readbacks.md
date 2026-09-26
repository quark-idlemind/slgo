# Reading back what a message did

Most of what `sl` sends to change something is a UDP message that
nothing answers. A reliable packet is acknowledged, and the
acknowledgement says only that the packet arrived, not that the
simulator did what it asked. So a call in `sl` that sends one of these
looks afterwards for what it asked for, and is an error if it never
shows. This page says where each one looks, and why there.

## Which calls go which way

In `sl/inventory_ops.go`, as the code stands:

| call | sent as | confirmed by |
|---|---|---|
| `CreateFolder` | UDP `CreateInventoryFolder` | the folder appearing in inventory, read over AIS |
| `CopyItem` | UDP `CopyInventoryItem` | a new item of that name in the folder, over AIS |
| `MoveItem`, `MoveFolder` | UDP `MoveInventoryItem`, `MoveInventoryFolder` | the destination listing it, over AIS ([Moves](#moves)) |
| `SetObjectPermissions` | UDP `ObjectPermissions` | the object's properties ([Object permissions](#object-permissions)) |
| `RezFromInventory` | UDP `RezObject` | a new object of ours where it was asked for |
| `ActivateGroup` | UDP `ActivateGroup` | the backend reporting that group active |
| `GiveToAvatar` | UDP `ImprovedInstantMessage` | nothing: it is an offer, and the answer is the recipient's |
| `DeleteItem`, `DeleteFolder`, `PurgeFolder` | AIS `DELETE` | AIS's answer |
| `RenameFolder` | AIS `PATCH` | AIS's answer |
| `SetItem` | AIS `PATCH` | AIS's answer, and then reading the item back |

`Delete`, in `sl/object.go`, sends a UDP `DeRezObject` and is confirmed
by the region's `KillObject` ([Deletes](#deletes)).

AIS answers each request, and says so when it refuses one. What sent
deletes and item edits there rather than over UDP was seen on the grid
before this page was written, and recorded without a date in the
comments of `sl/inventory_ops.go`:

- `MoveInventoryItem` into Trash is accepted and ignored: nothing comes
  back and nothing changes. So is moving an item into Trash over AIS.
  AIS `DELETE` works, and is a real removal: the item did not turn up
  in Trash afterwards.
- `UpdateInventoryItem`, the viewer's message for renaming an item,
  sent with the item's fields, its checksum and a transaction id, is
  accepted and the item does not change.

## Moves

AIS will not move an item. A `PATCH` of an item with a new
`parent_id` answers 400, saying

    Cannot change parent_id.  Use MOVE method.

The probe of inventory names read that off the grid (the README,
"Inventory names, and paths that survive them").

The viewer moves with the UDP messages. Firestorm sends
`MoveInventoryItem` from `LLViewerInventoryItem::updateParentOnServer`
(`newview/llviewerinventory.cpp:599-612`) and `MoveInventoryFolder`
from the category's (`:680-693`), and waits for nothing: it changes
its own copy of the inventory as it sends, and goes on
(`newview/llinventorymodel.cpp:1960-1969` and `:2006-2015`). Linden
Lab's viewer (`secondlife/viewer`, `develop`) does the same, and its
client for the inventory service has no move operation at all
(Firestorm's copy of it: `newview/llaisapi.h:52-67`). Both checked on
2026-09-26.

So `MoveItem` and `MoveFolder` send the viewer's message, and then list
the destination folder over AIS once a second until the item or folder
is in it -- under its new name, if `MoveItem` was given one -- for up
to fifteen seconds. One that never arrives is an error saying so,
wrapping `sl.ErrTimeout`; a move into Trash is one of those. An item
that arrives under its old name is an error saying what it is called.

The fifteen seconds is not a measurement. How long a move takes to
show over AIS has not been measured; fifteen seconds is the bound
`SetItem` already used for the same kind of read.

## Object permissions

Nothing answers `ObjectPermissions`. The masks come back in
`ObjectProperties`, which the region sends for a selected object, so
`SetObjectPermissions` selects the object and reads its properties
until the mask it set reads as sent, for up to fifteen seconds.

It reads more than once because a read straight after a write can
overtake it. That was measured for descriptions: building one prim and
reading its properties at once reported an empty description (see
`SetDescription` in `sl/build.go`). It has not been seen for
permissions, and is inferred to be possible for the same reason.

A mask may never read as sent, because the permission rules narrow a
request rather than refuse it. The viewer's copy of the rules
(`llinventory/llpermissions.cpp`, `LLPermissions::fix`, `:155-173`)
keeps the owner's mask within the base, the group's and everyone's
within the owner's, and the next owner's within the base; never gives
everyone modify; and, where the base has no transfer, gives neither the
group nor everyone copy. Setting the next owner's mask
(`setNextOwnerBits`, `:446-475`) turns transfer on whenever copy is
off. The simulator's own code is not public, and that it applies the
same rules is inferred. A request they narrow is reported as an error
saying what the mask allows, not as done, and `slsh perms` prints a
line only for a mask that read back as asked.

## Deletes

Nothing answers `DeRezObject` either. What says an object has gone is
the `KillObject` the region sends for it, naming its local id.

Measured on 2026-09-25 and 2026-09-26: all 57 deletes that probes
checked were followed by their `KillObject`, each within about a
second.

So `Delete` waits for that kill, for up to ten seconds, which is a
margin over what was measured and not a measurement itself. One that
does not come is an error saying the delete was asked for and not
confirmed, wrapping `sl.ErrTimeout`: the object may still be there, or
may have gone without this session hearing. `slrun`'s tidying up says
the first, and `slsh rez` names what it could not confirm.

Only a kill heard after the derez counts. One already recorded for the
same local id is forgotten before the derez goes, since it was not an
answer to it, and the wait counts nothing heard after the avatar has
changed region, where the same number names another object (see
[local ids](local-ids.md)).

Deleting a root takes its linkset with it. The viewer deletes a
selection by sending only its roots (Firestorm,
`newview/llselectmgr.cpp:4432-4437`, `SEND_ONLY_ROOTS`), and that a
root's derez takes the prims linked to it is inferred from that; it has
not been measured here. `slsh rez`, taking away a build that failed,
sends its root and any prim not linked to it, and no others, for the
same reason: a child sent after its root had gone would never be
killed again, and would be reported as not confirmed gone.
