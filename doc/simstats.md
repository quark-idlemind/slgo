# Simulator statistics

A simulator tells each avatar in its region how it is doing, in a
`SimStats` message: time dilation, frame rate, where the frame's time
goes, and how many agents, objects and scripts it is carrying.  The same
message repeats the region's flags and says how many objects the region
can hold.  `agent/simstats.go` keeps the last minute of the reports and
puts the flags and capacity into `Region`, `Session.SimStats`
(`sl/simstats.go`) reads them with averages over a window, and
`simstats` is the shell's command (`cmd/slsh/simstats.go`).

Paths below are under Firestorm's `indra/` unless they say otherwise.

## What the viewer does

`process_sim_stats` (`newview/llviewermessage.cpp:5126`) does two
things with the message.

1. **Each statistic is a sample.**  The `Stat` blocks are pairs of a
   number and a value.  The number is `ESimStatID`
   (`newview/llviewerstats.h:35-78`), and each is handed to the
   measurement registered for it, which the statistics floater draws
   (`newview/skins/default/xui/en/floater_stats.xml`).  The names slgo
   gives them follow that floater's labels.  Firestorm also reports a
   jump in the region's script count to local chat when a setting asks
   it to.
2. **The rest is the region.**  `ObjectCapacity` becomes the region's
   maximum task count.  The flags become its flags, taken from
   `RegionInfo.RegionFlagsExtended` when that block is present and
   from `Region.RegionFlags` when it is not.  If the avatar was flying
   and the region has just become no-fly, the viewer takes it out of
   the air.

The viewer applies what arrives to the region its agent is in,
whichever circuit it came on.

## What slgo does

Only the root circuit's `SimStats` are read.  A child circuit has a
dispatcher of its own that counts what it hears and handles nothing
else (`agent/neighbour.go`).  A report whose `RegionX`/`RegionY` names
another square than the avatar's region is dropped: that is the region
just left, still sending while a move completes.

A report that is kept does two things:

- It sets `Region.Flags`, `Region.Extended` and `Region.ObjectCapacity`,
  so that a change an estate manager makes while the avatar is there is
  seen; the handshake says them once, on arrival.
  `sl/landscripts.go` reads the flags that stop scripts.  Nothing here
  flies, so the viewer's third step has nothing to do.
- It is added to the history: the statistics in the order they came,
  with the time they arrived.  The history covers a
  minute, and a report older than that is dropped.  It belongs to one
  region: a report from another starts it again, and until one comes
  the history reads as empty.

slgod hands the history to a client with each report's age rather than
its time (`SimStats` in `proto/slgo.proto`).  A client on another
machine then places the reports by its own clock, and a skew between
the two clocks moves nothing.

`SimStats.Average(id, over)` is the mean over the reports that arrived
in the window ending when the history was read, along with how many
there were.  A window with no reports in it has no mean, rather than
the last value seen, so a simulator that stops reporting is not
mistaken for one that has held steady.

## Measured

On the main grid on 2026-09-30, watching `SimStats` on a daemon's root
circuit with `slsh watch`:

- **Every two seconds.**  Twenty reports in forty seconds, spaced 2.00
  to 2.03 s apart.  So a minute holds thirty reports, and the shell's
  five second average is of two or three of them.
- **`RegionX` and `RegionY` are grid squares, not metres.**  They are
  the numbers `msg.RegionHandle` takes, four digits long, where the
  region's corner in metres is 256 times that.  With this tree's daemon
  the reports were kept, which they would not be if the square worked
  out from them did not match the avatar's region handle.
- **Both flag fields held the same value**, and the `RegionInfo` block
  was present in every report.
- **Thirty-five statistics:** ids 0 to 15, 17 to 20, 24 to 35 and 38 to
  40.  16 (LSL instructions a second), 21 to 23 (process size, pending
  local uploads), and 36 and 37 (which the viewer's header marks
  "dataserver only") did not come.
- The counts of active objects and active scripts came with fractional
  parts, which suggests the simulator averages them over its own
  interval.  That is an inference.

Not measured: whether a child circuit is sent `SimStats` for its
region at all.  It makes no difference to what is kept, since only the
root's are read.

Afterwards the whole path was run against the live grid with this
tree's slgod and slsh: `simstats -g` printed the minute with all five
columns and the graph, `look` printed the capacity, and `SimStats` was
gone from the list of messages `status` has no handler for.

## What a region says about itself (RegionInfo)

`SimStats` repeats a region's flags and nothing else about it.  The rest
-- the estate, how many avatars and objects it may hold, how high its
water is, how far it lets the ground be raised and lowered, its object
bonus, how far chat carries -- comes in `RegionInfo` (Low 142), and a
region sends one when asked with `RequestRegionInfo` (Low 141, an
`AgentData` block and nothing else).  `agent/regioninfo.go` keeps the
last one, `Session.RegionDetails` (`sl/regioninfo.go`) asks and waits,
and `look` prints some of it.

### Measured

On 2026-10-02, through a daemon, an ordinary avatar -- not an estate
manager, on Linden mainland -- sent `RequestRegionInfo` and was answered
with a `RegionInfo` in 115 ms.  Every block was present: `RegionInfo`,
`RegionInfo2`, one `RegionInfo3` (the extended flags), one `RegionInfo5`
(the three chat ranges and offsets, and the chat flags) and one
`CombatSettings`, and `MaxAgents32` carried the same agent limit as the
U8 `MaxAgents`.  So asking needs no estate rights.  Not measured: how
the answer differs for an estate manager, whether the message ever
arrives unasked, and whether `MaxAgents32` ever differs from
`MaxAgents` in another region.  No value from that reply is written down here.

### What the viewer does

- **It asks when the Region/Estate floater opens.**
  `LLFloaterRegionInfo::requestRegionInfo`
  (`newview/llfloaterregioninfo.cpp:374-395`) sends `RequestRegionInfo`,
  with the comment "Must allow anyone to request the RegionInfo data so
  non-owners/non-gods can see the values".
- **It takes every `RegionInfo` that arrives**, asked for or not:
  `LLViewerRegion::processRegionInfo` (`newview/llviewerregion.cpp:1153`),
  registered at `newview/llstartup.cpp:3810`, hands it to
  `LLRegionInfoModel::update` (`newview/llregioninfomodel.cpp`) and the
  two floaters.
- **`update` starts from nothing** and reads the name, both estate ids,
  `SimAccess`, the U8 `MaxAgents`, the object bonus, billable factor,
  water height, terrain limits, price per metre, the redirect grid and
  the estate sun.  `HardMaxAgents` comes from `RegionInfo2`; the
  viewer's default of 100 stands when it is not read.  The flags are
  `RegionFlagsExtended` when a `RegionInfo3` block is present and the
  plain `RegionFlags` otherwise.  The chat block is read and only
  logged.  The product name is kept when the SKU or the name is
  non-empty.
- **It never reads `MaxAgents32` or `HardMaxObjects`.**  Nothing in the
  viewer does; the floater's agent limit is the U8.

### What slgo does

- **Every `RegionInfo` on the root circuit is kept**, decoded, with the
  time it arrived, and a count of how many this session has heard.
  Only the latest is kept.  The message does not name its region, so it
  is kept as the one the avatar is in when it arrives; a reply to a
  request made just before a move could therefore be filed under the
  next region.  That is inferred, not measured.
- **A region change leaves it not current.**  As with the statistics,
  what is kept belongs to a handle, and `Agent.RegionInfo` returns
  nothing when the avatar is in another region.
- **Flags** are as the viewer reads them: `Extended` is the
  `RegionInfo3` value when there is one and the plain flags widened
  when there is not.  `Chat` is absent without a `RegionInfo5`.
- **The agent limit is the U8 `MaxAgents`**, as the viewer reads it;
  the agent keeps `MaxAgents32` beside it, unused, and in the one region
  measured the two were the same.
- **`Session.RegionDetails` asks and waits for a reply heard after the
  question**, counted rather than timed, so a message from before it
  does not satisfy it and neither clock matters when the session is
  hosted.  It waits `Options.RegionInfoTimeout`, 15 s by default: the
  one reply took 115 ms and this is a wide margin, as the other
  defaults are.  After that it returns `ErrTimeout`, and never the
  old message.
- **slgod serves the last one** as `RegionDetails` in
  `proto/slgo.proto`, with its age in milliseconds rather than a time.
- **`look`** asks, and prints the agent limit, object bonus and
  terraform limits beside what the handshake gave (water, access,
  product).  It waits 3 s for the answer, against the 115 ms measured,
  and when the region does not answer in that time it says so on a
  `details` line and prints everything else.
