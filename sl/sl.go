// Package sl does things in Second Life and waits for them to have
// happened.
//
// It is the layer that knows the protocol so a program does not have
// to.  Below it, msg deals in messages and agent deals in the circuit;
// above it, a program says Build, Run, Say, ListInventory and reads the
// answer as ordinary Go values.
//
// # Why it exists
//
// Almost nothing in this protocol answers the question it was asked.
// Rezzing a prim produces no reply naming the prim.  Taking one into
// inventory produces no reply at all.  A script's output arrives as
// chat, minutes later or never.  Every program written straight against
// the message layer grew its own copy of the same bookkeeping, and each
// copy learned a slightly different subset of the lessons.
//
// So this package owns the reading, and offers calls that return when
// the thing has been observed to happen or say why they cannot tell.
// Rez returns the prim it rezzed, having confirmed we own it.  Take
// returns the inventory item, having found it in the folder.  Run
// returns what a script said, having watched for a sentinel.
//
// What it does not do is pretend the grid is reliable.  Everything here
// takes a timeout, everything can fail, and a call that could not
// confirm what it did says so rather than returning as though it had.
//
// # Two ways to be connected
//
// A session runs against a Backend, and there are two: one talks to
// slgod over gRPC, the other holds the grid connection in this process.
// Everything in this package works the same against either, and a
// program chooses at Dial time and never again.
//
// Through slgod is the better arrangement when it is available -- the
// session outlives the program, so a client can be restarted, rebuilt
// and debugged without the grid noticing, and several programs can
// share one avatar.  Direct needs nothing set up, at the cost of the
// avatar logging out when the program exits.
//
// # What a session keeps
//
// The rule, because getting it wrong is how a client comes to disagree
// with the daemon holding its session:
//
//	A session holds what only it can know -- what it asked for, and
//	what arrived on its subscriptions since it attached -- and asks
//	the backend for everything the grid said once, to whoever was
//	listening at the time.
//
// So the replies to its own requests, the chat and dialogs and
// permission requests it has heard, and caches keyed by something that
// says when they went stale, live here.  The region's objects, who is
// online, where the avatar is standing and what the simulator said in
// the handshake are asked for, every time, because the session may have
// attached long after any of it was said.
package sl
