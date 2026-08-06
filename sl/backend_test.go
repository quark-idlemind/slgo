package sl

// One suite, run against both backends.
//
// The promise this package makes is that a program works the same
// through slgod as it does holding the session itself.  Nothing but a
// test that runs twice keeps that true: the two implementations answer
// from different sources -- protobuf off a wire, and an agent's own
// maps -- and it is easy for one to drift.
//
// The live half needs a grid, so it is skipped unless SLGO_TEST_ADDR
// names a running slgod, and the direct half needs credentials, so it
// is skipped unless SLGO_TEST_PROFILE names a profile.  What is left
// without either is the part that needs no connection at all: that
// both types satisfy the interface, and that the session uses it
// rather than reaching past it.
//
// The two cannot be the same avatar.  Second Life allows one session
// per account, so an account slgod is holding cannot also be logged in
// here -- which means the comparison is between two avatars, and can
// only cover what belongs to the region rather than to either session.

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
)

// TestBothImplementBackend is the compile-time promise, stated where
// somebody looking for it would look.
func TestBothImplementBackend(t *testing.T) {
	var _ Backend = (*Hosted)(nil)
	var _ Backend = (*Direct)(nil)
}

// TestSessionOnlyTouchesTheBackend: the session must reach the grid
// through the interface and nowhere else, or one backend will work and
// the other will not.
func TestSessionOnlyTouchesTheBackend(t *testing.T) {
	st := reflect.TypeOf(Session{})
	for i := 0; i < st.NumField(); i++ {
		f := st.Field(i)
		name := f.Type.String()
		switch name {
		case "*client.Conn", "*slgov1.AgentInfo":
			t.Errorf("Session.%s is %s: the session should hold a Backend, "+
				"not a connection or a protobuf", f.Name, name)
		}
	}
}

// backends returns the ones this run can exercise, each with a name.
//
// They are necessarily different avatars; see the note at the top.
func backends(t *testing.T) map[string]Backend {
	t.Helper()
	out := map[string]Backend{}

	if addr := os.Getenv("SLGO_TEST_ADDR"); addr != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		h, err := Attach(ctx, addr, os.Getenv("SLGO_TEST_AGENT"))
		if err != nil {
			t.Fatalf("hosted: %v", err)
		}
		t.Cleanup(func() { h.Close() })
		out["hosted"] = h
	}

	if name := os.Getenv("SLGO_TEST_PROFILE"); name != "" {
		l, err := agent.LoadProfile(name)
		if err != nil {
			t.Fatalf("direct: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		d, err := Login(ctx, l)
		if err != nil {
			t.Fatalf("direct: %v", err)
		}
		t.Cleanup(func() { d.Close() })
		out["direct"] = d
	}

	if len(out) == 0 {
		t.Skip("set SLGO_TEST_ADDR and/or SLGO_TEST_PROFILE to exercise a backend")
	}
	return out
}

// TestBackendAgrees runs the same questions against whichever backends
// this run has, and checks each answer is the shape the package
// promises.  With both present the answers are compared.
func TestBackendAgrees(t *testing.T) {
	bs := backends(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type answers struct {
		info    *Info
		where   *Presence
		region  *Region
		known   bool
		objects int
		friends int
		syntax  bool
	}
	got := map[string]answers{}

	for name, b := range bs {
		t.Run(name, func(t *testing.T) {
			var a answers

			a.info = b.Info()
			if a.info.AgentID.IsZero() || a.info.SessionID.IsZero() {
				t.Errorf("Info has no ids: %+v", a.info)
			}
			if a.info.AvatarName == "" {
				t.Error("Info has no avatar name")
			}
			if len(a.info.Caps) == 0 {
				t.Error("Info lists no capabilities")
			}

			var err error
			if a.where, err = b.Presence(ctx, 0); err != nil {
				t.Errorf("Presence: %v", err)
			} else if a.where.Region == "" {
				t.Error("Presence names no region")
			}

			if a.region, a.known, err = b.Region(ctx); err != nil {
				t.Errorf("Region: %v", err)
			} else if a.known && a.region.Name == "" {
				t.Error("a known region has no name")
			}

			objs, err := b.Objects(ctx, "", "")
			if err != nil {
				t.Errorf("Objects: %v", err)
			}
			a.objects = len(objs)
			for _, o := range objs {
				if o.ID.IsZero() {
					t.Error("an object came back with no id")
					break
				}
			}

			fs, err := b.Friends(ctx)
			if err != nil {
				t.Errorf("Friends: %v", err)
			}
			a.friends = len(fs)

			// A capability both should have, fetched through the
			// same call.
			if b.HasCap("SimulatorFeatures") {
				s, err := New(b)
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				f, err := s.Features(ctx)
				if err != nil {
					t.Errorf("Features: %v", err)
				} else if len(f.Names()) == 0 {
					t.Error("Features answered with nothing")
				}
				a.syntax = true
			}

			got[name] = a
		})
	}

	h, okH := got["hosted"]
	d, okD := got["direct"]
	if !okH || !okD {
		t.Log("only one backend was available; nothing to compare")
		return
	}

	// Not the same avatar, and it cannot be: Second Life allows one
	// session per account, so an account held by slgod cannot also be
	// logged in here.  Two avatars is the most this can be, which
	// leaves only what does not depend on whose session it is.
	if h.info.AgentID == d.info.AgentID {
		t.Errorf("both backends report %s, which should not be possible", h.info.AgentID)
	}

	// Region facts belong to the region rather than to either
	// session, so two avatars standing in the same one must agree
	// about them.  In different regions there is nothing to compare.
	if h.where.Region != d.where.Region {
		t.Logf("the two avatars are in different regions (%q and %q); "+
			"skipping the region comparison", h.where.Region, d.where.Region)
		return
	}
	if h.known != d.known {
		t.Errorf("region known: hosted %v, direct %v", h.known, d.known)
	}
	if h.known && d.known {
		if h.region.ID != d.region.ID {
			t.Errorf("region id: hosted %s, direct %s", h.region.ID, d.region.ID)
		}
		if h.region.Name != d.region.Name {
			t.Errorf("region name: hosted %q, direct %q", h.region.Name, d.region.Name)
		}
		if h.region.Handle != d.region.Handle {
			t.Errorf("region handle: hosted %d, direct %d", h.region.Handle, d.region.Handle)
		}
		if h.region.Owner != d.region.Owner {
			t.Errorf("region owner: hosted %s, direct %s", h.region.Owner, d.region.Owner)
		}
		if h.region.WaterHeight != d.region.WaterHeight {
			t.Errorf("water height: hosted %v, direct %v", h.region.WaterHeight, d.region.WaterHeight)
		}
	}

	// Object counts differ legitimately -- two sessions see the region
	// from different cameras and have been listening for different
	// lengths of time -- so this only says whether both saw anything
	// at all, which is what a backend that answered from nowhere would
	// fail.
	if (h.objects == 0) != (d.objects == 0) {
		t.Errorf("objects: hosted %d, direct %d", h.objects, d.objects)
	}
	t.Logf("hosted %s: %d objects, %d friends; direct %s: %d objects, %d friends",
		h.info.AvatarName, h.objects, h.friends,
		d.info.AvatarName, d.objects, d.friends)
}
