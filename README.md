# slgo — Second Life in Go

Second Life without a viewer: `slgod`, a daemon that holds grid
sessions; `slsh`, a shell to drive them from; and the library `slsh` and
everything else here is written on. It began as a Go rewrite of a C
client that is not part of this repository, and most of what is written
down below is still about the wire.

This is a product of AI agentic coding with strong guidance from the human
author of the C client, which was written in the early part of the second decade
of the 21st century.  The slbench and slrun programs, described later in this
document, are based on Go programs written by the same human author in 2026.

**Start at `sl`.** It is the package a program uses, and the only one
most programs need: objects, inventory, chat, teleport, sitting, land,
textures, scripts -- said as calls that return when the thing has been
observed to have happened, rather than as messages sent into a protocol
that mostly does not answer. `slsh` is written on it and so should
anything else be.

    s, err := sl.Dial(ctx, "localhost:7807", "example")  // through slgod
    s, err := sl.LoginDirect(ctx, login)                 // this process holds it

Everything under `sl` is there because `sl` needed it. The bottom of it
is the UDP messages of Linden Lab's `message_template.msg`, as Go
structs that encode and decode themselves.

    go generate ./msg      # fetch the template and regenerate
    go test ./...

The template is Linden Lab's and is not kept here; `go generate` fetches
it from the shipping viewer.  `cmd/msggen`'s documentation says where
else it is published, and why that one is the default.

## Layout

`sl` is the front door and the rest is underneath it. A program that
reaches past `sl` is either watching the wire or handling something `sl`
does not model yet; both are legitimate and neither is the ordinary
case.

    sl/                 the package to write against: everything an avatar can do
    sl/session.go       Dial through slgod, or LoginDirect and hold it here
    sl/backend.go       one interface, two ways to be connected
    sl/object.go        an object: rez it, name it, link it, take it away
    sl/build.go         a multi-prim object in one call
    sl/inventory.go     folders, items, notecards, scripts, task inventory
    sl/list.go          inventory as a filesystem: paths, listings, depth
    sl/query.go         where the avatar is, and what the region has described
    sl/teleport.go      going somewhere else, within a region or across the grid
    sl/worldmap.go      finding a region on the grid's map, by name
    sl/landmark.go      a remembered place: read one, make one, go to one
    sl/parcel.go        the land under the avatar
    sl/sit.go           sitting on a thing, or on the ground
    sl/neighbour.go     the circuits held to the regions around this one
    sl/script.go        run a script and wait for what it said
    sl/image.go         textures as pictures: jpeg 2000 in, png out
    sl/resize.go        getting a picture to a size the grid takes
    sl/shape.go         what a prim is shaped like, packed and unpacked
    sl/objectjson.go    objects in the eLSL simulator's JSON
    sl/touch.go         touching a named point on a named face

    cmd/slsh/           the shell: inventory, the world, and chat
    cmd/slgod/          holds grid connections, serves clients
    cmd/slrun/       runs LSL scripts, prints what they said
    cmd/slbench/      measures what LSL constructs cost in memory
    cmd/slbotd/         attends several avatars, driven by instant message
    cmd/slbotd/chat.go  answering conversation with a local model, and keeping it
    cmd/msggen/         fetches message_template.msg, writes Go
    cmd/slgo-multiattach/  wearing several objects on one attachment point
    cmd/sl-host/        says where slgod is, from the network this machine is on

    agent/              one avatar's connection: login, then the circuit
    agent/profile.go    credentials under ~/.config/slgo
    agent/login.go      login_to_simulator, and the refusals it answers with
    agent/agent.go      the UDP circuit, its handshake, and the message handlers
    agent/caps.go       the seed capability
    agent/eventqueue.go the long poll the UDP-deprecated messages arrive on
    agent/inventory.go  the folder tree, over AIS v3
    agent/objects.go    what the region said about the things standing in it
    agent/regions.go    one object store per region, shared by the agents there
    agent/teleport.go   moving a live circuit to another simulator
    agent/crossing.go   the same move, when the avatar walked over a border
    agent/neighbour.go  child circuits, which a border crossing needs
    agent/parcel.go     what the simulator says about the land
    agent/posture.go    seated or standing, and on what
    agent/appearance.go what each avatar nearby looks like, said once and kept

    client/             attaching to slgod over gRPC, and holding no grid state
    client/xfer.go      the old UDP file transfer, reassembled
    client/transfer.go  the UDP asset transfer, reassembled
    server/             slgod's side: holds the circuits, relays the bytes
    server/lock.go      exclusive use of a named thing, for as long as a client lives
    viewer/             handing a live session over to a real viewer
    auth/               TLS, and mutual authentication between slsh and slgod
    llsd/               LLSD decoding and encoding
    proto/              slgo.proto, script.proto, and the Go they generate
    scripttest/         a script-running backend with no LSL and no grid in it
    internal/session/   get a session, and an object to run scripts in
    internal/slhost/    where slgod is, asking sl-host when it is there
    internal/creds/     who to log in as, when a program logs in itself
    internal/xmlrpc/    the login wire format, read and written
    internal/slots/     the pool of objects a run takes its places from

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

`doc/` describes what is here now:

    doc/guide.md            slgod, slrun and slbench, for a user
    doc/memory.md           how Second Life allocates script memory
    doc/slots.md            sharing the objects that scripts run in
    doc/login-parameters.md what a viewer sends to log in
    doc/im-senders.md       who an instant message says it is from

`handbook/` is the handbook, as web pages: setting slgod, slsh and
slbotd up on one machine or several, and `handbook/slsh-guide.html`,
the user's guide to slsh.  `handbook/bundle.py` folds it into one file.

`doc/history/` is the implementation plans, written before the work and
kept for the measurements folded into them.  A plan says what somebody
meant to build on the day it was written, which is not the same as what
the program does today, so the code and the man pages are the authority
and these are not:

    doc/history/teleport.md        cross-region teleport
    doc/history/neighbours.md      child circuits, and the border that was a wall
    doc/history/sit.md             sitting, and standing up again
    doc/history/parcel.md          parcels: the land under the avatar
    doc/history/landmark.md        landmarks: a place kept, and gone back to
    doc/history/many-avatars.md    several avatars in one slgod
    doc/history/viewer-frontend.md slgod as a viewer frontend
    doc/history/two-viewers.md     two viewers on one slgod session -- never built

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

Everything is little endian except what is in network order: the
packet sequence number, the acknowledgements appended to a packet, a
Low message's number, `IPADDR` and `IPPORT`.

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

A zerocoded message that runs off its end is read as `decodeData`
reads one, because the grid's zero coder sometimes leaves off the tail
of a final run of zeros: a fixed-size field past the end reads as
zeros, and a `Variable` field whose length prefix is past the end is
empty. One thing is not copied: a `Variable` payload that runs past
the end is cut at the last byte there is, where the viewer reads on
past it. The receiver counts these packets in `Stats.Padded`, and
`doc/wire.md` says what was measured and what the viewer does.

Where this package is *stricter* than Linden Lab: a message that is
not zerocoded has no zeros to have lost, so a truncated field in one is
an error here, whereas `decodeData` logs `logRanOffEndOfPacket` and
substitutes zeros. Matching them would buy interoperability on
malformed packets at the cost of turning a real bug into silent zeros,
so this errors instead. And a zerocoded body that would expand past
8,192 bytes, `MaxPacketSize`, is refused before it is read; the
viewer stops expanding there too.

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

Strings are quoted, so nothing is read back as an accidental number or
boolean; numbers, booleans, UUIDs, vectors and quaternions go out bare,
which is why `sequence: 1234` above has no quotes around it.

There is no YAML dependency: the emitter is about three hundred and
fifty lines against a deliberately small subset, and
`TestDumpParsesAsYAML` feeds all 483 messages through a real parser to
check the subset is real -- Ruby's Psych, or python3 with pyyaml,
whichever is on the machine. With neither it skips, so a clean run does
not by itself mean the subset was checked.

## Tests

`go test ./...` covers:

- every one of the 483 messages through a populate/encode/decode/compare
  round trip, plus a re-encode byte comparison
- golden byte vectors hand-derived from `q_UseCircuitCode.c`,
  `q_PacketAck.c` and `q_CompletePingCheck.c`
- `AgentUpdate` byte for byte, in the template's order rather than the
  order the viewer adds its fields in
- message number framing for all 483, and the shape of each priority class
- packet header, appended acks, zero coding round trip, the C's
  extended 256-zero run form, and the ceiling on what a body expands to
- the two forgiveness rules, what a zerocoded message's short tail
  reads as, and that ordinary truncation still errors
- generator parse errors: 40 malformed templates, each expected to fail
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
    group    = Builders
    neighbours= yes

Not every key is part of the login request. `group` is which group the
avatar acts as once it is up, `neighbours` is whether to hold the child
circuits a border crossing needs, and `viewer_password` is what a real
viewer must type to be handed this session -- deliberately not the
account password, since nothing on the grid has ever seen it. `url`,
`channel`, `version`, `mac`, `id0`, `platform`, `platform_version`,
`platform_string` and `options` are the login request's, and are there
for an account that has to look like something in particular.

The login itself is `agent`'s, and a session is three calls:

    acct, err := agent.LoginAs(ctx, "example")
    a, err := agent.Connect(ctx, acct, agent.Options{})
    defer a.Logout(ctx, 10*time.Second)

A program does not normally write that. `agent.LoadProfile` reads a
profile into an `agent.Login`, and `sl.LoginDirect` takes one and does
those three calls with a `Session` on top; `sl.Dial` gets the same
`Session` from a daemon that has already done them.

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
in `platform_string` would be a worse answer than naming none.

A profile may override the first three, and `platform` travels alone:
it is the two DESCRIPTIONS that go together, so `platform_version` and
`platform_string` are filled in from the host only when neither was set
by hand. `address_size` is not a profile key at all -- an unrecognised
key is a hard error rather than a setting that silently does nothing, so
a profile written to name one fails to load.

The login also asks for `extended_errors`, which is what makes a refusal
answerable in code: alongside the sentence meant for a person, the
server names it, and `LoginError.MessageID` and `.MessageArgs` carry
that -- `LoginFailedAccountSuspended` with the `TIME` it ends, rather
than a string to match on.

[doc/login-parameters.md](doc/login-parameters.md) is the full catalogue
of what a viewer sends, read out of the Firestorm source: every
parameter, the options array, the refusal reasons, and which of them we
deliberately do not send.

`agent.Connect` dials the simulator, starts the receiver, sender and
dispatcher, and runs the handshake: `UseCircuitCode` to open the
circuit, then `CompleteAgentMovement`, which the simulator answers with
`AgentMovementComplete`. For the life of the session it answers
`StartPingCheck` and replies to `RegionHandshake`; everything else is
yours through `Handle`.

The XML-RPC is `internal/xmlrpc`, in both directions -- slgod has to
*say* it as well as read it, since a viewer handed a running session
speaks the ordinary login protocol at slgod and expects a
`methodResponse` back. The decoder maps `<int>` to int64, `<struct>` to
a map and so on, and — the part that matters — renders a type it has
never seen as the text inside it rather than failing. That is the exact thing that
stopped the C client logging in when Linden Lab's response grew `<int>`
fields. The test fixture is a real 29KB response from the live grid,
348 members deep, with the identifiers scrubbed.

Nothing the response contains is discarded: `Account.Raw` holds the
whole decoded tree, so a field this code does not model is still
reachable.

## Capabilities and inventory

`agent.Connect` asks the seed capability for `agent.DefaultCaps` and
leaves the result on the agent, because almost everything above the
circuit needs them:

    url, ok := a.Caps().Get(agent.InventoryCap)   // "InventoryAPIv3"

Inventory comes over AIS v3, an HTTPS GET per folder:

    err := a.FetchInventory(ctx, agent.FetchOptions{Concurrency: 8})
    for _, f := range a.Inventory.Children(a.Inventory.Root()) {
        fmt.Println(f.Name, len(a.Inventory.Contents(f.ID)))
    }

Through `sl` the same tree is `ListInventory`, which asks AIS for the
depth it wants rather than walking -- see below.

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

Nothing in `msg` or `agent` keeps per-connection state at package level.
The message registry is read-only after init; the codec's plan cache is
a `sync.Map` filled the first time a message type is encoded, which is
shared safely without being read-only. Everything else -- sockets, sequence numbers,
retransmission queues, duplicate windows, capabilities, inventories,
every counter -- hangs off an `Agent`.

    x, _ := agent.LoginAs(ctx, "example")
    y, _ := agent.LoginAs(ctx, "builder")
    ax, _ := agent.Connect(ctx, x, agent.Options{})
    ay, _ := agent.Connect(ctx, y, agent.Options{})

That is what lets one slgod hold several avatars at once; see
[doc/history/many-avatars.md](doc/history/many-avatars.md).

`TestManySessionsAtOnce` connects five sessions to five simulators
simultaneously and checks each lands in its own region with its own
identity and socket, and that no simulator sees another's circuit.
`TestSessionsHaveSeparateInventories` fetches two trees at once and
checks neither leaks into the other. They exist so that a package level
cache added later fails a test instead of being discovered in
production.

### One object cache per region, not per avatar

Objects are the exception, and deliberately: `agent.Cache` hands out one
store per region and every avatar standing there shares it. What a
region says about its objects -- ids, shapes, appearances, names, and
the local ids most messages use -- is true for everybody there, so a
copy each is three answers to the same question, three sets of the same
few thousand objects, and a separate `ObjectProperties` request for
every name. It is keyed on the region's **UUID** rather than its handle:
a handle is grid coordinates, and Agni and Aditi both have regions at
the same ones.

Sharing also widens what is known. A region describes what is near each
avatar's camera, so avatars in different corners are told about
different things and the union is more than any one heard.

Two things that had to change with it:

  - **The trim answers to every viewpoint.** It used to discard whatever
    was beyond one camera's draw distance. Pointed at a shared store,
    one avatar walking away would throw out what another was standing in
    front of, so each agent registers a viewpoint with `Watch` and an
    object survives if *any* of them can see it. The same applies on the
    way in: the agent that HEARD an update need not be the one near it.
  - **The last one out drops the store.** A region goes on changing with
    nobody in it, and the only notice of an object being destroyed is a
    `KillObject` to the agents present -- so a store kept past the last
    agent fills with things that no longer exist and looks exactly like
    one that is right.

Measured on the beta grid with two avatars in Dovet: both listed the
same objects and flushing through one emptied what the other saw --
1504 each before, 0 each after. One store.

### Telling an attachment from a thing standing there

`objects` says which is which, because the coordinates alone cannot: a
root prim's position is a place in the region, a child's is an offset
from its root, and an attachment's is an offset from the avatar wearing
it. All three used to print as three numbers.

    Kerra Yule
      6bdc7e57-…  Tanagera Design       228, 66, 30
        d7987e57-…  HearthEmbers          offset -29.2, 6.5, 8.1
    Perrick Hobb
      ec247e57-…  HUD control           worn on HUD centre 2

Three levels: who owns it, what they own -- standing here or worn --
and the prims inside each. A region is mostly one person's things at a
time and the owner's name is the longest thing on the line, so it heads
the group instead of repeating down a column.

Whoever nobody has answered for is a group of their own, named
"(owner not known)" and put last, since a heap of things with no name
on it is the least useful thing to read first. A group owned object
heads its group with the group's id: nothing here resolves a group
name, and that it is not a person's name is the useful half of that
answer.

The wearer is named only when it is not the owner. An avatar wears its
own things, so under their own heading it would be the same name twice;
somebody wearing another's is the case worth the words.

`--owner` narrows it to one person:

    objects --owner kerra                 # a pattern, ignoring case
    objects --owner '^Perrick.*Hobb$'     # it is a regular expression
    objects --owner 345e7e57-…            # or exactly whom you mean
    objects --owner perrick Box           # with the name filter as well

A uuid is taken as one because nothing else looks like one, and it is
the only way to tell two residents of the same name apart -- or to ask
about an owner the region has not named. A pattern cannot match a name
nobody has answered with, so those are counted and said rather than
dropped: `3 prims whose owner nobody has named were not matched` is the
difference between "nobody here owns one" and "nobody has said".

Being worn is not a property of the object: what says so is the avatar
it hangs off, which is why the listing walks up the linkset -- a linked
hud is parented to its own root, so every prim of it but the root would
otherwise be listed as standing in the region.

An object is a linkset, and what a person means by one is its root, so
the roots are the listing and `-c` opens them up:

    objects            # a line per object
    objects -c         # its prims too, indented under it

    ec6c7e57-…  TrioBox                  30, 76, 1000
      f3a97e57-…  TrioChild2               offset 3, 0, 0
      95647e57-…  TrioChild1               offset 1.5, 0, 0

Listing every prim by default turned a hundred things into a thousand
lines, most called "Object" and placed at an offset from something the
listing did not name. What is left out is counted rather than dropped
(`20 more prims, not shown: -c lists them`), and a prim whose root was
never described says so, since it has nothing to sit under.

Browsing and searching differ: `objects` shows the objects, `objects
HearthEmbers` shows the prim that was asked for, wherever it is. The
name a person remembers is often on a prim inside, and answering
"nothing matched" about something standing in front of them would be a
lie by omission.

Names cost a second round. An `ObjectUpdate` carries none, so
`AllObjects` asks -- and `RequestObjectPropertiesFamily` is answered for
a root prim and **not** for a child of a linkset. That was the whole
difference between what `objects` could show and what `dump` could:
dump selects. So the resolve now falls back to an `ObjectSelect` for
whatever the family request never answered, which is cheaper than
asking (one message carries sixty-four objects where the family request
carries one), and gives the selection straight back -- a simulator
believes a selected object is being edited, and one avatar's selection
is another's object that will not move.

### What the cache may not do

A cache of a place you are standing in has two ways to lie, and both
were being told:

**It may not invent.** `named` used to create the object it was naming.
A name arrives only because something asked, and an answer can outlive
its object -- so a reply about something already trimmed conjured an
entry with a name and nothing else: no position, no shape, no parent,
listed as a root prim standing at the origin, and never removed because
nothing would ever describe it again. Eight of sixty-six objects in a
live region were exactly that. Naming now annotates what is there and
drops the rest.

**It may not churn.** The grace period for a prim whose root is unknown
was measured from when it was FIRST heard. But a region goes on
describing prims whose roots it never describes to us, and an update
about something unrooted cannot be judged for distance, so it is taken
in -- which meant: drop it after a minute, take it straight back,
forever. Caught by watching one prim across five minutes; the count
oscillated between 55 and 59 and the prim reappeared with its name
gone, which is the signature of a delete and a re-create. Each cycle
cost a fresh name lookup to recover the name. The clock is now the last
mention: a thing the simulator keeps talking about is a thing that is
there.

Related, and found on the way: the trim decided where a child was by
looking for a parent that had no parent of its own. An attachment's
root hangs off an **avatar**, so no prim of a linked attachment could be
placed at all -- every one was an orphan, dropped a minute later and
taken back on the next update. It now walks up to whatever has no
parent, which for an attachment is the avatar wearing it, and that is
where the whole thing is judged to be.

### What is worn is not what the store holds

Sharing forced a fix to something that was already wrong. Every agent in
a region is told about *everyone's* attachments -- that is how a viewer
draws other people's clothes -- so "carries an AttachItemID" never meant
"worn by me", and the receiving stream does not settle it either. Logged
in alone with a private cache, Perrick Hobb's own stream
carried 16 attachments, all of them a passing stranger's and none its
own.

What settles it is the parent: an attachment hangs off the avatar
wearing it. `WornObjects` filters on that once this avatar has been
described, and before then falls back to leaving out only what is known
to be somebody else's -- claiming nothing is worn would be worse than
claiming too much. Both halves were measured: with the fallback alone,
two avatars sharing a region each reported that stranger's hat as their
own; with the parent known, one avatar wearing a HUD saw it and the
other saw nothing.

HUDs are the case that makes this the client's job rather than the
grid's. Ordinary attachments are sent to everybody; a HUD is sent only
to the avatar wearing it, which is why none of that stranger's sixteen
were at points 31 to 38. Sharing a store puts a HUD where another
avatar can read it, so the filter is now what keeps the separation the
protocol used to provide for free.

## Presence

`AgentUpdate` is not optional, in a way that is not obvious. A session
that never sends one is in nobody's interest list: the simulator
streams no `ObjectUpdate` at all, so nothing can be seen, selected or
built on. Rezzing a prim and waiting for it to appear is how that was
found -- the object really was created, and the simulator simply never
mentioned it.

So `agent` sends it, once a second, from wherever the avatar arrived,
and it belongs beside the circuit for the reason the circuit does: it
has to keep being sent, and an attached client that stopped would
silently take object streaming with it. Something that wants to move
the camera, set a draw distance or hold a control flag down uses
`Agent.SetLook`; `agent.Options.Presence` is the interval and a
negative value stops it.

## The event queue

Some messages no longer come over UDP.  The template says so two
different ways and they mean different things: `UDPDeprecated`, which
`ParcelProperties` is, and `UDPBlackListed`, which `TeleportFinish`,
`CrossedRegion` and `EnableSimulator` are.  The generator keeps them
apart.  `EstablishAgentCommunication` is in neither list because it is
not in the template at all -- there is no UDP message of that name, only
the event.  Either way the simulator does not answer them on the
circuit, and they arrive on `EventQueueGet`, a long poll whose replies
carry an id that the next poll acknowledges.

That poll is `agent/eventqueue.go`, beside the circuit, for the same
reason the circuit is there: it has to run continuously, and a client
restart would lose the sequence and drop whatever arrived in the gap.
This is the one place where "do it in the client" does not work.

Most of what moves an avatar arrives this way. `TeleportFinish` and
`CrossedRegion` are how a session learns it is somewhere else, and
`EnableSimulator` and `EstablishAgentCommunication` are how it learns
what is next door -- none of them on the circuit, whatever the template
says.

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

`Session.Build` puts up a multi-prim object in one call, and `slsh
watch` prints what crosses the wire.  Both are client side -- the server
relays bytes and knows nothing about prims or chat.

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
    slsh --version

`--version` says which build this is: the release it was tagged as, or
that it is a development build after one, with the date and hash of
the commit and whether the tree had edits in it.  Every command here
takes it -- `slgod -version` and the rest, which read their flags the
Go way, take either spelling.  It is read from what the Go toolchain
records in a binary built inside a git checkout, so a plain
`go install ./cmd/...` is all it takes.

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
`cp`, `rm`, `find`, `emptytrash`, with `new` and `save` for notecards
and scripts and `get` and `put` for textures.

The region is where the shell has grown most. `where` is the region and
the position, `look` is what the simulator said about the region, `who`
lists the avatars nearest first and `map` draws them. `objects` is what
the region has described standing there and `worn` is what this avatar
is carrying; `rez`, `place`, `move`, `link`, `unlink`, `take`, `wear`,
`detach`, `drop`, `perms`, `texture`, `touch`, `dump` and `reform` do
things to it, and `start` and `stop` run the scripts inside it.

Going somewhere is `tp`, which takes a region name and crosses the grid
to it, or coordinates to move inside this one; `regions` finds a region
on the grid's map by the start of its name. `sit` sits on a thing or on
the ground and `stand` gets up from either. `parcel` is the land under
the avatar, drawn or listed, and `landmark` lists the landmarks in
inventory, says where one goes, makes one and goes to one.
`neighbours` says which circuits are held to the regions around this
one -- which is what walking over a border needs, and is off unless
asked for.

The simulator describes itself with `caps`, `features` and `lsl`.
Talking is `chat`, `say`, `im`, `talk`, `friends`, `lookup`, `offer`,
`give` and `profile`. Anything that wants an answer -- a teleport
offer, a script's dialog, a friendship, an inventory offer, a
permission request, a group invitation -- is counted at the prompt and
listed by `waiting`, and answered with `answer`, `accept`, `decline`,
`no` or `ignore`.

`agents`, `status`, `login`, `logout` and `viewer` are about the daemon
rather than the grid: which avatars it holds, how each circuit is
doing, and where a real viewer can take one over. `watch` prints grid
messages as they arrive. `set` lists the shell's own settings and
changes one, remembering it for the next slsh.

`help` lists the command groups, `help all` lists every command with
what it takes, and `man NAME` is the long description of one. `how`
is for when the name is the thing you do not know: `how do I make
this my home` looks the question up in an index of every command's
usage line and man page, built in when slsh is compiled, and -- with a
small language model set up under Ollama or llama-server -- asks the
model which of the matching commands does it. What the model suggests
is printed only after the shell has checked it: the command and its
flags parsed as the shell would parse them, words from the shell's own
lists (a setting's name, a rating) found on those lists, and a
quotation found word for word in that command's page. With no model it
answers from the index alone. Setting one up, sharing slbotd's
llama-server, and which small models were measured are in the guide
below, under *Finding your way about*.

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
anything; `ls -l` adds the kind, the date, the id and what the owner
may do -- `M`, `C` and `X` for modify, copy and transfer, a `-` for each
it may not. A folder has no date and no permissions, so those columns
hold a `-` rather than collapsing and moving every column after it. Names are not unique -- one folder here holds
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
[handbook/slsh-guide.html](handbook/slsh-guide.html); the daemon and the two
benchmark programs are in [doc/guide.md](doc/guide.md).

## Two clients on one avatar

Several programs attach to one session, and each used to see everything
the grid sent and nothing the others said. That is how the protocol
works rather than an oversight: the grid does not echo your own instant
messages back: a viewer shows your own remarks because it composed
them. So somebody watching through slsh while slbotd answered for the
same avatar saw every reply and none of the questions — a transcript
with one side missing, and nothing to say so.

slgod relays what one client sends to the other clients of that
session, never back to the sender, filtered by the same subscriptions
as the grid's own traffic and by nothing else. It still decodes
nothing: choosing which messages were worth echoing would be the server
learning what an instant message is.

`sl.IM` grew `To`, `Mine` and `Via`, and `sl.Line` grew `Mine`, `Via`
and `Channel`. `Mine` means this avatar sent it, from another client.
**`Conversation()` is false for these**, which is the load-bearing
part: it means "somebody is talking to this avatar", and a line this
avatar sent is not that however much it looks like one. Every program
written before this goes on working, and nothing that answers
conversation can answer itself — which for slbotd with `chat = *` would
not be a display fault but a loop that never ends. `Spoken()` is the
question for showing a conversation; `Conversation()` is the question
for answering one.

## Where slgod is

slgod does not always run on the machine talking to it, and the machine
it does run on moves between networks, so no client hardcodes an
address. In order:

1. `--addr HOST:PORT`, if given;
2. `addr = ...` in `~/.config/slsh/config`, for slsh -- `server` is
   accepted as another spelling of the same setting;
3. what the `sl-host` command prints, if it is on `$PATH`, with port
   7807 joined to it unless it names a port of its own;
4. this machine, when sl-host is not installed.

A rule in sl-host's file may carry a port -- `127.0.0.1:7808` -- and
may be kept for some profiles with `@` words after the address:

    0/0   127.0.0.1:7808   @dev   the development slgod
    0/0   127.0.0.1               this machine

A client tells sl-host which avatar it is about to ask for by running
it with `SLGO_AGENT` set to that profile, or taken out of its
environment when there is none; `sl-host -a dev` asks the same by
hand.  A rule with no `@` is for every profile and for a question that
names none, and the first rule that matches still wins, so a profile's
own rules go above the general ones.  It travels in the environment
and not as a flag because an older sl-host refuses a flag it does not
know, and would take every client down with it.  An older client
handed `host:port` adds `:7807` to it all the same, so rebuild the
clients and sl-host together.

The address a client ends up with also chooses its secret. One shared
secret, `~/.config/slgod/secret`, is the usual case; a slgod started
with a secret of its own (`-secret FILE`, or a `secret` in its `-config`
directory) is reached with a copy kept under the address as dialled --
`~/.config/slgod/secret.127.0.0.1.7808`, then `secret.127.0.0.1`, then
the shared one -- with an IPv6 address's colons turned into dots. So
`localhost:7808` and `127.0.0.1:7808` are two files; doc/guide.md has
the rest.

Not being installed is the ordinary case on a machine that runs its own
slgod, so it is a default rather than a failure. sl-host being there and
*failing* is reported instead:

    $ slsh -c where
    slsh: cannot find the host slgod is on: sl-host failed: exit status 1: no host configured
            --addr HOST:PORT overrides

Falling back to localhost there would turn "sl-host is misconfigured"
into a connection refused against this machine, which points the reader
at the wrong problem entirely.

## Running scripts: slrun and slbench

Two programs that came from the elsl project, where they reached Second
Life through slrund and a viewer. They run on `sl` now, so the only
thing between them and the grid is a session.

    slrun a.lsl b.lsl
    slrun --object "Test HUD" a.lsl
    slbench --code "integer gCNT;"
    slbench bench.lsl
    generate-it | slbench

A script needs an object to run in, so both take some: the shared `auto`
objects the avatar wears, held for as long as the run lasts and taken
all together or not at all, from whichever avatars the daemon holds.
`slrun` also takes `--object` to name one object already in the
region instead, and `--rez` to rez a prim beside the avatar and trash
it afterwards, which `--keep` leaves. `slrun` runs four scripts at
once by default -- 17.5 seconds of scripts in 5.2, measured -- `--jobs`
asks for more, and `--jobs 1` puts them back in the order they were
named. Twelve is the ceiling only when `--agent` names one avatar: the
objects otherwise come from the daemon's whole pool, which is every
avatar it holds.

The contract with a script is one line: it says `DONE` when it has
finished. Without a sentinel there is nothing to wait for but the
clock, and every run costs the whole timeout.

`slrun` prints what each script said, prefixed with the file it came
from, and exits non-zero if any of them would not compile, faulted, or
never finished. `slbench` measures the memory a construct costs by
finding the 512-byte block boundary it crosses; the measurement
machinery is about LSL and not about how a script reaches the grid.

What it is working around -- that a reading is `512·ceil(code/512) +
heap`, that code is 4-aligned, and what a function and a library call
actually cost -- is written up in [doc/memory.md](doc/memory.md), from
measurements against the grid.

What did not come over is what belongs to elsl: `--sim` (the eLSL
simulator), `-O` and `--std` (its compiler's flags), and the
compile-and-compare machinery. The compiler here is the grid's -- the
source goes up through `UpdateScriptTask` and comes back compiled or
refused. slgo does not depend on elsl, and this is where that shows.

What replaced `--sim` is `--backend=HOST:PORT`, which both take: it runs
the scripts through anything speaking the `script.v1` contract -- a
simulator, or a viewer daemon -- instead of Second Life. `slbench`
additionally takes `--test`, which answers that contract from a model in
its own process, so the measurement machinery can be exercised without a
grid at all.

Measured against the grid on 2026-08-06: creating a script item costs
about 8.1s -- the create, the copy into the object, and the settle --
and updating one that already exists costs about 0.9s. That is why both
programs hold one object and one script name for their whole run, and
why every figure that looks like "what compiling costs" has an item
creation hiding in it the first time.

## slbotd

A second daemon, on the other side of slgod from everything else here.
It holds several avatars at once and takes its orders from inside the
world: a trusted avatar sends an instant message beginning with a
colon, and the message is a command.

    :where
    :tp Example Bay 128 64 25
    :as builder place Objects/a lamp
    :slbench --statement "llSin(1.0);"

It logs nobody in and holds no credentials. For each avatar in its
configuration it calls slgod's `Host` and then attaches to the session
slgod is holding, so an slbotd that is killed leaves every avatar
exactly where it was. That division is the whole design: slgod owns the
grid connection and supervises it, and slbotd owns what is done with
it.

The configuration is one file beside the profiles it names,
`~/.config/slgo/slbotd.conf`, in the profiles' own `key = value` form.
It names the avatars to hold, who may send commands, and which external
programs a command may run; `slbotd --check` reads it, says what it
means, and exits without connecting to anything.

    avatar  = example
    avatar  = builder
    trusted = Quark Idlemind
    program = slbench /usr/local/bin/slbench
    alias   = autobench slbench

The commands are a subset of slsh's -- looking, moving, talking,
inventory and building -- plus the two benchmark programs, which stay
separate processes and are handed an argv rather than a command line:
nothing typed into a viewer reaches a shell. Which avatar a run is for
travels in `SLGO_AGENT`, so `:as builder slbench ...` measures as
builder.

`:errors` is what an avatar has to say about itself -- the failures it
has kept, newest last, each with how long ago it was, and `:errors
clear` forgets them. The log file is still the record; this is for the
one person who cannot read it, standing in the virtual world wondering
why the avatar has been quiet. An avatar that has been failing for
hours looks exactly like one with nothing to say.

Three things happen unasked and only three. The avatars are kept
attached -- though a session somebody logged out *on purpose* is left
alone, because slgod refuses to restart one and the refusal is the
point; `:host --force` is how a person says they have checked.
Inventory offered by a trusted avatar is accepted, because an offer
nobody answers stays pending for ever; an offer from anybody else is
left waiting rather than declined, so a person can still answer it from
a viewer. And the first remark of a fresh conversation from somebody
trusted -- after `error-gap`, an hour by default -- is answered first
with one line saying how many things have gone wrong since, so that
finding out does not depend on thinking to ask.

An answer goes back as instant messages, which hold about a kilobyte
each and are throttled per agent, so it is composed in full, cut at a
line ending, and sent as a few messages a quarter of a second apart
with whatever did not fit reported rather than dropped in silence.

An instant message that does not begin with the prefix is not a
command, and by default nothing happens to it. Given a model and a list
of who to answer, it becomes a conversation instead:

    llm-url   = http://127.0.0.1:8080
    chat      = *
    backstory = example /home/you/characters/hobb.txt

The backstory is the system prompt: what the avatar is, read from its
file every turn so that working on a character is an edit and not a
restart. It may name a **directory** instead, and then the avatar can
be told about particular people -- a file named for somebody, lower
case, is used while that person is the one speaking, and `default`
when nothing more specific matches. A line in one of those files that
is nothing but a relative path is the file it names, so a person's
file keeps the character by including it and can put it wherever it
wants. `slbotd --check` assembles them all and says what each came
to. The guide has the rest.

The model is `llama-server` from llama.cpp and is not interchangeable
with anything else that speaks the same chat API, because only half of
what is used is that API. Generation goes through
`/v1/chat/completions`; the kv cache goes through `/slots`, which is
llama.cpp's own, and is what lets a conversation be put down and picked
up again instead of being paid for from the beginning every time.

An avatar with no sense of time invents one -- asked how long it had
been, with nothing to go on, the model said "just a few days" four
times out of four. So a conversation quiet for longer than `chat-gap`
carries the elapsed time on the next remark, in parentheses, where it
is sent but not stored: nothing is forgotten, the kv cache prefix is
untouched, and the record still holds what the person actually wrote.

The conversation is kept as text, which is the record. The kv cache is
an accelerator and is treated as disposable: it is welded to one model,
one quantisation, one context size and one server build, and the server
checks none of that before loading one -- a state from another model
loads cleanly and answers nonsense. So a fingerprint of all four, plus
the backstory, travels with each conversation and a cache whose
fingerprint has moved is not restored at all.

Measured against llama-server b11056, Qwen2.5-0.5B Q4_K_M, on an Intel
i9 with no GPU offload: a 1442 token prompt cost 5.0 s cold, saving the
resulting 1457 tokens took 13.1 ms for 17.9 MB, restoring them into
another slot took 5.4 ms, and the next turn came back in 0.53 s with
1442 of 1461 tokens cached. Picking a conversation up costs about five
milliseconds and saves about five seconds, so what a conversation costs
to carry is the disk it sits on rather than the time to resume it.

Slots are pinned rather than left to the server, since a conversation
that lands somewhere different each turn has its cache somewhere else
each turn; the one that spoke longest ago gives its slot up, and a slot
taken from somebody else is erased first, because two conversations
with one avatar share the backstory as a prefix and the server would
otherwise match it. With a directory of characters they share only the
default, and each conversation's fingerprint carries the character that
person was answered under.

A context window does not hold a conversation that goes on for weeks,
and dropping the oldest exchanges gives an avatar no memory at all. So
at the budget the old turns are compacted rather than dropped: the
model writes a short note and the note replaces them, while the last
few exchanges stay word for word. Folding again merges the note with
whatever has accumulated since. The backstory is not part of this and
cannot be lost to it -- it is read from its file every turn and the
summariser is never given it.

Measured live against a 3B: sixteen turns became `THEM: Quark,
chandlery, upriver / TOPICS: ropes, tide, north berth, salt barge /
OWED: Three coils, Thursday`, the prompt went from 503 tokens to 279,
and the avatar -- asked what it had been given to hold, sixteen turns
after being asked -- answered "Three coils, Quark" out of that note and
nothing else.

Asking for the note in prose does not work and that was measured too:
at 0.5B it returned fragments of the transcript, at 3B it copied the
exchange back verbatim, and both lost the name. Three labelled lines
work, and survive being folded again, which is the property the whole
thing rests on.

An entry with `!` in front of it is somebody the avatar will not
answer, and a refusal beats a permission: `chat = *` with `chat =
!Somebody` is everybody except them. Two avatars one daemon attends
will otherwise talk to each other -- which is not a malfunction, but
never stops, since neither is answering itself and neither gets bored.
Measured before there was a bound: one message typed by hand ran to 26
exchanges in ninety seconds. `chat-own` bounds it, counted afresh
after `chat-own-rest` (30 minutes) of silence between the two, and
`chat-bot` names avatars some other program's model drives, which are
bounded the same way.

A reply is held back until a person could have written it:
`len(arrived)/read-cps` seconds of silence, then `len(reply)/type-cps`
spent sending the same "typing..." notice a viewer sends. The model's
own seconds count towards it, measured from when the remark arrived, so
a reply it laboured over goes out at once. Characters a second rather
than words a minute because that is what you are judging when you set
it, and per avatar, since one may type with two fingers and another
answer the instant they have read it.

Who an avatar will talk to is a list of its own and not a flag on
`trusted` -- driving an avatar and being spoken to by one are different
powers. It is decided by one function rather than a test at the call
site, because the list is the first answer to that question and not the
last one.

See [doc/guide.md](doc/guide.md) for the configuration in full and the
list of commands.

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

## Textures as pictures

    img, err := s.TextureImage(ctx, id)                    // fetch and decode
    b,   err := sl.EncodeTexture(img, sl.TextureOptions{}) // a codestream
    it,  _, err := s.UploadImage(ctx, up, img, opts)       // both, and upload

A texture is a JPEG 2000 codestream, which nothing on a desktop opens,
so `slsh` deals in PNGs at both ends:

    get 46f67e57-7e57-c0de-cb58-aff33c6b2282     # by asset id
    get -o wall.png /Textures/brick              # by inventory path
    get --raw /Textures/brick                    # the codestream, undecoded

    put -N photo.png                             # what it would do, and cost
    put --round up --filter catmullrom photo.png
    put -n "brick wall" -f /Textures/walls brick.png
    put already.j2c                              # uploaded byte for byte
    put -o tried.png --filter nearest photo.png  # to disk, to look at first

`put` reads png, jpeg, gif, bmp and tiff, applies the EXIF orientation a
camera leaves behind, resizes if it must, encodes, and uploads. It names
the item after the file unless `-n` says otherwise. `--round`,
`--round-x` and `--round-y` take nearest, up or down.

`--filter` takes any of imaging's fifteen by name, and `put --filters`
says which to pick -- keyed by **what you are uploading** rather than by
what each filter is, since the person at the prompt has a picture in
front of them and not a signal-processing question:

    a photograph                           lanczos     the default; the best of the slow ones
    pixel art, or an icon with hard edges  nearest     invents no colours, and any smoothing ruins these
    a mask, or anything read as data       nearest     same reason: a blended value is a wrong value
    a diagram, text, or a screenshot       catmullrom  sharp, and quicker than lanczos
    a picture lanczos leaves haloed        mitchell    smooth, with much less ringing at edges
    a big reduction, a quarter or less     box         plain averaging, which is what a reduction that size wants
    something you want softer on purpose   gaussian    blurs as it resamples
    a preview, where speed is the point    linear      fast and unremarkable

The rest are listed after, grouped by family -- the cubics with the
cubics and the windowed sincs with the sincs -- because "try another one
of these" only helps if the alternatives are actually alike. A test
asserts every filter the command accepts appears in the guide, so the
two cannot drift.

Three things about it are because an upload costs money and cannot be
undone.

`-N` does everything except the upload and reports what would have
happened, fee included:

    $ put -N --round up odd.png
    odd.png: 500x333 -> 512x512, 10992 bytes, L$10
    not uploaded: --dry-run

`-o` goes further and writes the result to disk instead of uploading, so
that what the filter and the rounding actually did can be looked at
first. **The extension picks the format** -- png, jpg, gif, bmp, tiff,
or one of the codestream extensions to get exactly the bytes the grid
would store. It is the only thing a person typing a filename has
already said about the format they want, and a flag saying it again is
a flag that can disagree with the name:

    $ put -o nearest.png --round up --filter nearest odd.png
    odd.png: 500x333 -> 512x512, 8781 bytes, L$10
    nearest.png: 512x512, not uploaded

    $ put -o lanczos.png --round up --filter lanczos odd.png
    odd.png: 500x333 -> 512x512, 10992 bytes, L$10
    lanczos.png: 512x512, not uploaded

-- where the 8781 against 10992 is the filter showing up in the
compressed size as well as in the picture. A `.j2c` given to `-o` as a
picture is decoded on the way out, since somebody asking for a PNG of
one wants to see it rather than copy it.

And a file that is already a codestream -- `.j2c`, `.j2k`, `.jpc`,
`.jp2` -- goes up untouched rather than being decoded and re-encoded,
which would cost quality for nothing.

**An asset id is enough.** The content delivery network serves by id
alone, so a texture on somebody else's object -- named nowhere in this
avatar's inventory, belonging to an avatar this one has never met -- is
fetchable the moment its id is known. Tried on the beta grid against
textures owned by two other avatars, including Governor Linden: fetched
and decoded, no permission involved. An id that is no asset comes back
as a **503 from the edge cache**, not a 404.

The codec is [`github.com/mububoki/jpeg2000`](https://github.com/mububoki/jpeg2000),
pure Go with no dependencies of its own. It was chosen by testing rather
than by reading: of the three pure-Go candidates on pkg.go.dev, one
returned a flat grey square for a real Second Life texture -- no error,
right dimensions, nothing in it -- while this one and
`ajroetker/go-jpeg2000` agreed to within a rounding error and each
decoded the other's output exactly. That interoperability is most of
why this is believable, and the rest is that the grid itself accepts
what it writes.

Encoding is lossless at or below `LosslessArea` (128×128, the viewer's
own cutoff) and lossy above it, aiming at `DefaultRatio` -- 8:1, which is
about what Second Life's own textures are: the stock plywood is 98282
bytes for 512×512. Up to five decomposition levels, as the viewer asks
OpenJPEG for, so the grid can serve a lower resolution from a prefix of
the stream -- fewer for an image too small to halve five times, since
asking for more levels than the size allows is an error rather than a
smaller number. A gradient is the case where lossy is *bigger* than
lossless, since the layers cost more than the rate control saves.

`EncodeTexture` does not resize -- an image whose sides are not powers
of two is refused, naming the size it would have to be. Resizing is a
separate call, because it is two decisions and neither is this
package's to make.

`DecodeTexture` is liberal where `EncodeTexture` is strict: any shape
decodes, power of two or not, up to `MaxDecodeSize` -- 4096 -- on each
side. That is the viewer's own `MAX_IMAGE_SIZE`
(`indra/llimage/llimage.h:56`), and by its source the viewer marks a
larger texture missing rather than draw it
(`newview/llviewertexture.cpp:1318`). It is not the grid's upload limit
of 2048, so a texture from a client that ignored that rule still
decodes, and so would a larger one if Linden Lab raised the limit. There
is a limit at all because the codec sizes its buffers from the
codestream's header and sets no bound of its own; by its source, a
header claiming 60000×60000 would have it try for tens of gigabytes. So
the size is read first, by the codec's own `DecodeConfig` -- the header
the decoder would have believed, a second SIZ marker included -- and
anything larger is refused before anything is allocated for the image.

The tiles are counted before that, because `DecodeConfig` itself makes
a record for every tile the header claims: a million tiles of four
pixels, which the codec allows, cost it 226 MB to be asked the size
(measured here, offline, on a crafted header). `MaxDecodeTiles`, 4096,
is a tile of 64 pixels a side over the largest texture decoded; the
viewer's own encoder writes one tile for the whole image. Every SIZ
marker is checked, including one after a tile-part, where
`DecodeConfig` has stopped reading and the decoder takes it afresh.

## Resizing to a size the grid takes

    img = sl.Resize(img, sl.ResizeOptions{
        Filter:     &imaging.CatmullRom,
        Horizontal: sl.RoundUp,
        Vertical:   sl.RoundDown,
    })

    b, resized, err := sl.EncodeResized(img, ropts, topts)   // both at once

Almost nothing anybody wants to upload is already a power of two by a
power of two, so something has to resample. The two decisions are the
caller's:

- **Which way to round**, per axis. `RoundUp` loses nothing and can
  quadruple the texture; `RoundDown` is cheaper and lossier -- 1023
  becomes 512; `RoundNearest` is the viewer's own biased rule and the
  default. The axes are separate because a wide banner may reasonably
  want one thing of its width and another of its height.
- **Which filter.** `sl.Filter` is
  [imaging](https://github.com/disintegration/imaging)'s
  `ResampleFilter`, so every filter that package offers works and none
  needs re-listing here: NearestNeighbor, Box, Linear, Hermite,
  MitchellNetravali, CatmullRom, BSpline, Gaussian, Bartlett, Lanczos
  and the windowed sincs. The default is Lanczos, which is imaging's
  recommendation for photographs -- a texture is resized once and
  looked at for as long as it exists, so the slow good one is right.
  NearestNeighbor is the one to reach for deliberately, for pixel art
  that any smoothing ruins; there is a test that says the two really do
  differ.

The ceiling is not a choice: over 2048 a dimension comes down to 2048
whatever the rounding says, `RoundUp` included, because the alternative
is an image the grid refuses. An image already at an acceptable size is
returned untouched rather than resampled to its own dimensions, which
is neither free nor lossless.

One thing to know about the dependency: imaging's last release is
v1.6.2, from November 2019, and there has been none since -- the
repository is not archived and has 31 issues open, so read that as
stalled rather than as finished. What makes it tolerable is how little
of it is load bearing here: `imaging.Resize` and the filter constants,
plus `Open`, `Save` and `AutoOrientation` where `put` reads a file from
disk and writes one back. Nothing else. Its own dependency is `golang.org/x/image`, which this
module pins to a current version rather than the 2019 one imaging asks
for, since the old one carries decoders with known problems that
nothing here calls but a scanner would still find.

## Uploading a file, and what the grid does to it

    it, res, err := s.UploadTexture(ctx, "a name", "why", folder, j2c)

This is the one asset path that costs money -- L$10 a go up to a
megapixel, more above it, and see the fee table below -- and the only
one where the grid makes the inventory item rather
than the client. `SaveScript` and `SaveNotecard` write to an item that
already exists, and `CreateItem` makes an item with nothing behind it;
neither shape works for a texture, because there is no capability that
fills in a texture somebody else created.

Things are checked before anything is sent, since the capability takes
any bytes at all and **charges before it looks** -- a refusal after that
has still cost the fee:

  - **The bytes are a JPEG 2000 codestream.** Second Life stores
    textures as J2C and converts nothing -- a viewer converts the PNG
    somebody chose before it uploads it. Sending the PNG is accepted,
    charged for, and stored as a texture no viewer can decode.
  - **The dimensions are ones the grid will take.** Read out of the SIZ
    marker, which is where a codestream keeps them; see below.
  - **The folder is named.** The grid would file it itself, but then
    nothing knows where to read it back from, so the system folder for
    the type is looked up and sent.

### What size a texture may be

Measured against the beta grid on 2026-08-09, one upload per row:

| size | verdict |
|---|---|
| 512×512 | uploads |
| 1024×64 | uploads -- **16:1 is fine** |
| 2048×256 | uploads |
| 1×1 | uploads |
| 300×200 | *"Invalid width: Value not a power of 2."* |
| 4096×256 | *"Invalid width: Value too large."* |
| 256×4096 | *"Invalid height: Value too large."* |

So the rule is **each dimension independently a power of two, at most
2048**. Any power of two by any other, not just square or 2:1 -- and
there is no minimum at the grid, whatever the viewer does.

`sl.TextureDim` is the size to resize a dimension to, ported from the
viewer's `LLImageRaw::biasedDimToPowerOfTwo`: the nearest power of two,
biased **downwards**, going up only past 1.75× the power below, since
the bandwidth saved is worth more than the detail lost. 100 becomes 64
and 115 becomes 128. The viewer floors it at 4; the grid does not.

### What it costs

L$10, except above a megapixel:

| area | fee |
|---|---|
| ≤ 1024×1024 | L$10 |
| > 1024×1024 | L$50 on the beta grid |

`Upload.Cost` is chosen from the size for that reason. It is checked,
not believed -- a 2048×2048 offered at L$10, L$20, L$30 or L$40 is
refused with *"The server expects a different upload fee"*, and the
refusal is free, which is how the L$50 above was found. The viewer
reads its own figure from the account's benefits package
(`LLAgentBenefits::get2KTextureUploadCost`), so an account whose
benefits differ needs `Upload.Cost` set.

The item is then read back out of that folder before the call returns,
because the capability answers with an id and an id is not evidence
that anything looking for the item would find it.

`folder_id` has to be an LLSD `<uuid>` and not a `<string>`: the
service refuses the same characters sent the other way -- *"Parameter
'folder_id' is `<type 'str'>`, expected lluuid.UUID"* -- while
`UpdateScriptAgent` takes its `item_id` either way. That is what
`llsd.UUID` is for. The strictness is per service and cannot be
guessed.

### What comes back is not quite what went up

Measured on the beta grid on 2026-08-09, by downloading the default
plywood texture and uploading those same bytes back -- a real
codestream, so no encoder is needed to test the path:

  - The stream returns **byte for byte identical except for its comment
    marker**, which Second Life rewrites from whatever the encoder left
    (`Kakadu-3.0.3`) to a record of its own:
    `a=<uploader>&h=512&z=20260809184539&w=512`. The image is untouched;
    provenance is added. 55 bytes, in this case.
  - **Identical bytes get one asset.** The same file uploaded twice
    produced two inventory items and *the same* asset id, with the `z=`
    stamp of the first upload. Change one character of the comment and
    the id moves. So the asset server deduplicates by content, and the
    timestamp in the comment is when those bytes were first seen -- not
    when this item was made.

`sl/upload_live_test.go` is that round trip, gated on
`SLGO_TEST_PROFILE` because it spends L$10. Run it against a profile
that names the beta grid's login URI.

## Objects as JSON

    dump "slgo tower"                  # what is there, as the simulator's JSON
    dump -o tower.json "slgo tower"
    rez --at 254,186,22 tower.json     # build what a file describes
    reform "slgo tower" edit.json      # change one to match a file

The format is the eLSL simulator's, from `simulator/primjson.go`, so one
file describes an object to both: elslsim runs it with no grid at all
and `rez` builds the same thing in Second Life. A probe written for one
is a probe for the other, and the difference between what the simulator
does and what the grid does becomes a diff of two files.

A four-prim object, built and read back on the beta grid:

```json
{"name": "slgo tower", "prims": [
  {"type": "box",      "size": [1,1,0.4], "hollow": 0.4, "twist": [0,0.5,0]},
  {"type": "sphere",   "size": [0.7,0.7,0.7], "dimple": [0.25,0.75,0]},
  {"type": "torus",    "size": [0.9,0.9,0.3], "revolutions": 2.0, "taper": [0.3,0.3,0]},
  {"type": "cylinder", "size": [0.2,0.2,1.2], "topsize": [0.1,0.1,0]}
]}
```

**Every position in the file is a region coordinate**, root and
children alike. That is not what the grid says: a child prim's update
describes it in its ROOT's frame -- an offset from the root, and a
rotation relative to the root's -- so `dump` composes the root's frame
back out and `reform` puts it back in. Without that, a three-prim
object two metres across dumps its children as `[2,0,0]` and rebuilds
them at the region's edge. Verified by round trip: a spread-out linkset
described at 254,186,22 / 256,186,22 / 254,186,24 came back at exactly
those three points.

`reform` applies a *partial* description: what a file omits is left as
it is, which is what makes a two-line file an edit rather than a
demolition. Reforming the root above with `{"hollow": 0.75}` raised the
hollow and **kept the twist**. A description with more prims than the
object is refused rather than half-applied -- adding prims is what `rez`
is for.

### What had to be learned to do this

**A prim has no type on the wire.** It has a profile curve and a path
curve, and a sphere is a half-circle swept round a circle. `sl.Shape`
is the translation, and the packing is the viewer's own, from
`indra/llprimitive/llvolumemessage.cpp` rather than guessed:

    begin        round(f / 0.00002)
    end          50000 - round(f / 0.00002)
    scale x, y   200 - round(f / 0.01)
    revolutions  round((f - 1) / 0.015)

Two of those are stored as the distance from the far end, so a zero
byte means *all of it*. Getting either backwards makes a prim that is
inside out rather than one that fails.

**The cut swaps with the path.** A prim swept along a line is cut by its
profile; one swept round a circle is cut by its path, and its profile is
cut by what LSL calls the advanced cut. That is why a sphere's cut is
called a dimple, and the format spells it that way too.

**Rotation and shape were not on the wire between slgod and its
clients.** `ObjectInfo` carried position and scale and neither of those,
so `Seen.Rotation` was a field nothing ever filled. Both are there now,
and the shape crosses in the protocol's own packed units -- unpacking it
at both ends would be the same arithmetic in two places.

## Setting what a face looks like

    texture --id 89556747-… --repeats 4,2 --color 255,80,80 \
            --alpha 200 --fullbright --glow 100 "a sign"
    texture -f 2 --offset 0.25,-0.5 --shiny 3 "a sign"

    w.SetFace(ctx, o, 2, func(f *sl.Face) {
        f.SetRepeats(4, 2); f.SetColour(255, 80, 80); f.SetFullbright(true)
    })

`texture.go` decoded a TextureEntry; `textureset.go` writes one and
sends it as `ObjectImage`. The blob is packed by exception -- a default
value per property, then face sets for the faces that differ -- so the
encoder picks the value most faces share as the default, which keeps a
prim with one odd face down to one exception instead of five. Verified
by round trip through the decoder that already existed, and in world by
asking a script with `llGetPrimitiveParams`.

Two things the format does that surprise:

  - **The colour is stored inverted.** Opaque white is four zero bytes,
    which is why an untouched prim costs nothing to describe -- and why
    forgetting the inversion would make every prim black and invisible.
  - **Bumpiness, shininess and fullbright share one byte.** The setters
    mask rather than assign, so turning fullbright off does not flatten
    the shine.

**One message carries every face**, so a face cannot be changed alone:
`SetFaces` sends the lot. `SetFace` reads the object first and changes
one -- but what it reads is the last appearance the *region* described,
and the region does not describe one the instant it changes. Two
`SetFace` calls in quick succession both start from the appearance
before either, and the second undoes the first. Measured: texturing
every face and then face 2 alone left faces 0 and 1 plain. Build the
faces once and send them with `SetFaces` when making several changes.

### Asking what a face looks like

    texture "a sign"          # every face
    texture -f 2 "a sign"     # one of them

    faces, err := w.Faces(ctx, o)

    a sign, 6 faces
      faces 0-1,3-5  texture none  colour 255,255,255  alpha 255  repeats 1,1
      face 2         texture 89556747-…  colour 255,80,80  alpha 200  repeats 4,2  offset 0.25,-0.5  fullbright  shiny high  glow 100

Given nothing to change, `texture` says what is there instead. Faces
that look alike are printed once under all their numbers, because that
is how a prim usually is -- five sides of a box alike and one different
-- and only what is not plain is mentioned, so what has been done to a
prim stands out from what has not.

Two bugs turned up in the reading, both of which had made everything
look untextured:

  - **A full update carries the appearance too**, and only the
    compressed one was being read. Most prims never get a compressed
    update, so most prims read back as plain white whatever they
    actually looked like.
  - **The compressed update orders the shape fields its own way**: the
    profile curve comes after every path field, where the other three
    messages put it second. Reading it in the familiar order shifted
    everything from `PathBegin` on by a byte -- and the result still
    looked like a prim, so nothing complained: a plain box came back as
    a cylinder with a skew, and a cylinder has three faces, so half the
    box's faces were not reported at all. The fixture that should have
    caught it was all zeros, which read the same however misaligned; it
    now gives every field a distinct value.

## Touching things

    touch "a button"                       # a click, middle of face 0
    touch -f 2 --st 0.9,0.25 "a button"    # a named point on a named face
    touch -H 1.5 "a button"                # held, so touch fires repeatedly

    w.Touch(ctx, o, sl.Touch{Face: 2, ST: msg.Vector3{X: 0.9, Y: 0.25}})
    w.TouchHold(ctx, o, t, 1500*time.Millisecond)

**The raycast is not on the wire.** A viewer works out what was clicked
by casting a ray from the camera through the mouse and then sends the
*answer*: an object, a face index, the intersection point, the normal,
the texture coordinates. The simulator does no geometry of its own --
`send_ObjectGrab_message` packs `pick.mIntersection` straight into the
message, and the sim hands those numbers to the script as
`llDetectedTouchPos` and the rest.

Which makes this **more** precise than a viewer, not less. A test that
must touch the third face nine tenths of the way along an edge does not
have to place a camera and aim; it says so. Verified against a script
in world:

    touch -f 2 --uv 0.9,0.25 --st 0.1,0.2 ...

    start face=2 uv=<0.90000, 0.25000, 0.00000> st=<0.10000, 0.20000, 0.00000>
    start pos=<254.30000, 187.00000, 21.00000> normal=<1.00000, 0.00000, 0.00000>

That shows each field arriving as it was given, and no more: with both
given, it cannot say which is which. That comes from the viewer's
source, not from a measurement. ST is where on the face the ray landed
and UV the same point in the texture after the face's repeats, offset
and rotation: `LLPickInfo::getSurfaceInfo` takes the first from the
raycast and computes the second with `LLFace::surfaceToTexture`
(`newview/llviewerwindow.cpp:7686-7697`, `llface.cpp:904-960`), and
`send_ObjectGrab_message` sends them as `STCoord` and `UVCoord`
(`lltoolgrab.cpp:1200-1202`). So `llDetectedTouchST` is the point on the
face and `llDetectedTouchUV` the point in the texture. Given one, `sl`
works out the other the same way from the face's texture entry; with
planar mapping or a texture animation, where the viewer uses more than
the entry, the one given goes as both.

Three messages, three events: `ObjectGrab` is `touch_start`,
`ObjectGrabUpdate` is `touch`, `ObjectDeGrab` is `touch_end`. A click
sends the first and last, and always lets go, cancellation included --
a grab left open makes the next touch of that object look like a
continuation of this one.

### A drag is points and three durations

    touch --press 0.5 --move 2 --dwell 0.5 "a slider" 0.1,0.5 0.9,0.5
    touch -f 2 --move 1 "a knob" 3:0.5,0.5      # across from face 2 to face 3

    w.Drag(ctx, o, sl.Drag{Points: []sl.Touch{a, b, c},
        Press: 500*time.Millisecond, Move: 2*time.Second, Dwell: 500*time.Millisecond})

Press at the first point, hold still, travel through the rest, rest at
the last, let go. The three durations are separate because a script can
tell them apart -- a menu that opens on a long press and a slider that
follows a drag are watching different halves of one gesture. Two
numbers is a place on a face and three is a place in the region; a
leading `N:` changes face, so a drag can cross from one to another.
Time is split equally between segments, and every named point is always
visited even when the update rate is slower than the path.

### How many touch events a hold actually produces

Not what I assumed, and worth measuring before you count. Holding a
touch on a counting script while varying how fast updates were sent:

| updates/s | 1 | 2 | 5 | 15 | 30 | 45 | 90 |
|---|---|---|---|---|---|---|---|
| 2s hold | | | 45 | 45 | 45 | 45 | 45 |
| 4s hold | 90 | 90 | | | | | 90 |

**The count depends on the duration and on nothing else.** One update a
second and ninety produce the same number of events. So `touch` fires
at 22.5 a second -- half the simulator's 45 fps -- and a client cannot
make a script see more of them by sending more, nor lose any by sending
fewer.

What the update rate *does* control is how finely a **moving** touch is
sampled: how often the script is told the point has changed. A still
hold needs almost none; a drag that has to be followed closely wants
many. The default is 45, which is what a viewer sends.

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
direction. `pb` appears in exactly one file that is not a test.

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
listening on -4242, the reply arrived -- `HEARD [Beta] on -4242`.
ChatFromViewer does not carry a negative channel, and the viewer sends
this message instead, as `Say` does; see "Saying things on a negative
channel" below.

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
        if q.Wants.Any(sl.PermissionDebit | sl.PermissionTeleport) {
            q.Deny(ctx)
            continue
        }
        q.Grant(ctx, sl.PermissionTriggerAnimation)
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

ChatFromViewer does not carry a negative channel, and ScriptDialogReply
does, with no dialog needing to have been opened: the simulator checks
only that the object id names something real. It is what Firestorm
sends for chat typed on a negative channel, with the avatar's own id as
the object -- "Hack: ChatFromViewer doesn't allow negative channels"
(`llfloaterimnearbychat.cpp:939-965`) -- and how it reports collisions
to scripts.

What it costs, measured: at most 254 bytes, since the template gives
ButtonLabel a one byte length prefix, and no volume, so whispering or
shouting on a negative channel is refused rather than quietly sent at
ordinary range. The reach is chat's reach -- heard at two metres, not
heard with the listener a hundred metres up, heard again when it came
back down.

## Not done

Teleport between regions works. `slsh tp REGION [X Y Z]` goes, an
accepted lure is followed rather than fired and forgotten, and the
daemon moves the circuit to the new simulator under everything holding
it: forty moves on Agni at a median of 425ms, capabilities refetched
from the new region's seed and every client told the region changed.
[doc/history/teleport.md](doc/history/teleport.md) is the whole of it, stage by stage.

Walking over a border works, but only with `neighbours` on. A simulator
will not hand an avatar over to a client holding no child circuit to the
region it is walking into, which is why the border used to be a wall: an
avatar walked to Pelmar Reach's west edge, stopped dead at x=0 and stayed
there. Hold the circuits -- `slsh neighbours on`, `slgod -neighbours`,
`neighbours = yes` in a profile -- and the same walk was in Pelmar Mill
four and a half seconds after the key went down, with the region change
reaching a client on the way. Off by default, because it costs a socket
and a share of the traffic per surrounding region and a daemon acting
only where its avatar stands should not be made to pay.

What that still does not do is cross *seamlessly*: `moveTo` dials the
new simulator afresh even when a child circuit to it is already open, so
the capabilities are fetched again and the new region describes itself
from nothing. Promoting the child instead is stage 3 of
[doc/history/neighbours.md](doc/history/neighbours.md), and objects in a neighbouring
region do not reach a client at all -- local ids are the region's own
numbering, and what the daemon says about objects carries no region, so
a client is told only about the one the avatar stands in. An `sl.Object`
does remember which region its local id came from, but only so that one
found before a move is looked up again after it; see
[doc/local-ids.md](doc/local-ids.md).

No appearance is ever sent. What other avatars look like *is* kept --
`AvatarAppearance` is said once and cannot be asked for again, so
`agent/appearance.go` remembers it for a viewer that attaches hours
later -- but this avatar never sends an `AgentSetAppearance` of its own
and is whatever the grid last stored for it.

Textures encode, decode and resize. Sounds, animations and meshes go
up through the same `UploadAsset` and none has been tried.

`Receiver` allocates a message per packet through `New`. At the packet
rates in the C client's stats — 600k in a long session — that is worth
a pool eventually, but not before there is something to measure.

## Licence

Apache License 2.0.  The full text is in `LICENSE`.

Second Life is Linden Lab's; this is an independent client and is not
endorsed by or affiliated with them.

`message_template.msg` is theirs and is deliberately not distributed
here: `cmd/msggen` fetches it from where they publish it.  The generated
`msg/messages_gen.go` is derived from it, and describes their protocol.
