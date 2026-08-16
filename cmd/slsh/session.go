package main

// The session itself, rather than the world on the other side of it:
// how the connection is doing, and what is actually crossing it.
//
// Both of these are below the level the rest of the shell works at.
// Everything else here asks about inventory or objects or people --
// things the sl package models -- and these two ask about the plumbing.
// They are here because when the plumbing is what is wrong, the
// alternative is a second program that exists only for the days it goes
// wrong, and which is therefore never up to date when one arrives.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
)

var sessionCommands = map[string]*command{
	"status": {
		flags: func() any { return new(helpOnly) },
		brief: "how this session and its circuit are doing",
		man:   "status",
		run:   cmdStatus,
	},
	"watch": {
		params: "[NAME...]",
		flags:  func() any { return new(watchOptions) },
		brief:  "print grid messages as they arrive; no NAME means everything",
		man:    "watch",
		run:    cmdWatch,
	},
}

// cmdStatus reports the circuit, which is the daemon's business and
// nothing the sl package models.
//
// Worth having at the prompt: the counters say whether a session that
// looks idle is idle or broken, and "no handler for" names the messages
// this build does not understand -- which is how a protocol change
// announces itself.
func cmdStatus(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var flags helpOnly
	if _, done, err := subOptions("status", &flags, out, args); err != nil || done {
		return err
	}
	conn, ok := sh.conn()
	if !ok {
		return fmt.Errorf("this session was logged in directly; there is no daemon to ask")
	}

	st, err := conn.Status(ctx)
	if err != nil {
		return err
	}
	a := st.GetAgent()
	fmt.Fprintf(out, "%s (%s) in %s\n", a.GetAvatarName(), a.GetName(), a.GetRegion())
	fmt.Fprintf(out, "  simulator   %s\n", a.GetChannelVersion())
	fmt.Fprintf(out, "  packets     %d in, %d out (%d resent, %d abandoned)\n",
		st.PacketsIn, st.PacketsOut, st.Resent, st.Abandoned)
	fmt.Fprintf(out, "  duplicates  %d\n", st.Duplicates)
	fmt.Fprintf(out, "  clients     %d\n", st.Clients)
	fmt.Fprintf(out, "  caps        %d\n", len(a.GetCaps()))

	if len(st.Unhandled) > 0 {
		keys := make([]string, 0, len(st.Unhandled))
		for k := range st.Unhandled {
			keys = append(keys, k)
		}
		// Commonest first: the one arriving hundreds of times is the
		// one worth writing a handler for.
		sort.Slice(keys, func(i, j int) bool { return st.Unhandled[keys[i]] > st.Unhandled[keys[j]] })
		fmt.Fprintln(out, "  no handler for:")
		for _, k := range keys {
			fmt.Fprintf(out, "    %-32s %d\n", k, st.Unhandled[k])
		}
	}
	return nil
}

// watchOptions is what watch was asked for.
type watchOptions struct {
	For  time.Duration `getopt:"--for=D -t     how long to watch; default half a minute"`
	Help bool          `getopt:"--help -h      show what this command takes"`
}

// cmdWatch prints relayed grid messages as they arrive.
//
// It opens a connection OF ITS OWN rather than using the shell's.
//
// Two reasons, and the second is the one that matters.  A session has a
// single goroutine consuming the relay -- sl.Session.read, which
// dispatches everything the rest of the shell depends on -- so there is
// no second reader to be had.  And a watch wants a different
// subscription, often "*"; applying that to the shared stream would
// pour every message the region produces through the reader that is
// keeping this shell's idea of the world up to date, and what it
// dropped while doing so would be missed silently.
//
// A separate connection costs one extra stream for as long as the watch
// runs, and isolates both.
func cmdWatch(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o watchOptions
	names, done, err := subOptions("watch", &o, out, args)
	if err != nil || done {
		return err
	}
	if o.For == 0 {
		o.For = 30 * time.Second
	}
	if len(names) == 0 {
		names = []string{"*"}
	}
	if sh.cfg.Direct {
		return fmt.Errorf("this session was logged in directly; watch needs a daemon to relay from")
	}

	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	conn, err := client.Dial(dialCtx, sh.cfg.Addr)
	if err != nil {
		return fmt.Errorf("opening a second connection to watch on: %w", err)
	}
	defer conn.Close()

	// The same agent this shell is attached to, named outright: the
	// daemon's default could in principle have moved since.
	if _, err := conn.Attach(ctx, sh.s.Info().Name, names...); err != nil {
		return fmt.Errorf("attaching to watch: %w", err)
	}

	fmt.Fprintf(out, "watching %v for %v\n", names, o.For)
	deadline := time.After(o.For)
	seen := 0
	for {
		select {
		case m, ok := <-conn.Messages():
			if !ok {
				return fmt.Errorf("the stream ended: %w", conn.Err())
			}
			showMessage(out, m)
			seen++
		case <-deadline:
			fmt.Fprintf(out, "%d messages\n", seen)
			return nil
		case <-ctx.Done():
			fmt.Fprintf(out, "%d messages\n", seen)
			return nil
		}
	}
}

// showMessage prints one, decoded where it can be.
//
// A message the template does not have is still reported rather than
// skipped: a number this build has never heard of is exactly what a
// protocol change looks like, and saying "%d bytes, not in this
// template" is the useful half of that.
func showMessage(out io.Writer, m *client.Message) {
	name := m.Name
	if name == "" {
		name = fmt.Sprintf("unknown %d", m.ID)
	}
	v, err := m.Decode()
	switch {
	case err != nil:
		fmt.Fprintf(out, "%s seq=%d: undecodable: %v\n", name, m.Sequence, err)
	case v == nil:
		fmt.Fprintf(out, "%s seq=%d: %d bytes, not in this template\n", name, m.Sequence, len(m.Body))
	default:
		fmt.Fprint(out, msg.DumpMessage(v))
	}
}
