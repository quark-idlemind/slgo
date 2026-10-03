package slate

import "testing"

func TestDialogOrderedAndSorted(t *testing.T) {
	for _, c := range []struct {
		name    string
		buttons []string
		expect  string
		want    bool
		detail  string
	}{
		{"ordered pass", []string{"a", "x", "b"}, `button "a" button "b" ordered`, true, ""},
		{"ordered fail", []string{"b", "a"}, `button "a" button "b" ordered`, false, `ordered: "b" comes before "a"`},
		{"unordered without the word", []string{"b", "a"}, `button "a" button "b"`, true, ""},
		// Kuhn's first assignment is p,q reversed (clause 1 gets "p"); only
		// clause 1 on "q" and clause 2 on "r" is in order.
		{"ordered needs a later assignment", []string{"q", "p", "r"}, `button matching "^[pq]$" button matching "^[qr]$" ordered`, true, ""},
		{"ordered no assignment is ordered", []string{"a", "b"}, `button matching "^[ab]$" button "a" ordered`, false, `ordered: "a" comes before "b"`},
		{"ordered non-first assignment", []string{"x1", "y", "x2"}, `button matching "^x" button "y" button matching "^x" ordered`, true, ""},
		{"ordered with only and count", []string{"a", "b", "c"}, `button "a" button "b" button "c" only ordered count 3`, true, ""},
		{"ordered with only and count, fails", []string{"c", "b", "a"}, `button "a" button "b" button "c" only ordered count 3`, false, `ordered: "b" comes before "a"`},
		{"sorted text pass", []string{"Apple", "banana", "Cherry"}, `sorted`, true, ""},
		{"sorted text fail", []string{"b", "A"}, `sorted`, false, `sorted: "A" comes before "b"`},
		{"sorted numeric group pass", []string{"n9", "n10"}, `sorted matching "^n([0-9]+)$"`, true, ""},
		{"sorted numeric group fail", []string{"n10", "n9"}, `sorted matching "^n([0-9]+)$"`, false, `sorted: "n9" comes before "n10"`},
		{"sorted skips navigation", []string{"<<", "n1", "n2", ">>"}, `sorted matching "^n"`, true, ""},
		{"sorted without matching sees navigation", []string{"<<", "a", ">>"}, `sorted`, false, `sorted: ">>" comes before "a"`},
		{"sorted mixed groups as text", []string{"n10", "nx"}, `sorted matching "^n(.+)$"`, true, ""},
		{"sorted mixed groups as text fail", []string{"n9", "n10", "nx"}, `sorted matching "^n(.+)$"`, false, `sorted: "n10" comes before "n9"`},
		{"sorted decimals", []string{"1.5", "10"}, `sorted matching "^(.+)$"`, true, ""},
	} {
		res := openDialog(t, "m", c.buttons, `text "m" `+c.expect)
		if got := res.Exit == 0; got != c.want {
			t.Errorf("%s: passed = %v, want %v\n%s", c.name, got, c.want, res.Transcript)
		}
		if c.detail != "" {
			mustHave(t, res, c.detail)
		}
	}
}
