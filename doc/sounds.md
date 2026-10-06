# The sounds objects play

A script plays a sound with `llPlaySound` or `llLoopSound`, which belong
to the prim (the region sends `AttachedSound`, and `AttachedSoundGainChange`
for `llAdjustSoundVolume`), with `llTriggerSound` or `llTriggerSoundLimited`,
which are one-shots at a place (`SoundTrigger`), and prepares one with
`llPreloadSound` (`PreloadSound`).  A worn object's sounds are sent to its
wearer.  `sl` keeps what it hears of them (`sl/sounds.go`) and Slate reads
it ([expect sound](slate-language.md#expectations)).  Nothing here plays a
sound.  What the viewer does with each message is read from Firestorm
885631b93a; what the region does is measured only where [Measured](#measured)
says.

## What is heard

`Session.Sounds()` is every sound the session still holds, oldest first,
as `HeardSound` values; `SoundsSince(seq)` is what came after the one
numbered `seq`; `SoundsNow()` is the loops in force and the number of the
last sound, taken together, so that `SoundsSince(st.Seq)` is what follows
`st.Loops`.  `ListenForSounds()` asks for the four messages until the
returned func is called.  The log is cut to its last `MaxHeardSounds`
(1024), and a reader that comes round often enough loses none.

Each entry has its kind, the sound's id, the prim and its owner, the gain,
the flags, the time it was heard and its number; a trigger has the place
(the region handle and the position) and the parent object the message
names; and each says whether a loop of its sound is in force on the prim
afterwards (`Looping`) and which loop it ended (`Replaced`).

The viewer reads the messages in `newview/llviewermessage.cpp`:

- `SoundTrigger`, `process_sound_trigger` `:4875`: the sound, owner and
  object ids at `:4893-4895`, then parent, region handle, position and
  gain at `:4927-4930`.  The gain is clamped to 0..1 (`:4931`) and `sl`
  clamps it the same.  The viewer then drops it when its owner or the
  object is muted (`:4944-4947`), when the parcel hides it (`:4941`) and in
  several other cases of its own (`:4898-4925`, `:4957-4978`) and plays it
  at `:4988`.  A trigger has no flags, so it is never a loop.  The viewer
  does not need the object to be known; neither does `sl`.
- `AttachedSound`, `process_attached_sound` `:5053`: sound, object and owner
  at `:5061-5063`, gain and flags at `:5084-5085`, the gain clamped at
  `:5086`.  An object the viewer does not know yet has the message held
  and played when it arrives (`:5093-5099`); `sl` keeps what it is told
  and has no such wait.  The prim's sound is set by
  `LLViewerObject::setAttachedSound` (`llviewerobject.cpp:6607`).
- `AttachedSoundGainChange`, `process_attached_sound_gain_change` `:5110`:
  the object id (`:5116`) and the gain (`:5124`), clamped (`:5125`) and
  applied by `adjustAudioGain` (`llviewerobject.cpp:6723`), which does
  nothing when the prim has no sound.
- `PreloadSound`, `process_preload_sound` `:4992`: one block for each sound,
  object, owner, sound at `:5006-5008`.  The viewer only starts fetching the
  sound (`:5040-5049`); nothing plays.

The flags are `LL_SOUND_FLAG_*` in `llcommon/lldefs.h:128-135`: loop 0x01,
sync master 0x02, sync slave 0x04, sync pending 0x08, queue 0x10, stop
0x20.  `sl` exports them as `SoundFlag*`.

### What ends a loop

`setAttachedSound` (`llviewerobject.cpp:6607-6703`) is the whole rule:

- a null sound id with a loop playing cleans the sound up (`:6614-6629`),
  and with no loop and the stop flag stops it (`:6630-6634`).  So an
  `AttachedSound` with a null id is a stop, whether or not the flag is
  set, for a loop; `sl` records it as `SoundStopped` and names the loop it
  ended in `Replaced`.
- a sound that is not a loop (`:6685`) stops what was playing unless the
  queue flag is set (`:6689-6692`), so a one-shot started on a prim that
  was looping ends the loop; a different loop replaces it.
- a loop asked for with the id it already loops is ignored (`:6656-6662`),
  so `sl` hears no new start and logs nothing.
- a gain change does not end one.  A loop at gain 0 is a loop that is
  silent, and `sl` keeps it in force.
- an object that is killed takes its sound with it (`llviewerobject.cpp:514-521`).
  `sl` ends the loops of a killed object, and those of the region the
  avatar leaves, and logs each as `SoundEnded`.

A one-shot is never in force: `sl` cannot know how long a sound lasts, so
what it can say of one is that it was heard to play.

### What the full object update says

`ObjectUpdate` carries the prim's sound, gain and flags too, and the
viewer reads them (`llviewerobject.cpp:1358-1376`) so as to play a loop that
began before it saw the object.  `sl` reads them for loops only: a looping
sound it did not know starts a loop, and an update without one ends the
loop the prim had.  A non-loop sound in an update is the last sound the
prim was given and not a play, and is not logged, or the same sound would
be heard at every update.  The compressed update, which carries the same
fields packed, is not read.  Updates are always relayed, so this is how a
loop that began before the session listened is known.

## The subscription, and what slgod does

None of the four is in `sl.Subscriptions`: a region sends them for every
sound near the avatar, which a program that reads none should not pay for.
`ListenForSounds` takes them out as a counted subscription, as the sit
borrows `AvatarAnimation` (`Session.borrow`, which the sit and the
account's `UserInfoReply` use too), so overlapping callers share it and
the last to stop gives it back.

`slgod` needs no change.  It relays a message to a client by the name the
client subscribed to, whatever the message is (`Hosted.relay`,
`Client.setSubs`, `internal/server/grpc.go`), keeps nothing of the sounds
and has nothing for the proto to carry; `TestTheSoundMessagesAreRelayedToAClientThatAskedForThem`
checks the four names are relayed to a client that asked and not to one
that did not.  A session not behind a daemon hears everything.

## Not measured

- That the simulator sends a `SoundTrigger` (a script's one-shot at a
  place) and a `PreloadSound` to the avatar: what the viewer's code
  expects; only attached sounds were measured (below).
- Which of the two carries a loop's stop -- an `AttachedSound` with a
  null id, as `llStopSound` is expected to send, or the prim's full
  `ObjectUpdate` -- was not looked at; both are read, and a stop was
  measured arriving (below).
- That the simulator sends a kill for a worn object's sound when the
  object is detached, so that `sl` ends its loop: inferred from the viewer
  destroying the sound with the object.
- That a one-shot attached sound is not also in the full updates of that
  prim.  They are not read for one-shots, whatever they say.
- A loop that began while nothing listened and in a prim whose full update
  `sl` has not heard is not known.

## Measured

6 October 2026, with a worn HUD whose script plays sounds from its
contents for its owner (one-shots at a volume, and a loop with a
duration or stopped on command):

- A one-shot arrived as an attached sound from the HUD, at gain 1, and
  one asked at a volume of 0.3 at gain 0.3.
- A loop arrived as looping from the HUD; asked to run 4 s, it was said
  stopped 4.02 s after it was said looping; stopped on command 3 s in,
  it was said stopped at once.
- So a worn object's sounds reach its wearer, attached sounds and their
  loops' stops among them.
