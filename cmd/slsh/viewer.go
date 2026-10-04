package main

// Where a real viewer would log in to this session, and starting one.
//
//	viewer            where this daemon serves viewer logins, and whether one is attached
//	viewer --launch   start a viewer and hand it the session, logged in as this agent
//
// The address is read out of slgod's StatusResponse, and a daemon that
// serves no viewer logins -- the ordinary case, since -viewer is not
// the default -- is answered in words, with the flag that fixes it.
//
// A launch logs in with a password slgod mints for it, good for a
// single login and short lived: a profile holds only a digest
// (agent.Login.ViewerPassword), and a viewer has to be given the
// plaintext.  It goes on the viewer's command line, so ps shows it
// while the viewer starts; nothing here prints, logs or keeps it, and
// the command is printed with it struck out.
//
// The viewer is told where to go by --grid, a nickname out of its own
// grid list, because Firestorm reads --loginuri and never uses it.  A
// viewer already running is refused, because "open -a" would raise it,
// log nobody in, and look as though it had worked.
// Why: doc/slsh.md#starting-a-viewer-from-slsh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/client"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

var viewerCommands = map[string]*command{
	"viewer": {
		flags:    func() any { return new(viewerOptions) },
		brief:    "where a real viewer can take this session over, and start one",
		keywords: "viewer client graphics eyes see avatar login password take over",
		man:      "viewer",
		run:      cmdViewer,
	},
}

// viewerOptions is what viewer was asked for.
type viewerOptions struct {
	Launch bool `getopt:"--launch -l  start a viewer and hand it this session"`
	Help   bool `getopt:"--help -h    show what this command takes"`
}

func cmdViewer(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o viewerOptions
	if _, done, err := subOptions("viewer", &o, out, args); err != nil || done {
		return err
	}
	conn, ok := sh.conn()
	if !ok {
		return fmt.Errorf("this session was logged in directly; " +
			"viewer logins are served by a daemon, so there is nothing here to hand over")
	}
	if o.Launch {
		return launchViewer(ctx, sh, out, conn)
	}
	return showViewer(ctx, out, conn)
}

// noViewerEndpoint is what a daemon serving no viewer logins is told
// in, and it says how to get one.
//
// The same instruction slgod gives when a credential is asked for of a
// daemon without an endpoint (internal/server/viewer.go, noViewerEndpoint).  Two
// wordings for two moments -- this one is an answer, that one is a
// refusal -- but the flag and the address must not drift apart.
const noViewerEndpoint = "this daemon serves no viewer logins.\n" +
	"Restart slgod with -viewer ADDR -- \"-viewer 127.0.0.1:9000\", say -- " +
	"and a real viewer can take this session over."

// showViewer is the plain report: where, as whom, and whether somebody
// is already there.
func showViewer(ctx context.Context, out io.Writer, conn *client.Conn) error {
	st, err := conn.Status(ctx)
	if err != nil {
		return err
	}
	v := st.GetViewer()
	if v.GetLoginUri() == "" {
		fmt.Fprintln(out, noViewerEndpoint)
		return nil
	}

	fmt.Fprintf(out, "viewer logins at %s\n", v.GetLoginUri())
	fmt.Fprintf(out, "  add a grid with that login URI and log in as %s\n", st.GetAgent().GetAvatarName())
	if v.GetAttached() {
		// One-sided, and said as such: a viewer that logs out is
		// noticed, but one that crashes tells the daemon nothing.
		fmt.Fprintln(out, "  a viewer has taken this session; one that went without logging out would still show here")
	} else {
		fmt.Fprintln(out, "  no viewer has taken this session")
	}
	fmt.Fprintln(out, "  \"viewer --launch\" starts one and logs it in, with a password good for that one login")
	return nil
}

// launchViewer mints a credential and starts a viewer with it.
func launchViewer(ctx context.Context, sh *Shell, out io.Writer, conn *client.Conn) error {
	if sh.cfg.ViewerLaunch == "" {
		return fmt.Errorf("nothing here knows how to start a viewer on %s; "+
			"put a \"viewer_launch = ...\" line in slsh's config (~/.config/slsh/config) "+
			"with {app} {grid} {first} {last} {password} where those belong", runtime.GOOS)
	}

	// Before minting: a credential made for a launch that cannot
	// happen is a live secret nobody asked for.
	if up, err := viewerAlreadyRunning(ctx, sh.cfg); err != nil {
		return err
	} else if up {
		return fmt.Errorf("a viewer is already running: starting it again would raise its " +
			"window and log nobody in, since the login goes on the command line and that one " +
			"has already started; quit the viewer and try again")
	}

	cred, err := conn.ViewerCredential(ctx)
	if err != nil {
		return err
	}
	argv, err := viewerArgv(sh.cfg, cred)
	if err != nil {
		return err
	}
	if err := startViewer(argv); err != nil {
		return err
	}

	fmt.Fprintf(out, "starting a viewer at %s, logged in as %s %s\n",
		cred.GetLoginUri(), cred.GetFirst(), cred.GetLast())
	fmt.Fprintf(out, "  %s\n", strings.Join(hidePassword(argv, cred.GetPassword()), " "))
	fmt.Fprintf(out, "  the password it was given works once and expires in %s\n",
		expiryInWords(cred.GetExpirySeconds()))
	return nil
}

// expiryInWords is how long the credential has, said the way somebody
// would say it.
//
// The daemon counts in seconds because that is the honest unit for a
// deadline, and five minutes of them reads as "300s", which is a number
// a person has to divide before it means anything.
func expiryInWords(seconds int32) string {
	switch {
	case seconds >= 120 && seconds%60 == 0:
		return fmt.Sprintf("%d minutes", seconds/60)
	case seconds == 60:
		return "a minute"
	default:
		return fmt.Sprintf("%d seconds", seconds)
	}
}

// viewerPlaceholders are the words a launch command may contain, and
// what each becomes.
//
// Substituted into words that have ALREADY been split, so that a value
// containing a space stays one argument: an application called "My
// Viewer" cannot become two, and nothing in a password can turn into
// another flag.
func viewerPlaceholders(cfg Config, c *pb.ViewerCredentialResponse) map[string]string {
	return map[string]string{
		"app":  cfg.ViewerApp,
		"grid": cfg.ViewerGrid,

		// The address is still offered even though the default no
		// longer passes it: a viewer that honours --loginuri, or a
		// platform whose launcher takes a URL, should not have to be
		// told the address some other way.
		"uri":      c.GetLoginUri(),
		"first":    c.GetFirst(),
		"last":     c.GetLast(),
		"password": c.GetPassword(),
	}
}

// viewerSettings are the placeholders that come from the config rather
// than from the daemon, so that an empty one can name the line that
// would fill it.
var viewerSettings = map[string]string{
	"app": "viewer_app, naming the viewer to start",
	"grid": "viewer_grid, naming the entry in that viewer's own grid list " +
		"that points at the login URI",
}

// viewerArgv is the command that would start a viewer.
//
// Built rather than run, and separately from running it, so that what
// slsh would do can be read -- by a test, and by anybody who wants to
// know before it happens.
func viewerArgv(cfg Config, c *pb.ViewerCredentialResponse) ([]string, error) {
	words, err := splitCommand(cfg.ViewerLaunch)
	if err != nil {
		return nil, fmt.Errorf("viewer_launch: %w", err)
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("viewer_launch is empty; it has to name a program to run")
	}
	return fillPlaceholders(words, viewerPlaceholders(cfg, c))
}

// fillPlaceholders replaces {name} in each word.
//
// An unknown name is refused rather than passed through.  A viewer
// handed a literal "{passwrd}" would take it for a password and fail
// with something about the login server, which is a long way from the
// typo that caused it.
func fillPlaceholders(words []string, vals map[string]string) ([]string, error) {
	out := make([]string, 0, len(words))
	for _, w := range words {
		var b strings.Builder
		for {
			i := strings.IndexByte(w, '{')
			if i < 0 {
				break
			}
			j := strings.IndexByte(w[i:], '}')
			if j < 0 {
				break
			}
			name := w[i+1 : i+j]
			v, ok := vals[name]
			if !ok {
				return nil, fmt.Errorf("{%s} is not something to fill in; the ones there are "+
					"{app} {grid} {uri} {first} {last} {password}", name)
			}
			if v == "" {
				if setting, ours := viewerSettings[name]; ours {
					return nil, fmt.Errorf("{%s} has nothing to fill it with; put a line in "+
						"slsh's config saying %s", name, setting)
				}
				return nil, fmt.Errorf("{%s} has nothing to fill it with; the daemon named no %s", name, name)
			}
			b.WriteString(w[:i])
			b.WriteString(v)
			w = w[i+j+1:]
		}
		b.WriteString(w)
		out = append(out, b.String())
	}
	return out, nil
}

// splitCommand splits a configured command line into words.
//
// Double quotes group and nothing else is special -- no redirects, no
// pipes, no variables.  This is a command to run and not a shell line,
// and a config setting that quietly grew a shell would be a way to run
// anything from a file that looks like settings.
func splitCommand(line string) ([]string, error) {
	var words []string
	var cur strings.Builder
	started, quoted := false, false
	for _, c := range line {
		switch {
		case c == '"':
			quoted, started = !quoted, true
		case quoted:
			cur.WriteRune(c)
			started = true
		case c == ' ' || c == '\t':
			if started {
				words = append(words, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(c)
			started = true
		}
	}
	if quoted {
		return nil, fmt.Errorf("unclosed \" quote")
	}
	if started {
		words = append(words, cur.String())
	}
	return words, nil
}

// hidePassword is the command with the secret struck out, for printing.
//
// By value rather than by position, since where the password lands in
// the command line is the setting's business and not this function's,
// and anywhere INSIDE a word as well as as a word of its own: a launch
// line may perfectly well say --login={password}, and a redaction that
// only matched whole arguments would print the secret for it.
func hidePassword(argv []string, password string) []string {
	out := make([]string, len(argv))
	for i, w := range argv {
		if password != "" {
			w = strings.ReplaceAll(w, password, "PASSWORD")
		}
		out[i] = w
	}
	return out
}

// viewerAlreadyRunning reports whether there is a viewer up already.
//
// The check is a configured command whose exit status is the answer, in
// the shape pgrep has: nothing found is a failure and not an error.
// Anything worse than that is reported rather than read as "no", since
// answering "no" to a check that could not run is how the case this
// exists to catch gets missed.
//
// An empty setting means no check, and the launch goes ahead.
func viewerAlreadyRunning(ctx context.Context, cfg Config) (bool, error) {
	if cfg.ViewerRunning == "" {
		return false, nil
	}
	words, err := splitCommand(cfg.ViewerRunning)
	if err != nil {
		return false, fmt.Errorf("viewer_running: %w", err)
	}
	if len(words) == 0 {
		return false, nil
	}
	// The config-side placeholders only.  There is no credential here
	// and there must not be one: this runs before anything is minted.
	argv, err := fillPlaceholders(words, map[string]string{
		"app": cfg.ViewerApp, "grid": cfg.ViewerGrid,
	})
	if err != nil {
		return false, fmt.Errorf("viewer_running: %w", err)
	}

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	err = cmd.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
}

// viewerStartGrace is how long to wait for the launch command to fail.
//
// It is not waiting for the viewer.  "open" hands the launch to
// LaunchServices and exits in milliseconds, so anything that comes back
// inside this window came back with something to say -- "Unable to find
// application named ..." is the one that matters, and reporting it
// beats printing a cheerful line about a viewer that is not starting.
// A command that is still running after it is a viewer being run in the
// foreground, which is left to get on with it.
const viewerStartGrace = 2 * time.Second

// startViewer runs the launch command without waiting for the viewer.
func startViewer(argv []string) error {
	cmd := exec.Command(argv[0], argv[1:]...)
	var said bytes.Buffer
	cmd.Stderr = &said
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%s: %w", argv[0], err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			if msg := strings.TrimSpace(said.String()); msg != "" {
				return fmt.Errorf("%s: %v: %s", argv[0], err, msg)
			}
			return fmt.Errorf("%s: %w", argv[0], err)
		}
		return nil
	case <-time.After(viewerStartGrace):
		// Still going, so it is the viewer itself.  The prompt is not
		// held for it; the goroutine above reaps it whenever it ends.
		return nil
	}
}
