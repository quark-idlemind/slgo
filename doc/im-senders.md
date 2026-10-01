# Who an instant message is from

An `ImprovedInstantMessage` says who sent it in two fields:
`AgentData.AgentID`, which `sl.IM` calls `From`, and
`MessageBlock.FromAgentName`, which it calls `FromName`. slbotd obeys
somebody when either one matches a `trusted` line (`Config.Trusts` in
`cmd/slbotd/config.go`), so what the grid puts in those two fields is
the whole question of who can drive an avatar.

Nothing in slgo rewrites either one on the way in. agent, slgod, the
viewer frontend and the client pass the message on as it came, and
`sl` reads `FromAgentName` as it stands (`instantMessage` in
`sl/im.go`). That is from reading the code. What the grid puts there
is below, and it was measured.

## An avatar's message

Measured on Agni on 2026-09-25. One avatar's client sent ordinary
instant messages (dialog 0) to another avatar with `FromAgentName` set
to three different things:

- its own name;
- an invented name;
- the receiving avatar's own name.

All three arrived carrying the sender's own name and id. The simulator
replaces the name on an avatar's message, whatever the client wrote
there. Only three messages were sent; that it does so every time is
inferred from them.

Messages of dialog 1 (a message box) and dialog 19, sent by an avatar
with an invented name, were not delivered at all: nothing arrived
within 30 seconds.

An inventory offer (dialog 4), which `accept-inventory = trusted` also
matches by name, was measured on Agni on 2026-09-26. One avatar offered
a notecard of its own to another twice: once with its own name in
`FromAgentName` and once with an invented one. Both offers arrived
carrying the sender's own name and id, and both were declined. The
simulator replaces the name on an offer as it does on a message. Only
two offers were sent; that it does so every time is inferred from them.

No other dialog was tried.

## An object's message

Measured on Agni on 2026-09-26: one `llInstantMessage(llGetOwner(), ...)`
from a prim, as the wire carried it.

| Field | What it held |
|---|---|
| `MessageBlock.Dialog` | 19 |
| `MessageBlock.Offline` | 0 |
| `MessageBlock.FromGroup` | false |
| `AgentData.AgentID` | the object's **owner**, which here was the avatar that received it; not the object |
| `MessageBlock.ID` | the object's key |
| `MessageBlock.ToAgentID` | the owner |
| `MessageBlock.FromAgentName` | the object's name |
| `MessageBlock.BinaryBucket` | where the object was, as text: `Region/x/y/z`, which for an invented region would read `Pelmar Reach/128/64/22` |
| `MessageBlock.RegionID` | set |
| `MessageBlock.ParentEstateID` | 1 |
| `MessageBlock.Position` | all zeros |

So on a script's message `IM.From` is the owner's id, `IM.ID` is the
object, and `IM.FromName` is whatever the object is called. The
viewer's source agrees: 19 is `IM_FROM_TASK` in `EInstantMessage`
(`indra/llmessage/llinstantmessage.h`), and `llimprocessing.cpp` takes
the id field for the object and the agent id for its owner. 20 is
`IM_DO_NOT_DISTURB_AUTO_RESPONSE`, the reply a viewer sends by itself
to a message that reaches somebody set to do not disturb.

An object can be given any name, a trusted person's included, and an
object a trusted person owns carries that person's id. Either one
passes `Trusts`. The name also used to be learned: the session filed
the object's name under the owner's id, and printed it wherever the
owner was named afterwards.

Read from the viewer's source and not measured: an object deeded to a
group sends with `FromGroup` set and the group in `AgentID`, and a
message the grid itself sends this way is named "Second Life" and
carries no region and a zero position.

Two more dialogs come from an object, and by the viewer's source carry
the object's name in the same way. Neither was measured.

- 9, `IM_TASK_INVENTORY_OFFERED`, an object giving an item. Its id
  field is the transaction (`llinstantmessage.h:81-84`).
  `inventory_offer_handler` shows the name as the object's
  (`OBJECTFROMNAME`), links the agent id as its owner, or as its group
  when `FromGroup` is set, and uses the notice for one's own objects
  when that id is the viewer's avatar (`llimprocessing.cpp:360-397`).
- 31, `IM_FROM_TASK_AS_ALERT`, a script's message shown as an alert.
  The viewer shows the name as the sender's (`llimprocessing.cpp:1941-1951`)
  and does nothing with the agent id but check it against the mute
  list. That it is the owner's is inferred from `llinstantmessage.h:141-144`,
  which calls 31 "Similar to IM_FROM_TASK".

`clean_name_from_im`, which strips "Resident" only from a name that
came from a user rather than a script, leaves out 9, 19 and 31 alike
(`llimprocessing.cpp:89-139`). Line numbers are Firestorm's.

## An object's give

Measured on Agni on 2026-10-01: one `llGiveInventory(llGetOwner(), ...)`
of a script from a prim, received by its owner.

| Field | What it held |
|---|---|
| `IM.Dialog` | 9 |
| `IM.From` | the object's **owner**, which here was the avatar that received it |
| `IM.FromName` | the object's name |
| `IM.ID` | a transaction id: neither the object's key nor the item's |
| `IM.Text` | the item's name in single quotes, two spaces, and where the object was, in parentheses: `'Example Box'  ( http://slurl.com/secondlife/Testville/128/64/22 )` |
| `IM.Bucket` | one byte, the asset type: 10, a script |

So the item's name is not the whole of `Text`, and the offer does not
carry the item's id.

What answered it, each tried on its own give and the inventory read
again afterwards:

- Nothing: no item in 9 seconds.
- `DialogInventoryAccepted` (5), which is what `AcceptInventoryOffer`
  sends, to `From` and quoting the transaction: no item in 10 seconds.
- `DialogTaskInventoryAccepted` (10), to `From`, quoting the
  transaction, with the destination folder as the bucket: the item was
  in that folder within 9 seconds. `AcceptTaskInventoryOffer` sends it.

A second give, measured the same day from an object another avatar
owned: `From` was that owner, not the receiver, and dialog 10 addressed
to that `From`, with the receiver's Scripts folder as the bucket, put
the item in the receiver's Scripts folder.

Not measured: whether 10 accepts any folder or only the default one for
the item's type (the Scripts folder was used both times).

## What slgo does with them

Until 2026-09-26 slgo called dialog 19 `DialogBusyAutoResponse` and
counted it as conversation. Now:

- `IM.Spoken` and `IM.Conversation` are false for 19
  (`DialogFromTask`) and for 20 (`DialogDoNotDisturbAutoResponse`).
- The session learns no name from 19, as it learns none from a group
  invitation, whose name is not the group's either.
- It learns none from 9 (`DialogTaskInventoryOffered`) or 31
  (`DialogFromTaskAsAlert`), which are treated as 19. That they carry
  the owner's id and the object's name is read from the viewer's
  source, above, and not measured.
- slbotd ignores a script's message entirely: no command, no model and
  no report of trouble. It logs
  `ignored an instant message from [Object] NAME, owned by OWNER`, the
  owner named from what the session already knew of that id and
  labelled `[Group]` when the object is a group's.
- slsh prints one as `< [Object] NAME: TEXT` and opens no
  conversation. A do-not-disturb auto response is a notice,
  `* do not disturb auto response from NAME: TEXT`.
- `examples/greeter` does not answer one.

## Labelling a sender

An object's name can be anybody's, so slsh and slbotd never print one
as though a person had said it. `sl.Sender` says what the name on a
message names -- a person, an object, a group or the grid -- and
`Sender.Label` is the one way either program prints a name with it:
`[Object] NAME`, `[Group] NAME`, `[Grid] NAME`, and a person's name
bare. The kinds are the viewer's, read from its source and not
measured beyond dialog 19 above:

- 9, 19 and 31 are an object's. 19 signed "Second Life", the name the
  grid signs with (`SYSTEM_FROM`), and sent from no region and no
  position is the grid's own (`llimprocessing.cpp:1739-1742`); an
  object called that and sent from somewhere is still an object.
- 28, a web page to open, is the grid's
  (`llimprocessing.cpp:2289`), and so is anything else with no sender
  id or signed "Second Life" (`llimprocessing.cpp:900`). So a message
  signed that way is not conversation: nothing answers it and slsh opens
  no conversation with it.
- Anything else sent as a group (`FromGroup`) is the group's, except a
  group invitation and a group notice, which carry the group's id with
  the name of whoever invited or posted
  (`llimprocessing.cpp:1341-1356`, `1477`).
- Local chat is labelled by its source type: an avatar's is a person's,
  an object's an object's, and the simulator's own is the grid's.

What is labelled, in slsh: chat (`< [Local] [Object] NAME: TEXT`), an
object's message, every other instant message that is not a person's
(`* object alert from [Object] NAME: TEXT`), a group notice's poster
when it is not a person, a script's dialog and permission request as
they arrive, in `waiting` and in what answering one says
(`pressed "on" on [Object] NAME`), and the line saying one was
forgotten unanswered. A group invitation that names nobody is listed
from `[Group]` and its id. In slbotd: everything logged about an
instant message that is not a person's, through `whoSaid`.

Checked and left as they were, because the name is a person's: a
conversation, a friendship offer, a teleport offer or request, an
inventory offer (dialog 4; an object's is 9), a group invitation's
inviter and a group notice's poster, and slbotd's lists of what is
waiting. A permission request's owner is printed as the simulator names
it (`ObjectOwner`), which may be a group's name; the viewer prints it
the same way, and nothing in the message says which it is. slgo shows
no `LoadURL` and no simulator alert as they arrive.

`TestNoObjectNameIsPrintedBare` in `sl` refuses an object's name used
in `sl`, slsh or slbotd outside `Label`, and
`TestNothingButAPersonIsPrintedAsOne` (slsh) and
`TestNothingButAPersonIsLoggedAsOne` (slbotd) send every dialog number
as an object, a group and the grid and refuse a line with the name
bare.

## Why matching a name is still safe

What reaches `Trusts` from a remark is a message for which
`IM.Conversation` is true: dialog 0 or 1, with a sender id, not from a
group, not signed "Second Life", and not sent by this avatar. On dialog 0 the name is the one the
simulator wrote, as measured above. Dialog 1 with an invented name was
not delivered. A script's message, whose name is its object's, no
longer gets there. An inventory offer's name, which
`accept-inventory = trusted` matches, is the simulator's too, as
measured above.

The name the simulator writes is the sender's own. "Two things to know
before trusting it" in `doc/guide.md` says that is the legacy name and
not a display name, which anybody can set to anything. That is not part
of what is recorded here.

The same holds for the `chat` and `chat-bot` lists, which also match
by id or by name.
