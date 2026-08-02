# slgo — Second Life message layer in Go

The first piece of a Go rewrite of the C client one directory up: the
483 UDP messages in `message_template.msg`, as Go structs that encode
and decode themselves.

    go generate ./msg      # regenerate msg/messages_gen.go
    go test ./...

## Layout

    cmd/msggen/         reads message_template.msg, writes Go
    client/profile.go   credentials under ~/.config/slgo
    client/xmlrpc.go    XML-RPC decoding
    client/llsd.go      LLSD decoding and encoding
    client/login.go     login_to_simulator
    client/session.go   the UDP circuit and its handshake
    client/caps.go      the seed capability
    client/inventory.go the folder tree, over AIS v3
    msg/types.go        UUID, Vector3, Quaternion, IPAddr, Info, ...
    msg/buffer.go       little endian read/write primitives
    msg/codec.go        the generic tag-driven encoder and decoder
    msg/framing.go      message numbers, packet header, zero coding
    msg/receive.go      the read goroutine
    msg/send.go         the write goroutine
    msg/dispatch.go     the routing goroutine
    msg/dump.go         YAML packet dumps
    msg/messages_gen.go generated: 483 messages, ~14700 lines

## How it works

The generator emits structs that carry the template's own vocabulary in
`ll` struct tags, and nothing else:

    // ChatFromViewer is Low 80, zerocoded.
    type ChatFromViewer struct {
        AgentData ChatFromViewer_AgentData `ll:"Single"`
        ChatData  ChatFromViewer_ChatData  `ll:"Single"`
    }

    type ChatFromViewer_ChatData struct {
        Message []byte `ll:"Variable,2"`
        Type     uint8 `ll:"U8"`
        Channel  int32 `ll:"S32"`
    }

    func (m *ChatFromViewer) MsgInfo() *Info          { return &infoChatFromViewer }
    func (m *ChatFromViewer) Encode() ([]byte, error) { return Marshal(m) }
    func (m *ChatFromViewer) Decode(b []byte) error   { return Unmarshal(b, m) }

`Marshal` and `Unmarshal` in `msg/codec.go` are the only code that knows
what `U32` or `Variable,2` mean on the wire. Consequences worth having:

- A new message, or a new block on an existing message, needs no code
  at all. Rerun the generator.
- A new *field type* is one line in `kindByName` plus a case in
  `encodeField`/`decodeField`, and one line in the generator's
  `goType` table. Nothing else.
- The generated file is boring enough to read, and diffing it after a
  template update shows exactly what Linden Lab changed.

Block quantifiers map to Go shapes, so the type system carries some of
the schema: `Single` is a struct, `Multiple 4` is a `[4]T`, and
`Variable` is a `[]T` with a one byte count.

Tags are parsed once per type and cached (`planCache`), not once per
packet. Encode and decode of a small message run in about 110ns with
2–3 allocations on an M-series laptop, which is far more headroom than
the protocol needs.

## Type mapping

| template | Go | wire |
|---|---|---|
| `U8` `U16` `U32` `U64` | `uint8` … `uint64` | little endian |
| `S8` `S16` `S32` `S64` | `int8` … `int64` | little endian |
| `F32` `F64` | `float32` `float64` | little endian IEEE 754 |
| `BOOL` | `bool` | one byte |
| `LLUUID` | `UUID` | 16 bytes verbatim |
| `LLVector3` | `Vector3` | 3 × F32 |
| `LLVector3d` | `Vector3d` | 3 × F64 |
| `LLVector4` | `Vector4` | 4 × F32 |
| `LLQuaternion` | `Quaternion` | 3 × F32 — see below |
| `IPADDR` | `IPAddr` (`[4]byte`) | 4 bytes, network order |
| `IPPORT` | `IPPort` (`uint16`) | 2 bytes, network order |
| `Variable N` | `[]byte` | N byte little endian length, then bytes |
| `Fixed N` | `[N]byte` | N bytes |

Everything is little endian except the packet sequence number, `IPADDR`
and `IPPORT`.

## Where the wire format came from

Read out of the working C client rather than assumed:

- Little endian integers: `queue16`/`queue32` in `queue.c` write
  `ptr[0] = value >> 0`.
- Big endian sequence number: `allocPacket` writes `data[1] = seq >> 24`.
- Message number framing — `nn`, `FF nn`, `FF FF hi lo`, `FF FF FF nn` —
  from the receive path in `s.c`, which demonstrably works against the
  live grid.
- Variable blocks take a one byte count: `p_InventoryDescendents.c`
  reads `read8(&n, ...)` and loops.
- Appended acks are stripped from the tail *before* zero expansion,
  per `s.c`.

One trap when reading viewer source against the template: field order
on the wire is the *template's*, not the order of the `add*()` calls.
`LLAgent::sendAgentUpdate` adds `Flags` right after `State`, but the
template puts `Flags` last and that is where it goes —
`nextBlock()` lays the block out from `template_data->mMemberVariables`
and `addData` fills it in by name.

Three places where this deliberately differs from the C:

1. **`LLQuaternion` is 12 bytes, not 16.** Settled by the viewer source:

       void LLTemplateMessageBuilder::addQuat(const char *varname, const LLQuaternion& quat)
       {
           addData(varname, quat.packToVector3().mV, MVT_LLQuaternion, sizeof(LLVector3));
       }

   `sizeof(LLVector3)`, three floats. `packToVector3` normalizes and
   then negates the vector part when W is negative, so the receiver can
   assume W is non-negative and recover it as sqrt(1 - x² - y² - z²).
   `PackQuaternion` and `Quaternion.W` in `msg/types.go` implement both
   halves.

   `Misc/genproc.c` maps it to `readVector4` — an easy mistake, since
   the type really is four floats *in memory* and only three on the
   wire. The C tree disagrees with itself about it: the generated
   `protos/q_AgentUpdate.c` writes `queueVector4` while the hand edited
   `messages/q_AgentUpdate.c` writes `queueVector3`.

   Worth recording that the obvious live experiment cannot decide this,
   because it looks like it can. Uncommenting the `queueAgentUpdate`
   call at `s.c:1267` draws a `CameraConstraint` (High 22) back from the
   simulator where the same run without it draws none, so a 114 byte
   `AgentUpdate` is accepted and acted on. That still proves nothing
   about the width: `LLTemplateMessageReader::decodeData` does not
   discard a short message, it calls `logRanOffEndOfPacket` and zero
   fills, so a 114 byte body against a 122 byte template would have
   been accepted identically. The message was also nearly all zeros, so
   a misread would have found zeros wherever it looked.

2. **Medium messages frame as `FF nn`.** `allocPacket` takes the marker
   byte from bits 8..15 of a code whose `0xFF` lives at bits 24..31, so
   it emits `00 nn` and any Medium message the C client *sends* is
   malformed. Receiving is fine, which is why `CoarseLocationUpdate`
   works and nobody noticed. `TestIDRoundTripAll` covers this.

3. **`IPPORT` is network order.** `genproc.c` maps it to `read16`
   (little endian). Linden Lab's `addIPPort` does `htons`, so big endian
   should be right — but no message using `IPPORT` is implemented in the
   C client, and the wiki gives the width (two bytes) while saying
   nothing about order; it states little endian only for the plain
   integer types. So this one is reasoned, not observed. Flagging it
   rather than burying it: if something using `IPPORT` ever misbehaves,
   look here first.

## Decoding is deliberately forgiving

Two rules in `Unmarshal`, both learned from the C client's failure mode:

- **Trailing bytes are ignored.** Linden Lab extends messages by
  appending blocks. `ImprovedInstantMessage` grew a `MetaData` block
  between the old template and the current one.
- **A missing trailing `Variable` block decodes as zero instances**
  rather than an error. The live grid accepts an `ImprovedInstantMessage`
  that stops before `MetaData` — verified by sending one and having it
  delivered — so we extend the same courtesy in the other direction.

Both rules turn out to match `LLTemplateMessageReader` exactly: at the
packet boundary it sets a Variable block's `repeat_number = 0` rather
than failing.

Where this package is *stricter* than Linden Lab: a truncated
fixed-size field is an error here, whereas `decodeData` logs
`logRanOffEndOfPacket` and substitutes zeros. Matching them would buy
interoperability on malformed packets at the cost of turning a real
bug into silent zeros, so this errors instead. Worth revisiting if a
message the grid genuinely sends ever trips it.

Any other short read is an error, and the error names the message,
block and field. Nothing in this package calls `panic`, `os.Exit` or
`log.Fatal` on bad input; that is the single thing that most needs to be
different from the C, where an unknown XML value type called `err(1,...)`
and took the whole client down.

## Receiving

`Receiver` reads datagrams and puts decoded packets on a channel:

    recv := msg.NewReceiver(conn)          // conn is a *net.UDPConn
    go func() { err = recv.Run(ctx) }()

    for p := range recv.C() {
        switch m := p.Message.(type) {
        case *msg.ChatFromSimulator:
            ...
        }
    }

`Run` blocks, so the caller decides where the goroutine lives. It
closes the channel on return, so `range` terminates. Cancelling the
context unblocks a read in progress via `SetReadDeadline`, which
`*net.UDPConn` supports; a cancelled `Run` returns nil rather than the
resulting timeout.

Each `Packet` is one of three things, and the doc comment says so:
`Message` set means it decoded, `Err` set means it did not and `Body`
holds the raw bytes, and both nil means the datagram carried only
acknowledgements. `Acks` is filled in either way — a packet whose body
is corrupt still releases whatever it was acknowledging, which is not
true of the C, where a bad body means the acks in the same datagram are
processed but the packet is then dropped on the floor.

Failures are delivered rather than swallowed. An unknown message number
produces a packet whose `Err` wraps `ErrUnknownMessage` with the raw
body attached, so a caller that meets a message this build predates can
see it, log it and continue. Only runt datagrams are dropped outright,
and they are counted.

The receiver deliberately does *not* acknowledge anything or track
sequence numbers. It hands `Header.Reliable` and `Header.Sequence` up
and lets the session layer decide, because retransmission and duplicate
suppression need state this layer has no business owning.

One read buffer is reused forever. That is safe because nothing in a
delivered `Packet` aliases it: the codec copies `Variable` fields, and
the header's extra bytes and any raw body are copied on the way out.
`TestReceiveNoAliasing` guards it.

When the channel is full the default is to block, which pushes back on
the network and lets the kernel drop datagrams — honest for UDP, but it
also stalls the caller's acknowledgements behind a slow consumer.
`DropWhenFull()` inverts that and counts what it discards.

## Sending

`Sender` owns the write end and is the only thing that assigns sequence
numbers, so neither needs a lock. It takes messages on one channel and
acknowledgements on another, and selects on both:

    send := msg.NewSender(conn)
    go func() { err = send.Run(ctx) }()

    send.SendReliable(ctx, &msg.UseCircuitCode{...})
    send.QueueAck(inbound.Header.Sequence)   // never blocks
    send.ConfirmAck(seq)                     // from Packet.Acks

The two channels are the whole point. When a message goes out, whatever
acknowledgements have piled up ride on its tail — `FlagAck`, four bytes
each, a count byte — so they cost no datagram at all. If nothing goes
out within `AckDelay` (100ms by default) they are flushed as a batched
`PacketAck` instead, so they never wait indefinitely for traffic that
may not come.

Both halves of that are things the C client leaves on the table.
`LL_ACK_FLAG` is read at `s.c:471` and never set on anything outbound,
so nothing is ever piggybacked; and `queuePacketAck` writes a count of
exactly one, so a session that acknowledged 23,371 packets sent 23,371
datagrams to do it. `PacketAck.Packets` is a `Variable` block that holds
255.

`QueueAck` never blocks. Dropping an acknowledgement under pressure
costs one retransmission from the peer, which is a much better trade
than stalling the receive loop — and it means the receiver can hand acks
to the sender without the two being able to deadlock on each other.

Reliable messages are retransmitted with the resent bit set and the
original sequence number, backing off each time, and are abandoned after
`maxTries` with a counter to say so. The C retried forever, which is why
its `acktime` only ever grew.

## Dispatching

`Dispatcher` consumes the receiver's channel, routes each packet, and
does the acknowledgement bookkeeping in between:

    send := msg.NewSender(conn)
    d := msg.NewDispatcher(
        msg.WithSender(send),                 // wire up acknowledgements
        msg.WithConcurrency(8),
        msg.OnUnhandled(func(p *msg.Packet) { log.Print(msg.DumpPacket(p)) }),
    )
    d.MustHandle("ChatFromSimulator", onChat)
    d.MustHandle("TransferInfo", onInfo, msg.Inline())
    go d.Run(ctx, recv.C())

Handlers run in their own goroutine, throttled by a counting semaphore:
acquire a slot, spawn, release on the way out. Concurrency is bounded,
so a slow handler backs pressure up into the receive channel and then
into the kernel rather than growing the heap without limit. The same
semaphore doubles as the shutdown barrier — collecting all N permits
after the loop waits for every handler still running.

Handlers registered `Inline()` run on the dispatch goroutine instead,
in arrival order. UDP does not guarantee order, but the protocol layers
it back on: `TransferInfo` carries the size that `TransferPacket`
assembles against, and `RegionHandshake`, the teleport sequence and
inventory descent all care. Those want `Inline()`; everything else does
not.

Acknowledgement bookkeeping happens for *every* packet, before anything
else and including duplicates and ones that failed to decode. A
duplicate arrived precisely because our previous acknowledgement did
not get through, so it needs another one.

Duplicates are then suppressed by sequence number over a ring of the
last 4096, because a retransmission means the same message is handled
twice otherwise. The C client has no such check.

Nothing is dropped silently. Unhandled messages are counted per ID and
reported by `Unhandled()`, with `OnUnhandled` for logging. That is not
hypothetical bookkeeping: the only reason anyone noticed the simulator
answers `AgentUpdate` with a `CameraConstraint` is that the C client
dumps messages it has no callback for.

## Dumping

`DumpPacket` and `DumpMessage` render YAML, driven by the same plan
machinery as the codec so they cannot drift from the wire format:

    from: "198.51.100.7:13010"
    at: "2026-08-01T20:34:12.000000Z"
    sequence: 1234
    flags: [zerocoded, reliable]
    acks: [1201, 1202]
    message: ImprovedInstantMessage
    id: {freq: Low, number: 254}
    blocks:
      MessageBlock:
        Position: {x: 188.429, y: 202.836, z: 26.3564}
        FromAgentName: "Example Resident"
        Message: "Self IM test one"
        BinaryBucket: "0x07c0b380"
      MetaData: []

A `Single` block is a mapping and `Multiple`/`Variable` blocks are
sequences; the template says which, so nothing is ambiguous. `Variable`
fields render as text when the bytes are text — dropping the NUL that
`queueString1` appends — and as a quoted `0x...` string when they are
not. Quaternions show the recovered `W` alongside the three components
that actually travel, because that is what the value means.

Every scalar is quoted, so nothing is read back as an accidental number
or boolean. There is no YAML dependency: the emitter is a hundred lines
against a deliberately small subset, and `TestDumpParsesAsYAML` feeds
all 483 messages through Ruby's Psych to check the subset is real.

## Tests

`go test ./...` covers:

- every one of the 483 messages through a populate/encode/decode/compare
  round trip, plus a re-encode byte comparison
- golden byte vectors hand-derived from `q_UseCircuitCode.c`,
  `q_PacketAck.c` and `q_CompletePingCheck.c`
- the `AgentUpdate` size cross-check against the C allocation
- message number framing for all 483, and the shape of each priority class
- packet header, appended acks, zero coding round trip and the C's
  extended 256-zero run form
- the two forgiveness rules, and that ordinary truncation still errors
- generator parse errors: 15 malformed templates, each expected to fail
  with a specific message

## Logging in

Credentials live one file per account under a private directory:

    ~/.config/slgo/          mode 700
    ~/.config/slgo/example      mode 600

    # slgo profile "example"
    first    = Example
    last     = Resident
    password = $1$00157e577e57c0de028f000000000000
    start    = last

and a session is three calls:

    acct, err := client.LoginAs(ctx, "example")
    s, err := client.Connect(ctx, acct, client.Options{})
    defer s.Logout(ctx, 10*time.Second)

`LoadProfile` refuses a profile anyone but its owner can read, and a
directory anyone but its owner can list, the way ssh does. An unknown
setting is an error rather than a line that silently does nothing.
`SaveProfile` writes the `$1$` digest rather than the password: it is
the only form that goes over the wire, so nothing is lost, and a
password that may be used elsewhere stays off the disk. Plain text in
the file works too and is hashed on the way out.

`Connect` dials the simulator, starts the receiver, sender and
dispatcher, and runs the handshake: `UseCircuitCode` to open the
circuit, then `CompleteAgentMovement`, which the simulator answers with
`AgentMovementComplete`. For the life of the session it answers
`StartPingCheck` and replies to `RegionHandshake`; everything else is
yours through `Handle`.

The XML-RPC decoder maps `<int>` to int64, `<struct>` to a map and so
on, and — the part that matters — renders a type it has never seen as
the text inside it rather than failing. That is the exact thing that
stopped the C client logging in when Linden Lab's response grew `<int>`
fields. The test fixture is a real 29KB response from the live grid,
348 members deep, with the identifiers scrubbed.

Nothing the response contains is discarded: `Account.Raw` holds the
whole decoded tree, so a field this code does not model is still
reachable.

## Capabilities and inventory

`Connect` asks the seed capability for `DefaultCaps` and leaves the
result on the session, because almost everything above the circuit
needs them:

    url, ok := s.Caps.Get("InventoryAPIv3")

Inventory comes over AIS v3, an HTTPS GET per folder:

    err := s.FetchInventory(ctx, client.FetchOptions{Concurrency: 8})
    for _, f := range s.Inventory.Children(s.Inventory.Root()) {
        fmt.Println(f.Name, len(s.Inventory.Contents(f.ID)))
    }

The UDP `FetchInventoryDescendents` the C client uses was retired by
Linden Lab -- the simulator accepts it and never answers -- so this is
the only way to get an inventory now.

Folders are fetched concurrently, bounded by `Concurrency`. On a real
account of 119 folders and 1248 items that is **1.8 seconds** against
about **50** for the C client doing the same 119 requests one at a
time. One folder failing does not lose the tree; the walk gives up
after `MaxFailures` so a broken capability cannot spin.

`Inventory` is safe to read while a fetch is running, which is what the
`OnFolder` progress callback invites. Links are recorded as items with
`IsLink` set and `linked_id` in `AssetID`, which is where the UDP
message used to put it.

## More than one account at a time

Nothing in either package keeps per-connection state at package level.
The message registry and the codec's plan cache are read-only after
init and shared safely; everything else -- sockets, sequence numbers,
retransmission queues, duplicate windows, capabilities, inventories,
every counter -- hangs off a `Session`.

    a, _ := client.LoginAs(ctx, "example")
    b, _ := client.LoginAs(ctx, "builder")
    sa, _ := client.Connect(ctx, a, client.Options{})
    sb, _ := client.Connect(ctx, b, client.Options{})

`TestManySessionsAtOnce` connects five sessions to five simulators
simultaneously and checks each lands in its own region with its own
identity and socket, and that no simulator sees another's circuit.
`TestSessionsHaveSeparateInventories` fetches two trees at once and
checks neither leaks into the other. They exist so that a package level
cache added later fails a test instead of being discovered in
production.

## Presence

`AgentUpdate` is not optional, in a way that is not obvious. A session
that never sends one is in nobody's interest list: the simulator
streams no `ObjectUpdate` at all, so nothing can be seen, selected or
built on. Rezzing a prim and waiting for it to appear is how that was
found -- the object really was created, and the simulator simply never
mentioned it.

So the server sends it, once a second, from wherever the avatar
arrived. It belongs there for the reason the circuit does: it has to
keep being sent, and a client that stopped would silently take object
streaming with it. A client that wants to move the camera or set a
draw distance uses `Agent.SetLook`; `Options.Presence` turns it off.

## The event queue

Some messages no longer come over UDP.  The template marks them
`UDPDeprecated` and the simulator simply does not answer them on the
circuit: `ParcelProperties`, `TeleportFinish`,
`EstablishAgentCommunication` and a growing list.  They arrive on
`EventQueueGet`, a long poll whose replies carry an id that the next
poll acknowledges.

That poll lives in the server, beside the circuit, for the same reason
the circuit does: it has to run continuously, and a client restart
would lose the sequence and drop whatever arrived in the gap.  This is
the one place where "do it in the client" does not work.

Events reach clients on their own channel, because their bodies are
LLSD rather than the binary message encoding:

    c.Attach(ctx, "example", "ParcelProperties")
    for e := range c.Events() {
        body, _ := e.Decode()   // map[string]any
    }

Subscriptions are by name and cover both, so `ParcelProperties` gets
you the event whether it arrives on the circuit or the queue.

One trap worth knowing: the queue sends packed fields as `<binary>`,
not `<integer>`.  `ParcelFlags` arrives as four big endian bytes.
`llsd.Int` decodes those, because the alternative -- returning zero --
reads as a confident "every permission is off" rather than as "I could
not tell", and that is exactly the wrong way for this to fail.

## Building, from the client

`slgo chat` records public chat; `slgo-build` rezzes two prims, links
them and reads back what the simulator says.  Both are client side --
the server relays bytes and knows nothing about prims or chat.

    slgo -for 1h -log chat.log chat
    slgo-build -at 193,206,27 -chatlog chat.log

Three things that cost time and are not written down anywhere obvious:

- **`ObjectUpdate.OwnerID` is not the object's owner.**  It carries
  sound ownership and is usually zero.  To know who owns something you
  ask `RequestObjectPropertiesFamily` and read the owner off the reply.
- **A newly rezzed prim is found by its local id, not by diffing the
  set of objects you own.**  The interest list fills gradually, so an
  attachment worn all along looks new the moment the simulator first
  mentions it.  Attachments have local id 0.
- **`ObjectProperties.CreationDate` is microseconds**, not seconds.
  Read as seconds it dates a fresh prim to the year 56 million.

`ObjectLink` makes the *first* id in the list the root; the rest become
children, which shows up as their `ParentID`.

Public chat is channel 0 by definition -- the simulator only sends
`ChatFromSimulator` for what an avatar could hear, and anything on
another channel never reaches a viewer -- so subscribing to that
message is the whole of listening to channel 0.

## Assets and object inventory

`slgo-asset` runs the whole chain against a live simulator: rez a prim,
create a notecard and a script in inventory, upload their contents, put
both inside the prim, read the prim's inventory back, edit the notecard
where it sits, and pull a copy out again.

Uploading an asset is two steps.  The first posts the item id to a
capability and the simulator answers with a one-shot uploader URL; the
second posts the bytes there.  That second step is why `CapRequest` has
a `URL` field -- without it a client would have to reach the simulator
itself, which is the coupling the server exists to remove.  Only a URL
on a host the simulator already serves a capability on is accepted, so
it cannot be used to make the server fetch anything at all.

Reading a prim's inventory goes over **xfer**, the old UDP file
transfer: `RequestTaskInventory` answers with a *filename*, and
`RequestXfer` pulls the file down in packets that each have to be
acknowledged before the next is sent.  `client.Xfers` reassembles them.
It is client side, being nothing but messages.

Reading an asset's bytes is a third mechanism again.  `ViewerAsset`
serves the content delivery network -- textures, meshes, sounds -- and
answers 403 for a notecard, so those come over the UDP asset transfer:
`TransferRequest` names the item and the asset, the simulator answers
with a `TransferInfo` carrying the size, and then a run of
`TransferPacket`.  Nothing is acknowledged, so packets arrive in any
order and the last is marked by its *status* rather than its number.
`client.Transfers` reassembles them.  This is the path the C client's
cache.c uses.

Four things that cost time here:

- **An item copied into a prim gets a new item id.**  The agent
  inventory id does not address it, and using it gets a `NotFound` from
  the capability.  The task's ids are in the inventory file.
- A notecard is not stored as plain text.  It is wrapped in a
  `Linden text version 2` container with a `Text length` header, and a
  notecard without it will not open.
- The first xfer packet carries a four byte length prefix that is not
  part of the file, and the top bit of the packet number marks the
  last one.
- **A newly rezzed prim has to be confirmed as yours.**  Objects stream
  in continuously, so the first local id you have not seen before is
  often a stranger's -- which is how a notecard was once offered to
  someone else's prim, answered with "Unable to edit this!".

## Not done

No region crossing, no teleport, no appearance, and inventory is read
only -- nothing creates, moves or deletes.

The sender writes to an `io.Writer`, which suits the connected socket a
single simulator needs. Neighbouring simulators at once will want a
`WriteTo` variant and a session per circuit.

`AgentUpdate` is not sent. Nothing needs it yet, and sending it once is
worse than not at all — see the note above about what it does and does
not prove.

`Receiver` allocates a message per packet through `New`. At the packet
rates in the C client's stats — 600k in a long session — that is worth
a pool eventually, but not before there is something to measure.
