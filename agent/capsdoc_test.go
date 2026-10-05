package agent

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// TestTheCapabilityDocListsWhatIsAskedFor holds doc/capabilities.txt's list
// of what slgo asks for at login to DefaultCaps, names and count, so that a
// capability added to one cannot leave the other saying something untrue:
// it had, twice, before this test.
func TestTheCapabilityDocListsWhatIsAskedFor(t *testing.T) {
	raw, err := os.ReadFile("../doc/capabilities.txt")
	if err != nil {
		t.Fatalf("doc/capabilities.txt is tracked and must be readable: %v", err)
	}
	lines := strings.Split(string(raw), "\n")
	start := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "# slgo asks for ") {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal(`doc/capabilities.txt has no "# slgo asks for" line`)
	}

	// The count, spelled out, is the word after "for".
	count := strings.Fields(lines[start])[4]
	if want := spelled(len(DefaultCaps)); count != want {
		t.Errorf("the doc says slgo asks for %q capabilities; DefaultCaps has %d (%s)", count, len(DefaultCaps), want)
	}

	// The names are the indented comment lines after the first blank one.
	var listed []string
	i := start + 1
	for i < len(lines) && lines[i] != "#" {
		i++
	}
	for i++; i < len(lines) && strings.HasPrefix(lines[i], "#   "); i++ {
		listed = append(listed, strings.Fields(strings.TrimPrefix(lines[i], "#"))...)
	}
	want := slices.Clone(DefaultCaps)
	slices.Sort(want)
	slices.Sort(listed)
	if !slices.Equal(listed, want) {
		t.Errorf("doc/capabilities.txt lists\n  %v\nDefaultCaps is\n  %v", listed, want)
	}
}

// spelled is n as the doc writes it, for the counts DefaultCaps has had.
func spelled(n int) string {
	ones := []string{"", "-one", "-two", "-three", "-four", "-five", "-six", "-seven", "-eight", "-nine"}
	tens := map[int]string{2: "twenty", 3: "thirty", 4: "forty"}
	if t, ok := tens[n/10]; ok {
		return t + ones[n%10]
	}
	return "a number this test does not spell"
}
