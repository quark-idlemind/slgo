// Command slate runs a Slate test file against a product in Second Life,
// and makes the one item it needs once.
//
//	slate [-addr ADDR] [-agent NAME] [--pay] [-run REGEX] FILE
//	slate -make-bridge [-addr ADDR] [-agent NAME]
//
// A Slate file says what a person does to an object (touches it, sits on
// it, says something near it, pays it) and what the product must do back
// (say, show a dialog, give an item, rez, change a texture, answer a
// link message). slate checks the file, dials the avatar that is to be
// the tester, and runs the tests in the order they are written. The
// language is in doc/slate-language.md and the runner in
// doc/slate-runner.md.
//
// -run REGEX runs only the tests whose names match, as RE2; the others
// are not printed. --pay lets the file spend Linden dollars: a file that
// pays must say "allow pay" and the process must be given --pay, and
// without it a payment fails the step before anything is sent.
//
// -addr is where slgod is, and $SLGO_ADDR is that same answer when the
// flag is empty. With neither set, where slgod runs is a question for
// sl-host, the same question slsh and slpic ask, and a machine without
// sl-host means this one, localhost:7807. -agent chooses the avatar. An
// empty one is left for sl.DialWeak, which reads $SLGO_AGENT, or takes the
// avatar slgod has held longest.
//
// Everything the run says goes to standard output as it happens, so one
// capture is the whole run: probe hellos, a line for each step, the
// failure block of a step that failed, and a last line saying how many
// tests passed. The failure of the command itself (a usage error, a dial
// that failed, a session that died) goes to standard error.
//
// -make-bridge makes the item "slate bridge" in the Objects folder of the
// avatar's inventory, which a file with a probe or a listen wears during a
// run. It needs build rights where the avatar stands, once per account: it
// rezzes a prim one metre away, names it and takes it. It prints
//
//	slate: make-bridge: "slate bridge" is in the Objects folder
//
// when it has made it, and
//
//	slate: make-bridge: "slate bridge" is already in the Objects folder; nothing was made
//
// when one was there (both exit 0; a second item is never made). A step
// that fails prints "slate: make-bridge: STEP (BUDGET): ERROR" and deletes
// a prim it had already rezzed.
//
// Exit codes:
//
//	0  every test passed (or the bridge item is in the Objects folder)
//	1  a test failed
//	2  the file did not parse or failed a check; nothing was dialled
//	3  setup failed (the dial, an object lookup, a probe, the bridge, or
//	   -make-bridge failing)
//	4  slate was used wrongly: an unknown flag, no FILE or more than one,
//	   a file that cannot be read, or a -run that does not compile or
//	   matches no test; nothing was dialled
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/internal/version"
	"github.com/quark-idlemind/slgo/sl"
	"github.com/quark-idlemind/slgo/slate"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, dial))
}

// dialer attaches to the avatar and returns its session. run closes it.
type dialer func(ctx context.Context, addr, agent string) (*sl.Session, error)

// dial asks where slgod is when nothing says, and attaches. The dial
// itself is limited to 30 seconds, the way slpic's is.
func dial(ctx context.Context, addr, agent string) (*sl.Session, error) {
	if addr == "" {
		addr = os.Getenv("SLGO_ADDR")
	}
	// Nothing on the command line and nothing in the environment leaves
	// the question to sl-host, which is how one binary works on a machine
	// whose slgod is somewhere else. It is asked about the avatar this
	// command is about to attach to.
	at, err := slhost.ResolveFor(addr, sl.AgentName(agent))
	if err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return sl.DialWeak(dctx, at, agent)
}

const usageText = `usage: slate [-addr ADDR] [-agent NAME] [--pay] [-run REGEX] FILE
       slate -make-bridge [-addr ADDR] [-agent NAME]

slate runs the tests in FILE against the products it names, as the avatar
slgod holds. The run is printed to standard output as it happens.

  -run REGEX     run only the tests whose names match (RE2)
  --pay          allow the file's pay steps to spend Linden dollars
  -make-bridge   make the "slate bridge" item once, where the avatar may build
  -addr ADDR     where slgod is; empty asks sl-host, and a machine without
                 sl-host uses localhost:7807 ($SLGO_ADDR is the same as -addr)
  -agent NAME    which avatar (default $SLGO_AGENT, or the one held longest)

exit: 0 passed, 1 a test failed, 2 the file did not parse or check, 3 setup
failed, 4 slate was used wrongly.
`

func run(ctx context.Context, args []string, stdout, stderr io.Writer, dial dialer) int {
	fs := flag.NewFlagSet("slate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usageText) }
	addr := fs.String("addr", "", "where slgod is")
	agent := fs.String("agent", "", "which avatar")
	pay := fs.Bool("pay", false, "allow the file's pay steps to spend Linden dollars")
	runRE := fs.String("run", "", "run only the tests whose names match")
	bridge := fs.Bool("make-bridge", false, "make the slate bridge item")
	showVersion := fs.Bool("version", false, "print the version")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 4 // the flag package has said what was wrong, and Usage
	}
	if *showVersion {
		fmt.Fprintln(stdout, version.String("slate"))
		return 0
	}

	if *bridge {
		var extra []string
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "pay" || f.Name == "run" {
				extra = append(extra, "-"+f.Name)
			}
		})
		if len(extra) > 0 || fs.NArg() > 0 {
			fmt.Fprintln(stderr, "slate: -make-bridge takes no FILE, --pay or -run")
			fs.Usage()
			return 4
		}
		return makeBridge(ctx, *addr, *agent, stdout, stderr, dial)
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "slate: need exactly one FILE")
		fs.Usage()
		return 4
	}
	file := fs.Arg(0)
	src, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(stderr, "slate: %v\n", err)
		return 4
	}
	script, err := slate.Parse(file, src)
	if err == nil {
		err = slate.Check(script)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	var re *regexp.Regexp
	if *runRE != "" {
		if re, err = regexp.Compile(*runRE); err != nil {
			fmt.Fprintf(stderr, "slate: -run %q: %v\n", *runRE, err)
			return 4
		}
		tests, err := script.Expand()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		var names []string
		matched := false
		for _, t := range tests {
			names = append(names, t.Test.Name)
			matched = matched || re.MatchString(t.Test.Name)
		}
		if !matched {
			fmt.Fprintf(stderr, "slate: -run %q matches no test (tests: %s)\n", *runRE, strings.Join(names, ", "))
			return 4
		}
	}

	sess, err := dial(ctx, *addr, *agent)
	if err != nil {
		fmt.Fprintf(stderr, "slate: dial: %v\n", err)
		return 3
	}
	defer sess.Close()

	res, err := slate.Run(ctx, sess, script, slate.Options{Pay: *pay, Run: re, Out: stdout})
	if err != nil {
		// A setup failure is already in the transcript on stdout; say it
		// again on stderr only when it is not.
		if res == nil || !strings.Contains(res.Transcript, strings.TrimPrefix(prefixed(err), "slate: ")) {
			fmt.Fprintln(stderr, prefixed(err))
		}
	}
	if res != nil && res.Exit != 0 {
		return res.Exit
	}
	if err != nil {
		return 1
	}
	return 0
}

// bridgeMaker is slate.MakeBridge, which a test replaces.
var bridgeMaker = slate.MakeBridge

// makeBridge is the -make-bridge mode.
func makeBridge(ctx context.Context, addr, agent string, stdout, stderr io.Writer, dial dialer) int {
	sess, err := dial(ctx, addr, agent)
	if err != nil {
		fmt.Fprintf(stderr, "slate: dial: %v\n", err)
		return 3
	}
	defer sess.Close()
	switch err := bridgeMaker(ctx, sess); {
	case err == nil:
		fmt.Fprintln(stdout, `slate: make-bridge: "slate bridge" is in the Objects folder`)
	case errors.Is(err, slate.ErrBridgeExists):
		fmt.Fprintln(stdout, err)
	default:
		fmt.Fprintln(stdout, err)
		return 3
	}
	return 0
}

// prefixed is an error's text with the command's name, once.
func prefixed(err error) string {
	if s := err.Error(); strings.HasPrefix(s, "slate:") {
		return s
	}
	return "slate: " + err.Error()
}
