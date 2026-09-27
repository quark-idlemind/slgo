# When a message's bytes do not add up

What `msg` does with a packet that ends before its message does, or
that expands to more than a packet can hold, and where that follows
the viewer and where it does not.  The last section is which message
template `msg` is generated from, and why.

The viewer's side is read from Firestorm's source, not measured. Line
numbers are in `indra/llmessage/lltemplatemessagereader.cpp` unless
another file is named.

## Past the end

What was measured, in 96f1379: on Agni, every decode failure over two
and a half minutes on three avatars was a zerocoded `ObjectUpdate`
short by exactly 5 or exactly 37 bytes, and every one of them decoded
once given that many zeros back. The simulator's zero coder now and
then leaves off the tail of a packet's final run of zeros. Refusing
those packets threw away every object they described; one was a seat,
which the rest of the store then could not place.

The viewer reads a message in `decodeData` (536), and for every
message, zerocoded or not:

- A Variable block whose count byte is past the end has no instances
  (585-593).
- A field of fixed width that runs off the end is read as zeros, the
  whole field, even when part of it is there (690-699).
- A Variable field whose length prefix runs off the end has length 0
  (654-660).
- A Variable field's payload is not checked at all: it is copied from
  the buffer for as long as the prefix says (683), whatever the buffer
  holds there.

`Unmarshal` (`msg/codec.go`) does the same for a zerocoded message,
with two differences:

- A width that is partly there keeps the bytes that are, and reads the
  rest as zeros. This and the viewer differ only when the bytes that
  are there are not zero.
- A Variable payload that runs past the end is cut at the last byte
  there is, and never padded. That is the owner's decision. The viewer
  copies whatever lies past the end of the packet instead. Padding
  with zeros, which is what slgo did before, made a length of 65,535
  with three bytes behind it into 64 KiB, and a four byte length into
  as much as four gigabytes.

A length prefix that runs off the end leaves nothing behind it to
take, so that field comes out empty, as the viewer's length 0 does.

A message that is not zerocoded has no zeros to have lost, so a short
read in one is an error. Which is which is the template's `Zerocoded`,
not the flag on the packet the message came in.

`Stats.Padded` counts the packets that were read past the end, padded
or cut, once each. Whether the grid ever sends a payload cut short is not
known; that count is where one would show. slgod hands it to a client
as `StatusResponse.padded`, beside `undecodable`, and `slsh status`
prints both on its `decoding` line once either is not nought.

## Zero expansion

The viewer expands a zerocoded packet in `zeroCodeExpand`
(`message.cpp:2840`) into a buffer of `MAX_BUFFER_SIZE`, which is
`NET_BUFFER_SIZE`, 8,192 bytes (`message.h:124`, `net.h:33`). A packet
that would expand past it is reported as `MX_WROTE_PAST_BUFFER_SIZE`
(`message.cpp:2882-2918`) and not read as sent. That buffer holds the
six byte packet header as well as the body (`message.cpp:2869-2873`).

`ZeroExpand` (`msg/framing.go`) refuses a body that would expand past
`MaxPacketSize`, the same 8,192 bytes, counted over the body alone. The
receiver counts such a packet in `Stats.Failed` and delivers it with
its error. Before this it had no ceiling: a count byte of zero stands
for 256 zeros, so 257 bytes could expand to 65,535, and a whole
datagram to about two megabytes.

## Which message template

`cmd/msggen` builds `msg` from the shipping viewer's
`message_template.msg` by default, rather than Linden Lab's master
template or Firestorm's, because it is the only one of the three that
describes what the grid actually sends. The other two each carry a
block that Second Life never puts on the wire.

Measured rather than assumed, because a message can be wrong in a way
that never fails: a trailing Variable block that the sender omitted
decodes as empty and reports no error, so believing the template is
free until the day it matters.

- Firestorm adds Size to MapBlockReply, for OpenSim's variable-sized
  regions. Asking Second Life for a region list and counting the bytes,
  three replies of 4, 28 and 26 regions accounted for every byte with
  none left over: the grid sends no Size block at all.

- The master template adds NewScriptInfo to RezScript, so that a new
  script can start from an inventory item rather than the stock one.
  Nothing in the viewer sends it or mentions the name, and it was added
  in February 2026 and changed again in March -- a field was dropped --
  so it is server-side work the client has not taken up.
