// Command slsh is a shell for Second Life.
//
//	slsh [--addr HOST:PORT] [--agent example]
//	slsh --direct [--first Quark] [--last Idlemind]
//	slsh -c "ls -l Objects"
//
// Commands are the outer mode: cd and ls walk inventory, who and where
// look at the world, and "chat" enters chat mode, which the escape key
// leaves again.  Everything heard is printed whichever mode is in
// force, above the line being typed.
//
// Output redirects with > and >>, and ". file" runs a file of commands,
// which together are the point: a listing can be written out, edited
// into a list of moves, and run.
//
// By default it attaches to a session slgod is holding, so it starts
// instantly and leaves the avatar logged in when it exits.  With
// --direct it logs in itself and holds the session for as long as it
// runs.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/creds"
	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/internal/version"
	"github.com/quark-idlemind/slgo/sl"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type opts struct {
	Direct  bool   `getopt:"--direct -d        log in to Second Life directly, without slgod"`
	Addr    string `getopt:"--addr=HOSTPORT    the slgod to attach to; default sl-host, or this machine"`
	Agent   string `getopt:"--agent=NAME -a    the profile to use; $SLGO_AGENT, or the daemon's default"`
	Login   string `getopt:"--login=NAME       log NAME in through slgod unless it is up, and use it"`
	Logout  string `getopt:"--logout=NAME      log NAME out through slgod, and exit"`
	Agents  bool   `getopt:"--agents          list the avatars slgod holds, and exit"`
	First   string `getopt:"--first=NAME       the avatar's first name, for --direct"`
	Last    string `getopt:"--last=NAME        the avatar's last name, for --direct"`
	Start   string `getopt:"--start=WHERE      where to arrive: last, home, or a region, for --direct"`
	Command string `getopt:"--command=TEXT -c  run one command line and exit"`
	File    string `getopt:"--file=PATH -f     run the commands in a file and exit"`
	Chat    bool   `getopt:"--chat            start in chat mode rather than at a prompt"`
	Escape  string `getopt:"--escape=KEY      the key that leaves chat mode: ESC, ^G, or one character"`
	Help    bool   `getopt:"--help -h         show this message"`
	Version bool   `getopt:"--version         say which build this is, and exit"`
}

// errReported ends a one-shot run whose command failed.  The shell
// said why when it happened, so main exits 1 without saying it again.
// It is returned rather than exited on so that run's deferred calls
// happen first: for --direct, one of them is the logout.
var errReported = errors.New("already reported")

func main() {
	if err := run(); err != nil {
		// Made visible, since the error that ends a session can carry
		// the grid's own words -- a kick's reason is the simulator's
		// text, passed on as it came -- and this is written to the
		// terminal after the shell has let go of it.  See Term.Print.
		if !errors.Is(err, errReported) {
			fmt.Fprintf(os.Stderr, "slsh: %s\n", visible(err.Error()))
		}
		os.Exit(1)
	}
}

func run() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}

	// SLGO_AGENT sits between the flag and the file: it describes this
	// shell, where the file describes the machine, and --agent is the
	// deliberate statement that beats both.  Using it as the flag's
	// DEFAULT is what gives that order, since a flag actually given
	// then overrides it.
	o := &opts{Addr: cfg.Addr, Agent: session.AgentName(""), Start: "last"}
	if o.Agent == "" {
		o.Agent = cfg.Agent
	}
	args := options.RegisterAndParse(o)
	if o.Help {
		getopt.PrintUsage(os.Stdout)
		return nil
	}
	if o.Version {
		fmt.Println(version.String("slsh"))
		return nil
	}

	if o.Login != "" {
		if o.Direct {
			return fmt.Errorf("--login asks slgod to log an avatar in; --direct logs in by itself")
		}
		if getopt.IsSet("agent") && o.Agent != o.Login {
			return fmt.Errorf("--login %s and --agent %s name two avatars", o.Login, o.Agent)
		}
		o.Agent = o.Login
	}
	cfg.Addr, cfg.Agent, cfg.Chat = o.Addr, o.Agent, o.Chat
	if o.Escape != "" {
		r, err := ParseKey(o.Escape)
		if err != nil {
			return err
		}
		cfg.Prefix = r
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if o.Agents {
		if o.Direct || o.Login != "" || o.Logout != "" || o.Command != "" || o.File != "" {
			return fmt.Errorf("--agents lists slgod's avatars and exits; it takes nothing to do after")
		}
		addr, err := slhost.ResolveFor(cfg.Addr, cfg.Agent)
		if err != nil {
			return err
		}
		return agentsOnly(ctx, addr, os.Stdout)
	}
	if o.Logout != "" {
		if o.Direct || o.Login != "" || o.Command != "" || o.File != "" {
			return fmt.Errorf("--logout logs an avatar out and exits; it takes nothing to do after")
		}
		addr, err := slhost.ResolveFor(cfg.Addr, o.Logout)
		if err != nil {
			return err
		}
		return logoutOnly(ctx, addr, o.Logout, os.Stdout)
	}

	// Connect, one way or the other.  Everything after this is the
	// same either way.
	var s *sl.Session
	if o.Direct {
		login, err := creds.Resolve(os.Stdin, os.Stdout, cfg.Agent, o.First, o.Last, o.Start)
		if err != nil {
			return err
		}
		if login.Channel == "" {
			login.Channel = "slsh"
		}
		fmt.Printf("logging in as %s %s...\n", login.First, login.Last)
		loginCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		if s, err = sl.LoginDirect(loginCtx, login); err != nil {
			return err
		}
		cfg.Direct = true
	} else {
		if o.First != "" || o.Last != "" {
			return fmt.Errorf("--first and --last are for --direct; through slgod the session knows who it is")
		}
		// Nothing said on the command line and nothing in the file
		// leaves the question to sl-host, which is how one config
		// works on a machine whose slgod is somewhere else.
		if cfg.Addr, err = slhost.ResolveFor(cfg.Addr, cfg.Agent); err != nil {
			return err
		}
		dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if o.Login != "" {
			s, err = loginAndDial(ctx, dialCtx, cfg.Addr, o.Login, os.Stderr)
		} else {
			s, err = sl.Dial(dialCtx, cfg.Addr, cfg.Agent)
		}
		if err != nil {
			// A session down for good cannot be attached to, and a
			// daemon holding none has no default: --login is the way
			// in for both, and needs no session up to ask through.
			if o.Login == "" {
				switch code := status.Code(err); {
				case code == codes.FailedPrecondition && cfg.Agent != "":
					return fmt.Errorf("%w\n        slsh --login %s brings it back", err, cfg.Agent)
				case code == codes.NotFound && cfg.Agent == "":
					return fmt.Errorf("%w\n        slsh --login NAME logs one in", err)
				}
			}
			return fmt.Errorf("%w\n        --direct logs in without slgod", err)
		}
	}
	defer s.Close()

	// A one-shot run reads no keys, so the terminal is given nothing
	// to read.  Handing it os.Stdin as well would put two readers on
	// one file and they would take alternate lines -- which is a race
	// that looks like commands going missing.
	oneShot := o.Command != "" || o.File != "" || len(args) > 0
	in := os.Stdin
	if oneShot {
		if devNull, err := os.Open(os.DevNull); err == nil {
			defer devNull.Close()
			in = devNull
		}
	}

	t, err := NewTerm(in, os.Stdout)
	if err != nil {
		return err
	}
	defer t.Close()

	sh := NewShell(cfg, t, s)
	defer sh.Close()

	// One command, a file of them, or arguments left on the command
	// line: run and leave, which is what makes slsh usable from a
	// script and from another program.  Chat is swallowed rather than
	// printed, so it cannot land in the middle of the output.
	if oneShot {
		go sh.watchQuietly(ctx, sh.s.Chat(sl.ChatFilter{}, 64))
		// A failed command is a failed run.  Exiting 0 either way left
		// anything driving slsh from a script no way to tell without
		// scraping the output.
		var err error
		switch {
		case o.Command != "":
			err = sh.Do(ctx, o.Command)
		case o.File != "":
			// A failed line is reported where it failed; a file that
			// will not open is not, and goes back to main to say.
			if err = sh.Source(ctx, o.File); err != nil && !errors.Is(err, errStopped) {
				return err
			}
		case len(args) == 1:
			// One argument is a line, as -c takes one: slsh "ls > list".
			err = sh.Do(ctx, args[0])
		default:
			// Several are words the calling shell has already split,
			// so a quoted name with a space in it stays one name.
			err = sh.DoWords(ctx, args)
		}
		if err != nil {
			return errReported
		}
		return nil
	}

	// Everything else is the loop, whether the input is a terminal or
	// a pipe: in plain mode the terminal turns lines into keys, so a
	// piped session runs the same code an interactive one does.
	err = sh.Run(ctx)
	t.Close()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// watchQuietly reads the relay without printing it, so that a one-shot
// run does not have chat landing in the middle of its output.  It takes
// the subscription rather than making one, and stops it when ctx ends
// or the session closes it.
func (sh *Shell) watchQuietly(ctx context.Context, lines <-chan sl.Line) {
	defer sh.s.StopChat(lines)
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-lines:
			if !ok {
				return
			}
		}
	}
}

// loginAndDial asks slgod to bring a session up, as the login command
// does, and attaches to it on the same connection: one that is already
// up is left as it is, and one that was down is logged in and said so
// on note.  A session down on purpose is started too, since naming it
// here is the deliberate act login's comment describes.
//
// The dial and the attach are bounded by dialCtx; the login is bounded
// by the daemon, which is what knows how long one takes.
func loginAndDial(ctx, dialCtx context.Context, addr, name string, note io.Writer) (*sl.Session, error) {
	conn, err := client.Dial(dialCtx, addr)
	if err != nil {
		return nil, fmt.Errorf("sl: cannot reach slgod at %s: %w", addr, err)
	}
	r, err := conn.Host(ctx, name, true)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("logging %s in: %w", name, err)
	}
	if !r.GetAlready() {
		fmt.Fprintf(note, "%s: %s in %s\n", name, r.GetAgent().GetAvatarName(), r.GetAgent().GetRegion())
	}
	h, err := sl.AttachConn(dialCtx, conn, name)
	if err != nil {
		conn.Close()
		return nil, err
	}
	s, err := sl.New(h)
	if err != nil {
		h.Close()
		return nil, err
	}
	return s, nil
}

// logoutOnly logs a session out without attaching to it, so the shell
// asking is never one of the clients the logout is refused for, and the
// last avatar up can be put down.  It is not forced: a client using the
// session still stops it, and is named.
func logoutOnly(ctx context.Context, addr, name string, out io.Writer) error {
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := client.Dial(dialCtx, addr)
	if err != nil {
		return fmt.Errorf("sl: cannot reach slgod at %s: %w", addr, err)
	}
	defer conn.Close()
	r, err := conn.Logout(ctx, name, false)
	if err != nil {
		if cs := r.GetClients(); len(cs) > 0 {
			return fmt.Errorf("%w\n        attached: %s", err, strings.Join(cs, ", "))
		}
		return err
	}
	fmt.Fprintf(out, "%s logged out; slsh --login %s brings it back\n", name, name)
	return nil
}

// agentsOnly lists the daemon's avatars without attaching to one, so it
// answers with nobody up, which is when the list is most wanted.
func agentsOnly(ctx context.Context, addr string, out io.Writer) error {
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := client.Dial(dialCtx, addr)
	if err != nil {
		return fmt.Errorf("sl: cannot reach slgod at %s: %w", addr, err)
	}
	defer conn.Close()
	agents, err := conn.ListAgents(ctx)
	if err != nil {
		return err
	}
	printAgents(out, agents, "")
	return nil
}
