# Which packets are duplicates

A reliable packet whose acknowledgement goes missing arrives again, and
a message acted on twice is a chat line heard twice or a movement
replayed.  So the dispatcher drops a retransmission of a packet it has
already handled (`msg.Dispatcher`, `duplicate` in `msg/dispatch.go`).
Which packets count as one is the viewer's rule.

## The viewer's rule

Firestorm at revision 885631b93a, `indra/llmessage/`:

- A packet is dropped as a duplicate only when it is flagged RESENT and
  its number is among those the circuit has recorded
  (`message.cpp:602-605`, `LLCircuitData::isDuplicateResend` at
  `llcircuit.cpp:608`).
- Only reliable packets are recorded, when they are first handled
  (`message.cpp:710-713`).
- The record belongs to a circuit, found by the sender's address, so
  two simulators never share one.
- A retransmission is flagged RESENT by its sender (`llcircuit.cpp:354`);
  slgo's own sender does the same (`msg/send.go`).

## What slgo did, and does

slgo remembered the number of every packet that carried a message,
reliable or not, and dropped any later packet under a remembered
number, flagged or not.  So it could drop a packet the viewer would
handle: one that reused a number without the RESENT flag, which is a
peer whose numbering started again rather than a retransmission.  That
happened in a test (a new region's `AgentMovementComplete` after a move,
under a number a packet from the region left had used) and is the
reason a move, and a replaced viewer, forget the record.  It has not
been seen on the grid.

Now the dispatcher follows the viewer: it remembers only reliable
packets, and drops only a packet flagged RESENT under a remembered
number.  An unflagged packet under a remembered number is handled.
Each circuit has its own dispatcher, and a move still forgets the record
on the new simulator's first packet, as before.

## Measured

On 2026-10-03, with an avatar logged in directly and every packet
classified before the duplicate check: 10 minutes standing in a region,
35,330 packets, 20,833 of them reliable, none flagged RESENT, and
nothing dropped by either rule.

Over the 5 h 45 min before that, the everyday slgod's three other
sessions took in about 2.7 million packets and the old rule dropped 2.
Whether those two were flagged RESENT was not recorded.

So on a healthy circuit the two rules agree; they differ only where a
number is reused, which the measurement did not see happen.
