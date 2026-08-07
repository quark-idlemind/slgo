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
	"github.com/quark-idlemind/slgo/msg"
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

// AutoObject is the object automate and autobench run their scripts in,
// and AutoLock is the lock that says whose turn it is.
//
// One object, kept and worn, because making one costs seconds every run
// and -- far more -- because the script inside it then already exists:
// installing a script into an object that has never held one takes
// about eight seconds, and replacing one that is there takes under one.
const (
	AutoObject = "auto"
	AutoLock   = "auto"
)

// AutoPoints are where the auto objects are worn, one each.
//
// An attachment point holds one object, so more objects means more
// points; the HUD ones are used because nothing else wants them and
// they are not part of how the avatar looks.
var AutoPoints = []int{sl.HUDBottomLeft, sl.HUDBottom, sl.HUDBottomRight, sl.HUDTopLeft}

// AutoName is what the nth auto object is called.  The first keeps the
// bare name, so an account that has only ever run one at a time is not
// asked to grow a second object it will not use.
func AutoName(n int) string {
	if n == 0 {
		return AutoObject
	}
	return fmt.Sprintf("%s %d", AutoObject, n+1)
}

// UseAutoN gets several auto objects, for work that can be done in
// parallel: a script in one object cannot be told from a script in the
// same object by anything it says, but two objects are two speakers.
//
// One lock covers the set.  Locking them one by one would let two
// benchmarks each hold some and wait for the rest, which is a deadlock
// where the present arrangement is a queue.
//
// Fewer objects than asked for is not an error.  The caller can do less
// at once, which is slower and not wrong -- and there is a limit on how
// many attachments this is prepared to make.
func UseAutoN(ctx context.Context, s *sl.Session, n int) ([]*sl.Object, func(), error) {
	if n < 1 {
		n = 1
	}
	if n > len(AutoPoints) {
		n = len(AutoPoints)
	}

	lockCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := s.Lock(lockCtx, AutoLock); err != nil {
		return nil, nil, fmt.Errorf("waiting for the %s objects: %w\n"+
			"        (an slgod older than the lock does not answer; --rez avoids it)",
			AutoObject, err)
	}
	unlock := func() { s.Unlock(AutoLock) }

	folder, err := objectsFolder(ctx, s)
	if err != nil {
		unlock()
		return nil, nil, err
	}

	var objs []*sl.Object
	for i := 0; i < n; i++ {
		a, err := s.EnsureAttached(ctx, folder, AutoName(i), AutoPoints[i])
		if err != nil {
			if i == 0 {
				unlock()
				return nil, nil, err
			}
			// One is enough to work with; the rest only make it
			// quicker.  Say so and carry on rather than fail a
			// benchmark over an attachment point.
			fmt.Fprintf(os.Stderr, "only %d of %d objects: %v\n", i, n, err)
			break
		}
		obj := a.Object
		objs = append(objs, &obj)
	}
	return objs, unlock, nil
}

// UseAuto gets the shared auto object, waiting for its turn.
//
// The lock is not politeness.  A benchmark carries its base reading in
// the OBJECT's linkset data, which belongs to the object and not to the
// script, and the script inside is installed under a fixed name -- so
// two runs at once would overwrite each other's reading and each
// other's script.  Labelling the output would not help; the clash is
// over the data.
//
// The returned function gives the lock back.  So does going away: slgod
// frees what a client holds when its stream ends, so a run that panics
// or is killed does not leave the object locked.
func UseAuto(ctx context.Context, s *sl.Session, point int) (*sl.Object, func(), error) {
	// Bounded, and generously: a benchmark ahead of us in the queue can
	// legitimately take minutes.  The bound is not for that -- it is so
	// that an slgod too old to know about locks fails with something a
	// reader can act on instead of waiting for ever.  It answers
	// nothing at all, since an unknown frame on the stream is ignored.
	lockCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	if err := s.Lock(lockCtx, AutoLock); err != nil {
		return nil, nil, fmt.Errorf("waiting for the %s object: %w\n"+
			"        (an slgod older than the lock does not answer; --rez avoids it)",
			AutoObject, err)
	}
	unlock := func() { s.Unlock(AutoLock) }

	folder, err := objectsFolder(ctx, s)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	// EnsureAttached asks slgod what is worn before touching anything,
	// so the ordinary case -- already on -- costs one question and no
	// seconds.  Nothing is written down here: the object's id changes
	// every time it is put on and every time the avatar logs in, so a
	// remembered id is wrong after every relog, and it would be wrong
	// per machine besides.  The inventory item is what does not change,
	// and slgod is what heard the attachment described.
	a, err := s.EnsureAttached(ctx, folder, AutoObject, point)
	if err != nil {
		unlock()
		return nil, nil, err
	}

	obj := a.Object
	return &obj, unlock, nil
}

// objectsFolder is where a taken object lands, and so where the auto
// object is kept.
func objectsFolder(ctx context.Context, s *sl.Session) (msg.UUID, error) {
	top, err := s.ListInventory(ctx, "/", 0)
	if err != nil {
		return msg.UUID{}, err
	}
	for _, e := range top {
		if e.IsFolder() && e.Name == "Objects" {
			return e.ID, nil
		}
	}
	// Not a failure worth stopping for: the root will do.
	return s.InventoryRoot(), nil
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
