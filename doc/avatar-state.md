# What the simulator says about the avatar

Three messages say something about the avatar itself rather than about
the region or the people in it, and each was ignored until now:
`ScriptControlChange` (a script has taken some of the avatar's
controls), `TeleportStart` (a teleport is beginning, and why) and
`HealthMessage` (the avatar's health).  The first changes what a walk
can do, the second says why the avatar was moved, and the third is a
number.

Paths below are under Firestorm's `indra/` unless they say otherwise,
and Firestorm is the reference: this follows it and departs only where
it says so.  Dates are when the measurement was taken, on the main grid.

## ScriptControlChange

### What the viewer does

A script holding the take-controls permission calls `llTakeControls`,
and the simulator tells the avatar's viewer with one `ScriptControlChange`
of any number of blocks, each `{TakeControls, Controls, PassToAgent}`.
`LLAgent::processScriptControlChange` (`newview/llagent.cpp:4532`)
keeps two arrays of counts, one slot for each of the 32 bits
(`TOTAL_CONTROLS`, `llcommon/indra_constants.h:321`): one for controls
taken and not passed on, one for controls taken and passed on.  A take
adds one to each bit named, in the array the block's `PassToAgent`
picks; a release takes one off and stops at zero.  The counts are zeroed
once, where the agent is constructed (`llagent.cpp:519-522`), and in
nothing that happens afterwards: not a teleport, not a region change.

A control taken and not passed on goes to the script and does not move
the avatar; one passed on does both.  The control bits are the
`AGENT_CONTROL_*` constants (`indra_constants.h:323-359`).

### What slgo does

`agent/controls.go` does the same, to the letter: two arrays of 32
counts under a lock, one more on a take, one fewer on a release floored
at zero, never cleared.  `Agent.ScriptControls` gives the two masks, a
bit set while its count is above zero, so two scripts taking a control
and one letting go of it leave it taken.  The masks ride in the
`Presence` answer (`PresenceResponse.script_controls_taken` and
`script_controls_passed_on`, `sl.Presence.ScriptControlsTaken` and
`ScriptControlsPassedOn`), because a client that attached after the take
could not otherwise hear it.

The bits are named beside the walk's in `agent/walk.go`, and
`agent.ControlWords` says a mask in words: forward, back, left, right,
up, down, turn left, turn right, click and mouselook click (the LSL
constants of the same values are `CONTROL_FWD`, `CONTROL_BACK`,
`CONTROL_LEFT`, `CONTROL_RIGHT`, `CONTROL_UP`, `CONTROL_DOWN`,
`CONTROL_ROT_LEFT`, `CONTROL_ROT_RIGHT`, `CONTROL_LBUTTON` and
`CONTROL_ML_LBUTTON`).  A bit with no name is printed as hex.

The consequence is for the walk.  A walk is `AGENT_CONTROL_AT_POS` held
in every `AgentUpdate` (`agent/walk.go`), and if a script has taken the
forward control and does not pass it on, the flag goes to the script:
the avatar would stand still and the walk, left alone, would end
blocked two seconds later without saying why (inferred, not watched).  So `Move` is refused at the start
with `agent.ErrControlsTaken`, as a `Refused` end whose reason is that
sentence, and a walk under way ends `Cancelled` with
`CancelControlsTaken` on the next look, which is a tenth of a second.
Both cross the daemon as the walk's own `Reason` (the same field the
other refusals use), and `slsh walk` prints it.  Only forward matters:
`Face` sends no movement control and is not refused.  A control passed
on still moves the avatar, so it does not refuse a walk.

`slsh where` prints `  controls taken by a script: forward, back` and,
separately, `  controls taken by a script and passed on: ...`, each only
when its mask is not empty.

### Measured

On 2026-09-30, at login, avatars wearing scripted attachments were each
sent one to six `ScriptControlChange` (one avatar six); an avatar
wearing nothing scripted was sent none.  The flags and contents of
those were not captured.

Later the same day, with a box in the region whose script took
`CONTROL_FWD | CONTROL_BACK` without passing them on, once the avatar
had granted it the permission:

- The take came as one `ScriptControlChange` of two blocks: first a
  release of every control (`Controls` 0xFFFFFFFF, `TakeControls`
  false), then the take, whose mask was 0x00180003.  That is forward
  and back with the forward and back nudges, bits 19 and 20, which no
  LSL constant names.  Why the simulator releases everything first is
  not known; it does no harm here, because the counts stop at zero as
  the viewer's do, and a release of what was never taken changes
  nothing.
- `where` then listed the four as taken, and `walk` was refused with
  `ErrControlsTaken`'s words; the avatar did not move.
- The script's `llReleaseControls` came as one block, a release of
  0x00180003.  `where` listed nothing, and the same walk went there and
  back.

Not measured: what the attachments' takes at login were, whether the
counts drift upward across teleports as they would if a region sent its
takes again without releasing the old region's (the viewer has the same
exposure and nothing resets it), and a take arriving during a walk,
which has been exercised against a simulated simulator only.

## TeleportStart

### What the viewer does

`process_teleport_start` (`newview/llviewermessage.cpp:3455`) reads
`Info.TeleportFlags`, logs a warning if a teleport was already in
progress, and uses the flags for one thing: `TELEPORT_FLAGS_DISABLE_CANCEL`
hides the progress bar's cancel button.  If no teleport was in progress
it starts the teleport screen, which is how a teleport nobody asked for
(a kick, a script's `llTeleportAgentHome`) still puts up the progress
bar.  The comment there says the simulator sends two `TeleportStart`
for a landmark teleport.

The flags are `TELEPORT_FLAGS_*` (`llmessage/llteleportflags.h:30-48`):
set home to target, set last to target, via lure, via landmark, via
location, via home, via telehub, via login, via godlike lure, godlike,
911, disable cancel, via region id, is flying, show reset home, force
redirect ("used to force a redirect to some random location -- used when
kicking someone from land"), via global coords and within region.

### What slgo does

`agent/teleportflags.go` keeps the flags of the latest `TeleportStart`
and when it came, and the viewer circuit goes on absorbing
`TeleportStart` and `TeleportProgress` for an attached viewer
(`viewer/circuit.go`): an agent handler is one more reader of what the
session was sent and changes nothing on the way to the viewer.

The next arrival takes the flags and they are spent.  In the order the
cases come:

- **Arrival in another region** (a `TeleportFinish` moves the session):
  `arrive` takes the flags and gives them to `OnRegionChange`, and so
  to the notice the daemon sends every client (`AgentEvent.teleport_flags`),
  `client.RegionChange.TeleportFlags` and `sl.RegionChange`.
- **A start older than a minute** (`agent.TeleportCauseKept`) is not
  attached, only dropped: it was for a teleport that never arrived.
- **A second start for one teleport** replaces the first, so the two
  the viewer's comment says a landmark teleport is sent are one cause,
  spent by its one arrival.
- **The first arrival of a session** takes and discards them, so a start
  sent at login is spent there and cannot be given to a later move.
  Only a start that arrives before that arrival is covered: one after it
  stays until the next teleport's own start replaces it, a crossing
  spends it, or a minute passes.
- **`TeleportLocal`**, a teleport within the region, takes and discards:
  it arrives nowhere else, so nothing is told, but its cause is spent.
  The two came twenty microseconds apart when measured
  ([history/teleport.md](history/teleport.md)).
- **A border crossed** sends no `TeleportStart`, so whatever is kept is
  from something else, and is spent before the crossing arrives.

`TeleportFailed` does not clear them: a teleport that failed leaves its
flags to be replaced by the next teleport's own start, spent by the next
crossing, or dropped after a minute.

`sl` has a constant for each flag (`sl.TeleportViaHome` and the rest),
and `RegionChange.Cause` says the first that matches in a word: `home`,
`lure`, `landmark`, `location`, `god` (either godlike bit), `forced`
(force redirect), in that order, and empty for none or for a flag it does
not name (a telehub, a login, a region by its id).  The order matters
for the one combination that was seen with more than one: the parcel
ejection in [history/landmark.md](history/landmark.md), flags 34848,
force redirect and disable cancel and via home, which is `home`.

`slsh` names the cause in the line it prints for a move nobody typed:
`the avatar is now in Testville (sent home) -- what the last region
described is gone`.  The same line is printed for a `tp` typed at this
shell, so a cause appears there too.

### Measured

On 2026-09-30, at login, avatars wearing scripted attachments were each
sent one `TeleportStart`; an avatar wearing nothing scripted was not.
The flags of those were not captured.  So "a start is sent at login" is
measured only for avatars with scripted attachments, and what it says is
not measured: it is assumed to be via login, and nothing here depends
on it, since the first arrival spends it whatever it says.

Earlier, and in the other documents: a `TeleportLocationRequest` was
answered by a `TeleportStart` with flags 0x10 (via location) about 100
ms later; a `TeleportStart` and a `TeleportLocal` arrived twenty
microseconds apart; and an ejection from land was sent one with flags
34848.  See
[history/teleport.md](history/teleport.md) and
[history/landmark.md](history/landmark.md).

Later the same day, going home with `landmark --home` was preceded by a
`TeleportStart` with flags 32 (via home).

Not measured: that a landmark teleport is sent two.  That is the
viewer's own comment (`llviewermessage.cpp:3467`), and what was recorded
of a landmark teleport ([history/landmark.md](history/landmark.md)) names
one `TeleportStart`.  The handling of two is written for it and tested
with two, not seen.

Not measured: the flags of any other cause (a lure, a god's, a script's
`llTeleportAgent`), whether a `TeleportStart` can follow the arrival it
belongs to, and whether a teleport ever arrives with none.

## HealthMessage

### What the viewer does

`process_health_message` (`newview/llviewermessage.cpp:5113`) reads
`HealthData.Health` and, if the status bar exists, sets its health to the
value cast to a whole number.  It does nothing else with it.

### What slgo does

`agent/health.go` keeps the latest value as it came, with whether one
has come, because 100 is the health of an avatar nothing has hurt and a
session that has heard nothing is not that; zero is a health and not the
absence of one.  Like the viewer it is not cleared by a move.  The two
ride in `Presence` (`health`, `health_known`; `sl.Presence.Health` and
`HealthKnown`).  `slsh where` prints `  health 73%` only when it is
known and below 100, truncated to a whole number as the viewer's status
bar does.

### Measured

On 2026-09-30 every avatar was sent one `HealthMessage` at login, and
for an avatar standing on its own land it read 100.  Going home later
brought another, also 100, about a third of a second after the
`TeleportStart`.

Not measured: what a region with damage enabled sends as an avatar is
hurt, how often, or whether a region without it ever sends one after
login.

## AlertMessage

`AlertMessage` is the simulator's general channel for telling the avatar
something: a refusal to sit, a notice, and the warning that the region
is about to restart.  The text is in `AlertData`; a warning the viewer
acts on also carries `AlertInfo` blocks, each a notification name and
its parameters as serialized LLSD.  For a restart the name is
`RegionRestartMinutes` or `RegionRestartSeconds`, with `MINUTES` or
`SECONDS` in the parameters.  `attempt_standard_notification` in
`newview/llviewermessage.cpp` reads them: at Firestorm 4ae31a7ad6 the
name at :6445, the parameters at :6478, and the countdown from `MINUTES`
or `SECONDS` at :6551-6561 (at d1f415d442, :6424, :6457 and
:6530-6545).

`sl` has always kept the text (`Session.Alerts`, `OnAlert`) and never
read `AlertInfo`.  `agent/alert.go` now logs every alert as it comes,
text and each `AlertInfo` block, quoted:

    alert: "TEXT" info "NAME" "PARAMETERS"

so that the daemon's log has a restart warning on record when one
happens.  Clients are unaffected: they get every alert through the tap,
whatever handles it in the agent.

### Not yet measured

No restart warning has been seen.  No region is available to restart
on demand, so the measurement waits for a region an avatar is in to
restart on its own, for a rolling update or otherwise.  Until
then what a warning carries, how many come and how far apart, and what
follows the last of them, are read from Firestorm and not seen.
