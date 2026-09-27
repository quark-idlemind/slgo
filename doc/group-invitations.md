# Group invitations

A parcel usually grants "create objects" to a group rather than to
individuals, so an avatar that belongs to no group can be locked out of
land its siblings build on. A group whose enrolment is closed can only
be joined by being invited, and an invitation is not a message of its
own: it is an instant message of dialog 3 (`llinstantmessage.h:60`,
`IM_GROUP_INVITATION`). Before `sl/invite.go` the package could see one
arrive and had no way to answer it, so an avatar could be invited and
still never join.

The comments in `sl/invite.go` say what the code does. This page is
why: what the viewer's source says, and what was measured.

## How it was found out

Three things about the message are not what they look like. All three
were read out of the viewer first, because no avatar the daemon held
had the power to invite another and so no invitation could be made to
arrive; they have since been measured, by creating a group on Agni for
the purpose and inviting two of these avatars into it. Where a
paragraph below says what was measured, that is what it means.

## The sender is the group

The sender id IS the group, not the avatar who invited. The viewer says
so where it files one for its spam filter -- "haystack.mOwnerID =
from_id; // group ID" (`llimprocessing.cpp:822`) -- and again where it
builds the answer, taking the group from the sender when the message is
marked as coming from a group and from the offline path's spare id
otherwise (`llimprocessing.cpp:1522`). That spare id is null on the
circuit: it is a defaulted parameter (`llimprocessing.h:55`) and
`process_improved_im` passes nothing for it
(`llviewermessage.cpp:2552`). So for an invitation that arrives while
logged in the sender is the group and nothing else is. Whoever invited
is in the agent-name field, which here holds a person who is not the
sender.

## Nothing carries the group's name

Nothing carries the group's NAME. The name a person recognises the
group by, if it appears at all, is inside the text the simulator
composed; the viewer puts that text into its dialog and shows nothing
else beside it (`args["MESSAGE"] = message`, `llimprocessing.cpp:1529`,
into a notification whose whole body is `[MESSAGE]`,
`skins/default/xui/en/notifications.xml:8727-8731`). The name is not
derivable here either: a group this avatar has not joined is not in
`AgentGroupDataUpdate`, and this package has never asked a group
profile for anything.

## The fee and the role

The fee and the role id are in the binary bucket, as an S32 fee in
network byte order followed by a role id -- twenty bytes, which the
viewer requires exactly and drops the message otherwise
(`llimprocessing.cpp:1502-1518`). The fee is the part that matters,
because accepting spends the person's real money; the viewer stops on
it and asks (`JoinGroupCanAfford`, `llviewermessage.cpp:696-707`).

## The bucket is the one place worth doubting

The comment beside the dialog in `llinstantmessage.h:52-59` describes
an entirely different shape -- a null terminated string of one byte for
officer or member and then the cost in decimal -- and says "ID is the
group id", which the code contradicts twice over. The comment is taken
to be stale: the parsing is what today's viewer runs, and a viewer that
dropped every group invitation on Agni would not have gone unnoticed,
so an invitation there carries twenty bytes.

Measured, in the end: an invitation into a free group read as a stated
fee of zero, and the same group with its signup fee set to L$50 read as
fifty -- so the length is twenty and the four bytes are in network
order, since little-endian would have made that fifty into 838860800.
The doubt is kept anyway. Two grids and one message do not settle a
shape, and a bucket of any other length is still the case worth being
careful about.

So a bucket of any other length is not read as a fee of zero -- it is
recorded as a fee the message did not state, which a caller can be
told about, rather than a number that would be wrong in the direction
of money.

## Answering

Accepting sends an ordinary instant message back to the group id with
dialog 35, and declining with 36 (`llinstantmessage.h:151-152`),
quoting the invitation's transaction id -- `llviewermessage.cpp:736-750`,
the same shape as `DeclineLure` answering a lure by its id.

There are also `AcceptGroupInvite` and `DeclineGroupInvite`
capabilities (`llviewermessage.cpp:713-730`). They are not implemented
because the viewer uses them for one case only: an invitation that
arrived while the avatar was logged out has no transaction id to quote
(`use_offline_cap` is `session_id.isNull() && offline == IM_OFFLINE`,
`llimprocessing.cpp:1526`), and this client asks for offline messages
nowhere, so every invitation it can see arrived on the circuit with a
transaction id of its own. If one ever turns up with a null
transaction it is that offline case, it cannot be answered this way,
and the capabilities are where to look.
