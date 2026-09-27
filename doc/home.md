# Home

Home is a place chosen rather than a place kept. `sl.GoHome` goes there
and `sl.SetHome` sets it, both in `sl/landmark.go`, whose comments say
what each sends. This page is what the grid takes, what it says back,
and how anybody knows it worked.

## What the grid takes

`SetStartLocationRequest`, carrying a location id saying which of the
account's start locations is meant, the position, and the direction the
avatar is facing. The region is not named: the field for it is sent
empty by Linden Lab's own viewer, with the comment "corrected by sim",
and the simulator that receives the message is the region -- which is
the whole reason home can be set nowhere but where the avatar is
standing.

There is also a `HomeLocation` capability, which is what a current
viewer uses where a region offers one; it carries the same three fields
as LLSD and answers with a success flag rather than with an alert. It
is not used, because it is not among the capabilities a session asks
the seed for -- see `agent.DefaultCaps` -- and adding it would put a
second way of doing one thing in the tree. The UDP message was answered
by Agni on 2026-09-01, so there is nothing to fix; if it is ever
stopped, that capability is where to go.

## What the grid says back

An `AlertMessage`, and nothing else: there is no reply to this request
and no field anywhere that says home moved. Both voices were measured
on Agni on 2026-09-01, one avatar, two parcels, minutes apart:

	Home position set.
	You can only set your 'Home Location' on your land or at a mainland Infohub.

So the sentence is the whole of the answer, and the first of them is
the only thing that says it worked.

## How anybody knows it worked

By going there, which is the only check there is: nothing reads home
back, and the alert above is the simulator's word rather than a fact
anything can confirm at the time. Done once, on Agni on 2026-09-01 and
end to end -- home set in one region, the avatar teleported to
another, `GoHome` from there, and it arrived in the region home had
been set in. That is what says the location id `SetHome` sends is the
right one, and it is why nothing reports a home that moved on the
strength of having sent a message.
