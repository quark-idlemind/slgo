// Command slchat is a shell for talking in Second Life.
//
//	slchat [--addr localhost:7807] [--agent example] [--prefix ESC]
//	slchat --direct [--first Quark] [--last Idlemind] [--start last]
//
// By default it attaches to a session slgod is holding, so it starts
// instantly, can be stopped and started as often as you like, and
// leaves the avatar logged in when it exits.
//
// With --direct it logs in itself and holds the session for as long as
// it runs.  That needs no daemon set up and running, at the cost of
// what the daemon is for: quitting logs the avatar out.  Credentials
// come from a profile under ~/.config/slgo when there is one for the
// avatar, and are asked for when there is not -- the password without
// echo.
//
// What is typed goes to the current session: the region's open chat, or
// one person.  Tab on an empty line moves to the next session, the
// prompt says which one that is, and the prefix key -- ESC unless the
// config says otherwise -- starts a command.  Everything heard is
// printed above the line being typed, and everything said is printed
// alongside it with the arrow the other way round.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"
	"github.com/quark-idlemind/slgo/client"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// opts are the command line options, in the getopt style: --direct and
// -d are the same thing, and either may be written --direct=true.
type opts struct {
	Direct bool   `getopt:"--direct -d        log in to Second Life directly, without slgod"`
	Addr   string `getopt:"--addr=HOSTPORT    the slgod to attach to"`
	Agent  string `getopt:"--agent=NAME -a    the profile to use; the only one, by default"`
	First  string `getopt:"--first=NAME       the avatar's first name, for --direct"`
	Last   string `getopt:"--last=NAME        the avatar's last name, for --direct"`
	Start  string `getopt:"--start=WHERE      where to arrive: last, home, or a region name, for --direct"`
	Prefix string `getopt:"--prefix=KEY       the key that starts a command: ESC, ^G, or one character"`
	Help   bool   `getopt:"--help -h          show this message"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "slchat: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}

	// The config file supplies the defaults, so an option left off the
	// command line means "whatever the file said".
	o := &opts{
		Addr:  cfg.Addr,
		Agent: cfg.Agent,
		Start: "last",
	}
	options.RegisterAndParse(o)
	if o.Help {
		getopt.PrintUsage(os.Stdout)
		return nil
	}

	cfg.Addr, cfg.Agent = o.Addr, o.Agent
	if o.Prefix != "" {
		r, err := ParseKey(o.Prefix)
		if err != nil {
			return err
		}
		cfg.Prefix = r
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if o.Direct {
		return runDirect(ctx, cfg, o)
	}
	if o.First != "" || o.Last != "" {
		return fmt.Errorf("--first and --last are for --direct; through slgod the session already knows who it is")
	}
	return runHosted(ctx, cfg)
}

// runHosted talks to a session slgod is holding.
func runHosted(ctx context.Context, cfg Config) error {
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	conn, err := client.Dial(dialCtx, cfg.Addr)
	if err != nil {
		return fmt.Errorf("cannot reach slgod at %s: %w\n"+
			"        --direct logs in without it", cfg.Addr, err)
	}
	defer conn.Close()

	name, err := pickAgent(dialCtx, conn, cfg.Agent)
	if err != nil {
		return err
	}
	cfg.Agent = name

	info, err := conn.Attach(dialCtx, name, subscriptions...)
	if err != nil {
		return fmt.Errorf("cannot attach to %q: %w", name, err)
	}
	return chat(ctx, cfg, conn, info)
}

// runDirect logs in and holds the session itself.
func runDirect(ctx context.Context, cfg Config, o *opts) error {
	login, err := credentials(os.Stdin, os.Stdout, cfg.Agent, o.First, o.Last, o.Start)
	if err != nil {
		return err
	}
	if login.Channel == "" {
		login.Channel = "slchat"
	}

	fmt.Printf("logging in as %s %s...\n", login.First, login.Last)
	loginCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	d, info, err := LoginDirect(loginCtx, login)
	if err != nil {
		return err
	}
	// Logging out is the part a daemon would have made unnecessary,
	// and the part that has to happen on every way out of here.
	defer func() {
		out, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		d.Logout(out, 10*time.Second)
	}()

	if name, ok := d.activeGroup(loginCtx); ok {
		fmt.Printf("acting as group %s\n", name)
	}
	cfg.Direct = true
	return chat(ctx, cfg, d, info)
}

// chat runs the terminal against whichever grid it was given.
func chat(ctx context.Context, cfg Config, g Grid, info *pb.AgentInfo) error {
	t, err := NewTerm(os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	defer t.Close()

	app, err := NewApp(cfg, t, g, info)
	if err != nil {
		return err
	}

	err = app.Run(ctx)
	t.Close()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// pickAgent settles which hosted session to attach to.
//
// Naming one is only necessary when slgod is holding more than one:
// with a single session there is nothing to choose, and with several
// there is nothing to guess.
func pickAgent(ctx context.Context, c *client.Conn, want string) (string, error) {
	if want != "" {
		return want, nil
	}
	agents, err := c.ListAgents(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot list the hosted sessions: %w", err)
	}
	switch len(agents) {
	case 1:
		return agents[0].Name, nil
	case 0:
		return "", fmt.Errorf("that slgod is holding no sessions")
	default:
		names := make([]string, 0, len(agents))
		for _, a := range agents {
			names = append(names, a.Name)
		}
		return "", fmt.Errorf("that slgod holds %d sessions (%v); name one with --agent", len(agents), names)
	}
}
