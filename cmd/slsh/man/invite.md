Asks somebody into one of this avatar's groups.  They get an instant
message and decide for themselves; nothing here can make anybody a
member, and nothing here is told what they decided.

The person is read off the front of the line and the rest of it is the
group, so neither name needs quoting:

    invite Example Resident Example Builders
    invite Example Resident 488f7e57-...

The group is named every time and has no default.  It could have meant
whichever group this avatar is acting as, which would be shorter and
wrong the first time somebody typed it after activating something
else -- and what this command does reaches a person who is not at this
prompt.  There is no message that cancels an invitation, so the group
belongs on the line, where it can be read before pressing return.

## Nothing answers an invitation

The request has no reply.  `InviteGroupResponse` exists in the message
template and is a trusted message -- simulator to simulator -- so a
client never sees one.  Measured on Agni on 2026-09-02: an invitation
that arrived at the far end within seconds produced nothing whatever
on the sender's circuit.

So the line printed here says what was sent, in those words, and never
that anybody was invited or joined:

    asked Example Resident into Example Builders (488f7e57-...), in its
    everyone role
    nothing answers an invitation, so this says what was sent and not what
    became of it; they see it as an instant message and it waits for them

Whether they joined cannot be seen from this avatar at all.  The
membership list this shell reads is this avatar's own, and somebody
else's memberships are not in it; the answer to the invitation goes to
the group rather than to whoever sent it.  Asking the person is the
way to find out.

## What the person on the other end sees

An instant message the simulator composes, which their viewer shows as
a dialog and which this shell lists under `waiting`.  Measured, as the
invitee's session printed it:

    1  group       quark.idlemind invites you into a group: "Quark Idlemind
                   (quark.idlemind) has invited you to join a group.\nThere is
                   no cost to join this group.\nGroup:\nQuark Engineering
                   Works: A group for testing slgo's group commands.\n\n" (no fee)
                    answer, no, ignore

The group's name is inside the text the simulator wrote and nowhere
else in the message, which is why the invitation this shell receives
prints as a paragraph rather than as fields.  The inviter appears as a
username -- `quark.idlemind` -- and not as the display name.

It waits for them whether or not they are logged in.  What it does not
do is wait for a shell: an invitation that arrives while no client is
attached to that avatar's session is not in a listing made afterwards,
because `waiting` is a client's own record of what it saw arrive.  Two
invitations sent while nothing was attached were invisible to the next
`slsh` to start.

## What is checked before it goes

Two things, and they are checked because the grid answers both of them
with silence:

- **that this avatar is in the group.**  Only a member invites.
- **that its role there carries the power to invite.**  Membership is
  not the power.  Measured: an ordinary member's everyone role carries
  `0x0000080018010000`, which does not include it, where the group's
  owner carries every bit.

Both refuse before anything is sent, and say which of the two it was:

    slsh: invite: sl: this avatar cannot invite into that group: this avatar
    is in "Example Builders" (488f7e57-...) and its role there does not carry
    the power to invite

    slsh: invite: sl: this avatar cannot invite into that group: this avatar
    is not a member of 93787e57-..., and only a member invites

The membership list they are read out of is the one `group` prints.
It arrives unasked shortly after login, so an avatar that has not been
told yet is allowed to try rather than refused on a list that has not
come.

A group named by a word has to be one this avatar has joined, since the
list is the only place a name can be looked up; a key is taken as
typed.  That is `group`'s rule and this follows it.

## The role

The invitation is into the group's everyone role, which every group has
and which Linden Lab's viewer starts on.  Nothing here offers the
others: reading a group's roles is a request this shell does not make,
and a role named by a key nobody could look up would be worse to type
than none.

## What it costs the person invited

A group may charge to join, and the person accepting pays it -- their
money and their decision.  The fee is stated in the invitation they
see, and this shell says it back to them when they are the one being
invited.  Nothing on this side states it: the invitation is composed by
the simulator, and the sender is told neither the fee nor the answer.

## Examples

    invite Example Resident Example Builders
    invite Example Resident 488f7e57-...

See also: `group` for the groups this avatar has joined and which one
it is acting as, `who` and `lookup` for finding somebody by name,
`offer` for offering friendship rather than a group, and `waiting` for
the invitations that arrive at THIS avatar and how to answer them.
