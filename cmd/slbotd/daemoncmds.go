package main

// The commands about the daemon itself rather than about the grid:
// what it holds, what it is doing, and how to run something as one of
// the other avatars.

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/sl"
)

var daemonCommands = map[string]*command{
	"help": {
		params: "[COMMAND]",
		flags:  func() any { return new(helpOnly) },
		brief:  "the commands, or what one of them takes",
		group:  groupDaemon,
		run:    cmdHelp,
	},
	"agents": {
		flags: func() any { return new(helpOnly) },
		brief: "the avatars slgod holds, and which of them this daemon attends",
		group: groupDaemon,
		run:   cmdAgents,
	},
	"status": {
		flags: func() any { return new(helpOnly) },
		brief: "this avatar: who, where, and how the session is doing",
		group: groupDaemon,
		run:   cmdStatus,
	},
	"as": {
		params: "AVATAR COMMAND [ARG ...]",
		flags:  func() any { return new(helpOnly) },
		brief:  "run a command as another of the avatars this daemon holds",
		group:  groupDaemon,
		run:    cmdAs,
	},
	"host": {
		params: "[AVATAR]",
		flags:  func() any { return new(hostFlags) },
		brief:  "ask slgod to bring an avatar back up; --force overrules a deliberate logout",
		group:  groupDaemon,
		run:    cmdHost,
	},
	"trusted": {
		flags: func() any { return new(helpOnly) },
		brief: "who this daemon takes commands from",
		group: groupDaemon,
		run:   cmdTrusted,
	},
	"errors": {
		params: "[clear]",
		flags:  func() any { return new(helpOnly) },
		brief:  "what has gone wrong with this avatar lately",
		group:  groupDaemon,
		run:    cmdErrors,
	},
}

// asDepth is how many "as" commands may nest.
//
// One is all anybody wants and the limit is not about taste: three
// avatars can be made to hand a command round in a circle, and a limit
// is the only thing that ends it.  Two, so that "as a as b where" still
// works for somebody who meant it.
const asDepth = 2

func cmdHelp(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("help", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	p := r.d.cfg.Prefix

	if len(rest) == 1 {
		name := rest[0]
		if to, ok := r.d.cfg.Aliases[name]; ok {
			fmt.Fprintf(out, "%s is another name for %s\n", name, to)
			name = to
		}
		if c, ok := commands[name]; ok {
			fmt.Fprintf(out, "%s -- %s\n", name, c.brief)
			// The command's own --help composes the usage line and
			// lists the options, so it is asked rather than imitated.
			_, _, err := subOptions(name, c.flags(), out, []string{"--help"})
			return err
		}
		if prog, ok := r.d.cfg.Programs[name]; ok {
			fmt.Fprintf(out, "%s runs %s, with whatever you type after it.\n",
				name, strings.Join(prog.Argv, " "))
			fmt.Fprintf(out, "%s%s --help asks the program itself.\n", p, name)
			return nil
		}
		return fmt.Errorf("no command called %q", rest[0])
	}
	if len(rest) > 1 {
		return usage("help", "one command at a time")
	}

	fmt.Fprintf(out, "A message beginning with %s is a command.\n", p)
	byGroup := map[string][]string{}
	for _, n := range commandNames() {
		c := commands[n]
		byGroup[c.group] = append(byGroup[c.group], n)
	}
	for _, g := range groupOrder {
		names := byGroup[g]
		if len(names) == 0 {
			continue
		}
		fmt.Fprintf(out, "\n%s\n", g)
		for _, n := range names {
			fmt.Fprintf(out, "  %-10s %s\n", n, commands[n].brief)
		}
	}
	if len(r.d.cfg.Programs) > 0 {
		fmt.Fprintf(out, "\n%s\n", groupRunning)
		var names []string
		for n := range r.d.cfg.Programs {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Fprintf(out, "  %-10s run %s\n", n, r.d.cfg.Programs[n].Argv[0])
		}
	}
	if len(r.d.cfg.Aliases) > 0 {
		var lines []string
		for from, to := range r.d.cfg.Aliases {
			lines = append(lines, fmt.Sprintf("%s=%s", from, to))
		}
		sort.Strings(lines)
		fmt.Fprintf(out, "\nalso answers to: %s\n", strings.Join(lines, ", "))
	}
	fmt.Fprintf(out, "\n%shelp COMMAND says what one of them takes.\n", p)
	return nil
}

func cmdAgents(ctx context.Context, r *req, out io.Writer, args []string) error {
	if _, done, err := subOptions("agents", new(helpOnly), out, args); err != nil || done {
		return err
	}
	rows, err := r.d.agentLines(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		mark := " "
		if row.Mine {
			mark = "*"
		}
		line := fmt.Sprintf("%s %-10s %s", mark, row.Name, row.State)
		if row.Avatar != "" {
			line += ", " + row.Avatar
		}
		if row.Region != "" {
			line += " in " + row.Region
		}
		fmt.Fprintln(out, line)
		if row.Attends != "" {
			fmt.Fprintf(out, "      slbotd: %s\n", row.Attends)
		}
	}
	fmt.Fprintln(out, "* is one this daemon attends")
	return nil
}

func cmdStatus(ctx context.Context, r *req, out io.Writer, args []string) error {
	if _, done, err := subOptions("status", new(helpOnly), out, args); err != nil || done {
		return err
	}
	st, detail := r.bot.State()
	fmt.Fprintf(out, "%s: %s", r.bot.Name(), st)
	if detail != "" && st != stateAttached {
		fmt.Fprintf(out, " (%s)", detail)
	}
	fmt.Fprintf(out, ", for %s\n", time.Since(r.bot.Since()).Round(time.Second))

	s, err := r.bot.Need()
	if err != nil {
		return err
	}
	info := s.Info()
	fmt.Fprintf(out, "%s, %s\n", info.AvatarName, info.AgentID)
	if p, err := s.Where(ctx); err == nil {
		fmt.Fprintf(out, "in %s at %.0f, %.0f, %.0f\n",
			p.Region, p.Position.X, p.Position.Y, p.Position.Z)
	}
	fmt.Fprintf(out, "%d of %d commands running\n", len(r.bot.jobs), cap(r.bot.jobs))
	if h, ok := s.Backend().(*sl.Hosted); ok {
		if n := h.Dropped(); n > 0 {
			fmt.Fprintf(out, "%d relayed messages dropped\n", n)
		}
	}
	return nil
}

func cmdAs(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("as", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	if len(rest) < 2 {
		return usage("as", "an avatar and a command to run as it")
	}
	if r.depth >= asDepth {
		return fmt.Errorf("as is %d deep already; that is as far as it goes", r.depth)
	}
	b, ok := r.d.Bot(rest[0])
	if !ok {
		return fmt.Errorf("this daemon does not hold %q; %sagents lists what it holds",
			rest[0], r.d.cfg.Prefix)
	}
	if _, err := b.Need(); err != nil {
		return err
	}
	sub := &req{d: r.d, bot: b, from: r.from, who: r.who, depth: r.depth + 1, base: r.base}
	fmt.Fprintf(out, "as %s:\n", b.Name())
	return sub.run(ctx, out, rest[1:])
}

// hostFlags is what host was asked for.
type hostFlags struct {
	Force bool `getopt:"--force -f  start it even though somebody stopped it on purpose"`
	Help  bool `getopt:"--help -h   show what this command takes"`
}

// cmdHost asks slgod to bring an avatar up again.
//
// Without --force this is only a nudge: slgod refuses to restart a
// session that was logged out deliberately, because somebody is
// probably using that avatar in a viewer, and slbotd stops asking when
// it is told that.  The flag is how a person says they have checked.
func cmdHost(ctx context.Context, r *req, out io.Writer, args []string) error {
	var o hostFlags
	rest, done, err := subOptions("host", &o, out, args)
	if err != nil || done {
		return err
	}
	b := r.bot
	switch len(rest) {
	case 0:
	case 1:
		var ok bool
		if b, ok = r.d.Bot(rest[0]); !ok {
			return fmt.Errorf("this daemon does not hold %q", rest[0])
		}
	default:
		return usage("host", "one avatar")
	}

	if s := b.Session(); s != nil && !o.Force {
		fmt.Fprintf(out, "%s is already attached, as %s\n", b.Name(), s.Info().AvatarName)
		return nil
	}
	b.Wake(o.Force)
	if o.Force {
		fmt.Fprintf(out, "asked for %s, overruling a deliberate logout\n", b.Name())
	} else {
		fmt.Fprintf(out, "asked for %s\n", b.Name())
	}
	fmt.Fprintf(out, "%sagents says what came of it\n", r.d.cfg.Prefix)
	return nil
}

func cmdTrusted(ctx context.Context, r *req, out io.Writer, args []string) error {
	if _, done, err := subOptions("trusted", new(helpOnly), out, args); err != nil || done {
		return err
	}
	for _, t := range r.d.cfg.Trusted() {
		fmt.Fprintln(out, t)
	}
	fmt.Fprintf(out, "inventory offers: %s\n", r.d.cfg.AcceptInventory)
	return nil
}

// cmdErrors says what has gone wrong, and forgets it when asked.
//
// The log file has all of this and has it across restarts.  What this
// is for is the one person who cannot read that file: whoever is in
// the virtual world, talking to the avatar, wondering why it has been
// quiet.
func cmdErrors(ctx context.Context, r *req, out io.Writer, args []string) error {
	rest, done, err := subOptions("errors", new(helpOnly), out, args)
	if err != nil || done {
		return err
	}
	switch {
	case len(rest) == 0:
		list := r.bot.Troubles()
		fmt.Fprintln(out, renderTroubles(list, r.bot.trouble.dropped, time.Now()))
		// Asked for is told about: somebody who has just read them
		// does not want them announced again next time.
		r.bot.trouble.noteReported()
		return nil
	case len(rest) == 1 && rest[0] == "clear":
		fmt.Fprintf(out, "forgot %d.\n", r.bot.trouble.clear())
		return nil
	default:
		return fmt.Errorf("errors takes nothing, or the word \"clear\"")
	}
}
