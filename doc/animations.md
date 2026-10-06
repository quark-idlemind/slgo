# Playing an animation

`Session.StartAnimation` and `StopAnimation` (`sl/animation.go`) play
and stop an animation on this avatar, and `animate` is the shell's
command for them.  What is sent is read from the viewer's source
(Firestorm, `indra/`).  What the simulator does with it is measured only
as far as [Measured](#measured) says; anywhere else, a sentence about the
simulator says what the viewer expects.

## What is sent

One message, `AgentAnimation`, reliably, built as the viewer builds it in
`LLAgent::sendAnimationRequest` (`newview/llagent.cpp:3922`):

- the agent block: this avatar's id and the session id;
- one `AnimationList` block: the animation's id and `StartAnim`, true to
  start it and false to stop it;
- one `PhysicalAvatarEventList` block with empty `TypeData`, which the
  viewer always sends and which this sends too.

The viewer sends nothing before it and waits for nothing after it, and
the simulator has no reply to it, so the calls return when the message
is sent.  The viewer refuses a null id (`llagent.cpp:3924`), and so
does this.

### The id is an asset id

For an animation in inventory the id is the item's asset, not the
item's own id.  The inventory preview's play button
(`LLPreviewAnim::play`, `newview/llpreviewanim.cpp:85-127`) takes
`item->getAssetUUID()` and passes that to `sendAnimationRequest`, and its
stop (`llpreviewanim.cpp:143`) and its close (`:227`) do the same.  So
`animate NAME` looks the item up and sends `Entry.Asset`.  A link in
inventory has an item id where an animation has an asset (see
`linkTarget` in `cmd/slsh/inventory.go`), so a link is followed to the
item first.

Nothing beyond `AgentAnimation` is sent for an inventory animation in
the viewer: no asset request and no permission message.  What the
simulator does when the avatar has never fetched the asset, or the
animation is one the avatar may not use, has not been looked at.

`AvatarAnimation` -- what the simulator says is playing -- is what
[What is playing](#what-is-playing) reads.  `StartAnimation` itself does
not confirm that anything played; that is a later read of it.

## What is playing

`Session.Animations()` (`sl/playing.go`) returns what the simulator last
said this avatar is playing, each as a `PlayingAnimation` (asset id,
sequence number, source object) with the time the list was heard.
`Session.ListenForAnimations()` asks for the message until the returned
func is called.

The state is kept in `sl` only.  `slgod` already relays `AvatarAnimation`
to a client that subscribes to it and keeps nothing of it for the client,
as it keeps nothing of chat; the session does the keeping, and there is
nothing for the proto to carry.  A session behind a daemon is sent the
message only while it has asked for it, because it arrives for every
avatar in range, in full, about every three seconds (see
`borrowAnimations`); `ListenForAnimations` is that same borrowed
subscription, so a sit that overlaps it does not take it away.

What the viewer does with the message is read from Firestorm
885631b93a, `newview/llviewermessage.cpp`, `process_avatar_animation`:

- `:5235` starts the function, which finds the avatar from the sender's
  id (`:5243-5257`);
- `:5265` clears the avatar's `mSignaledAnimations`, so each message is
  the whole list and an animation that has stopped is only missing from
  the next one.  `sl` replaces its list the same way;
- `:5267` is the branch for the viewer's own avatar.  It reads `AnimID`
  and `AnimSequenceID` of each block (`:5273-5274`) and keeps them
  (`:5286`), and then, only `if (i < num_source_blocks)` (`:5299`), the
  `ObjectID` of the source block of the same index (`:5301`), to mark
  the object as an animation source;
- the branch for another avatar (`:5347`) reads the animations and no
  sources (`:5349-5354`).

So a source belongs to an animation by position, and the source list may
be shorter than the animation list: `sl` gives an animation past the end
of it, or with a null id in its place, a zero `Source`.  A session keeps
the list of its own avatar only (`Sender.ID` is this avatar), for the
reason the posture code gives: keeping everyone's would be a list that
grows with the region.

Measured (6 October 2026, a worn object's script playing an animation
on its owner): the list named that object as the source of the
animation, and a list came at each start and stop, about 0.25 s after
it was asked for.  Not measured: that the simulator names no object, or
the null id, for an animation the avatar plays by itself (a stand, a
walk), which is what the viewer's code expects.  A first list can take several seconds to come when the
avatar has not changed what it plays, because that is the resend period,
so `Animations` says when it was heard and has no list before that.

## The built-in animations

`agent.BuiltinAnimations` is the table of every animation the viewer
names in `LLAnimationLibrary`'s constructor
(`indra/llcharacter/llanimationstates.cpp`): 139 of them, the
`ANIM_AGENT_*` ids from the top of that file with the string each is
registered under.  The names are Linden's, in lower case with
underscores, and are matched exactly like every inventory name
(`sl.PickNamedFunc`); the viewer lowercases what it is given before it
looks, and this does not, so `Hello` is refused with `hello` offered.

Every id is in `tools/known-uuids`, with the constant it is.  The seven
that posture uses were in it already, and the table uses those
constants.  One constant in the file, `ANIM_AGENT_BENTO_IDLE`, is
Firestorm's own and is registered under no name, so it is not in the
table.

## One name, two things

`animate NAME` looks in inventory first.  A name that is both an
animation in inventory and a built-in plays the inventory one and says
so, and `--builtin` plays the built-in.  The alternative was to refuse
the name, which leaves a built-in out of reach for anybody whose
inventory has an animation called it, and needs a rename to get past.
A name that several inventory items share is refused with their ids
whatever a built-in is called, as everywhere.  An inventory item that is
not an animation does not count: a notecard called `bow` does not hide
the built-in `bow`.  This is a decision, not a measurement.

## Measured

On Agni on 2026-09-29, with Quark Idlemind through the everyday slgod
and a slsh built from this code, while a second client listened for the
`AvatarAnimation` messages the simulator sends about that avatar:

- `animate dance1` put the built-in's id, `b68a3d7c-…`, in the list of
  animations the simulator said the avatar was playing, in the next
  message about it.
- `animate --stop dance1` took it out of the next list.

An animation from inventory has not been tried.
