# When a message's bytes do not add up

What `msg` does with a packet that ends before its message does, and
where that follows the viewer and where it does not.

The viewer's side is read from Firestorm's source, not measured. Line
numbers are in `indra/llmessage/lltemplatemessagereader.cpp` unless
another file is named.

## Past the end

What was measured, in 96f1379: on Agni, every decode failure over two
and a half minutes on three avatars was a zerocoded `ObjectUpdate`
short by exactly 5 or exactly 37 bytes, and every one of them decoded
once given that many zeros back. The simulator's zero coder now and
then leaves off the tail of a packet's final run of zeros. Refusing
those packets threw away every object they described.

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
with two differences. A message that is not zerocoded has no zeros to
have lost, so a short read in one is an error.

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

`Stats.Padded` counts the packets that were read past the end either
way, once each. Whether the grid ever sends a payload cut short is not
known; that count is where one would show.
