// Command slbotd attends a handful of avatars and takes commands from
// them over instant messages.
//
//	slbotd
//	slbotd -config ~/.config/slgo/slbotd.conf -v
//
// It is a client of slgod and holds no credentials of its own.  For
// each avatar named in its configuration it asks slgod to bring the
// session up, attaches to it, and then listens: an instant message
// beginning with the prefix -- ":" unless the file says otherwise -- is
// a command, and anything else is somebody talking and is logged and
// left alone.  Commands are obeyed only from the avatars the
// configuration trusts, and inventory offered by one of them is
// accepted without anybody being at a keyboard.
//
// The commands are a subset of what slsh can do -- looking, moving,
// talking, inventory and building -- and the programs slbench and slrun,
// which are run as separate processes as the avatar that was written
// to.  ":help" lists them; ":help COMMAND" says what one takes.
//
// The configuration is ~/.config/slgo/slbotd.conf, beside the profiles
// it names.  See config.go for what goes in it, and the example at the
// foot of this file's documentation in doc/slbotd.md.
//
// # What it does not do
//
// It does not log anybody in.  slgod owns the grid connection, the
// credentials and the supervision of a session that drops; slbotd asks
// for a session by profile name and attaches to what it is given.  An
// slbotd that is killed leaves every avatar exactly where it was, which
// is the whole reason for the division.
//
// It does not overrule a deliberate logout.  slgod refuses to restart a
// session somebody stopped on purpose -- the usual reason being that
// they are using that avatar in a viewer -- and slbotd stops asking
// when it is told that, until ":host --force" says somebody has
// checked.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/quark-idlemind/slgo/internal/logfile"
	"github.com/quark-idlemind/slgo/internal/slhost"
	"github.com/quark-idlemind/slgo/internal/version"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "slbotd: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		config = flag.String("config", "",
			"the configuration to read; default is slbotd.conf under the profile directory")
		addr = flag.String("addr", "",
			"the slgod to attach to; overrides the configuration, which asks sl-host when it says nothing")
		check = flag.Bool("check", false,
			"read the configuration, say what it means, and exit without connecting")
		quiet = flag.Bool("q", false, "log commands and offers only, not conversation")
		ver   = flag.Bool("version", false, "say which build this is, and exit")
		logTo = flag.String("log", "",
			"append the log to this file, mode 600, rather than writing it to stderr;"+
				" its directory is made 700 if missing and refused if group or others can open it."+
				" ~/.local/log/slbotd.log is the suggested place")
	)
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: slbotd [-config PATH] [-addr HOST:PORT] [-log PATH]\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if *ver {
		fmt.Println(version.String("slbotd"))
		return nil
	}
	if flag.NArg() != 0 {
		flag.Usage()
		return fmt.Errorf("slbotd takes no arguments; the avatars are named in the configuration")
	}

	log.SetFlags(log.Ltime)
	// The log names everybody who spoke to an avatar and, without -q,
	// what they said, so it is kept where only this user can read it;
	// see package logfile.  --check writes to standard output and
	// never here.
	//
	// There is no -log-secrets beside it, as there is on slgod.  This
	// daemon holds no session credentials to redact: it attaches to
	// sessions slgod owns, and what slgod tells it has already been
	// through slgod's redaction.
	if *logTo != "" && !*check {
		f, err := logfile.Append(*logTo)
		if err != nil {
			return fmt.Errorf("log: %w", err)
		}
		defer f.Close()
		log.SetOutput(f)
	}

	cfg, err := LoadConfig(*config)
	if err != nil {
		return err
	}
	if *addr != "" {
		cfg.Addr = *addr
	}

	// Where slgod is.  An address given here or in the file is the
	// operator saying where to go and is not second-guessed; only the
	// empty string is worth asking sl-host about.
	//
	// One slbotd talks to one slgod.  Holding a single avatar, it asks
	// sl-host about that profile, whose rules may send it to a slgod
	// of its own; holding several, it asks about none, and the rules
	// for every profile decide.
	var only string
	if len(cfg.Avatars) == 1 {
		only = cfg.Avatars[0]
	}
	where, err := slhost.ResolveFor(cfg.Addr, only)
	if err != nil {
		return err
	}

	if *check {
		return describe(os.Stdout, cfg, where)
	}

	log.Printf("slbotd, from %s", cfg.Path)
	log.Printf("slgod at %s", where)
	log.Printf("attending %s", strings.Join(cfg.Avatars, ", "))
	log.Printf("commands from %s, beginning with %q",
		strings.Join(cfg.Trusted(), ", "), cfg.Prefix)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := newDaemon(cfg, where, log.Printf)
	d.quiet = *quiet

	// The model, if there is one.  Nothing is asked of it here: it may
	// well be started after this daemon, and one that refused to run
	// until llama-server was up would be one that has to be started in
	// an order.
	if why := cfg.ChatProblem(); why != "" {
		// Loudly, and not fatally.  Attending the avatars is what this
		// daemon is for; chat is beside it, and a missing line there
		// must not be the reason nobody can drive an avatar.
		log.Printf("CHAT IS OFF: %s", why)
		log.Printf("CHAT IS OFF: everything else works; %s --check says what the file means",
			os.Args[0])
	}
	if cfg.ChatOn() && cfg.ChatProblem() == "" {
		chat, err := NewChatter(cfg, log.Printf)
		if err != nil {
			return err
		}
		d.chat = chat
		// A failure the Chatter reports belongs to one avatar, and
		// that avatar is the one whose admin can be told about it.
		chat.troubled = d.noteTrouble
		d.audience = ownAvatarsBounded(d, cfg.ChatOwn, listAudience(cfg))
		store, _ := cfg.ChatStore()
		log.Printf("chatting through %s, conversations in %s", cfg.LLMURL, store)
		log.Printf("will talk to %s", strings.Join(cfg.Chat, ", "))
	}
	d.Run(ctx)
	log.Printf("stopped; the avatars are still logged in")
	return nil
}

// describe says what a configuration means, for somebody who has just
// written one.
//
// It connects to nothing.  The point is to find a mistake in the file
// before an avatar is logged in on the strength of it -- a misspelt
// profile, a program that is not where it was said to be -- and a check
// that needed a grid would not be usable for that.
func describe(out *os.File, cfg Config, where string) error {
	fmt.Fprintf(out, "configuration: %s\n", cfg.Path)
	fmt.Fprintf(out, "slgod:         %s\n", where)
	fmt.Fprintf(out, "avatars:       %s\n", strings.Join(cfg.Avatars, ", "))
	fmt.Fprintf(out, "trusted:       %s\n", strings.Join(cfg.Trusted(), ", "))
	fmt.Fprintf(out, "prefix:        %q\n", cfg.Prefix)
	fmt.Fprintf(out, "timeouts:      %s a command, %s a program run\n", cfg.Timeout, cfg.RunTimeout)
	fmt.Fprintf(out, "at once:       %d commands per avatar\n", cfg.Jobs)
	fmt.Fprintf(out, "answers:       at most %d instant messages\n", cfg.ReplyLimit)
	fmt.Fprintf(out, "offers:        accepted from %s\n", cfg.AcceptInventory)
	if cfg.ChatOn() && cfg.ChatProblem() == "" {
		store, err := cfg.ChatStore()
		if err != nil {
			store = fmt.Sprint(err)
		}
		fmt.Fprintf(out, "model:         %s\n", cfg.LLMURL)
		fmt.Fprintf(out, "will talk to:  %s\n", strings.Join(cfg.Chat, ", "))
		rest := "never resets"
		if cfg.ChatOwnRest > 0 {
			rest = "counted again after " + cfg.ChatOwnRest.String() + " of silence"
		}
		fmt.Fprintf(out, "bots:          %d things said with one, %s\n", cfg.ChatOwn, rest)
		if bots := cfg.ChatBots(); len(bots) > 0 {
			fmt.Fprintf(out, "other bots:    %s\n", strings.Join(bots, ", "))
		}
		if cfg.LLMSlots > 0 {
			fmt.Fprintf(out, "slots:         the first %d of the server's\n", cfg.LLMSlots)
		}
		fmt.Fprintf(out, "conversations: %s, %d tokens kept, %d at once per avatar\n",
			store, cfg.ChatContext, cfg.ChatJobs)
		for _, name := range cfg.Avatars {
			path, ok := cfg.Backstory[name]
			switch {
			case !ok:
				fmt.Fprintf(out, "  %-12s no backstory\n", name)
			default:
				lines, err := describeCharacters(path)
				if err != nil {
					fmt.Fprintf(out, "  %-12s %v\n", name, err)
					break
				}
				fmt.Fprintf(out, "  %-12s %s\n", name, lines[0])
				for _, l := range lines[1:] {
					fmt.Fprintf(out, "  %-12s   %s\n", "", l)
				}
			}
		}
	} else {
		fmt.Fprintf(out, "chat:          off\n")
	}
	if why := cfg.ChatProblem(); why != "" {
		fmt.Fprintf(out, "PROBLEM:       %s\n", why)
	}

	// Every profile named has to exist, and the message when one does
	// not should name the file rather than waiting for slgod to answer
	// "no agent named qx" an hour later.
	for _, name := range cfg.Avatars {
		if _, err := profileExists(name); err != nil {
			fmt.Fprintf(out, "  %-12s %v\n", name, err)
		} else {
			fmt.Fprintf(out, "  %-12s ok\n", name)
		}
	}
	for _, name := range sortedProgramNames(cfg) {
		p := cfg.Programs[name]
		path, err := lookProgram(p.Argv[0])
		if err != nil {
			fmt.Fprintf(out, "  %-12s %v\n", name, err)
		} else {
			fmt.Fprintf(out, "  %-12s %s\n", name, path)
		}
	}
	return nil
}
