# slgo — Second Life message layer in Go

The first piece of a Go rewrite of the C client one directory up: the
483 UDP messages in `message_template.msg`, as Go structs that encode
and decode themselves.

    go generate ./msg      # regenerate msg/messages_gen.go
    go test ./...

## Layout

    cmd/msggen/         reads message_template.msg, writes Go
    cmd/slgod/          holds grid connections, serves clients
    cmd/slsh/           the shell: inventory, the world, and chat
    cmd/automate/       runs LSL scripts, prints what they said
    cmd/autobench/      measures what LSL constructs cost in memory
    internal/session/   get a session, and an object to run scripts in
    server/lock.go      exclusive use of a named thing, for as long as a client lives
    internal/slhost/    where slgod is, asking sl-host when it is there
    sl/                 the client library: everything an avatar can do
    sl/backend.go       one interface, two ways to be connected
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
    doc/messages.txt    what each of those 483 is for, and which way it goes
    doc/capabilities.txt the http capabilities that can be asked for

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
    password = $1$<the md5 of the password>
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

The login server also asks which computer this is: `mac`, the network
card's address, and `id0`, a digest of the first disk's serial number.
A viewer reads both off the hardware. slgod has none to read -- and
reading this host's would hand Linden Lab an identifier shared with
every other program on the machine -- so it invents a pair on its first
run and keeps it in its own directory, separate from the profiles
because it describes the machine rather than an account:

    ~/.config/slgod/config      mode 600

    mac = 02:1B:9C:4E:77:A3
    id0 = 5e027e577e57c0deb52d5fd2dded42e5

Keeping it matters more than what it is. A pair that changes every
login looks like a different computer every time, which is what an
abuser looks like; one that never changes looks like an ordinary
resident with one computer. The address is drawn from the locally
administered range, so no real card can ever have it. A profile may set
`mac` and `id0` of its own for an account that has always logged in from
somewhere else, and those win.

Neither is sent as it stands. Despite the name, the `mac` a viewer sends
is not an address but the md5 of one, and `id0` is the md5 of a disk
serial, so both are 32 hex digits on the wire. The address is hashed on
the way out, which leaves the file holding the half a person can read
and check while the grid sees the half a viewer would send. A value that
is already a digest passes through untouched, the way an already hashed
password does.

The operating system is described the way a viewer describes it --
`platform`, `platform_version`, `platform_string` and `address_size`,
so `mac`, `15.7.7`, `macOS 15.7.7`, `64` -- all four taken from the host
this is running on, since naming one system in `platform` and another
in `platform_string` would be a worse answer than naming none. A profile
may override them, as a set.

The login also asks for `extended_errors`, which is what makes a refusal
answerable in code: alongside the sentence meant for a person, the
server names it, and `LoginError.MessageID` and `.MessageArgs` carry
that -- `LoginFailedAccountSuspended` with the `TIME` it ends, rather
than a string to match on.

[doc/login-parameters.md](doc/login-parameters.md) is the full catalogue
of what a viewer sends, read out of the Firestorm source: every
parameter, the options array, the refusal reasons, and which of them we
deliberately do not send.

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

`sl.Build` puts up a multi-prim object in one call, and `slsh watch`
prints what crosses the wire.  Both are client side -- the server relays
bytes and knows nothing about prims or chat.

    slsh -c "watch --for 1h ChatFromSimulator" > chat.log

Building is exercised against a live simulator by the tests in `sl`,
which are skipped unless `SLGO_TEST_ADDR` names a running slgod:

    SLGO_TEST_ADDR=localhost:7807 go test ./sl -run Build -v

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

`TestInventoryChain` in `sl` runs the whole chain against a live
simulator: make a folder, create a notecard and a script, upload their
contents, put them inside a prim, read the prim's inventory back, and
take it all away again -- cleaning up after itself either way.

    SLGO_TEST_ADDR=localhost:7807 go test ./sl -run InventoryChain -v

It is a test rather than a command because almost nothing in this
protocol confirms itself, so every step reads back what it did; a fake
simulator would simply agree with whatever the code believed.

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

## slsh

A shell for Second Life, on the sl package, against either backend:

    slsh [--addr localhost:7807] [--agent example]
    slsh --direct [--first Quark] [--last Idlemind]
    slsh -c "ls -l Objects"

Commands are the outer mode, because that is what the keyboard is
mostly for: chat arrives whatever mode is in force, and cd and ls are
wanted more often than talking. `chat` goes the other way and the
escape key comes back, with each mode keeping the line that was half
typed in it. The prompt says which mode and where:

    /Objects$ ls
    /Objects$ chat
    Local> hello everyone
    (ESC)
    /Objects$

Inventory is a filesystem: `cd`, `ls`, `pwd`, `cat`, `mkdir`, `mv`,
`rm`, `find`. The world is `who`, `where`, `look`, `objects`. The
simulator will describe itself with `caps`, `features` and `lsl`.
Talking is `chat`, `say`, `im`, `friends`, `lookup`, `offer`, `offers`,
`accept`, `decline` and `talk`.

Tab completes commands and inventory paths in command mode, and moves
between conversations in chat mode -- which is the argument for having
modes at all, since the same key then means the obvious thing in both
places. Up and down walk the history.

### Listing, editing, running

Output redirects with `>` and `>>`, and `. file` runs a file of
commands, which together are the point:

    $ slsh -c "ls -l /Objects" > listing
    $ awk '{print "mv " $3 " /Objects/sorted"}' listing > moves
    $ slsh -f moves

`ls` prints one bare path per line so a listing can be cut up by
anything; `ls -l` adds the kind, the date and the id. A folder has no
date, so that column holds a `-` rather than collapsing and moving
every column after it. Names are not unique -- one folder here holds
eighteen things of the same name -- so `mv`, `rm` and `cat` take an id
wherever they take a path, which is what makes a listing of duplicates
editable into commands that each mean one thing.

Paths quote with `"` and `'`, and a backslash is left alone, since
inventory paths use it to escape a separator.

`mv` handles folders as well as items, and the two halves go different
ways for the same reason as before: a rename is an AIS `PATCH`, and a
move is UDP, because AIS refuses to change a parent. A folder renamed
to `odd / name \ here` comes back with exactly that name.

For how to use the commands rather than how they work, see
[doc/guide.md](doc/guide.md).

## Where slgod is

slgod does not always run on the machine talking to it, and the machine
it does run on moves between networks, so no client hardcodes an
address. In order:

1. `--addr HOST:PORT` (or `-server`), if given;
2. `addr = ...` in `~/.config/slsh/config`, for slsh;
3. what the `sl-host` command prints, if it is on `$PATH`, with port
   7807 joined to it -- sl-host prints a bare host and no port;
4. this machine, when sl-host is not installed.

Not being installed is the ordinary case on a machine that runs its own
slgod, so it is a default rather than a failure. sl-host being there and
*failing* is reported instead:

    $ slsh -c where
    slsh: cannot find the host slgod is on: sl-host failed: exit status 1: no host configured
            --addr HOST:PORT overrides

Falling back to localhost there would turn "sl-host is misconfigured"
into a connection refused against this machine, which points the reader
at the wrong problem entirely.

## Running scripts: automate and autobench

Two programs that came from the elsl project, where they reached Second
Life through slrund and a viewer. They run on `sl` now, so the only
thing between them and the grid is a session.

    automate a.lsl b.lsl
    automate --object "Test HUD" a.lsl
    autobench -1 --title "global integer" --code "integer g;"

A script needs an object to run in, so both find one or make one:
`--object` names one already in the region, and without it a prim is
rezzed beside the avatar and trashed afterwards. `--keep` leaves it.

The contract with a script is one line: it says `DONE` when it has
finished. Without a sentinel there is nothing to wait for but the
clock, and every run costs the whole timeout.

`automate` prints what each script said, prefixed with the file it came
from, and exits non-zero if any of them would not compile, faulted, or
never finished. `autobench` measures the memory a construct costs by
finding the 512-byte block boundary it crosses; the measurement
machinery came over unchanged, because it is about LSL and not about
how a script reaches the grid.

What did not come over is what belongs to elsl: `--sim` (the eLSL
simulator), `-O` and `--std` (its compiler's flags), and the
compile-and-compare machinery. The compiler here is the grid's -- the
source goes up through `UpdateScriptTask` and comes back compiled or
refused. slgo does not depend on elsl, and this is where that shows.

Measured against the grid on 2026-08-06: creating a script item costs
about 8.1s -- the create, the copy into the object, and the settle --
and updating one that already exists costs about 0.9s. That is why both
programs hold one object and one script name for their whole run, and
why every figure that looks like "what compiling costs" has an item
creation hiding in it the first time.

## Inventory names, and paths that survive them

An inventory name may hold very nearly any printable character, so a
path made of names needs escaping to be readable back: `/` separates,
`\/` is a slash inside a name, and `\\` is a backslash. `SplitPath` and
`JoinPath` are inverses.

What the grid accepts was measured rather than assumed. An item was
created for every character from space to tilde, all hundred listed
back, and every one returned byte for byte -- including `/` and `\`.
The only exceptions are at the edges: a name given a leading or
trailing space comes back trimmed. So a name may contain anything
printable and may not begin or end with a space.

The probe found three bugs, which is what probes are for:

  - Listing a folder by path escaped the paths a second time, so
    `Notecards/thing` came back as `Notecards\/thing`. Every entry in
    every listing was affected and the unit tests had not caught it,
    because they tested splitting and joining but never the composition
    a listing does.
  - `MoveItem` had never worked. It used an AIS `PATCH` with a new
    `parent_id`, and the grid answers `400` with `Cannot change
    parent_id.  Use MOVE method.` -- so it moves over UDP now, the way
    a viewer does, which also carries a new name and makes moving and
    renaming one round trip.
  - `CreateFolder` sent the name without its terminating NUL, so the
    simulator read the length and took the last byte for the
    terminator. Every folder made through this package was one
    character short of its name, and nothing found it afterwards by
    the name it was asked for.

## Reading inventory, and fetching an asset

    es, err := s.ListInventory(ctx, "Objects", 1)

An empty path is the root, and depth is how far to descend: 0 is what is
directly in the folder, 1 adds what is in those folders. Folders come
back alongside items, in tree order -- each folder immediately followed
by what is inside it -- so a listing shows the shape rather than only
the leaves.

The depth goes on the AIS request, which is the difference between one
round trip and a hundred. Against a real inventory: 25 entries at depth
0 in 260ms, and 766 entries at depth 1 in 630ms. Walking the same tree
a folder at a time is 119 requests and the better part of a minute.

That took a fix underneath. A reply to depth=1 nests each child
folder's own `_embedded` map inside it, and the parser was reading only
the outer one -- so the deeper request cost the extra time on the
server, sent the extra bytes, and threw the answer away. The parse
recurses now.

Assets come by id and type:

    b, err := s.Texture(ctx, id)              // 98k of jpeg 2000, 197ms
    b, err := s.Asset(ctx, id, sl.AssetMesh)

`Asset` is the ViewerAsset capability, which is the content delivery
network: textures, meshes, sounds, animations, clothing. It answers 403
for a notecard or a script, so those are refused here with a message
saying to use `ReadAsset`, which goes over the transfer protocol and
needs to know which item is being asked about rather than just which
asset. Nothing guesses between the two, because a call that is
sometimes an http fetch and sometimes a UDP transfer, with no way to
tell which happened, is worse than being told.

## Two ways to be connected

A session runs against a Backend, and there are two of them:

    s, err := sl.Dial(ctx, "localhost:7807", "example")   // through slgod
    s, err := sl.LoginDirect(ctx, login)              // this process holds it

Everything above that line is the same either way. Through slgod the
session outlives the program, so a client can be restarted, rebuilt and
debugged without the grid noticing, and several programs can share one
avatar. Direct needs nothing set up, and the avatar logs out when the
program exits.

The interface is in this package's own types rather than the protobuf
ones. If it spoke protobuf, the direct backend would have to build
protobuf for a wire it is not using and the server's conversions would
be mirrored here; instead each side converts once, in its own
direction. `pb` appears in exactly one file.

`sl/backend_test.go` runs one suite against both, because two
implementations answering from different sources is exactly the shape
that drifts. It cannot use one avatar for both -- Second Life allows a
single session per account, so an account slgod is holding cannot also
be logged in here -- so it compares what belongs to the region rather
than to either session: id, name, handle, owner, water height. Run it
with SLGO_TEST_ADDR and SLGO_TEST_PROFILE set; without them it checks
the part that needs no grid, including that Session holds a Backend and
has not quietly grown a connection again.

## Answering scripts, and asking the simulator about itself

Three things a probe kept needing.

`Answer` presses a button on a dialog a script put up. `WaitDialog`
catches one, `OnDialog` sees them as they arrive, and the dialogs
already seen count -- a script that opens one the instant it is rezzed
would otherwise be a race nobody can win:

    d, err := w.WaitDialog(ctx, 30*time.Second, nil)
    err = w.Answer(ctx, d, "Beta")

Worth recording where it lands: the reply goes on whichever channel the
script chose, and those are usually negative. Measured against a script
listening on -4242, the reply arrived -- `HEARD [Beta] on -4242` --
while ordinary chat from this client on a negative channel still goes
nowhere. Until that bug is found, this is the way to reach one.

`Features` is one GET that answers what the region supports: whether
mesh may be rezzed, how many attachments and groups an avatar may have,
which voice server runs, and the id of the LSL it implements. The set
of keys is Linden Lab's to grow, so the map is the truth and the named
accessors are conveniences over it.

`LSLSyntax` is the language itself, from the machine that runs it: 519
functions with their arguments, return types, energy and sleep, 1007
constants with their values, 43 events, and the types and control
keywords. For anything checking a compiler against Second Life that is
the oracle -- a function this does not list does not exist here, and a
constant whose value differs is a bug in the compiler:

    s, _ := w.LSLSyntax(ctx)
    s.Functions["llDialog"].Signature()
    // void llDialog(key AvatarID, string Text, list Buttons, integer Channel)

It is half a megabyte, so it is kept and keyed by the id in `Features`.
Asking again costs one small GET for that id and, when it has not
moved, nothing else.

## Scripts asking for permission

llRequestPermissions puts a ScriptQuestion on the wire and then waits.
A client asks for a channel of them and answers on the request itself:

    asks := w.Permissions(0)
    for q := range asks {
        if q.Wants.Any(world.PermissionDebit | world.PermissionTeleport) {
            q.Deny(ctx)
            continue
        }
        q.Grant(ctx, world.PermissionTriggerAnimation)
    }

Grant sends only the bits named, and drops any that were not asked for.
Nothing is granted by default, because some of these bits hand over real
authority: `PermissionDebit` spends the avatar's money,
`PermissionTeleport` moves it, `PermissionTakeControls` reads the
keyboard.

Two things the message names hide, both measured rather than assumed.
There is no ScriptAnswerNo -- refusing is the same message with no bits
set, which is what `Deny` sends. And a request for several things can be
answered with one of them: a script that asked for debit and animation,
answered with animation alone, saw `run_time_permissions` fire with
`mask=16, anim=1, debit=0`. A viewer cannot send that, since its dialog
has one accept button, so it is worth knowing the simulator honours it.

## Saying things on a negative channel

`Say` takes a channel, and a negative one goes a different way:

    w.Say(ctx, "hello", 42)      // ChatFromViewer
    w.Say(ctx, "hello", -7001)   // ScriptDialogReply

ChatFromViewer from this client does not reach a negative channel --
that bug is still open -- and ScriptDialogReply does, with no dialog
needing to have been opened: the simulator checks only that the object
id names something real. Firestorm uses the same trick to report
collisions to scripts.

What it costs, measured: at most 254 bytes, since the template gives
ButtonLabel a one byte length prefix, and no volume, so whispering or
shouting on a negative channel is refused rather than quietly sent at
ordinary range. The reach is chat's reach -- heard at two metres, not
heard with the listener a hundred metres up, heard again when it came
back down.

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
