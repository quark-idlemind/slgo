package main

// How the region's simulator says it is doing: the last minute of its
// reports, as they are now and averaged over windows back from now.

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

var simStatsCommands = map[string]*command{
	"simstats": {
		params:   "[NAME...]",
		flags:    func() any { return new(simStatsOptions) },
		brief:    "how the region's simulator is doing: time dilation, frame rate, script time, over the last minute",
		keywords: "lag laggy slow performance time dilation fps frame rate scripts script time region health statistics sim load",
		man:      "simstats",
		run:      cmdSimStats,
	},
}

// simStatsOptions is what simstats was asked for.
type simStatsOptions struct {
	All   bool `getopt:"--all -a     every statistic the simulator sends"`
	Graph bool `getopt:"--graph -g   add the minute as a graph, oldest at the left"`
	Help  bool `getopt:"--help -h    show what this command takes"`
}

// simStatsShown are the statistics printed when none are named and
// --all is not given: the ones that say whether the region is keeping
// up, and what it is carrying.
var simStatsShown = []sl.StatID{
	sl.StatTimeDilation, sl.StatFPS, sl.StatPhysicsFPS,
	sl.StatFrameTime, sl.StatScriptTime, sl.StatSpareTime,
	sl.StatScriptsRun, sl.StatScriptEvents,
	sl.StatAgents, sl.StatChildAgents,
	sl.StatObjects, sl.StatActiveObjects, sl.StatActiveScripts,
	sl.StatPacketsIn, sl.StatPacketsOut,
}

// simStatsWindows are the averages printed after the latest value.
var simStatsWindows = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, time.Minute}

func cmdSimStats(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o simStatsOptions
	names, done, err := subOptions("simstats", &o, out, args)
	if err != nil || done {
		return err
	}
	if o.All && len(names) > 0 {
		return usageError("simstats", "--all and a NAME are two different lists")
	}
	var ids []sl.StatID
	for _, n := range names {
		id, ok := sl.StatNamed(n)
		if !ok {
			return usageError("simstats", fmt.Sprintf("no statistic is called %q; they are %s",
				n, strings.Join(sl.StatNames(), ", ")))
		}
		ids = append(ids, id)
	}

	st, err := sh.s.SimStats(ctx)
	if err != nil {
		return err
	}
	if len(st.Samples) == 0 {
		fmt.Fprintln(out, "the simulator has not reported yet; it does every two seconds")
		return nil
	}
	switch {
	case o.All:
		ids = st.IDs()
	case len(ids) == 0:
		ids = simStatsShown
	}

	newest := st.Samples[len(st.Samples)-1].At
	where := "the region"
	if r, err := sh.s.Region(ctx); err == nil && r.Handle == st.Handle {
		where = r.Name
	}
	fmt.Fprintf(out, "%s: %d %s over the last %s, the newest %s ago\n",
		where, len(st.Samples), plural(len(st.Samples), "report", "reports"),
		agoText(st.Read.Sub(st.Samples[0].At)), agoText(st.Read.Sub(newest)))

	width := 0
	for _, id := range ids {
		width = max(width, len(id.String()))
	}
	// A line is written whole: the shell's writer takes each write as
	// a line of its own.
	head := fmt.Sprintf("  %-*s %8s", width, "", "now")
	for _, w := range simStatsWindows {
		head += fmt.Sprintf(" %7s", agoText(w))
	}
	fmt.Fprintln(out, head)

	for _, id := range ids {
		var b strings.Builder
		fmt.Fprintf(&b, "  %-*s", width, id)
		if v, ok := st.Latest(id); ok {
			fmt.Fprintf(&b, " %8s", statText(v))
		} else {
			fmt.Fprintf(&b, " %8s", "-")
		}
		for _, w := range simStatsWindows {
			if mean, n := st.Average(id, w); n > 0 {
				fmt.Fprintf(&b, " %7s", statText(mean))
			} else {
				fmt.Fprintf(&b, " %7s", "-")
			}
		}
		if g := sparkline(st, id); o.Graph && g != "" {
			fmt.Fprintf(&b, "  %s", g)
		}
		fmt.Fprintln(out, b.String())
	}
	return nil
}

// agoText is a length of time in whole seconds, as the windows are
// written: "5s", "60s".
func agoText(d time.Duration) string {
	return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + "s"
}

// statText writes a value in about four figures, which is what a
// time dilation of 0.9967 and an object count of 8652 both want, and a
// whole number as one.
func statText(v float64) string {
	a := math.Abs(v)
	decimals := 0
	switch {
	case a == math.Trunc(a):
	case a < 1:
		decimals = 4
	case a < 10:
		decimals = 3
	case a < 100:
		decimals = 2
	case a < 1000:
		decimals = 1
	}
	return strconv.FormatFloat(v, 'f', decimals, 64)
}

// sparkBars are the heights a graph is drawn in, lowest first.
var sparkBars = []rune("▁▂▃▄▅▆▇█")

// sparkline draws the minute before st.Read as thirty columns of two
// seconds, each the last report in it, scaled from the lowest value to
// the highest, which follow it.  A column with no report is a space, so
// a simulator that went quiet shows as a gap rather than a flat line.
func sparkline(st *sl.SimStats, id sl.StatID) string {
	const cols = 30
	span := time.Minute / cols
	from := st.Read.Add(-time.Minute)

	var vals [cols]float64
	var have [cols]bool
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, p := range st.Series(id) {
		i := int(p.At.Sub(from) / span)
		if i < 0 || i >= cols {
			continue
		}
		vals[i], have[i] = p.Value, true
		lo, hi = min(lo, p.Value), max(hi, p.Value)
	}
	if math.IsInf(lo, 1) {
		return ""
	}

	var b strings.Builder
	for i := range cols {
		switch {
		case !have[i]:
			b.WriteByte(' ')
		case hi == lo:
			b.WriteRune(sparkBars[len(sparkBars)/2-1])
		default:
			h := int((vals[i] - lo) / (hi - lo) * float64(len(sparkBars)-1))
			b.WriteRune(sparkBars[h])
		}
	}
	if l, h := statText(lo), statText(hi); l == h {
		fmt.Fprintf(&b, "  %s", l)
	} else {
		fmt.Fprintf(&b, "  %s to %s", l, h)
	}
	return b.String()
}
