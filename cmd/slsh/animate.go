package main

// Playing an animation, and stopping it.
//
// One message and no answer (sl/animation.go), so what this prints is
// what was asked for and never that the animation is playing: the
// simulator drops a request it will not honour without a word.
//
// A NAME is looked for in inventory first and among the built-ins
// second.  A name that is both is the inventory animation, said so, and
// --builtin is how to reach the built-in; refusing instead would make
// every built-in name unusable to somebody whose inventory happens to
// hold an animation called it, and a plain listing of what was played
// costs nothing.
// Why: doc/animations.md#one-name-two-things

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var animateCommands = map[string]*command{
	"animate": {
		params:   "NAME|UUID",
		flags:    func() any { return new(animateFlags) },
		brief:    "play an animation, from inventory or built in; --stop stops it, --list names the built-ins",
		keywords: "animation animate play dance gesture pose wave stop motion emote",
		man:      "animate",
		run:      cmdAnimate,
	},
}

type animateFlags struct {
	Stop    bool `getopt:"--stop -s     stop it rather than start it"`
	Builtin bool `getopt:"--builtin -b  the built-in of that name, even when inventory has an animation called it"`
	List    bool `getopt:"--list -l     name the built-in animations"`
	Help    bool `getopt:"--help -h     show what this command takes"`
}

func cmdAnimate(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o animateFlags
	rest, done, err := subOptions("animate", &o, out, args)
	if err != nil || done {
		return err
	}
	if o.List {
		if len(rest) != 0 || o.Stop || o.Builtin {
			return usageError("animate", "--list takes no name and no other flag")
		}
		for _, a := range sl.BuiltinAnimations() {
			fmt.Fprintln(out, a.Name)
		}
		return nil
	}
	if len(rest) != 1 {
		return usageError("animate", "one animation; a name with a space in it is one argument")
	}

	id, name, err := sh.animationNamed(ctx, out, rest[0], o.Builtin)
	if err != nil {
		return err
	}
	if o.Stop {
		if err := sh.s.StopAnimation(ctx, id); err != nil {
			return err
		}
		fmt.Fprintf(out, "asked %s (%s) to stop\n", name, id)
		return nil
	}
	if err := sh.s.StartAnimation(ctx, id); err != nil {
		return err
	}
	fmt.Fprintf(out, "asked for %s (%s)\n", name, id)
	return nil
}

// animationNamed is the asset id a word means, and the name to print
// for it.  A uuid is an asset id, as get takes one, which is what the
// simulator plays by; anything else is an inventory path first and a
// built-in name second.
func (sh *Shell) animationNamed(ctx context.Context, out io.Writer, word string, builtin bool) (msg.UUID, string, error) {
	if id, err := msg.ParseUUID(strings.TrimSpace(word)); err == nil {
		return id, id.String(), nil
	}
	if builtin {
		b, err := sl.BuiltinAnimation(word)
		return b.ID, b.Name, err
	}

	// Only a bare name can also be a built-in.
	bare := len(sl.SplitPath(word)) == 1 && !strings.HasPrefix(word, "/")
	b, berr := sl.BuiltinAnimation(word)
	if !bare {
		berr = errors.New("not a built-in name")
	}

	e, err := sh.thingAt(ctx, word)
	var ne *sl.NameError
	switch {
	case err == nil:
		id, aerr := sl.AnimationAsset(e)
		if aerr != nil {
			// A notecard called "hello" does not hide the built-in.
			if berr == nil {
				return b.ID, b.Name, nil
			}
			return msg.UUID{}, "", aerr
		}
		if berr == nil {
			fmt.Fprintf(out, "%s is an animation in inventory, and a built-in has that name too; "+
				"animate --builtin %s plays the built-in\n", word, b.Name)
		}
		return id, e.Name, nil
	case errors.As(err, &ne) && len(ne.IDs) == 0 && bare:
		// Nothing in inventory has the name.  Several that do are
		// refused below, whatever a built-in is called.
		if berr == nil {
			return b.ID, b.Name, nil
		}
		return msg.UUID{}, "", fmt.Errorf("%v; %s", err, strings.TrimPrefix(berr.Error(), "sl: "))
	}
	return msg.UUID{}, "", err
}
