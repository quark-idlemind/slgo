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

No other dialog was tried. In particular an inventory offer (dialog 4),
which `accept-inventory = trusted` also matches by name, was not.

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

## What slgo does with them

Until 2026-09-26 slgo called dialog 19 `DialogBusyAutoResponse` and
counted it as conversation. Now:

- `IM.Spoken` and `IM.Conversation` are false for 19
  (`DialogFromTask`) and for 20 (`DialogDoNotDisturbAutoResponse`).
- The session learns no name from 19, as it learns none from a group
  invitation, whose name is not the group's either.
- slbotd ignores a script's message entirely: no command, no model and
  no report of trouble. It logs
  `ignored an instant message from the object "NAME", owned by OWNER`.
- slsh prints one as `< [Object] NAME: TEXT` and opens no
  conversation. A do-not-disturb auto response is a notice,
  `* do not disturb auto response from NAME: TEXT`.
- `examples/greeter` does not answer one.

## Why matching a name is still safe

What reaches `Trusts` from a remark is a message for which
`IM.Conversation` is true: dialog 0 or 1, with a sender id, not from a
group, and not sent by this avatar. On dialog 0 the name is the one the
simulator wrote, as measured above. Dialog 1 with an invented name was
not delivered. A script's message, whose name is its object's, no
longer gets there.

The name the simulator writes is the sender's own. "Two things to know
before trusting it" in `doc/guide.md` says that is the legacy name and
not a display name, which anybody can set to anything. That is not part
of what is recorded here.

The same holds for the `chat` and `chat-bot` lists, which also match
by id or by name.
