package main

// --avatar NAME=PROFILE: which slgod profile drives a second avatar the
// file declares, and the setup errors for one that cannot be used. No
// message names a profile.
// Why: doc/slate-runner.md#second-avatars

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/sl"
)

const (
	withVisitor = "slate 1\nobject sign is \"Example Sign\"\navatar visitor\ntest \"visits\" {\n  touch sign anywhere as visitor\n}\n"
	profileOne  = "example-one"
	profileTwo  = "example-two"
	profileGone = "example-gone"
)

// profiles dials the grid of the profile it is asked for and refuses any
// other, the way a daemon does, with a message that quotes the profile.
type profiles struct {
	t      *testing.T
	grids  map[string]*fakeGrid
	dialed []string
}

func (p *profiles) dial(_ context.Context, _, agent string) (*sl.Session, error) {
	p.dialed = append(p.dialed, agent)
	g, ok := p.grids[agent]
	if !ok {
		return nil, fmt.Errorf("attach: no agent %q is held", agent)
	}
	return g.session(p.t), nil
}

func twoProfiles(t *testing.T) *profiles {
	return &profiles{t: t, grids: map[string]*fakeGrid{
		profileOne: newGrid(t),
		profileTwo: newGrid(t).withMe(idSecond),
	}}
}

func TestASecondAvatarIsDialledByItsProfileAndTouchesOnItsOwnSession(t *testing.T) {
	p := twoProfiles(t)
	code, out, errw := do(t, p.dial, "-agent", profileOne, "--avatar", "visitor="+profileTwo, writeFile(t, withVisitor))
	if code != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr: %s", code, out, errw)
	}
	if strings.Join(p.dialed, " ") != profileOne+" "+profileTwo {
		t.Errorf("dialled %q, want the tester then the second avatar", p.dialed)
	}
	if n := p.grids[profileTwo].grabs(); n != 1 {
		t.Errorf("%d grabs on the second avatar's session, want 1", n)
	}
	if n := p.grids[profileOne].grabs(); n != 0 {
		t.Errorf("%d grabs on the tester's session, want 0", n)
	}
	for _, h := range []string{profileOne, profileTwo} {
		if strings.Contains(out+errw, h) {
			t.Errorf("the output names a profile, %q:\n%s%s", h, out, errw)
		}
	}
	// The sessions were closed with the run.
	for name, g := range p.grids {
		select {
		case <-g.done:
		default:
			t.Errorf("the session of %s was left open", name)
		}
	}
}

func TestAnAvatarFlagThatDoesNotFitTheFileIsExit3(t *testing.T) {
	for _, c := range []struct {
		name string
		src  string
		args []string
		want string
		dial int // sessions dialled before the error: 0 means none, not even the tester's
	}{
		{"declared and not given", withVisitor, []string{"-agent", profileOne},
			"visitor is declared but no --avatar visitor=... was given", 0},
		{"given and not declared", passing, []string{"--avatar", "ghost=" + profileTwo},
			"an --avatar was given for ghost, which the file does not declare", 0},
		{"given for one of two", "slate 1\navatar a\navatar b\ntest \"t\" {\n  say \"x\" on 0\n}\n",
			[]string{"--avatar", "a=" + profileTwo}, "b is declared but no --avatar b=... was given", 0},
		{"given twice", withVisitor, []string{"--avatar", "visitor=" + profileTwo, "--avatar", "visitor=" + profileOne},
			"--avatar was given twice for visitor", 0},
		{"the tester's own, by -agent", withVisitor, []string{"-agent", profileOne, "--avatar", "visitor=" + profileOne},
			"the profile given for visitor is the tester's own", 0},
		{"the tester's own, by identity", withVisitor, []string{"-agent", profileOne, "--avatar", "visitor=example-twin"},
			"the profile given for visitor is the tester's own", 2},
		{"a profile the daemon does not hold", withVisitor, []string{"-agent", profileOne, "--avatar", "visitor=" + profileGone},
			"the profile given for visitor is not held by the daemon", 2},
		{"one profile for two names", "slate 1\navatar a\navatar b\ntest \"t\" {\n  say \"x\" on 0\n}\n",
			[]string{"--avatar", "a=" + profileTwo, "--avatar", "b=" + profileTwo}, "the profiles given for a and b are the same", 0},
		{"two names, one avatar by identity", "slate 1\navatar a\navatar b\ntest \"t\" {\n  say \"x\" on 0\n}\n",
			[]string{"-agent", profileOne, "--avatar", "a=" + profileTwo, "--avatar", "b=example-clone"}, "the profiles given for a and b are the same", 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := twoProfiles(t)
			p.grids["example-twin"] = newGrid(t) // the tester's id on another profile
			p.grids["example-clone"] = newGrid(t).withMe(idSecond)
			args := append(append([]string{}, c.args...), writeFile(t, c.src))
			code, out, errw := do(t, p.dial, args...)
			if code != 3 {
				t.Errorf("exit = %d, want 3\nstdout:\n%s\nstderr: %s", code, out, errw)
			}
			if !strings.Contains(errw, c.want) {
				t.Errorf("stderr = %q, want it to contain %q", errw, c.want)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			// An error the file and flags decide is found before anything is dialled.
			if c.dial == 0 && len(p.dialed) != 0 {
				t.Errorf("dialled %q before the error", p.dialed)
			}
			if c.dial > 0 && len(p.dialed) != c.dial {
				t.Errorf("dialled %q, want %d sessions", p.dialed, c.dial)
			}
			for _, h := range []string{profileOne, profileTwo, profileGone, "example-twin", "example-clone"} {
				if strings.Contains(errw, h) {
					t.Errorf("stderr names a profile, %q: %s", h, errw)
				}
			}
		})
	}
}

func TestAProfileTheDaemonDoesNotHoldIsNotRepeatedInTheError(t *testing.T) {
	p := twoProfiles(t)
	code, out, errw := do(t, p.dial, "-agent", profileOne, "--avatar", "visitor="+profileGone, writeFile(t, withVisitor))
	if code != 3 {
		t.Fatalf("exit = %d\n%s%s", code, out, errw)
	}
	// The daemon's own answer quotes the profile; the error names the binding.
	if strings.Contains(strings.ToLower(errw+out), profileGone) {
		t.Errorf("the profile handle is in the output:\n%s%s", out, errw)
	}
	if !strings.Contains(errw, "the profile given for visitor is not held by the daemon") {
		t.Errorf("stderr = %q", errw)
	}
	// Whatever case the daemon quotes it in.
	p.dialed = nil
	d := func(ctx context.Context, a, agent string) (*sl.Session, error) {
		if agent == profileGone {
			return nil, fmt.Errorf("no profile %s", strings.ToUpper(agent))
		}
		return p.dial(ctx, a, agent)
	}
	_, out, errw = do(t, d, "-agent", profileOne, "--avatar", "visitor="+profileGone, writeFile(t, withVisitor))
	if strings.Contains(strings.ToLower(errw+out), profileGone) {
		t.Errorf("the profile handle is in the output:\n%s%s", out, errw)
	}
}

func TestAMalformedAvatarFlagIsAUsageErrorThatDoesNotEchoIt(t *testing.T) {
	for _, v := range []string{profileTwo, "=" + profileTwo, "visitor=", ""} {
		d := &noDial{t: t}
		code, out, errw := do(t, d.dial, "--avatar", v, writeFile(t, withVisitor))
		if code != 4 || !strings.Contains(errw, "--avatar wants NAME=PROFILE") || out != "" || d.calls != 0 {
			t.Errorf("%q: exit %d, stdout %q, stderr %q, %d dials", v, code, out, errw, d.calls)
		}
		if strings.Contains(errw, profileTwo) {
			t.Errorf("%q: stderr echoes the value: %s", v, errw)
		}
	}
	d := &noDial{t: t}
	code, _, errw := do(t, d.dial, "-make-bridge", "--avatar", "visitor=example-four")
	if code != 4 || !strings.Contains(errw, "takes no FILE") || strings.Contains(errw, "example-four") {
		t.Errorf("make-bridge: exit %d, stderr %q", code, errw)
	}
}
