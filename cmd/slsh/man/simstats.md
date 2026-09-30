`simstats` is how the region's simulator says it is doing: whether it
is keeping up, where its time goes, and what it is carrying.  The
simulator reports this every two seconds whether anybody asks or not,
the session keeps the last minute of it, and this prints each
statistic as it is now and averaged over the last 5, 15, 30 and 60
seconds.

    simstats dilation fps script-ms agents
    Testville: 30 reports over the last 59s, the newest 1s ago
                     now      5s     15s     30s     60s
      dilation    0.9981  0.9981  0.9981  0.9561  0.9709
      fps          44.95   44.95   44.95   43.40   43.89
      script-ms    4.212   4.212   4.212   7.958   6.670
      agents           3       3       3       3       3

With no names it prints the ones that say most about lag.  Naming
statistics prints those, in the order given; `--all` prints every one
the simulator sent.  A dash is a statistic the simulator did not
report in that window, which is not the same as a zero.

It answers the same whether the shell logged in itself or is attached
to a daemon, and a shell that attached a moment ago gets the whole
minute: the history is kept by whoever holds the circuit.

## Options

**-a, --all**

Every statistic in the reports, in the viewer's order, where the
list without it is fifteen.  Some of the rest are only interesting to
somebody working on the simulator, which is why they are left out.

**-g, --graph**

Adds the minute after the averages: thirty columns of two seconds
each, the oldest at the left, drawn between the lowest value and the
highest, which follow it.  A column the simulator sent nothing for is
a space, so a region that stopped reporting shows as a gap rather than
as the last value held.  A value that did not change is a level line
and one number.

    simstats -g dilation script-ms
    ...
      dilation    0.9981  0.9981  0.9981  0.9561  0.9709  ██████████████▁▁▂▃▄▄██████████  0.8120 to 0.9981
      script-ms    4.212   4.212   4.212   7.958   6.670  ▁▁▁▁▁▁▁▁▁▁▁▁▁▁█▇▆▅▄▃▁▁▁▁▁▁▁▁▁▁  4.212 to 21.75

Each line is scaled to itself, so a tall bar on one line and a tall
bar on the next are not the same size of thing.

## The names

These are the fifteen printed with no names, and what they measure.
A name ending in `-ms` is milliseconds of a simulator frame, and one
ending in `-pct` a percentage.

| name | what |
|---|---|
| dilation | simulated time per real second: 1 is keeping up, and below it everything in the region runs slow |
| fps | simulator frames a second |
| physics-fps | physics frames a second |
| frame-ms | the whole frame |
| script-ms | the frame's time spent running scripts |
| spare-ms | the frame's time left over; near nothing is a region at its limit |
| scripts-run-pct | the share of scripts that got to run in a frame |
| script-events | script events a second |
| agents | avatars in the region |
| child-agents | avatars in the regions around it who can see in |
| objects | objects in the region |
| active-objects | the ones scripted or moving |
| active-scripts | scripts running |
| packets-in | packets a second the simulator received |
| packets-out | packets a second it sent |

`--all` adds `agent-updates`, the other parts of the frame (`net-ms`,
`sim-ms`, `physics-ms`, `agent-ms`, `images-ms`, `sleep-ms`,
`pump-io-ms`, and the physics step split three ways), pending
transfers, `unacked-kb`, `physics-mb`, pathfinding, and the physics
object counts.  A statistic this build has no name for prints as
`stat-N`, and can be named that way.

## Two seconds apart

The reports are two seconds apart, so the five second average is of
two or three of them and moves in steps.  The averages are there to
tell a moment from a trend: a dilation that is low now and high over
the minute was a spike, and one low in every column is the region.

The minute starts again in each region.  Straight after a teleport or
a crossing there is nothing for two seconds, and then one report.

## Examples

    simstats
    simstats -a
    simstats -g dilation fps spare-ms

See also: `look` for what the region said about itself when the
avatar arrived, its object capacity among it, `status` for this
session's own circuit rather than the simulator, and `who` for the
avatars counted here.
