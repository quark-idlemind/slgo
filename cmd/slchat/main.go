// Command slchat is a shell for talking in Second Life.
//
//	slchat [-addr localhost:7807] [-agent example] [-prefix ESC]
//
// It attaches to a session slgod is holding, so it starts instantly,
// can be stopped and started as often as you like, and leaves the
// avatar logged in when it exits.
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
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/quark-idlemind/slgo/client"
)

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

	var (
		addr   = flag.String("addr", cfg.Addr, "the slgod to attach to")
		agent  = flag.String("agent", cfg.Agent, "which hosted profile to talk as; the only one, by default")
		prefix = flag.String("prefix", "", "the key that starts a command: ESC, ^G, or one character")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: slchat [-addr host:port] [-agent name] [-prefix key]\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	cfg.Addr, cfg.Agent = *addr, *agent
	if *prefix != "" {
		r, err := ParseKey(*prefix)
		if err != nil {
			return err
		}
		cfg.Prefix = r
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	conn, err := client.Dial(dialCtx, cfg.Addr)
	if err != nil {
		return fmt.Errorf("cannot reach slgod at %s: %w", cfg.Addr, err)
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

	t, err := NewTerm(os.Stdin, os.Stdout)
	if err != nil {
		return err
	}
	defer t.Close()

	app, err := NewApp(cfg, t, conn, info)
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
		return "", fmt.Errorf("that slgod holds %d sessions (%v); name one with -agent", len(agents), names)
	}
}
