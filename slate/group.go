package slate

// group NAME "Group Name" and group NAME none: a second avatar's active
// group, put back to what it was when the run ends.
// Why: doc/slate-language.md#stimuli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// groupUndoFor bounds putting one second avatar's group back; a variable
// so that a test of one that never lands need not wait it out.
var groupUndoFor = 30 * time.Second

// setupGroups reads the active group of each second avatar that a group
// step names, before anything runs: what the end of the run puts back. The
// message never names the avatar's real name or its profile.
// Why: doc/slate-runner.md#second-avatars
func (r *runner) setupGroups(ctx context.Context) error {
	named := map[string]bool{}
	note := func(steps []Step) {
		for _, st := range steps {
			if st.Stimulus != nil && st.Stimulus.Group != nil {
				named[st.Stimulus.Group.Avatar.Text] = true
			}
		}
	}
	for _, t := range r.s.Tests {
		note(t.Steps)
	}
	for _, b := range r.s.Befores {
		note(b.Steps)
	}
	for _, b := range r.s.Afters {
		note(b.Steps)
	}
	for _, q := range r.s.Sequences {
		note(q.Steps)
	}
	for _, sc := range r.seconds {
		if !named[sc.name] {
			continue
		}
		g, err := sc.sess.ActiveGroup(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &setupError{fmt.Sprintf("the active group of %s could not be read: %v", sc.name, err)}
		}
		sc.group, sc.groupRead = g, true
	}
	return nil
}

// groupStimulus is group NAME "Group Name" or group NAME none: a blocking
// ActivateGroup on the second avatar's session, done when that avatar's
// active group is the one asked for. The group is looked for in the
// avatar's own list, by name and ignoring case, as slsh's group is.
func (s *stepRun) groupStimulus(g *SetGroup) (*stimulus, error) {
	sc := s.r.secondOf(g.Avatar.Text)
	if sc == nil {
		return nil, fmt.Errorf("%s is not given as a second avatar", g.Avatar.Text)
	}
	return &stimulus{
		blocking: true,
		send: func(ctx context.Context, budget time.Duration) (string, error) {
			p, err := sc.sess.Where(ctx)
			if err != nil {
				return "", fmt.Errorf("the active group of %s could not be read: %v", sc.name, err)
			}
			var id msg.UUID
			if !g.None {
				var found []msg.UUID
				for _, gr := range p.Groups {
					if strings.EqualFold(gr.Name, g.Group) {
						found = append(found, gr.ID)
					}
				}
				switch {
				case len(found) > 1:
					return "", fmt.Errorf("%s is in %d groups called %q, and a step cannot say which", sc.name, len(found), g.Group)
				case len(found) == 0 && len(p.Groups) == 0:
					return "", fmt.Errorf("%s has no groups that are known yet, so %q matches none; the list arrives on its own after login", sc.name, g.Group)
				case len(found) == 0:
					return "", fmt.Errorf("%s has not joined a group called %q", sc.name, g.Group)
				}
				id = found[0]
			}
			what := "none"
			if !g.None {
				what = fmt.Sprintf("%q", g.Group)
			}
			if p.ActiveGroup == id {
				return fmt.Sprintf("the group of %s was already %s", sc.name, what), nil
			}
			if !sc.groupRead {
				// Not read at setup: the group a step changes is put back to
				// the one before the first change.
				sc.group, sc.groupRead = p.ActiveGroup, true
			}
			sc.changed = true
			if err := sc.sess.ActivateGroup(ctx, id, budget); err != nil {
				return "", fmt.Errorf("the group of %s did not become %s: %v", sc.name, what, err)
			}
			return fmt.Sprintf("set the group of %s to %s", sc.name, what), nil
		},
	}, nil
}

// restoreGroups sets every second avatar whose group a step changed back to
// the group it had when the run began. A failure is a cleanup warning, as
// the probe's is, and does not change the exit code. It runs on a context
// of its own, and again does nothing for an avatar already put back.
// Why: doc/slate-runner.md#cleanup-and-what-a-failure-leaves-behind
func (r *runner) restoreGroups(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	for _, sc := range r.seconds {
		if !sc.changed {
			continue
		}
		sc.changed = false
		c, cancel := context.WithTimeout(ctx, groupUndoFor)
		err := sc.sess.ActivateGroup(c, sc.group, groupUndoFor)
		cancel()
		if err != nil {
			r.printf("slate: cleanup: warning: the group of %s was not put back: %v", sc.name, err)
			continue
		}
		r.printf("slate: cleanup: put the group of %s back to what it was", sc.name)
	}
}
