package main

import "testing"

// TestGroupFlag: the ordinary place for the group is the profile, and
// -group is a one-off that beats it.  A bare value means every session,
// which is what it has always meant; PROFILE=GROUP names one, which is
// the only thing that can mean anything once several are hosted.
func TestGroupFlag(t *testing.T) {
	cases := []struct {
		name    string
		set     []string
		profile string // what the profile asked for
		agent   string
		want    string
	}{
		{
			name:    "the profile decides when nothing is given",
			profile: "Builders",
			agent:   "example",
			want:    "Builders",
		},
		{
			name:  "and nothing at all is a legitimate answer",
			agent: "example",
			want:  "",
		},
		{
			name:    "a bare flag applies to every session",
			set:     []string{"Testers"},
			profile: "Builders",
			agent:   "qi",
			want:    "Testers",
		},
		{
			name:    "a named one applies to that session",
			set:     []string{"example=Builders", "qi=Testers"},
			profile: "Ignored",
			agent:   "qi",
			want:    "Testers",
		},
		{
			name:    "and leaves the others to their profiles",
			set:     []string{"example=Builders"},
			profile: "FromProfile",
			agent:   "qi",
			want:    "FromProfile",
		},
		{
			name:    "a named one beats a bare one",
			set:     []string{"Testers", "qi=Builders"},
			profile: "Ignored",
			agent:   "qi",
			want:    "Builders",
		},
		{
			name:  "a uuid is a value, not a name=value",
			set:   []string{"33a57e57-7e57-c0de-da54-ed9b5d7d8f09"},
			agent: "example",
			want:  "33a57e57-7e57-c0de-da54-ed9b5d7d8f09",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var g groupFlag
			for _, s := range c.set {
				if err := g.Set(s); err != nil {
					t.Fatal(err)
				}
			}
			if got := g.For(c.agent, c.profile); got != c.want {
				t.Errorf("For(%q, %q) = %q, want %q", c.agent, c.profile, got, c.want)
			}
		})
	}
}
