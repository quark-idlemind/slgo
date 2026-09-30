package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

// aMinuteOfStats is thirty reports two seconds apart, the newest a
// second before read: dilation steady until the last ten seconds and
// then falling, one script-heavy report, and a gap of six seconds in
// the middle where the simulator said nothing.
func aMinuteOfStats(read time.Time) *sl.SimStats {
	s := &sl.SimStats{Handle: 1099511628032, Read: read}
	for k := range 30 {
		if k >= 12 && k <= 14 {
			continue
		}
		dilation := 0.998
		if k >= 25 {
			dilation = 0.998 - float64(k-24)*0.05
		}
		script := 4.5
		if k == 20 {
			script = 18.25
		}
		s.Samples = append(s.Samples, sl.StatSample{
			At: read.Add(-time.Second - time.Duration(29-k)*2*time.Second),
			Values: map[sl.StatID]float64{
				sl.StatTimeDilation: dilation,
				sl.StatFPS:          44.9,
				sl.StatScriptTime:   script,
				sl.StatAgents:       3,
				sl.StatObjects:      6120,
				sl.StatPumpIO:       0.07,
			},
		})
	}
	return s
}

func TestSimStatsPrintsNowAndTheAverages(t *testing.T) {
	x := newTestShell(t)
	x.grid.simStats = aMinuteOfStats(time.Now())

	got := x.do(t, "simstats")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	if !strings.HasPrefix(lines[0], "Test Region: 27 reports over the last 59s, the newest 1s ago") {
		t.Errorf("heading %q", lines[0])
	}
	if f := strings.Fields(lines[1]); strings.Join(f, " ") != "now 5s 15s 30s 60s" {
		t.Errorf("column heads %q", lines[1])
	}
	// The last three dilations are 0.848, 0.798, 0.748; 0.798 is their
	// mean.  What the simulator did not send is a dash, and not a zero.
	for name, want := range map[string]string{
		"dilation":    "0.7480 0.7980 0.9042 0.9480 0.9702",
		"agents":      "3 3 3 3 3",
		"objects":     "6120 6120 6120 6120 6120",
		"script-ms":   "4.500 4.500 4.500 5.417 5.009",
		"physics-fps": "- - - - -",
	} {
		if row := statRow(got, name); row != want {
			t.Errorf("%s is %q, want %q", name, row, want)
		}
	}
	// pump-io-ms is sent and is not in the short list.
	if strings.Contains(got, "pump-io-ms") {
		t.Errorf("the short list has pump-io-ms in it:\n%s", got)
	}
}

func TestSimStatsNamedAllAndGraphed(t *testing.T) {
	x := newTestShell(t)
	x.grid.simStats = aMinuteOfStats(time.Now())

	got := x.do(t, "simstats script-ms agents")
	if n := strings.Count(got, "\n"); n != 4 {
		t.Errorf("%d lines for two statistics, want 4:\n%s", n, got)
	}

	got = x.do(t, "simstats -a")
	for _, want := range []string{"pump-io-ms", "fps", "objects"} {
		if !strings.Contains(got, want) {
			t.Errorf("--all should list %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "physics-fps") {
		t.Errorf("--all listed a statistic the simulator never sent:\n%s", got)
	}

	got = x.do(t, "simstats -g script-ms agents")
	lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
	script, agents := lines[2], lines[3]
	// Thirty columns: the gap is three spaces where reports 12 to 14
	// would be, and the one busy report is the tallest bar.
	graph := func(line string) string {
		i := strings.IndexRune(line, '▁')
		if i < 0 {
			i = strings.IndexRune(line, '▄')
		}
		if i < 0 {
			t.Fatalf("no graph in %q", line)
		}
		return string([]rune(line[i:])[:30])
	}
	if g := graph(script); g != "▁▁▁▁▁▁▁▁▁▁▁▁   ▁▁▁▁▁█▁▁▁▁▁▁▁▁▁" {
		t.Errorf("script graph %q", g)
	}
	if !strings.HasSuffix(script, "  4.500 to 18.25") {
		t.Errorf("script line should end with its range: %q", script)
	}
	if g := graph(agents); g != "▄▄▄▄▄▄▄▄▄▄▄▄   ▄▄▄▄▄▄▄▄▄▄▄▄▄▄▄" {
		t.Errorf("a steady value should be a level line: %q", g)
	}
	if !strings.HasSuffix(agents, "  3") {
		t.Errorf("a steady value's range is the value: %q", agents)
	}
}

func TestSimStatsRefusesAndReports(t *testing.T) {
	x := newTestShell(t)

	if got := x.do(t, "simstats"); !strings.Contains(got, "has not reported yet") {
		t.Errorf("before any report: %q", got)
	}
	x.grid.simStats = aMinuteOfStats(time.Now())
	if got := x.do(t, "simstats lag"); !strings.Contains(got, `no statistic is called "lag"`) ||
		!strings.Contains(got, "dilation, fps, physics-fps") {
		t.Errorf("an unknown name: %q", got)
	}
	if got := x.do(t, "simstats -a fps"); !strings.Contains(got, "two different lists") {
		t.Errorf("--all with a name: %q", got)
	}
	x.grid.simStatsErr = errors.New("the daemon went away")
	if got := x.do(t, "simstats"); !strings.Contains(got, "the daemon went away") {
		t.Errorf("a failure: %q", got)
	}
}

// statRow is the values simstats printed for a statistic, a space
// between each.
func statRow(out, name string) string {
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && f[0] == name {
			return strings.Join(f[1:], " ")
		}
	}
	return ""
}
