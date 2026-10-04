package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// TestSeatsSurviveARestart, which is the whole point of writing them
// down: a reconnect could be served from memory, and the case that
// prompted this is the daemon being restarted.
func TestSeatsSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seats")
	chair := msg.MustParseUUID("2ddb7e57-7e57-c0de-1fc2-4a8634a86f7a")
	bench := msg.MustParseUUID("2ec47e57-7e57-c0de-6b58-cb3b7c24e0d2")

	s, err := openSeats(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A file that is not there yet is not an error: nothing has sat
	// down.
	if got, _ := s.Seat("example"); !got.IsZero() {
		t.Errorf("an empty store answered %v", got)
	}
	s.SetSeat("example", chair, msg.UUID{})
	s.SetSeat("other", bench, msg.UUID{})

	again, err := openSeats(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := again.Seat("example"); got != chair {
		t.Errorf("after reopening, example sits on %v, want %v", got, chair)
	}
	if got, _ := again.Seat("other"); got != bench {
		t.Errorf("after reopening, other sits on %v, want %v", got, bench)
	}
	// A profile nobody has seen sitting has no seat, rather than
	// somebody else's.
	if got, _ := again.Seat("nobody"); !got.IsZero() {
		t.Errorf("a profile with no seat answered %v", got)
	}
}

// TestStandingUpForgetsTheSeat.
//
// The zero id means standing, and standing has to be written down as
// firmly as sitting: an avatar deliberately stood up and then put back
// in its chair at the next login would be the daemon overruling
// somebody rather than remembering for them.
func TestStandingUpForgetsTheSeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seats")
	chair := msg.MustParseUUID("2ecf7e57-7e57-c0de-f290-91d53f85f922")

	s, err := openSeats(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.SetSeat("example", chair, msg.UUID{})
	s.SetSeat("example", msg.UUID{}, msg.UUID{})

	again, err := openSeats(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := again.Seat("example"); !got.IsZero() {
		t.Errorf("standing up left %v remembered", got)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "example") {
		t.Errorf("the file still names the profile:\n%s", b)
	}
}

// TestAFileThatCannotBeReadStopsRatherThanForgetting.
//
// Carrying on would forget every seat in it and then write the
// forgetting over the top, turning a typo into the permanent loss of
// what it was trying to record.
func TestAFileThatCannotBeReadStopsRatherThanForgetting(t *testing.T) {
	for _, c := range []struct{ what, body string }{
		{"a line with no id", "example\n"},
		{"an id that is not one", "example not-a-uuid\n"},
	} {
		path := filepath.Join(t.TempDir(), "seats")
		if err := os.WriteFile(path, []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openSeats(path, nil); err == nil {
			t.Errorf("%s was read without complaint", c.what)
		}
	}
}

// TestCommentsAndBlankLinesAreSkipped, so that somebody can comment a
// line out to stop an avatar being put back in its chair, and say in
// the file why they did.
func TestCommentsAndBlankLinesAreSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seats")
	body := "# written by slgod\n\n" +
		"# example is out of its chair on purpose\n" +
		"other 33437e57-7e57-c0de-8e76-32e61051b820\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := openSeats(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Seat("example"); !got.IsZero() {
		t.Errorf("a commented out line was read: %v", got)
	}
	if got, _ := s.Seat("other"); got.IsZero() {
		t.Error("the line that was not commented out was skipped too")
	}
}

// TestAWriteThatFailsComplainsOnce.
//
// An avatar that goes on sitting where it is sitting is not made worse
// by the note being lost, so a failure here is reported rather than
// returned -- but reported once, not every time somebody sits down.
func TestAWriteThatFailsComplainsOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sub")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	var said []string
	s, err := openSeats(filepath.Join(dir, "seats"), func(f string, v ...any) {
		said = append(said, f)
	})
	if err != nil {
		t.Fatal(err)
	}
	// Read-only from here on, so that writing cannot work however the
	// store goes about it.
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if f, err := os.CreateTemp(dir, "probe-"); err == nil {
		f.Close()
		t.Skip("this user can write into a read-only directory; nothing to test")
	}
	for i := 0; i < 3; i++ {
		s.SetSeat("example", msg.MustParseUUID("33a37e57-7e57-c0de-8194-c2ede48c33c0"), msg.UUID{})
	}
	if len(said) != 1 {
		t.Errorf("a store that cannot write said %d things, want one: %v", len(said), said)
	}
	// And it still answers from memory, since the session it is
	// serving has not stopped happening.
	if got, _ := s.Seat("example"); got.IsZero() {
		t.Error("a store that could not write forgot what it was told")
	}
}

// TestTheRegionIsKeptWithTheSeat: a line may carry the region, and one
// without reads as the region not known; both are written back as they
// were.
func TestTheRegionIsKeptWithTheSeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seats")
	chair := msg.MustParseUUID("99a57e57-7e57-c0de-73bb-5c5bfbf00fd1")
	bench := msg.MustParseUUID("99c17e57-7e57-c0de-6569-645d75e4ae41")
	region := msg.MustParseUUID("9c1a7e57-7e57-c0de-171a-cf0e69059309")

	body := "example " + chair.String() + " " + region.String() + "\n" +
		"other " + bench.String() + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := openSeats(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if on, in := s.Seat("example"); on != chair || in != region {
		t.Errorf("example read as %v in %v, want %v in %v", on, in, chair, region)
	}
	if on, in := s.Seat("other"); on != bench || !in.IsZero() {
		t.Errorf("an old line read as %v in %v, want %v and no region", on, in, bench)
	}

	s.SetSeat("third", chair, region)
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"example " + chair.String() + " " + region.String() + "\n",
		"other " + bench.String() + "\n",
		"third " + chair.String() + " " + region.String() + "\n",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the file lacks the line %q:\n%s", want, out)
		}
	}
}
