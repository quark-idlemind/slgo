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
