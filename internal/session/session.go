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

// AgentName resolves which session was asked for.  See sl.AgentName:
// the environment is read in one place so that no two commands can
// disagree about what it means.
func AgentName(named string) string { return sl.AgentName(named) }

// Connect gets a session, through slgod or by logging in.
func Connect(ctx context.Context, o Options) (*sl.Session, error) {
	o.Agent = AgentName(o.Agent)
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

// AutoPoints are where the auto objects are worn.
//
// The HUD points are used because nothing else wants them and they are
// not part of how the avatar looks.  There are only EIGHT of them, so
// the first eight entries are one each and the rest double up: a point
// holds several objects when the attach asks to add rather than to
// replace, which was measured rather than assumed (cmd/slgo-multiattach)
// and puts the real ceiling at the 38-attachment total.
//
// THIS LIST IS APPEND-ONLY AND MUST NEVER BE REORDERED.  A slot is
// identified by its INDEX -- that is what a lock is taken on, and what
// one program tells another -- so moving an entry makes two versions
// disagree about which object slot 5 is.  Nothing detects that: two
// benchmarks quietly share an object and both report plausible numbers.
// Adding to the end is safe; anything else is not.
var AutoPoints = []int{
	// The original four.
	sl.HUDBottomLeft, sl.HUDBottom, sl.HUDBottomRight, sl.HUDTopLeft,
	// The remaining HUD points, one object each.
	sl.HUDTop, sl.HUDTopRight, sl.HUDCenter1, sl.HUDCenter2,
	// Doubling up, which is what takes this past eight.
	sl.HUDBottomLeft, sl.HUDBottom, sl.HUDBottomRight, sl.HUDTopLeft,
}

// AutoName is what the nth auto object is called.  The first keeps the
// bare name, so an account that has only ever run one at a time is not
// asked to grow a second object it will not use.
func AutoName(n int) string {
	if n == 0 {
		return AutoObject
	}
	return fmt.Sprintf("%s %d", AutoObject, n+1)
}

// AutoGroupSize is how many objects a benchmark takes at once.
//
// Four because that is what the search uses: quarterSearch takes three
// readings at once alongside the measured object.  It is autobench's
// number and not the pool's -- the pool hands out any count up to
// AutoPool, and a program running scripts asks for one object per script
// it wants going at once.
const AutoGroupSize = 4

// AutoPool is how many objects one avatar's pool holds -- the ceiling on
// how many things can run on it at once.
func AutoPool() int { return len(AutoPoints) }

// AutoSlotLock names the lock over one place in the pool, and
// AutoAllocLock the one held while deciding who gets which.
//
// NOT the old bare "auto", nor the "auto/0" that named a group of four,
// deliberately: a client old enough to lock either would not exclude
// against these, and the two would quietly share objects.  A different
// name makes the mismatch visible -- the old client takes a lock nobody
// else wants -- rather than silent.
func AutoSlotLock(i int) string { return fmt.Sprintf("%s/slot/%d", AutoLock, i) }

// AutoAllocLock is held while a caller decides which places it is
// taking, and never while it waits for one.  See auto.go.
var AutoAllocLock = AutoLock + "/alloc"

// EnsureAutoItems makes sure the first n auto items exist in inventory,
// by COPYING the first one rather than building each.
//
// Two reasons, and the first is not an optimisation:
//
//   - An avatar may not be allowed to rez.  A parcel grants "create
//     objects" to a group, and an avatar in no group is refused -- with
//     a message blaming the land.  Such an avatar can still be given one
//     object by somebody who can build, and from that one it can make
//     all the others, because copying an item it already owns asks the
//     land nothing at all.
//   - It is quicker even when rezzing is allowed.  Rez, name, take is
//     eight seconds and change; a copy is under two.
//
// Only the first has to be built, and only if the account has never had
// one.  Everything after it is a copy, which means every auto object is
// the same object -- exactly what a benchmark wants.
func EnsureAutoItems(ctx context.Context, s *sl.Session, folder msg.UUID, n int) error {
	items, err := s.FolderItems(ctx, folder)
	if err != nil {
		return err
	}

	have := make(map[string]bool, len(items))
	var seed *sl.Item
	for _, it := range items {
		have[it.Name] = true
		if it.Name == AutoObject {
			seed = it
		}
	}

	// Nothing to copy from.  EnsureAttached will build the first one
	// the slow way, and then there is a seed for the rest.
	if seed == nil {
		if n <= 1 {
			return nil
		}
		a, err := s.EnsureAttached(ctx, folder, AutoObject, AutoPoints[0]|sl.AttachAdd)
		if err != nil {
			return fmt.Errorf("making the first %s object: %w\n"+
				"        (an avatar that may not rez needs one given to it by one that may)",
				AutoObject, err)
		}
		if seed, err = s.FindItem(ctx, folder, AutoObject); err != nil {
			return err
		}
		_ = a
	}

	for i := 1; i < n; i++ {
		name := AutoName(i)
		if have[name] {
			continue
		}
		if _, err := s.CopyItem(ctx, seed.ID, folder, name, 60*time.Second); err != nil {
			// Not fatal: fewer objects is slower, not wrong, and the
			// caller already copes with getting fewer than it asked
			// for.
			fmt.Fprintf(os.Stderr, "could not make %q: %v\n", name, err)
			return nil
		}
	}
	return nil
}

// SetupAuto makes an avatar ready for benchmarking: the items exist
// and are worn, so that the first run of the day does not pay for it.
//
// It takes EVERY group first, and refuses if any is busy.  Wearing
// things is not something to do underneath a benchmark: an attach
// replaces what is on the point, and a run whose object went away
// reports nothing useful about why.
func SetupAuto(ctx context.Context, s *sl.Session, n int) ([]*sl.Object, error) {
	if n < 1 {
		n = 1
	}
	if n > len(AutoPoints) {
		n = len(AutoPoints)
	}

	// Every place, and the allocation lock over them: setting up moves
	// attachments about, and an object moving under a running benchmark
	// is a wrong number rather than a failure.  The allocation lock
	// first, so that a caller part way through choosing finishes and
	// nobody starts choosing while this holds half the pool.
	var held []string
	defer func() {
		for i := len(held) - 1; i >= 0; i-- {
			s.Unlock(held[i])
		}
	}()
	for _, name := range append([]string{AutoAllocLock}, autoSlotLocks()...) {
		got, holder, err := s.TryLock(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("asking for the %s objects: %w", AutoObject, err)
		}
		if !got {
			return nil, fmt.Errorf("%s is in use by %s; "+
				"setting up moves attachments about and cannot be done "+
				"under a running benchmark",
				name, holderOr(holder))
		}
		held = append(held, name)
	}

	folder, err := objectsFolder(ctx, s)
	if err != nil {
		return nil, err
	}
	if err := EnsureAutoItems(ctx, s, folder, n); err != nil {
		return nil, err
	}

	var objs []*sl.Object
	for i := 0; i < n; i++ {
		// AttachAdd, because past the eighth slot two objects share a
		// point and a bare attach would throw the first one off.
		a, err := s.EnsureAttached(ctx, folder, AutoName(i), AutoPoints[i]|sl.AttachAdd)
		if err != nil {
			if i == 0 {
				return nil, err
			}
			fmt.Fprintf(os.Stderr, "only %d of %d objects: %v\n", i, n, err)
			break
		}
		obj := a.Object
		objs = append(objs, &obj)
	}
	return objs, nil
}

// holderOr names whoever holds a lock, when the daemon said.
func holderOr(holder string) string {
	if holder == "" {
		return "something else"
	}
	return holder
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

// autoSlotLocks names every place in the pool.
func autoSlotLocks() []string {
	out := make([]string, 0, AutoPool())
	for i := 0; i < AutoPool(); i++ {
		out = append(out, AutoSlotLock(i))
	}
	return out
}
