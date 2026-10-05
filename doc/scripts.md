# Putting a script in, and asking whether it runs

`sl.Run` and `sl.InstallScript` put a script into an object through the
`UpdateScriptTask` capability, and `sl.ScriptRunning` asks whether one
is running. The comments in `sl/inventory.go` and `sl/script.go` say
what the code does. This page is why: what was measured, and what the
viewer's source says.

## Asking the first half of an upload again

An upload is two steps: the first half describes what is about to be
written and is told where to write it, and the second half writes it to
that URL. `describeUpload` asks the first half a second time when the
far end answers with a server error, and nothing else is asked again.

Only this half, and only a 5xx. The first half writes nothing: it hands
over a description and is given a URL, so asking again cannot install
anything twice, cannot make a second inventory item, and cannot be
charged for twice. That is what makes it safe to retry without knowing
which capability it was for, and none of it is true of the second half,
where an answer that went missing may have been an upload that landed.

Measured on Agni in August 2026: `slrun` running thirty scripts at once,
with the objects cleared first, produced one or two of these per run --

	sl: UpdateScriptTask: status 500: <html> ... 'method': 'handle_request'

-- a Python stack trace out of Linden Lab's own web service. They were
all the first half: the name in the error is the capability, and the
second half names the uploader URL instead. So nothing had been written
when they happened, and a run of thirty scripts was thrown away over a
hiccup that could have been asked again.

Once, and then the error stands. A far end that is still failing a
second later is having more than a moment, and a program that kept
asking would be adding to whatever is wrong.

## Dropping a script into an object

`UpdateTaskInventory` puts an inventory item into an object, and for a
script it puts one in that does not run. Measured on Agni on 5 October
2026: three scripts the avatar may not modify, dropped into a box with
`slsh drop`, never ran, and `slsh start` on them got "no answer", which
is `SetScriptRunning` having nothing compiled to start. A script with
full permissions ran only once it was recreated from its text with `slsh
new --in`, which uploads the source through `UpdateScriptTask`. That
way in needs the source, and an avatar cannot read the source of a
script it may not modify, so for those there was no way in that ran.

The viewer does not send `UpdateTaskInventory` for a script. In
Firestorm 885631b93a there are two ways to drop one in, and both end in
`LLToolDragAndDrop::dropScript` (`lltooldraganddrop.cpp:1828-1880`),
which calls `LLViewerObject::saveScript` (`llviewerobject.cpp:2886-2922`)
on the prim chosen:

- the edit window's Contents tab, which drops into the prim selected,
  child or root (`llpanelobjectinventory.cpp:837-856`); and
- a drag onto the object in the world, `dad3dRezScript`
  (`lltooldraganddrop.cpp:2644-2679`), which moves a child to its root
  (2667-2676), since there the mouse chose the prim.

`saveScript` sends `RezScript` (Low 304, zerocoded):

- `AgentData`: the agent, the session, and `GroupID`, the agent's active
  group.
- `UpdateBlock`: `ObjectLocalID`, the prim it goes into, and `Enabled`,
  true, and false only with Control held (`lltooldraganddrop.cpp:2665`,
  `llpanelobjectinventory.cpp:855`).
- `InventoryBlock`: the item as `LLInventoryItem::packMessage` packs it
  (`llinventory.cpp:590-607`), with that prim as its folder
  (`llviewerobject.cpp:2900`), plus a
  `TransactionID` the item carries (null for an item taken from
  inventory), and the same checksum `UpdateTaskInventory` carries.

An item the avatar may not copy is moved: the viewer deletes its own
local copy (`lltooldraganddrop.cpp:1846-1853`).

`Session.PutInObject` sends it for an item of LSL text, with `Enabled`
set, and `Session.PutScriptInObject` takes `running` for the Control
case. Both put the script into the prim they are given, as the Contents
tab does: a caller that names a child has chosen it. `InstallScript` and `Run`
keep their own path, the copy through `UpdateTaskInventory` followed by
the source saved into the object, because they have the source and read
the compile's verdict from it.

Measured on Agni on 5 October 2026, with `RezScript` from this branch:

- A full-permission script dropped into a one-prim box ran: its
  `state_entry` line arrived about one second after the drop, and
  `slsh start` then said `already`.
- Three scripts the avatar may copy but not modify ran the same way, and
  stayed in inventory, being copyable.
- Dropped with `Enabled` false (`slsh drop --stopped`), the script lay
  stopped, and `slsh start` then started it: the region had compiled it,
  where the copy through `UpdateTaskInventory` gave `start` nothing.
- Slate's `drop ITEM into OBJ` of the full-permission script: the copy
  showed in the contents and was matched as the drop's own, so the entry
  carries the asset Slate compares; the script ran, and the copy was
  taken out at the end of the test.
- A notecard dropped in still goes with `UpdateTaskInventory`, as before.
- Into the child of a two-prim linkset, by `slsh drop` naming the child
  and by Slate's `drop ITEM into OBJ link 2`: the script landed in the
  child, ran there (its `llGetObjectName` was the child's), and the root
  held nothing; Slate found its copy in the child and took it out.

Nothing comes back to say the script went in or started: the contents
and `GetScriptRunning` are how a caller finds out.

## Whether a script is running

`SetScriptRunning` is answered by nothing at all, so `ScriptRunning`
asks afterwards, which is the viewer's way too: its script editor sends
`GetScriptRunning` the moment it opens a script in a prim, to decide
whether the "Running" box is ticked.

### The request is not deprecated, and the reply is

`GetScriptRunning` is an ordinary template message, sent over the
circuit, and the viewer still sends it that way rather than through any
capability: it packs one and sends it reliably to the region's host at
`llpreviewscript.cpp:2875-2881`, and it has no entry of its own in
`message.xml`, so it takes the server default flavour -- "template", at
`message.xml:4-10` -- which is what chooses the template builder over
the LLSD one (`message.cpp:3427-3452`).

The reply has moved off the circuit. `ScriptRunningReply` is marked
`UDPDeprecated` in the template (`message_template.msg:5510`) and
`message.xml` gives it the llsd flavour, under a heading that says
"UDPDeprecated Messages" (`message.xml:590-597`). The tell is a field:
the template's `Mono` is commented out with "Added to LLSD message"
(`message_template.msg:5516`), and the viewer reads `Mono` by name when
a reply arrives (`llpreviewscript.cpp:3328`). It could not do that off
the circuit, since the template reader kills the viewer outright when
asked for a variable its template does not have
(`lltemplatemessagereader.cpp:98-103`). So the reply the viewer
actually handles is the LLSD one off the event queue, dispatched by
name into the same handler either transport reaches
(`lleventpoll.cpp:110`, `llstartup.cpp:3857`).

So the question goes out on the circuit -- there is no capability for
it in the viewer's list or in this grid's -- and the answer is watched
for on both relays. Which of them it arrives on is the simulator's
choice and not the caller's, and a grid that still answers on the
circuit is handled by the type switch in `Session.handle`.

### What Agni does

Second Life answers only on the event queue. Measured on Agni as hobb,
in Pelmar Reach, with a second client attached to the same slgod
watching both relays: a script was installed and started in a rezzed
box, "stop" was asked for, and nothing arrived on the circuit. Every
reply came over the queue, in this shape:

	<llsd><map><key>Script</key><array><map>
	  <key>Running</key><boolean>1</boolean>
	  <key>ItemID</key><string>d1a87e57-...</string>
	  <key>Luau</key><boolean>0</boolean>
	  <key>LuauLanguage</key><boolean>0</boolean>
	  <key>Mono</key><boolean>1</boolean>
	  <key>ObjectID</key><string>785f7e57-...</string>
	</map></array></map></llsd>

Two things in that are worth writing down. The `Script` block arrives
as an array of maps although the template declares it Single, so it is
read as a list; and the map carries fields the template has never had
-- `Mono`, which the template at least mentions, and `Luau` and
`LuauLanguage`, which it does not and which Agni had grown by August
2026. Only `ObjectID`, `ItemID` and `Running` are read, so the next
field Linden Lab adds goes past unlooked at
(`Session.scriptRunningEvent`).

The first reply said Running 1 and every later one said 0: the stop had
worked all along and only the confirmation was deaf. That is the reason
`ScriptRunning` waits for a real answer rather than reporting the
request as the outcome -- and the reason its timeout still exists. A
question can go unanswered, and when it does the caller must be told
the object has not agreed, never that it has.
