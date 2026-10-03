package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/sl"
	"github.com/quark-idlemind/slgo/slate"
)

const (
	passing = "slate 1\ntest \"opens\" {\n  say \"go\" on 0\n}\ntest \"closes\" {\n  say \"stop\" on 0\n}\n"
	failing = "slate 1\nobject sign is \"Example Sign\"\ntest \"opens\" {\n  say \"go\" on 0\n}\ntest \"waits\" {\n  expect say \"never\" on public from object sign within 100ms\n}\n"
)

// noDial is a dialler that fails the test if it is called.
type noDial struct {
	t     *testing.T
	calls int
}

func (n *noDial) dial(context.Context, string, string) (*sl.Session, error) {
	n.calls++
	n.t.Error("dialled")
	return nil, errors.New("must not dial")
}

func writeFile(t *testing.T, src string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.slate")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// do runs the command with a dialler and returns the exit code and both outputs.
func do(t *testing.T, d dialer, args ...string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	code := run(context.Background(), args, &out, &errw, d)
	return code, out.String(), errw.String()
}

func TestNothingDialledBeforeTheFileIsGood(t *testing.T) {
	good := writeFile(t, passing)
	cases := []struct {
		name string
		args []string
		src  string // when set, the file is made and appended to args
		code int
		want string
	}{
		{"no file", nil, "", 4, "need exactly one FILE"},
		{"two files", []string{good, good}, "", 4, "need exactly one FILE"},
		{"unknown flag", []string{"-bogus", good}, "", 4, "flag provided but not defined"},
		{"missing file", []string{filepath.Join(t.TempDir(), "absent.slate")}, "", 4, "no such file"},
		{"run does not compile", []string{"-run", "(", good}, "", 4, "-run"},
		{"run matches no test", []string{"-run", "^nothing$", good}, "", 4, "matches no test"},
		{"parse error", nil, "slate 1\nbogus\n", 2, "t.slate:2:1:"},
		{"check error", nil, "slate 1\ntest \"x\" {\n  touch ghost anywhere\n}\n", 2, "t.slate:3:"},
		{"make-bridge with a file", []string{"-make-bridge", good}, "", 4, "takes no FILE"},
		{"make-bridge with -run", []string{"-make-bridge", "-run", "x"}, "", 4, "takes no FILE"},
		{"make-bridge with --pay", []string{"-make-bridge", "--pay"}, "", 4, "takes no FILE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			args := c.args
			if c.src != "" {
				args = append(args, writeFile(t, c.src))
			}
			d := &noDial{t: t}
			code, out, errw := do(t, d.dial, args...)
			if code != c.code {
				t.Errorf("exit = %d, want %d\nstderr: %s", code, c.code, errw)
			}
			if !strings.Contains(errw, c.want) {
				t.Errorf("stderr = %q, want it to contain %q", errw, c.want)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			if d.calls != 0 {
				t.Errorf("dialled %d times", d.calls)
			}
		})
	}
}

func TestUsageIsOnStderrForAWrongCommandLine(t *testing.T) {
	d := &noDial{t: t}
	_, _, errw := do(t, d.dial)
	if !strings.Contains(errw, "usage: slate [-addr ADDR]") {
		t.Errorf("stderr = %q", errw)
	}
}

// grid dials a session over a fresh fake and remembers what was asked.
func grid(t *testing.T) (dialer, *fakeGrid, *struct{ addr, agent string }) {
	f := newGrid(t)
	var got struct{ addr, agent string }
	return func(_ context.Context, addr, agent string) (*sl.Session, error) {
		got.addr, got.agent = addr, agent
		return f.session(t), nil
	}, f, &got
}

func TestPassingScriptExitsZeroAndStreamsTheTranscript(t *testing.T) {
	d, f, got := grid(t)
	code, out, errw := do(t, d, "-addr", "host:1", "-agent", "Example Resident", writeFile(t, passing))
	if code != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr: %s", code, out, errw)
	}
	for _, want := range []string{`slate: test "opens"`, `slate: pass test "opens"`, `slate: pass test "closes"`, "slate: passed 2 tests"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if errw != "" {
		t.Errorf("stderr = %q", errw)
	}
	if got.addr != "host:1" || got.agent != "Example Resident" {
		t.Errorf("dialled %+v", *got)
	}
	if said := f.said(); len(said) != 2 || said[0] != "go" || said[1] != "stop" {
		t.Errorf("said %q", said)
	}
}

func TestFailingScriptExitsOne(t *testing.T) {
	d, _, _ := grid(t)
	code, out, errw := do(t, d, writeFile(t, failing))
	if code != 1 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr: %s", code, out, errw)
	}
	for _, want := range []string{`slate: pass test "opens"`, `slate: fail `, `test "waits"`, "slate: failed 1 of 2 tests"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestRunSelectsTestsByName(t *testing.T) {
	d, f, _ := grid(t)
	code, out, _ := do(t, d, "-run", "^closes$", writeFile(t, passing))
	if code != 0 {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	if strings.Contains(out, `"opens"`) {
		t.Errorf("a test that does not match was printed:\n%s", out)
	}
	if said := f.said(); len(said) != 1 || said[0] != "stop" {
		t.Errorf("said %q", said)
	}
}

func TestADialThatFailsIsExit3OnStderr(t *testing.T) {
	d := func(context.Context, string, string) (*sl.Session, error) { return nil, errors.New("no slgod") }
	code, out, errw := do(t, d, writeFile(t, passing))
	if code != 3 || out != "" || !strings.Contains(errw, "slate: dial: no slgod") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errw)
	}
	code, out, errw = do(t, d, "-make-bridge")
	if code != 3 || out != "" || !strings.Contains(errw, "slate: dial: no slgod") {
		t.Errorf("make-bridge: exit %d, stdout %q, stderr %q", code, out, errw)
	}
}

func TestMakeBridgePrintsItsSentences(t *testing.T) {
	defer func(old func(context.Context, *sl.Session) error) { bridgeMaker = old }(bridgeMaker)
	for _, c := range []struct {
		err  error
		code int
		out  string
	}{
		{nil, 0, "slate: make-bridge: \"slate bridge\" is in the Objects folder\n"},
		{slate.ErrBridgeExists, 0, "slate: make-bridge: \"slate bridge\" is already in the Objects folder; nothing was made\n"},
		{errors.New("slate: make-bridge: take (20s): timed out"), 3, "slate: make-bridge: take (20s): timed out\n"},
	} {
		bridgeMaker = func(context.Context, *sl.Session) error { return c.err }
		d, _, _ := grid(t)
		code, out, errw := do(t, d, "-make-bridge", "-agent", "Example Resident")
		if code != c.code || out != c.out || errw != "" {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", c.err, code, out, errw)
		}
	}
}

func TestMakeBridgeFailureIsExit3(t *testing.T) {
	// The fake serves no inventory, so the first thing MakeBridge reads fails.
	d, f, _ := grid(t)
	code, out, errw := do(t, d, "-make-bridge")
	if code != 3 || !strings.HasPrefix(out, "slate: make-bridge: ") {
		t.Errorf("exit %d, stdout %q, stderr %q", code, out, errw)
	}
	if f.said() != nil {
		t.Errorf("said %q", f.said())
	}
}
