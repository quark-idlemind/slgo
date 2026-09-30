package sl

import (
	"testing"
	"time"
)

// twoSecondly is a minute of reports two seconds apart ending a second
// before read, as a simulator sends them, with dilation falling by a
// hundredth each time from 1.
func twoSecondly(read time.Time) *SimStats {
	s := &SimStats{Read: read}
	for k := 0; k < 30; k++ {
		s.Samples = append(s.Samples, StatSample{
			At: read.Add(-time.Second - time.Duration(29-k)*2*time.Second),
			Values: map[StatID]float64{
				StatTimeDilation: 1 - float64(k)/100,
				StatAgents:       3,
			},
		})
	}
	return s
}

func TestAnAverageIsOverTheSamplesInItsWindow(t *testing.T) {
	read := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s := twoSecondly(read)

	// The newest is 1 s old and they are 2 s apart: 5 s holds those at
	// 1, 3 and 5 s, which are the last three.
	for _, tc := range []struct {
		over time.Duration
		n    int
	}{
		{5 * time.Second, 3},
		{15 * time.Second, 8},
		{30 * time.Second, 15},
		{time.Minute, 30},
	} {
		mean, n := s.Average(StatTimeDilation, tc.over)
		if n != tc.n {
			t.Errorf("over %v: %d samples, want %d", tc.over, n, tc.n)
			continue
		}
		// The last n values are 1-k/100 for k from 30-n to 29.
		var want float64
		for k := 30 - tc.n; k < 30; k++ {
			want += 1 - float64(k)/100
		}
		want /= float64(tc.n)
		if d := mean - want; d > 1e-9 || d < -1e-9 {
			t.Errorf("over %v: mean %v, want %v", tc.over, mean, want)
		}
	}

	if mean, n := s.Average(StatScriptTime, time.Minute); n != 0 || mean != 0 {
		t.Errorf("a statistic no sample has: %v over %d", mean, n)
	}
	if v, ok := s.Latest(StatTimeDilation); !ok || v != 0.71 {
		t.Errorf("latest %v %v, want 0.71", v, ok)
	}
	if _, ok := s.Latest(StatScriptTime); ok {
		t.Error("a latest value for a statistic no sample has")
	}

	series := s.Series(StatTimeDilation)
	if len(series) != 30 || series[0].Value != 1 || series[29].Value != 0.71 || !series[29].At.Equal(read.Add(-time.Second)) {
		t.Errorf("series of %d from %v to %v", len(series), series[0], series[len(series)-1])
	}

	if ids := s.IDs(); len(ids) != 2 || ids[0] != StatTimeDilation || ids[1] != StatAgents {
		t.Errorf("ids %v", ids)
	}
}

// TestAWindowWithNothingInItSaysSo: a simulator that has stopped
// reporting has no five second average, rather than the last one it
// had.
func TestAWindowWithNothingInItSaysSo(t *testing.T) {
	read := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s := twoSecondly(read.Add(-20 * time.Second))
	s.Read = read
	if _, n := s.Average(StatTimeDilation, 5*time.Second); n != 0 {
		t.Errorf("%d samples in the last 5 s of a quiet 20 s", n)
	}
	if _, n := s.Average(StatTimeDilation, 30*time.Second); n != 5 {
		t.Errorf("%d samples in the last 30 s, want the 5 before it went quiet", n)
	}
}

func TestEveryStatisticHasANameThatReadsBack(t *testing.T) {
	for _, name := range StatNames() {
		id, ok := StatNamed(name)
		if !ok || id.String() != name {
			t.Errorf("%q reads back as %v, %v", name, id, ok)
		}
	}
	if id, ok := StatNamed("stat-99"); !ok || id != 99 || id.String() != "stat-99" {
		t.Errorf("an unknown number: %v %v", id, ok)
	}
	for _, bad := range []string{"", "Dilation", "stat-", "stat-1x", "stat-01"} {
		if id, ok := StatNamed(bad); ok {
			t.Errorf("%q was read as %v", bad, id)
		}
	}
}
