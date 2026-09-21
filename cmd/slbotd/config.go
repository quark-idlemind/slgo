package main

// What slbotd was told to do, and where it was told.
//
// The file is beside the profiles rather than in a directory of its
// own:
//
//	~/.config/slgo/            mode 700, the profiles live here
//	~/.config/slgo/slbotd.conf
//
// which is deliberate.  Every avatar line in it is the name of a
// profile in that same directory, so the two are read together and
// keeping them apart would mean remembering two paths to say one thing.
// It is not a profile and does not look like one: agent.ListProfiles
// decides what is a profile by loading it, and this file fails that
// test on its first line, so it is passed over rather than offered as
// an avatar to log in as.
//
// Nothing secret is in it.  slbotd never handles a password -- it asks
// slgod to bring a session up and slgod reads the credentials -- so the
// file names avatars and people and holds no secret of its own, and is
// not refused for being readable.  The directory around it still has to
// be private, because the profiles are in it, and that is said here as
// well as by the profile loader so that the two cannot drift.
//
// The format is the profiles' own: key = value, # to the end of the
// line for a comment, and an unknown key is an error rather than a
// setting that silently does nothing.  Some keys may be given more than
// once -- avatar, trusted, program, alias -- and those accumulate; the
// rest take the last word.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// ConfigName is what the file is called, under the profile directory.
const ConfigName = "slbotd.conf"

// EnvConfig names the file outright, for a second daemon on one machine
// or for a test that must not read the operator's own.
const EnvConfig = "SLBOTD_CONFIG"

// Config is the whole of what slbotd was told.
type Config struct {
	// Path is where this was read from, for saying so in a message
	// about it.
	Path string

	// Avatars are the profiles to hold, in the order they were
	// written.  The order is the only thing that decides which one
	// answers a command that names no avatar, so it is kept.
	Avatars []string

	// Addr is the slgod to attach to.  Empty asks sl-host, and failing
	// that means this machine.
	Addr string

	// Prefix is what marks a command.  An instant message that does not
	// begin with it is somebody talking.
	Prefix string

	// Timeout bounds one command, and RunTimeout one program run.  Two
	// numbers because they answer different questions: a listing that
	// has not arrived in a minute is a fault, and a benchmark that has
	// not finished in a minute is a benchmark.
	Timeout    time.Duration
	RunTimeout time.Duration

	// Jobs is how many commands one avatar runs at once.  Beyond it a
	// sender is told the avatar is busy rather than being queued
	// silently behind somebody else's benchmark.
	Jobs int

	// ReplyLimit is how many instant messages one answer may be sent
	// back as.  A listing longer than that is cut off and says so.
	ReplyLimit int

	// AcceptInventory says whose inventory offers are taken: trusted,
	// anyone, or nobody.
	AcceptInventory string

	// AnswerStrangers says whether somebody who is not trusted is told
	// that their command was refused, or simply not answered.
	AnswerStrangers bool

	// Programs are the external commands a trusted avatar may run,
	// by the name the command is typed under.
	Programs map[string]*Program

	// Aliases are other names for a command, resolved once before the
	// table is looked in.
	Aliases map[string]string

	// Chat is who an avatar will hold a conversation with, as the file
	// wrote it: names, uuids, or the one word that means everybody.
	// Kept as written rather than resolved here, because what it means
	// is audience.go's business and this is only the reading of it.
	//
	// A list of its own rather than a flag on the trusted list: driving
	// an avatar and being spoken to by one are different powers, and
	// somebody may reasonably have either without the other.
	Chat []string

	// ChatGap is how long a silence has to be before an avatar is told
	// about it.  Below it the pause is not worth remarking on and
	// saying so would be an avatar that notices every coffee break.
	//
	// It does not touch what is remembered.  Nothing is dropped, no
	// conversation is started again; the avatar is simply told that
	// time passed, in the same conversation it was already having.
	ChatGap time.Duration

	// ErrorGap is how long a silence makes the next remark from a
	// trusted person a FRESH approach -- the one that is told, in a
	// line, that things have gone wrong since.
	//
	// Long rather than short.  What it guards against is an avatar
	// that opens every other remark with the same complaint; somebody
	// working with an avatar all afternoon has already been told, and
	// asks for the detail when they want it.
	ErrorGap time.Duration

	// Backstory is where each avatar's character is written, by
	// profile: a file, or a directory holding one character per person
	// it talks to.  See character.go for what a directory may hold.
	//
	// Read when it is used rather than held here, so that working on a
	// character does not mean restarting the daemon -- and safely,
	// because its text is part of the fingerprint that decides whether
	// a saved kv cache still describes this avatar.
	Backstory map[string]string

	// The model.  An empty LLMURL is a daemon with no model, which
	// answers nobody and is the default: chat is something switched on
	// rather than something that happens.
	LLMURL     string
	LLMModel   string
	LLMTimeout time.Duration

	// ChatJobs is how many conversations one avatar answers at once.
	// Its own number rather than sharing Jobs, because a reply takes
	// seconds and a command takes milliseconds: a busy region would
	// otherwise fill the avatar with small talk and leave no room for
	// anybody to drive it.
	ChatJobs int

	// ChatContext is how many tokens of conversation are sent.  It is
	// a budget on the PROMPT, checked against what the server says it
	// actually counted rather than against a guess, and the oldest
	// turns go when it is exceeded.
	ChatContext int

	// ChatReply bounds a reply in instant messages, and ChatTokens
	// bounds it at the model.  Both, because they fail differently: a
	// model told to stop at 160 tokens writes a whole short answer,
	// and one cut off at the message boundary has half a sentence
	// taken off the end of it.
	ChatReply  int
	ChatTokens int

	// ChatTemp is the sampling temperature.
	ChatTemp float64

	// ReadCPS and TypeCPS are how fast an avatar reads what arrived and
	// writes what it sends, in characters a second, and PaceMax bounds
	// the whole wait.  Per avatar where one says so, and these where it
	// does not: see SpeedsFor.
	//
	// Characters a second rather than words a minute because it is what
	// somebody setting this is judging -- how fast this avatar should
	// seem -- and a word is a fiction of five characters that exists to
	// make typing tests comparable.  A zero turns that half off.
	//
	// PaceMax is off by default.  The arithmetic is the point, and a
	// cap quietly contradicts it; it is here for an operator who would
	// rather not have an avatar typing for four minutes because the
	// model felt expansive.
	ReadCPS    float64
	TypeCPS    float64
	AvatarRead map[string]float64
	AvatarType map[string]float64
	PaceMax    time.Duration

	// ChatOwn is how many things two avatars THIS daemon drives may say
	// to each other before one of them stops answering.
	//
	// They are not stopped outright, because two of them talking is not
	// a malfunction and an operator who wants it not to happen can
	// write the name with a "!" in front of it.  What is wrong with it
	// is that neither side will ever be the one to get bored, so
	// something has to be, and this is how many exchanges it waits.
	// Zero is never.
	ChatOwn int

	// ChatKeep is how many turns survive a compaction word for word.
	// The recent ones carry the thread of what is being said; the
	// older ones become the summary, which carries what it was about.
	ChatKeep int

	// ChatSummary bounds the summary, in tokens.  It has to be well
	// under ChatContext or compacting would make no room, and short
	// enough that what it leaves out is obvious rather than surprising.
	ChatSummary int

	// ChatDir is where conversations are kept, as text.  The text is
	// the durable record: a kv cache is welded to one model and one
	// server build, and this is what survives changing either.
	ChatDir string

	// SlotDir is the server's --slot-save-path as THIS process sees
	// it, which is not always the same path the server sees and may
	// not be reachable at all.  Only tidying needs it; saving and
	// restoring name a file and let the server find it.
	SlotDir string

	// trustedIDs and trustedNames are who may send commands.  Two maps
	// because an avatar may be written either way.
	//
	// The names are keyed by their lower case and hold the spelling
	// the file used, so that matching can ignore case while a listing
	// still shows what somebody wrote.  A configuration answered back
	// in a case nobody typed reads like a daemon that has misunderstood
	// it.
	trustedIDs   map[msg.UUID]bool
	trustedNames map[string]string
}

// Program is an external command slbotd may run for a trusted avatar.
type Program struct {
	// Name is what the command is typed as.
	Name string

	// Argv is the program and the arguments that are always passed,
	// before whatever the sender typed.
	Argv []string
}

// Accepting is what an AcceptInventory setting may say.
const (
	AcceptTrusted = "trusted"
	AcceptAnyone  = "anyone"
	AcceptNobody  = "nobody"
)

// DefaultConfig is what slbotd does when the file says nothing about
// it.  Every one of these is a setting in the file as well.
func DefaultConfig() Config {
	return Config{
		Prefix:          ":",
		Timeout:         2 * time.Minute,
		RunTimeout:      30 * time.Minute,
		Jobs:            4,
		ReplyLimit:      8,
		AcceptInventory: AcceptTrusted,
		Programs: map[string]*Program{
			"slbench": {Name: "slbench", Argv: []string{"slbench"}},
			"slrun":   {Name: "slrun", Argv: []string{"slrun"}},
		},
		// The two programs were called autobench and automate until
		// 2026-08-22 and are still called that by the people who use
		// them.  An alias costs nothing and a renamed program that
		// answers "unknown command" to its own old name costs a
		// puzzled minute every time.
		Aliases: map[string]string{
			"autobench": "slbench",
			"automate":  "slrun",
		},
		Backstory:    map[string]string{},
		LLMTimeout:   2 * time.Minute,
		ChatJobs:     2,
		ChatContext:  1536,
		ChatReply:    2,
		ChatTokens:   160,
		ChatTemp:     0.8,
		ChatKeep:     6,
		ChatSummary:  200,
		ChatOwn:      8,
		ChatGap:      time.Hour,
		ErrorGap:     time.Hour,
		ReadCPS:      23.0,
		TypeCPS:      3.2,
		AvatarRead:   map[string]float64{},
		AvatarType:   map[string]float64{},
		trustedIDs:   map[msg.UUID]bool{},
		trustedNames: map[string]string{},
	}
}

// ConfigPath is where the configuration lives.
func ConfigPath() (string, error) {
	if p := os.Getenv(EnvConfig); p != "" {
		return p, nil
	}
	dir, err := agent.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ConfigName), nil
}

// LoadConfig reads the file at a path, or the default one when the path
// is empty.
//
// A missing file is an error and not an empty configuration.  There is
// nothing slbotd can usefully do without one -- it would hold no
// avatars and trust nobody -- and a daemon that started anyway and sat
// there doing nothing would look like one that was working.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		p, err := ConfigPath()
		if err != nil {
			return Config{}, err
		}
		path = p
	}

	// The directory holds the profiles, whose credentials are the
	// reason it is private.  Said here as well as in the profile
	// loader: slbotd reads this file before it reads any profile, and
	// finding out at the first login that the directory has been world
	// readable all along is finding out too late.
	dir := filepath.Dir(path)
	if fi, err := os.Stat(dir); err == nil {
		if m := fi.Mode().Perm(); m&0o077 != 0 {
			return Config{}, fmt.Errorf(
				"%s is mode %04o, wanted 0700: chmod 700 %s", dir, m, dir)
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("%w\n\tsee %s for what goes in it", err, ConfigName)
	}
	defer f.Close()

	cfg, err := parseConfig(f)
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	cfg.Path = path
	if err := cfg.check(); err != nil {
		return Config{}, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// check is what has to be true of a configuration before a daemon is
// started on it.
func (c *Config) check() error {
	if len(c.Avatars) == 0 {
		return fmt.Errorf("no avatar lines: name at least one profile to hold")
	}
	if len(c.trustedIDs) == 0 && len(c.trustedNames) == 0 {
		return fmt.Errorf("no trusted lines: nobody could send a command, " +
			"so nothing would ever happen")
	}
	if c.Prefix == "" {
		return fmt.Errorf("prefix is empty, so every remark would be a command")
	}
	for _, p := range c.Programs {
		if len(p.Argv) == 0 {
			return fmt.Errorf("program %q names nothing to run", p.Name)
		}
	}
	for who := range c.Backstory {
		if !c.Holds(who) {
			return fmt.Errorf("backstory names %q, which is not an avatar this daemon holds", who)
		}
	}
	for from, to := range c.Aliases {
		// An alias to a name nothing answers to is worth refusing at
		// startup.  It is a typo in a file, and the alternative is a
		// command that reports "no such command: slbnech" as though
		// the sender had typed it.
		if _, ok := commands[to]; !ok {
			if _, ok := c.Programs[to]; !ok {
				return fmt.Errorf("alias %q points at %q, which is no command", from, to)
			}
		}
	}
	return nil
}

// Trusts reports whether commands from this avatar are obeyed.
//
// Either identifier is enough and both are checked, because a
// configuration may name somebody by uuid -- which never changes and
// cannot be taken by anybody else -- or by the name a person reads.  The
// name is what an instant message carries and the id is what it is
// really from, so a file written in names is matched on what the grid
// said the sender is called.
func (c *Config) Trusts(id msg.UUID, name string) bool {
	if !id.IsZero() && c.trustedIDs[id] {
		return true
	}
	if name == "" {
		return false
	}
	_, ok := c.trustedNames[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// Trusted is who the configuration named, for saying so.  The ids are
// printed as ids and the names as names, in the order a listing wants:
// sorted, since a map has none.
func (c *Config) Trusted() []string {
	out := make([]string, 0, len(c.trustedIDs)+len(c.trustedNames))
	for id := range c.trustedIDs {
		out = append(out, id.String())
	}
	for _, written := range c.trustedNames {
		out = append(out, written)
	}
	sort.Strings(out)
	return out
}

// Holds reports whether this avatar is one slbotd was told to attend.
func (c *Config) Holds(name string) bool {
	for _, a := range c.Avatars {
		if strings.EqualFold(a, name) {
			return true
		}
	}
	return false
}

func parseConfig(r io.Reader) (Config, error) {
	c := DefaultConfig()

	// A program line replaces the built-in table rather than adding to
	// it.  Somebody who has written down which programs may be run has
	// said which programs may be run, and a default that survived
	// alongside their list would be a program they did not name and
	// cannot see.
	saidProgram := false

	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return c, fmt.Errorf("line %d: want key = value, got %q", n, line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "avatar", "agent":
			if value == "" {
				return c, fmt.Errorf("line %d: %s names no profile", n, key)
			}
			if strings.ContainsAny(value, `/\ `) {
				return c, fmt.Errorf("line %d: %q is not a profile name", n, value)
			}
			if !c.Holds(value) {
				c.Avatars = append(c.Avatars, value)
			}
		case "trusted":
			if value == "" {
				return c, fmt.Errorf("line %d: trusted names nobody", n)
			}
			if id, err := msg.ParseUUID(value); err == nil {
				c.trustedIDs[id] = true
			} else {
				c.trustedNames[strings.ToLower(value)] = value
			}
		case "addr":
			c.Addr = value
		case "prefix":
			if value == "" {
				return c, fmt.Errorf("line %d: prefix is empty, "+
					"so every remark would be a command", n)
			}
			c.Prefix = value
		case "timeout":
			d, err := time.ParseDuration(value)
			if err != nil {
				return c, fmt.Errorf("line %d: timeout: %w", n, err)
			}
			c.Timeout = d
		case "run-timeout", "run_timeout":
			d, err := time.ParseDuration(value)
			if err != nil {
				return c, fmt.Errorf("line %d: run-timeout: %w", n, err)
			}
			c.RunTimeout = d
		case "jobs":
			v, err := strconv.Atoi(value)
			if err != nil || v < 1 {
				return c, fmt.Errorf("line %d: jobs wants a number above zero, got %q", n, value)
			}
			c.Jobs = v
		case "reply-limit", "reply_limit":
			v, err := strconv.Atoi(value)
			if err != nil || v < 1 {
				return c, fmt.Errorf("line %d: reply-limit wants a number above zero, got %q", n, value)
			}
			c.ReplyLimit = v
		case "accept-inventory", "accept_inventory":
			switch strings.ToLower(value) {
			case AcceptTrusted, AcceptAnyone, AcceptNobody:
				c.AcceptInventory = strings.ToLower(value)
			default:
				return c, fmt.Errorf("line %d: accept-inventory wants trusted, anyone or nobody, got %q",
					n, value)
			}
		case "answer-strangers", "answer_strangers":
			on, err := configBool(value)
			if err != nil {
				return c, fmt.Errorf("line %d: %s: %w", n, key, err)
			}
			c.AnswerStrangers = on
		case "program":
			words := strings.Fields(value)
			if len(words) < 2 {
				return c, fmt.Errorf("line %d: program wants a name and a path, got %q", n, value)
			}
			if !saidProgram {
				c.Programs = map[string]*Program{}
				saidProgram = true
			}
			c.Programs[words[0]] = &Program{Name: words[0], Argv: words[1:]}
		case "chat":
			if value == "" {
				return c, fmt.Errorf("line %d: chat names nobody", n)
			}
			c.Chat = append(c.Chat, value)
		case "chat-gap", "chat_gap":
			d, err := time.ParseDuration(value)
			if err != nil || d < 0 {
				return c, fmt.Errorf("line %d: chat-gap wants a duration, got %q", n, value)
			}
			c.ChatGap = d
		case "error-gap", "error_gap":
			d, err := time.ParseDuration(value)
			if err != nil || d < 0 {
				return c, fmt.Errorf("line %d: error-gap wants a duration, got %q", n, value)
			}
			c.ErrorGap = d
		case "backstory":
			who, path, ok := strings.Cut(value, " ")
			who, path = strings.TrimSpace(who), strings.TrimSpace(path)
			if !ok || who == "" || path == "" {
				return c, fmt.Errorf("line %d: backstory wants an avatar and a file or "+
					"directory, got %q", n, value)
			}
			c.Backstory[who] = expandHome(path)
		case "llm-url", "llm_url":
			c.LLMURL = value
		case "llm-model", "llm_model":
			c.LLMModel = value
		case "llm-timeout", "llm_timeout":
			d, err := time.ParseDuration(value)
			if err != nil {
				return c, fmt.Errorf("line %d: llm-timeout: %w", n, err)
			}
			c.LLMTimeout = d
		case "chat-jobs", "chat_jobs":
			v, err := strconv.Atoi(value)
			if err != nil || v < 1 {
				return c, fmt.Errorf("line %d: chat-jobs wants a number above zero, got %q", n, value)
			}
			c.ChatJobs = v
		case "chat-context", "chat_context":
			v, err := strconv.Atoi(value)
			if err != nil || v < 64 {
				return c, fmt.Errorf("line %d: chat-context wants a number of tokens, at least 64, got %q", n, value)
			}
			c.ChatContext = v
		case "chat-reply-limit", "chat_reply_limit":
			v, err := strconv.Atoi(value)
			if err != nil || v < 1 {
				return c, fmt.Errorf("line %d: chat-reply-limit wants a number above zero, got %q", n, value)
			}
			c.ChatReply = v
		case "chat-max-tokens", "chat_max_tokens":
			v, err := strconv.Atoi(value)
			if err != nil || v < 16 {
				return c, fmt.Errorf("line %d: chat-max-tokens wants at least 16, got %q", n, value)
			}
			c.ChatTokens = v
		case "chat-temperature", "chat_temperature":
			v, err := strconv.ParseFloat(value, 64)
			if err != nil || v < 0 {
				return c, fmt.Errorf("line %d: chat-temperature wants a number, got %q", n, value)
			}
			c.ChatTemp = v
		case "chat-keep", "chat_keep":
			v, err := strconv.Atoi(value)
			if err != nil || v < 2 || v%2 != 0 {
				return c, fmt.Errorf("line %d: chat-keep wants an even number of turns, at least 2, got %q", n, value)
			}
			c.ChatKeep = v
		case "chat-summary", "chat_summary":
			v, err := strconv.Atoi(value)
			if err != nil || v < 32 {
				return c, fmt.Errorf("line %d: chat-summary wants at least 32 tokens, got %q", n, value)
			}
			c.ChatSummary = v
		case "chat-own", "chat_own":
			v, err := strconv.Atoi(value)
			if err != nil || v < 0 {
				return c, fmt.Errorf("line %d: chat-own wants a number, zero or more, got %q", n, value)
			}
			c.ChatOwn = v
		case "read-cps", "read_cps":
			who, cps, err := speedLine(value)
			if err != nil {
				return c, fmt.Errorf("line %d: read-cps: %w", n, err)
			}
			if who == "" {
				c.ReadCPS = cps
			} else {
				c.AvatarRead[who] = cps
			}
		case "type-cps", "type_cps":
			who, cps, err := speedLine(value)
			if err != nil {
				return c, fmt.Errorf("line %d: type-cps: %w", n, err)
			}
			if who == "" {
				c.TypeCPS = cps
			} else {
				c.AvatarType[who] = cps
			}
		case "pace-max", "pace_max":
			d, err := time.ParseDuration(value)
			if err != nil || d < 0 {
				return c, fmt.Errorf("line %d: pace-max wants a duration, got %q", n, value)
			}
			c.PaceMax = d
		case "chat-dir", "chat_dir":
			c.ChatDir = expandHome(value)
		case "slot-save-path", "slot_save_path":
			c.SlotDir = expandHome(value)
		case "alias":
			words := strings.Fields(value)
			if len(words) != 2 {
				return c, fmt.Errorf("line %d: alias wants two words, got %q", n, value)
			}
			c.Aliases[words[0]] = words[1]
		default:
			return c, fmt.Errorf("line %d: unknown setting %q", n, key)
		}
	}
	return c, sc.Err()
}

// configBool reads a setting somebody typed by hand, the way the
// profiles do: wider than strconv.ParseBool, and a word that is neither
// is an error rather than a silent false.
func configBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "yes", "y", "on", "true", "t", "1":
		return true, nil
	case "no", "n", "off", "false", "f", "0":
		return false, nil
	}
	return false, fmt.Errorf("want yes or no, got %q", v)
}

// profileExists says whether slgod will find credentials for a profile,
// by the same reading slgod itself does.
//
// The check is worth making here because the alternative is finding out
// from the grid: a misspelt profile name is answered by slgod with "no
// agent named qx", minutes after startup, in a log nobody was watching.
func profileExists(name string) (string, error) {
	path, err := agent.ProfilePath(name)
	if err != nil {
		return "", err
	}
	if _, err := agent.LoadProfile(name); err != nil {
		return path, err
	}
	return path, nil
}

// lookProgram is where a configured program will be found, or why it
// will not be.
func lookProgram(name string) (string, error) {
	if strings.ContainsRune(name, os.PathSeparator) {
		fi, err := os.Stat(name)
		if err != nil {
			return "", err
		}
		if fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
			return "", fmt.Errorf("%s is not something that can be run", name)
		}
		return name, nil
	}
	return exec.LookPath(name)
}

// sortedProgramNames is the configured programs in a settled order, for
// a listing.
func sortedProgramNames(c Config) []string {
	out := make([]string, 0, len(c.Programs))
	for n := range c.Programs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ChatOn reports whether this daemon answers conversation at all.
//
// Both halves are needed and neither implies the other: a chat list
// with no model would be a daemon that has decided who to talk to and
// has nothing to say, and a model with no chat list is one that could
// talk and has been told to talk to nobody.  Either on its own is
// almost certainly a half-finished configuration, which check() says
// so about.
func (c *Config) ChatOn() bool { return c.LLMURL != "" && len(c.Chat) > 0 }

// ChatStore is where conversations are kept.
//
// Under the state directory rather than beside the profiles: these are
// what the daemon has said and been told, they grow without bound, and
// they are not configuration.  XDG_STATE_HOME names it, or
// ~/.local/state, which is where the specification puts exactly this
// -- state a program wants between runs that is not a cache and not a
// setting.
func (c *Config) ChatStore() (string, error) {
	if c.ChatDir != "" {
		return c.ChatDir, nil
	}
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "slbotd"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no home directory: %w", err)
	}
	return filepath.Join(home, ".local", "state", "slbotd"), nil
}

// expandHome turns a leading ~ into the home directory.
//
// Only a leading one, and only when what follows is a separator or
// nothing: a file really called "~snapshot" is a file really called
// that, and a path this expanded in the middle would be a path nobody
// could write.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// ChatProblem says why chat is not on, when the file plainly meant it
// to be, and is empty when there is nothing wrong.
//
// Half a chat configuration is almost certainly an unfinished one, and
// it used to be fatal.  That was wrong, and it was wrong in the way
// that matters for a daemon meant to run for weeks: attending avatars
// and taking commands is what slbotd is FOR, chat is something bolted
// on beside it, and a missing line in the bolted-on part took the whole
// thing down on the next restart.  slgod has had the right answer to
// this all along -- a profile it cannot read is logged and the others
// are served -- and this is the same rule.
//
// So it is said, loudly, every time the daemon starts and again
// whenever anybody asks; and the avatars are attended.  Silence was
// never the alternative: a setting that does nothing and says nothing
// is the fault this used to be trying to prevent.
func (c *Config) ChatProblem() string {
	switch {
	case len(c.Chat) > 0 && c.LLMURL == "":
		return "chat names somebody to talk to but no llm-url says where a model is, " +
			"so nothing will be answered"
	case c.LLMURL != "" && len(c.Chat) == 0:
		return "llm-url names a model but no chat line says who may be answered, " +
			"so nothing will be answered"
	case c.ChatOn() && c.ChatSummary >= c.ChatContext/2:
		return fmt.Sprintf("chat-summary is %d tokens and chat-context only %d; "+
			"the summary has to be well under the budget or there is no room left to talk",
			c.ChatSummary, c.ChatContext)
	}
	return ""
}

// SpeedsFor is how fast this avatar reads and types.
//
// The avatar's own where it has one and the file's otherwise, which is
// what makes a line naming no avatar the default rather than the only
// answer.
func (c *Config) SpeedsFor(avatar string) Speeds {
	s := Speeds{Read: c.ReadCPS, Type: c.TypeCPS}
	if cps, ok := c.AvatarRead[avatar]; ok {
		s.Read = cps
	}
	if cps, ok := c.AvatarType[avatar]; ok {
		s.Type = cps
	}
	return s
}

// speedLine reads "23.0" or "qi 30.0": a rate, or an avatar and a rate.
//
// Which it is comes from how many words there are and not from what
// they look like, because a profile name is never a number and a
// listing that guessed would be one more thing to be wrong about.
func speedLine(value string) (avatar string, cps float64, err error) {
	f := strings.Fields(value)
	switch len(f) {
	case 1:
	case 2:
		avatar, f = f[0], f[1:]
	default:
		return "", 0, fmt.Errorf("wants a rate, or an avatar and a rate, got %q", value)
	}
	cps, err = strconv.ParseFloat(f[0], 64)
	if err != nil || cps < 0 {
		return "", 0, fmt.Errorf("wants a number of characters a second, zero or more, got %q", f[0])
	}
	return avatar, cps, nil
}
