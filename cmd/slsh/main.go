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
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"
	"github.com/quark-idlemind/slgo/internal/creds"
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/sl"
)

type opts struct {
	Direct  bool   `getopt:"--direct -d        log in to Second Life directly, without slgod"`
	Addr    string `getopt:"--addr=HOSTPORT    the slgod to attach to; default sl-host, or this machine"`
	Agent   string `getopt:"--agent=NAME -a    the profile to use; the only one, by default"`
	First   string `getopt:"--first=NAME       the avatar's first name, for --direct"`
	Last    string `getopt:"--last=NAME        the avatar's last name, for --direct"`
	Start   string `getopt:"--start=WHERE      where to arrive: last, home, or a region, for --direct"`
	Command string `getopt:"--command=TEXT -c  run one command line and exit"`
	File    string `getopt:"--file=PATH -f     run the commands in a file and exit"`
	Chat    bool   `getopt:"--chat            start in chat mode rather than at a prompt"`
	Escape  string `getopt:"--escape=KEY      the key that leaves chat mode: ESC, ^G, or one character"`
	Help    bool   `getopt:"--help -h         show this message"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "slsh: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}

	o := &opts{Addr: cfg.Addr, Agent: cfg.Agent, Start: "last"}
	args := options.RegisterAndParse(o)
	if o.Help {
		getopt.PrintUsage(os.Stdout)
		return nil
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
		if cfg.Addr, err = slhost.Resolve(cfg.Addr); err != nil {
			return err
		}
		dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if s, err = sl.Dial(dialCtx, cfg.Addr, cfg.Agent); err != nil {
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

	// One command, a file of them, or arguments left on the command
	// line: run and leave, which is what makes slsh usable from a
	// script and from another program.  Chat is swallowed rather than
	// printed, so it cannot land in the middle of the output.
	if oneShot {
		go sh.watchQuietly(ctx)
		switch {
		case o.Command != "":
			sh.Do(ctx, o.Command)
		case o.File != "":
			return sh.Source(ctx, o.File)
		default:
			sh.Do(ctx, strings.Join(args, " "))
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
// run does not have chat landing in the middle of its output.
func (sh *Shell) watchQuietly(ctx context.Context) {
	lines := sh.s.Chat(sl.ChatFilter{}, 64)
	defer sh.s.StopChat(lines)
	for {
		select {
		case <-ctx.Done():
			return
		case <-lines:
		}
	}
}
