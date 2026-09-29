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

`AvatarAnimation` -- what the simulator says is playing -- was already
read for posture (`agent/posture.go`, `doc/history/sit.md`), and `sl`
keeps the last list of this avatar's own, but only for the length of a
sit or a stand: the subscription is borrowed for one call
(`borrowAnimations`) because the message arrives for every avatar in
range.  So `StartAnimation` does not confirm that anything played and
there is no call for "what is playing".  Adding one means deciding how
old a list may be before it is stale, which is not small.

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
