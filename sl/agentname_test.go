package sl

import "testing"

// TestAgentName pins the precedence a person relies on: what was named
// beats the environment, and the environment beats nothing at all.
//
// The last case is the one worth a test.  An empty answer means "let
// the daemon pick", and a caller that treats named and unnamed
// differently -- moving on to another avatar when this one is busy --
// depends on the environment counting as NAMED.
func TestAgentName(t *testing.T) {
	cases := []struct {
		name  string
		named string
		env   string
		want  string
	}{
		{"what was named wins", "qi", "example", "qi"},
		{"the environment answers when nothing was named", "", "example", "example"},
		{"neither leaves it to the daemon", "", "", ""},
		{"a blank environment is not an answer", "", "   ", ""},
		{"and is trimmed when it is one", "", "  example \n", "example"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv(EnvAgent, c.env)
			if got := AgentName(c.named); got != c.want {
				t.Errorf("AgentName(%q) with %s=%q = %q, want %q",
					c.named, EnvAgent, c.env, got, c.want)
			}
		})
	}
}
