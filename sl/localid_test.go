package sl

// A local id belongs to the region that handed it out.
//
// The next region numbers its own objects from the same numbers, and a
// region that restarts numbers everything afresh, so an Object kept
// across either and sent by its old local id acts on whatever has that
// number now.  Every call that sends a local id asks Session.local for
// it; these are the tests of what that answers, and of every call
// having asked.

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// elsewhere is a region the avatar moves to.
var elsewhere = msg.MustParseUUID("59a37e57-7e57-c0de-294d-d4e0b1fdbf47")

// named is an object as a region describes it, with a name so that
// finding it asks for nothing.
func named(id msg.UUID, local uint32) *Seen {
	return &Seen{Object: Object{ID: id, Local: local, Name: "workbench"}, PCode: pcodePrim}
}

// findHere finds an object the way a caller would, by asking the region
// the session is in.
func findHere(t *testing.T, w *Session, id msg.UUID) *Object {
	t.Helper()
	s, err := w.ObjectByID(context.Background(), id, time.Second)
	if err != nil {
		t.Fatalf("finding %s: %v", id, err)
	}
	return &s.Object
}

// moveTo relays the avatar arriving in a region holding these objects.
func moveTo(t *testing.T, f *fakeBackend, region msg.UUID, objs ...*Seen) {
	t.Helper()
	f.mu.Lock()
	f.region = &Region{ID: region, Name: "Example Region", Handle: 1}
	f.objects = objs
	f.mu.Unlock()
	f.RelayRegion(t, "Example Region", 1)
}

// countLookups counts the times the backend is asked about objects from
// now on.
func countLookups(f *fakeBackend) func() int {
	n := 0
	f.mu.Lock()
	f.afterObjects = func() { n++ }
	f.mu.Unlock()
	return func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return n
	}
}

// localsSent is every object local id that went out, from each message
// that carries one.
func localsSent(f *fakeBackend) []uint32 {
	var out []uint32
	for _, s := range f.Sent() {
		switch m := s.Msg.(type) {
		case *msg.ObjectSelect:
			for _, d := range m.ObjectData {
				out = append(out, d.ObjectLocalID)
			}
		case *msg.ObjectDeselect:
			for _, d := range m.ObjectData {
				out = append(out, d.ObjectLocalID)
			}
		case *msg.ObjectLink:
			for _, d := range m.ObjectData {
				out = append(out, d.ObjectLocalID)
			}
		case *msg.ObjectDelink:
			for _, d := range m.ObjectData {
				out = append(out, d.ObjectLocalID)
			}
		case *msg.DeRezObject:
			for _, d := range m.ObjectData {
				out = append(out, d.ObjectLocalID)
			}
		case *msg.MultipleObjectUpdate:
			for _, d := range m.ObjectData {
				out = append(out, d.ObjectLocalID)
			}
		case *msg.ObjectDescription:
			for _, d := range m.ObjectData {
				out = append(out, d.LocalID)
			}
		case *msg.ObjectName:
			for _, d := range m.ObjectData {
				out = append(out, d.LocalID)
			}
		case *msg.ObjectPermissions:
			for _, d := range m.ObjectData {
				out = append(out, d.ObjectLocalID)
			}
		case *msg.ObjectShape:
			for _, d := range m.ObjectData {
				out = append(out, d.ObjectLocalID)
			}
		case *msg.ObjectImage:
			for _, d := range m.ObjectData {
				out = append(out, d.ObjectLocalID)
			}
		case *msg.RequestMultipleObjects:
			for _, d := range m.ObjectData {
				out = append(out, d.ID)
			}
		case *msg.UpdateTaskInventory:
			out = append(out, m.UpdateData.LocalID)
		case *msg.RemoveTaskInventory:
			out = append(out, m.InventoryData.LocalID)
		case *msg.MoveTaskInventory:
			out = append(out, m.InventoryData.LocalID)
		case *msg.RequestTaskInventory:
			out = append(out, m.InventoryData.LocalID)
		case *msg.ObjectGrab:
			out = append(out, m.ObjectData.LocalID)
		case *msg.ObjectDeGrab:
			out = append(out, m.ObjectData.LocalID)
		}
	}
	return out
}

// sendsLocal is every call that sends an object's local id.  Each is
// handed the two objects, so that the calls taking several are asked
// about several, and is cut short by the context once it has sent: what
// is under test is what went out, not what came back.
var sendsLocal = []struct {
	name string
	call func(ctx context.Context, w *Session, a, b *Object) error
}{
	{"SetName", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.SetName(ctx, a, "bench")
	}},
	{"Properties", func(ctx context.Context, w *Session, a, _ *Object) error {
		_, err := w.Properties(ctx, a, time.Minute)
		return err
	}},
	{"Select", func(ctx context.Context, w *Session, a, b *Object) error {
		return w.Select(ctx, a, b)
	}},
	{"Link", func(ctx context.Context, w *Session, a, b *Object) error {
		return w.Link(ctx, a, b)
	}},
	{"Unlink", func(ctx context.Context, w *Session, a, b *Object) error {
		return w.Unlink(ctx, a, b)
	}},
	{"Take", func(ctx context.Context, w *Session, a, _ *Object) error {
		_, err := w.Take(ctx, a, aFolder, time.Minute)
		return err
	}},
	{"TakeCopy", func(ctx context.Context, w *Session, a, _ *Object) error {
		_, err := w.TakeCopy(ctx, a, aFolder, time.Minute)
		return err
	}},
	{"Delete", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.Delete(ctx, a, aFolder)
	}},
	{"Place", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.Place(ctx, a, msg.Vector3{X: 1, Y: 2, Z: 3}, msg.Quaternion{}, msg.Vector3{X: 1, Y: 1, Z: 1})
	}},
	{"SetDescription", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.SetDescription(ctx, a, "a bench")
	}},
	{"PutInObject", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.PutInObject(ctx, a, anItem(theOther, "a script"))
	}},
	{"RemoveFromObject", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.RemoveFromObject(ctx, a, theOther)
	}},
	{"FetchFromObject", func(ctx context.Context, w *Session, a, _ *Object) error {
		_, err := w.FetchFromObject(ctx, a, TaskItem{ID: theOther, Name: "a note"}, aFolder, time.Minute)
		return err
	}},
	{"TaskInventory", func(ctx context.Context, w *Session, a, _ *Object) error {
		_, err := w.TaskInventory(ctx, a)
		return err
	}},
	{"RenameInObject", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.RenameInObject(ctx, a, TaskItem{ID: theOther, Name: "a note", SaleType: "not"}, "a letter", time.Minute)
	}},
	{"SetObjectPermissions", func(ctx context.Context, w *Session, a, _ *Object) error {
		_, err := w.SetObjectPermissions(ctx, a, WhoEveryone, PermCopy)
		return err
	}},
	{"SetShape", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.SetShape(ctx, a, Shape{Type: "box"})
	}},
	{"SetFaces", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.SetFaces(ctx, a, PlainFaces(1))
	}},
	{"Touch", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.Touch(ctx, a, bothCoordinates)
	}},
	{"TouchEnd", func(ctx context.Context, w *Session, a, _ *Object) error {
		return w.TouchEnd(ctx, a, bothCoordinates)
	}},
}

// bothCoordinates is a touch given both its coordinates, so that nothing
// is read to work one out from the other: what these tests watch is the
// local id, and a touch that reads the appearance may ask the region for
// it by local id as well.
var bothCoordinates = Touch{ST: middle, UV: middle}

// TestALocalIDIsTheRegionsItCameFrom: two prims found at 77 and 78, and
// then the avatar is somewhere else.  Where the same two are there too,
// under other numbers, every call sends the numbers they have there.
// Where they are not, every call refuses and sends nothing at all --
// the old numbers name something else here, and one sent anyway is a
// stranger's prim renamed, taken or deleted.
func TestALocalIDIsTheRegionsItCameFrom(t *testing.T) {
	for _, c := range sendsLocal {
		t.Run(c.name+" after a move", func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
			f.objects = []*Seen{named(thePrim, 77), named(theChild, 78)}
			a, b := findHere(t, w, thePrim), findHere(t, w, theChild)

			moveTo(t, f, elsewhere, named(theChild, 1078), named(thePrim, 1077))
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			err := c.call(ctx, w, a, b)
			if errors.Is(err, ErrNotHere) {
				t.Fatalf("%s refused a prim the region has: %v", c.name, err)
			}
			sent := localsSent(f)
			if len(sent) == 0 {
				t.Fatalf("%s sent no local id (%v); sent %s", c.name, err, f.describe())
			}
			for _, l := range sent {
				if l != 1077 && l != 1078 {
					t.Errorf("%s sent local id %d; the prims are 1077 and 1078 here", c.name, l)
				}
			}
			if a.Local != 1077 {
				t.Errorf("the object still says it is local %d", a.Local)
			}
		})

		t.Run(c.name+" into a region without it", func(t *testing.T) {
			t.Parallel()
			w, f := newFakeSession(t)
			f.ServeInventory(t, func(msg.UUID) []*Item { return nil })
			f.objects = []*Seen{named(thePrim, 77), named(theChild, 78)}
			a, b := findHere(t, w, thePrim), findHere(t, w, theChild)

			// Something else has 77 here, which is the danger.
			moveTo(t, f, elsewhere, named(theOther, 77))
			err := c.call(context.Background(), w, a, b)
			if !errors.Is(err, ErrNotHere) {
				t.Errorf("%s = %v, want ErrNotHere", c.name, err)
			}
			if err == nil || !strings.Contains(err.Error(), "or is beyond the draw distance") {
				t.Errorf("%s = %v, want it to say the prim may be out of sight", c.name, err)
			}
			if got := f.Sent(); len(got) != 0 {
				t.Errorf("%s sent %s for a prim that is not here", c.name, f.describe())
			}
		})
	}
}

// TestAnObjectFoundHereIsNotLookedUpAgain: the region the avatar is in
// is the one that numbered it, so its number is sent as it is.
func TestAnObjectFoundHereIsNotLookedUpAgain(t *testing.T) {
	w, f := newFakeSession(t)
	f.objects = []*Seen{named(thePrim, 77)}
	o := findHere(t, w, thePrim)

	looked := countLookups(f)
	if err := w.Place(context.Background(), o, msg.Vector3{X: 1}, msg.Quaternion{}, msg.Vector3{X: 1, Y: 1, Z: 1}); err != nil {
		t.Fatalf("Place: %v", err)
	}
	if got := localsSent(f); len(got) != 1 || got[0] != 77 {
		t.Errorf("sent %v, want 77", got)
	}
	if n := looked(); n != 0 {
		t.Errorf("the object was looked up %d times, having been found in this region", n)
	}
}

// TestAReEstablishedSessionLooksObjectsUpAgain: a daemon that logs the
// avatar in again says so as a region change, and may bring it back to
// the same region -- with the same uuid, and after a restart every
// object renumbered.  So the same region is a new visit, and the number
// is asked for again.
func TestAReEstablishedSessionLooksObjectsUpAgain(t *testing.T) {
	w, f := newFakeSession(t)
	f.objects = []*Seen{named(thePrim, 77)}
	o := findHere(t, w, thePrim)

	f.Reidentify(msg.MustParseUUID("fcc97e57-7e57-c0de-7feb-681af09f6a1e"))
	moveTo(t, f, testRegionID, named(thePrim, 88))
	looked := countLookups(f)
	if err := w.Place(context.Background(), o, msg.Vector3{X: 1}, msg.Quaternion{}, msg.Vector3{X: 1, Y: 1, Z: 1}); err != nil {
		t.Fatalf("Place: %v", err)
	}
	if got := localsSent(f); len(got) != 1 || got[0] != 88 {
		t.Errorf("sent %v, want 88, the number the region has now", got)
	}
	if n := looked(); n != 1 {
		t.Errorf("the object was looked up %d times, want once", n)
	}
}

// further is a third region, for an avatar that moves again during a
// lookup.
var further = msg.MustParseUUID("9c077e57-7e57-c0de-b075-ceb07fcba67d")

// movesDuringLookups stages the avatar changing region while the
// backend answers each of the first n lookups from now on: the answer
// is the region it is leaving, and the session has heard of the move
// before the answer is back.  It returns the count of lookups.
func movesDuringLookups(t *testing.T, f *fakeBackend, n int) func() int {
	t.Helper()
	looked := 0
	f.mu.Lock()
	f.afterObjects = func() {
		looked++
		if looked > n {
			return
		}
		// Every move is a new visit, so going back and forth between
		// two regions is as good as a new one each time.
		next := further
		if looked%2 == 0 {
			next = elsewhere
		}
		f.region = &Region{ID: next, Name: "Example Region", Handle: 1}
		f.objects = []*Seen{named(thePrim, 2000+uint32(looked))}
		f.RelayRegion(t, "Example Region", 1)
	}
	f.mu.Unlock()
	return func() int {
		f.mu.Lock()
		defer f.mu.Unlock()
		return looked
	}
}

// TestALookupThatCrossesAMoveIsNotSent: the avatar moved while the
// object was being looked up, so the number found is the region's it
// left.  It is looked up once more, and a second answer from a visit
// already over is refused like a prim that is not here.
func TestALookupThatCrossesAMoveIsNotSent(t *testing.T) {
	t.Run("once", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.objects = []*Seen{named(thePrim, 77)}
		o := findHere(t, w, thePrim)
		moveTo(t, f, elsewhere, named(thePrim, 1077))

		looked := movesDuringLookups(t, f, 1)
		if err := w.Place(context.Background(), o, msg.Vector3{X: 1}, msg.Quaternion{}, msg.Vector3{X: 1, Y: 1, Z: 1}); err != nil {
			t.Fatalf("Place: %v", err)
		}
		if got := localsSent(f); len(got) != 1 || got[0] != 2001 {
			t.Errorf("sent %v, want 2001, the number where the avatar is now", got)
		}
		if n := looked(); n != 2 {
			t.Errorf("the object was looked up %d times, want twice", n)
		}
	})

	t.Run("every time", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.objects = []*Seen{named(thePrim, 77)}
		o := findHere(t, w, thePrim)
		moveTo(t, f, elsewhere, named(thePrim, 1077))

		looked := movesDuringLookups(t, f, 3)
		err := w.Place(context.Background(), o, msg.Vector3{X: 1}, msg.Quaternion{}, msg.Vector3{X: 1, Y: 1, Z: 1})
		if !errors.Is(err, ErrNotHere) {
			t.Errorf("Place = %v, want ErrNotHere", err)
		}
		if err == nil || !strings.Contains(err.Error(), "changed region while it was being looked up") {
			t.Errorf("Place = %v, want it to say the avatar moved during the lookup", err)
		}
		if got := f.Sent(); len(got) != 0 {
			t.Errorf("sent %s with a number from a visit already over", f.describe())
		}
		if n := looked(); n != 2 {
			t.Errorf("the object was looked up %d times, want twice", n)
		}
	})

	// here leaves a visit's region unnamed when the backend cannot say
	// it, and a stamp of no region is not trusted.
	t.Run("the region not yet named", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.objects = []*Seen{named(thePrim, 77)}
		o := findHere(t, w, thePrim)
		moveTo(t, f, elsewhere, named(thePrim, 1077))
		f.mu.Lock()
		f.regionKnown = false
		f.mu.Unlock()

		looked := countLookups(f)
		err := w.Place(context.Background(), o, msg.Vector3{X: 1}, msg.Quaternion{}, msg.Vector3{X: 1, Y: 1, Z: 1})
		if !errors.Is(err, ErrNotHere) {
			t.Errorf("Place = %v, want ErrNotHere", err)
		}
		if err == nil || !strings.Contains(err.Error(), "has not said which region") {
			t.Errorf("Place = %v, want it to say the region was not named", err)
		}
		if got := f.Sent(); len(got) != 0 {
			t.Errorf("sent %s from a region that was never named", f.describe())
		}
		if n := looked(); n != 2 {
			t.Errorf("the object was looked up %d times, want twice", n)
		}
	})
}

// TestAnObjectBuiltByHandIsLookedUp: nothing says which region its
// local id came from, so the id is what finds it -- and the region's
// number is sent, not the one it was built with.
func TestAnObjectBuiltByHandIsLookedUp(t *testing.T) {
	w, f := newFakeSession(t)
	f.objects = []*Seen{named(thePrim, 12)}
	if err := w.TouchStart(context.Background(), &Object{ID: thePrim, Local: 77}, bothCoordinates); err != nil {
		t.Fatalf("TouchStart: %v", err)
	}
	if got := localsSent(f); len(got) != 1 || got[0] != 12 {
		t.Errorf("sent %v, want 12", got)
	}

	f.Forget()
	err := w.TouchStart(context.Background(), &Object{Local: 77}, bothCoordinates)
	if err == nil || len(f.Sent()) != 0 {
		t.Errorf("a local id with no object id to check it by went out: %v, %s", err, f.describe())
	}
}

// TestAnObjectFromAnotherSessionIsLookedUp: two sessions draw their
// visits from one count, so one session's object never passes for
// another's, even where both have seen one region change.
func TestAnObjectFromAnotherSessionIsLookedUp(t *testing.T) {
	a, fa := newFakeSession(t)
	fa.objects = []*Seen{named(thePrim, 77)}
	o := findHere(t, a, thePrim)

	// The same prim in the same region, as far as b can tell, and b
	// has found things there too, so knows the region as a does.
	b, fb := newFakeSession(t)
	fb.objects = []*Seen{named(thePrim, 55)}
	findHere(t, b, thePrim)
	fb.Forget()
	if err := b.TouchStart(context.Background(), o, bothCoordinates); err != nil {
		t.Fatalf("TouchStart: %v", err)
	}
	if got := localsSent(fb); len(got) != 1 || got[0] != 55 {
		t.Errorf("sent %v, want 55", got)
	}
}

// TestParentReadsWhatThisRegionSaid: Parent sends nothing, but it reads
// the session's parents by local id, and an old number reads another
// prim's.
func TestParentReadsWhatThisRegionSaid(t *testing.T) {
	w, f := newFakeSession(t)
	f.objects = []*Seen{named(thePrim, 77)}
	o := findHere(t, w, thePrim)

	moveTo(t, f, elsewhere)
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: theOther, ID: 77, ParentID: 5}))
	if p, ok := w.Parent(o); ok {
		t.Errorf("Parent = %d, which is what the prim now at 77 said", p)
	}
	f.Relay(t, anUpdate(msg.ObjectUpdate_ObjectData{FullID: thePrim, ID: 1077, ParentID: 6}))
	if p, ok := w.Parent(o); !ok || p != 6 {
		t.Errorf("Parent = %d, %v; want 6, what the prim itself said here", p, ok)
	}
}

// TestALinksetIsFoundByIDBeforeLocalID: a local id from somewhere else
// can be any prim here, and the list is in local id order, so trying it
// alongside the id finds a stranger first.
func TestALinksetIsFoundByIDBeforeLocalID(t *testing.T) {
	all := []*Seen{named(theOther, 5), named(thePrim, 1077)}
	parts, err := linkset(all, &Object{ID: thePrim, Local: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(parts) != 1 || parts[0].ID != thePrim {
		t.Errorf("found %s, want %s", parts[0].ID, thePrim)
	}
	if parts, err := linkset(all, &Object{Local: 5}); err != nil || parts[0].ID != theOther {
		t.Errorf("an object with no id was not found by its local id: %v", err)
	}
}

// rawLocalsIn finds a local id taken straight off something and put
// into a message: inside a composite literal of one of msg's types,
// assigned to a field named for a local id, or handed to selectMsg or
// deselectMsg.
func rawLocalsIn(fset *token.FileSet, f *ast.File) []string {
	var out []string
	isLocal := func(e ast.Expr) bool {
		s, ok := e.(*ast.SelectorExpr)
		return ok && s.Sel.Name == "Local"
	}
	note := func(e ast.Expr) {
		out = append(out, fset.Position(e.Pos()).String())
	}
	isMsg := func(t ast.Expr) bool {
		if a, ok := t.(*ast.ArrayType); ok {
			t = a.Elt
		}
		s, ok := t.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		x, ok := s.X.(*ast.Ident)
		return ok && x.Name == "msg"
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if !isMsg(n.Type) {
				return true
			}
			ast.Inspect(n, func(m ast.Node) bool {
				if e, ok := m.(ast.Expr); ok && isLocal(e) {
					note(e)
				}
				return true
			})
			return false
		case *ast.AssignStmt:
			for i, lhs := range n.Lhs {
				s, ok := lhs.(*ast.SelectorExpr)
				if ok && strings.HasSuffix(s.Sel.Name, "LocalID") && i < len(n.Rhs) && isLocal(n.Rhs[i]) {
					note(n.Rhs[i])
				}
			}
		case *ast.CallExpr:
			s, ok := n.Fun.(*ast.SelectorExpr)
			if !ok || (s.Sel.Name != "selectMsg" && s.Sel.Name != "deselectMsg") {
				return true
			}
			for _, a := range n.Args {
				if isLocal(a) {
					note(a)
				}
			}
		}
		return true
	})
	return out
}

// TestNoLocalIDIsSentStraightOffAnObject: every local id that goes out
// comes from Session.local, which knows whether it still means the same
// prim.  One read off an Object's Local means whatever prim has that
// number wherever the avatar is now.
func TestNoLocalIDIsSentStraightOffAnObject(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, at := range rawLocalsIn(fset, f) {
			t.Errorf("%s puts a local id in a message straight off an object, "+
				"which after a move names some other prim.  Take it from "+
				"Session.local (region.go).", at)
		}
	}
}

// TestTheLocalIDCheckFindsOne: a check that found nothing would pass on
// a package that did it everywhere.
func TestTheLocalIDCheckFindsOne(t *testing.T) {
	const src = `package p

func a(w *Session, o *Object, s *Seen, local uint32) {
	m := &msg.ObjectGrab{}
	m.ObjectData.LocalID = o.Local
	m.ObjectData.LocalID = local
	_ = []msg.ObjectLink_ObjectData{{ObjectLocalID: s.Local}}
	_ = msg.ObjectShape_ObjectData{ObjectLocalID: local}
	_ = w.selectMsg(local, o.Local)
	_ = PrimJSON{LocalID: s.Local}
	before := map[uint32]bool{o.Local: true}
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := rawLocalsIn(fset, f)
	want := []string{"p.go:5:25", "p.go:7:50", "p.go:9:25"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("found\n\t%s\nwant\n\t%s", strings.Join(got, "\n\t"), strings.Join(want, "\n\t"))
	}
}
