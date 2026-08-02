# slgo — Second Life message layer in Go

The first piece of a Go rewrite of the C client one directory up: the
483 UDP messages in `message_template.msg`, as Go structs that encode
and decode themselves.

    go generate ./msg      # regenerate msg/messages_gen.go
    go test ./...

## Layout

    cmd/msggen/         reads message_template.msg, writes Go
    msg/types.go        UUID, Vector3, Quaternion, IPAddr, Info, ...
    msg/buffer.go       little endian read/write primitives
    msg/codec.go        the generic tag-driven encoder and decoder
    msg/framing.go      message numbers, packet header, zero coding
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

## Not done

This is the message layer only. There is no session, no circuit, no
retransmission, no XML-RPC login and no AIS. The zero coding and packet
header helpers are here because a message codec you cannot point at a
real packet is hard to trust, not because the packet layer is finished.
