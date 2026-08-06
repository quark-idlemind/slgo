package main

import "testing"

// The one behavioural difference from the original: autobench/bench returned
// only RESULT: lines, with the marker already stripped. slrun/bench returns
// EVERYTHING a script says, so this program strips the marker and skips
// anything that is not a labelled measurement.
func TestResultPayload(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"RESULT:SIZE=4.5", "SIZE=4.5", true},
		{"RESULT: TITLE=global integer", "TITLE=global integer", true},
		{"RESULT:BASE_MEM=1024", "BASE_MEM=1024", true},

		// Not measurements: commentary, the script's leading blank line, and
		// anything else it chose to say.
		{"INFO:COUNT=8", "", false},
		{"", "", false},
		{"just talking", "", false},
	} {
		got, ok := resultPayload(tc.in)
		if ok != tc.ok || got != tc.want {
			t.Errorf("resultPayload(%q) = %q,%v; want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// The generated script must satisfy the runner's contract, or every benchmark
// run would be refused before it started.
func TestGeneratedScriptSaysDone(t *testing.T) {
	if !contains(code, `llOwnerSay("DONE")`) {
		t.Error("the benchmark script must end with a literal DONE")
	}
	if !contains(probeScript, `llOwnerSay("DONE")`) {
		t.Error("the probe script must end with a literal DONE")
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
