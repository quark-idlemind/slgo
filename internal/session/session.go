// Package session is the part of starting up that every program which
// runs scripts does the same way: get a session, and get an object to
// run in.
//
// Neither belongs in sl.  Whether to attach to slgod or log in, and
// whether to rez something or use an object already in the region, are
// decisions a command line makes; sl provides the operations and has no
// business having a policy about them.
package session

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/quark-idlemind/slgo/internal/creds"
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/sl"
)

// Options says how to get a session.
type Options struct {
	// Addr is the slgod to attach to, when Direct is not set.  Empty
	// asks sl-host, and failing that means this machine.
	Addr string

	// Agent names the profile: the session slgod is holding, or the
	// credentials on disk for a direct login.
	Agent string

	// Direct logs in from this process rather than attaching to slgod,
	// which means this process holds the session and quitting logs the
	// avatar out.
	Direct bool

	First, Last, Start string

	// Channel is what the login tells the grid this program is.
	Channel string

	// In and Out are where credentials are asked for, when a direct
	// login needs something that is not on disk.  Zero values are
	// standard input and output.
	In  *os.File
	Out io.Writer
}

// Connect gets a session, through slgod or by logging in.
func Connect(ctx context.Context, o Options) (*sl.Session, error) {
	if !o.Direct {
		if o.First != "" || o.Last != "" {
			return nil, fmt.Errorf("--first and --last are for --direct; through slgod the session knows who it is")
		}
		addr, err := slhost.Resolve(o.Addr)
		if err != nil {
			return nil, err
		}
		dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		s, err := sl.Dial(dialCtx, addr, o.Agent)
		if err != nil {
			return nil, fmt.Errorf("%w\n        --direct logs in without slgod", err)
		}
		return s, nil
	}

	in, out := o.In, o.Out
	if in == nil {
		in = os.Stdin
	}
	if out == nil {
		out = os.Stdout
	}
	l, err := creds.Resolve(in, out, o.Agent, o.First, o.Last, o.Start)
	if err != nil {
		return nil, err
	}
	if l.Channel == "" {
		l.Channel = o.Channel
	}
	loginCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	return sl.LoginDirect(loginCtx, l)
}

// RunIn returns an object to run scripts in, and a function that undoes
// whatever getting it took.
//
// A named object is left exactly as it was found -- it is somebody's
// object, and the script that ran in it is the only trace.  One rezzed
// here is ours and goes in the trash afterwards, unless keep.
//
// The same object is used for every run, which is not merely tidy: a
// benchmark carries a reading from one script to the next through the
// object's linkset data, and that is only meaningful if the object does
// not change underneath it.
func RunIn(ctx context.Context, s *sl.Session, name string, keep bool) (*sl.Object, func(), error) {
	if name != "" {
		found, err := s.ObjectsNamed(ctx, name, 15*time.Second)
		if err != nil {
			return nil, nil, err
		}
		for _, seen := range found {
			if seen.IsRoot() {
				return &seen.Object, nil, nil
			}
		}
		if len(found) > 0 {
			// Every match was a child prim: the script would run, but
			// in the linkset's root rather than where it was asked for.
			return nil, nil, fmt.Errorf("%q is a child prim; name the object, not a part of it", name)
		}
		return nil, nil, fmt.Errorf("no object called %q is in range", name)
	}

	// Beside the avatar rather than on top of it, and high enough that
	// the ground is not in the way.
	where, err := s.Where(ctx)
	if err != nil {
		return nil, nil, err
	}
	at := where.Position
	at.X += 1.5
	at.Z += 0.5

	obj, err := s.Rez(ctx, sl.RezOptions{At: at})
	if err != nil {
		return nil, nil, fmt.Errorf("rezzing something to run in: %w", err)
	}
	if err := s.SetName(ctx, obj, "slgo run "+time.Now().Format("15:04:05")); err != nil {
		// Not fatal: the name is so that a fault header reads well.
		fmt.Fprintf(os.Stderr, "naming the object: %v\n", err)
	}

	if keep {
		return obj, nil, nil
	}
	return obj, func() {
		// A fresh context: the run's may well be why we are here.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		trash, err := s.TrashFolder(ctx)
		if err == nil {
			err = s.Delete(ctx, obj, trash)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s is still there: %v\n", obj, err)
		}
	}, nil
}
