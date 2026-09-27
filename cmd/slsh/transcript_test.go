package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// logShell is a shell keeping a transcript in a directory of its own,
// and the path it is keeping it in.
func logShell(t *testing.T) (*testShell, string) {
	t.Helper()
	dir := t.TempDir()
	x := newTestShellOn(t, newFakeGrid(t), Config{
		Addr: "fake:7807", Prefix: 27,
		Log: true, LogDir: dir,
	})
	t.Cleanup(func() { x.Close() })
	return x, dir
}

// written is everything the transcript holds.
func written(t *testing.T, dir string) string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(dir, "*.log"))
	if err != nil || len(found) != 1 {
		t.Fatalf("wanted one log in %s, found %v (%v)", dir, found, err)
	}
	b, err := os.ReadFile(found[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestTheTranscriptKeepsWhatWasRunAndWhatItSaid: the two halves that
// come from the shell itself.  A command is written before it runs, so
// that one which hung is still in the file.
func TestTheTranscriptKeepsWhatWasRunAndWhatItSaid(t *testing.T) {
	x, dir := logShell(t)
	if err := x.Do(context.Background(), "pwd"); err != nil {
		t.Fatal(err)
	}
	got := written(t, dir)
	if !strings.Contains(got, "$ pwd") {
		t.Errorf("the command is not in the transcript:\n%s", got)
	}
	// Its output, indented, so that the two can be told apart.
	if !strings.Contains(got, "\n  /") && !strings.HasSuffix(strings.TrimRight(got, "\n"), "  /") {
		t.Errorf("what pwd printed is not in the transcript:\n%s", got)
	}
	// A date as well as a time: a transcript outlives the day.
	if !strings.Contains(got, "20") || !strings.Contains(got, ":") {
		t.Errorf("no timestamp:\n%s", got)
	}
}

// TestNothingRedirectedIsInTheTranscript: the rule that decides what
// belongs is whether somebody SAW it, and a redirection is output they
// did not see -- it went to a file, which is usually about to be read
// back.  The command still appears, because they did type it.
func TestNothingRedirectedIsInTheTranscript(t *testing.T) {
	x, dir := logShell(t)
	out := filepath.Join(t.TempDir(), "listing")
	if err := x.Do(context.Background(), "pwd > "+out); err != nil {
		t.Fatal(err)
	}
	got := written(t, dir)
	if !strings.Contains(got, "$ pwd > ") {
		t.Errorf("the redirected command is not in the transcript:\n%s", got)
	}
	if strings.Contains(got, "\n  /\n") {
		t.Errorf("output that went to a file reached the transcript:\n%s", got)
	}
	// And it really did go somewhere.
	if b, err := os.ReadFile(out); err != nil || strings.TrimSpace(string(b)) == "" {
		t.Errorf("the redirection wrote nothing: %q %v", b, err)
	}
}

// TestWhatWasHeardIsInTheTranscript: an instant message arriving is the
// half of a transcript that nothing else records.  heard is the function
// the watch loop hands one to.
func TestWhatWasHeardIsInTheTranscript(t *testing.T) {
	x, dir := logShell(t)
	x.heard(&sl.IM{
		From:     msg.UUID{0x7a, 0x4f, 0x2b, 0x90},
		FromName: "Example Resident",
		Text:     "are you still at the build",
		Dialog:   sl.DialogMessage,
	})
	got := written(t, dir)
	for _, want := range []string{
		"* new conversation with Example Resident",
		"< [IM Example Resident] are you still at the build",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the transcript is missing %q:\n%s", want, got)
		}
	}
}

// TestTheTranscriptHoldsTheTextOfAnEscapeRatherThanTheEscape.
//
// The file is read later with cat or tail, which put it on a terminal
// as it stands, so an escape sequence kept in it would do there what
// Print stopped it doing here.  It says what the screen said: the
// same caret notation, from what was heard and from a command's
// output alike.
func TestTheTranscriptHoldsTheTextOfAnEscapeRatherThanTheEscape(t *testing.T) {
	x, dir := logShell(t)
	x.heard(&sl.IM{
		From:     msg.UUID{0x7a, 0x4f, 0x2b, 0x90},
		FromName: "Example\x1b]0;a title\x07Resident",
		Text:     "hello\x1b[2J\x1b]52;c;aGk=\x07",
		Dialog:   sl.DialogMessage,
	})
	x.grid.objects = []*sl.Seen{
		{Object: sl.Object{ID: testLamp, Local: 1, Name: "a\x1b[2Jlamp"}, PCode: 9},
	}
	if err := x.Do(context.Background(), "objects"); err != nil {
		t.Fatal(err)
	}

	got := written(t, dir)
	if strings.ContainsAny(got, "\x1b\x07") {
		t.Errorf("the transcript kept a control character:\n%q", got)
	}
	for _, want := range []string{
		"< [IM Example^[]0;a title^GResident] hello^[[2J^[]52;c;aGk=^G",
		" a^[[2Jlamp ",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the transcript is missing %q:\n%s", want, got)
		}
	}
}

// TestNoticesAndRefusalsAreInTheTranscript: everything a person sees
// that is not a command's own output goes through printf, which is what
// makes one funnel enough.
func TestNoticesAndRefusalsAreInTheTranscript(t *testing.T) {
	x, dir := logShell(t)
	x.noticef("something happened")
	x.errorf("cd: no folder %q", "spares")
	got := written(t, dir)
	for _, want := range []string{"* something happened", `slsh: cd: no folder "spares"`} {
		if !strings.Contains(got, want) {
			t.Errorf("the transcript is missing %q:\n%s", want, got)
		}
	}
}

// TestNoTranscriptWhenLoggingIsOff: off means no file, not an empty one.
// A shell that had quietly opened a file in somebody's home directory
// would be a surprise found later.
func TestNoTranscriptWhenLoggingIsOff(t *testing.T) {
	dir := t.TempDir()
	x := newTestShellOn(t, newFakeGrid(t), Config{
		Addr: "fake:7807", Prefix: 27,
		Log: false, LogDir: dir,
	})
	t.Cleanup(func() { x.Close() })
	x.noticef("something happened")
	if err := x.Do(context.Background(), "pwd"); err != nil {
		t.Fatal(err)
	}
	found, _ := filepath.Glob(filepath.Join(dir, "*"))
	if len(found) != 0 {
		t.Errorf("logging is off and %v was written anyway", found)
	}
}

// TestSetLogActsAtOnce: "set log off" stops the transcript in the shell
// it was typed in, and "set log_dir" with "set log on" starts it again
// somewhere else, without waiting for the next slsh.
func TestSetLogActsAtOnce(t *testing.T) {
	t.Setenv("SLSH_CONFIG_DIR", t.TempDir())
	x, dir := logShell(t)
	x.noticef("before")
	x.do(t, "set log off")
	x.noticef("after")
	x.do(t, "pwd")
	got := written(t, dir)
	if !strings.Contains(got, "before") || !strings.Contains(got, "$ set log off") {
		t.Errorf("what came before \"set log off\" is missing:\n%s", got)
	}
	if strings.Contains(got, "after") || strings.Contains(got, "$ pwd") {
		t.Errorf("\"set log off\" did not stop the transcript:\n%s", got)
	}

	other := t.TempDir()
	x.do(t, "set log_dir "+other)
	x.noticef("still off")
	x.do(t, "set log on")
	x.noticef("in the other place")
	if got := written(t, other); !strings.Contains(got, "in the other place") || strings.Contains(got, "still off") {
		t.Errorf("the transcript in log_dir holds:\n%s", got)
	}
	if got := written(t, dir); strings.Contains(got, "in the other place") {
		t.Errorf("the old place was still written to:\n%s", got)
	}

	// A place that cannot be opened is said, and leaves no transcript.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out := x.do(t, "set log_dir "+blocked); !strings.Contains(out, "no transcript") {
		t.Errorf("a log_dir that cannot be made printed:\n%s", out)
	}
	x.noticef("nowhere")
	if got := written(t, other); strings.Contains(got, "nowhere") {
		t.Errorf("the transcript went on in the old place:\n%s", got)
	}
}

// TestTheLogIsNamedAfterTheAvatar: one file per avatar is the whole
// arrangement, and the name arrives from the grid -- so a name with a
// slash in it must not decide where the file goes.
func TestTheLogIsNamedAfterTheAvatar(t *testing.T) {
	for _, c := range []struct{ avatar, profile, want string }{
		{"Example Resident", "ex", "example-resident.log"},
		{"Example Resident", "", "example-resident.log"},
		{"", "ex", "ex.log"},
		{"", "", "slsh.log"},

		// Nothing here becomes a path.
		{"../../etc/passwd", "", "etc-passwd.log"},
		{"a/b", "", "a-b.log"},
		{"...", "x", "x.log"},
		{"Ünicode Resident", "", "nicode-resident.log"},
	} {
		if got := logName(c.avatar, c.profile); got != c.want {
			t.Errorf("logName(%q, %q) = %q, want %q", c.avatar, c.profile, got, c.want)
		}
		if strings.ContainsAny(logName(c.avatar, c.profile), `/\`) {
			t.Errorf("logName(%q, %q) is a path", c.avatar, c.profile)
		}
	}
}

// TestTheDefaultPlaceIsTheXDGOne: $XDG_DATA_HOME/slgo, or
// ~/.local/share/slgo, which is what the rest of the tree does one
// directory over for configuration.
func TestTheDefaultPlaceIsTheXDGOne(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "/somewhere/data")
	got, err := logDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/somewhere/data", "slgo"); got != want {
		t.Errorf("with XDG_DATA_HOME: %q, want %q", got, want)
	}

	t.Setenv("XDG_DATA_HOME", "")
	got, err = logDir()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory to check the fallback against")
	}
	if want := filepath.Join(home, ".local", "share", "slgo"); got != want {
		t.Errorf("without XDG_DATA_HOME: %q, want %q", got, want)
	}
}

// TestTwoShellsShareOneTranscript: two shells may be attached to one
// avatar, and both then append to one file.  Every line has to survive
// whole -- a half of one line inside another is a transcript that
// cannot be read back.
func TestTwoShellsShareOneTranscript(t *testing.T) {
	dir := t.TempDir()
	const lines = 200
	done := make(chan struct{}, 2)
	for i := 0; i < 2; i++ {
		go func(which int) {
			defer func() { done <- struct{}{} }()
			tr, err := openTranscript(dir, "Example Resident", "")
			if err != nil {
				t.Error(err)
				return
			}
			defer tr.Close()
			for n := 0; n < lines; n++ {
				tr.line(strings.Repeat(string(rune('a'+which)), 60))
			}
		}(i)
	}
	<-done
	<-done

	got := strings.Split(strings.TrimRight(written(t, dir), "\n"), "\n")
	if len(got) != 2*lines {
		t.Errorf("wrote %d lines, found %d", 2*lines, len(got))
	}
	for _, l := range got {
		body := l[strings.LastIndex(l, " ")+1:]
		if body != strings.Repeat("a", 60) && body != strings.Repeat("b", 60) {
			t.Fatalf("a line arrived mixed with another: %q", l)
		}
	}
}
