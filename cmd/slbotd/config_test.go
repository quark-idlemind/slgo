package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// A configuration is read once, at startup, and everything the daemon
// does afterwards comes out of it.  A setting that parses to something
// other than what it says is therefore not a small fault: it is a
// daemon quietly trusting somebody it was not told to trust, or
// obeying a word nobody meant as a command.

func parse(t *testing.T, text string) Config {
	t.Helper()
	c, err := parseConfig(strings.NewReader(text))
	if err != nil {
		t.Fatalf("parsing:\n%s\n%v", text, err)
	}
	return c
}

func TestAConfigurationSaysWhoAndWhat(t *testing.T) {
	c := parse(t, `
# a comment, and a blank line

avatar = example
avatar  =  builder
trusted = Quark Idlemind
trusted = 3ac37e57-7e57-c0de-5607-527da8fa08de
addr = grid.example:7807
prefix = !
timeout = 45s
run-timeout = 1h
jobs = 2
reply-limit = 3
accept-inventory = anyone
answer-strangers = yes
`)

	if got, want := c.Avatars, []string{"example", "builder"}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("avatars = %v, want %v", got, want)
	}
	if c.Addr != "grid.example:7807" {
		t.Errorf("addr = %q", c.Addr)
	}
	if c.Prefix != "!" {
		t.Errorf("prefix = %q", c.Prefix)
	}
	if c.Timeout != 45*time.Second || c.RunTimeout != time.Hour {
		t.Errorf("timeouts = %s, %s", c.Timeout, c.RunTimeout)
	}
	if c.Jobs != 2 || c.ReplyLimit != 3 {
		t.Errorf("jobs = %d, reply-limit = %d", c.Jobs, c.ReplyLimit)
	}
	if c.AcceptInventory != AcceptAnyone || !c.AnswerStrangers {
		t.Errorf("offers = %q, strangers = %v", c.AcceptInventory, c.AnswerStrangers)
	}
}

// A name in the file is matched against the name an instant message
// carries, without regard to case, and an id against the id it really
// came from.  Either is enough on its own: a file written in names must
// work for a sender whose id nobody wrote down, and one written in ids
// must work for a sender whose name arrived empty.
func TestTrustIsByNameOrByID(t *testing.T) {
	c := parse(t, `
avatar = example
trusted = Quark Idlemind
trusted = 3ac37e57-7e57-c0de-5607-527da8fa08de
`)
	known := msg.MustParseUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")
	other := msg.MustParseUUID("19d17e57-7e57-c0de-11be-a4325a5080a2")

	for _, tc := range []struct {
		id   msg.UUID
		name string
		want bool
	}{
		{other, "Quark Idlemind", true},
		{other, "quark idlemind", true},
		{other, "QUARK IDLEMIND", true},
		{known, "", true},
		{known, "Somebody Else", true},
		{other, "Somebody Else", false},
		{msg.UUID{}, "", false},
		{other, "Quark", false},
	} {
		if got := c.Trusts(tc.id, tc.name); got != tc.want {
			t.Errorf("Trusts(%s, %q) = %v, want %v", tc.id, tc.name, got, tc.want)
		}
	}
}

// A misspelt key is a setting somebody believes in and that does
// nothing, which is worse than an error at startup.
func TestAnUnknownSettingIsRefused(t *testing.T) {
	_, err := parseConfig(strings.NewReader("avatar = example\ntrusted = A B\nprefx = :\n"))
	if err == nil || !strings.Contains(err.Error(), "prefx") {
		t.Fatalf("err = %v, want one naming the misspelt key", err)
	}
}

func TestSettingsAreCheckedAsTheyAreRead(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"no equals", "avatar example\n", "key = value"},
		{"empty prefix", "prefix =\n", "prefix is empty"},
		{"jobs zero", "jobs = 0\n", "above zero"},
		{"jobs not a number", "jobs = plenty\n", "above zero"},
		{"bad duration", "timeout = soon\n", "timeout"},
		{"bad offer rule", "accept-inventory = maybe\n", "trusted, anyone or nobody"},
		{"bad yes or no", "answer-strangers = sometimes\n", "yes or no"},
		{"program with no path", "program = slbench\n", "a name and a path"},
		{"alias of one word", "alias = autobench\n", "two words"},
		{"a profile that is a path", "avatar = ../elsewhere\n", "not a profile name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseConfig(strings.NewReader(tc.text))
			if err == nil {
				t.Fatalf("parsed %q, wanted a refusal", tc.text)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// A configuration that names nobody to hold, or nobody to obey, is one
// that would start a daemon that can never do anything.
func TestAUselessConfigurationIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, text, want string }{
		{"no avatars", "trusted = A B\n", "no avatar lines"},
		{"nobody trusted", "avatar = example\n", "no trusted lines"},
		{"alias to nothing", "avatar = example\ntrusted = A B\nalias = x nosuchcommand\n", "which is no command"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := parse(t, tc.text)
			err := c.check()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("check() = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// The two programs were renamed and the old names are still what people
// type, so both reach the same place with nothing said in the file.
func TestTheOldProgramNamesStillWork(t *testing.T) {
	c := DefaultConfig()
	for old, want := range map[string]string{"autobench": "slbench", "automate": "slrun"} {
		if got := c.Aliases[old]; got != want {
			t.Errorf("alias %q = %q, want %q", old, got, want)
		}
		if _, ok := c.Programs[want]; !ok {
			t.Errorf("no program called %q", want)
		}
	}
}

// A file that names programs has said which programs may be run.  A
// default left standing beside that list would be a program the
// operator did not name and cannot see in their own file.
func TestNamingAProgramReplacesTheDefaults(t *testing.T) {
	c := parse(t, `
avatar = example
trusted = A B
program = measure /usr/local/bin/measure --quiet
`)
	if _, ok := c.Programs["slbench"]; ok {
		t.Error("slbench survived a file that named its own programs")
	}
	p, ok := c.Programs["measure"]
	if !ok {
		t.Fatal("measure was not read")
	}
	if want := []string{"/usr/local/bin/measure", "--quiet"}; strings.Join(p.Argv, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", p.Argv, want)
	}
}

// The same avatar twice is one avatar.  Two attendants on one profile
// would both ask slgod to host it and both attach, which is two clients
// answering every instant message.
func TestAnAvatarNamedTwiceIsHeldOnce(t *testing.T) {
	c := parse(t, "avatar = example\navatar = Example\navatar = builder\ntrusted = A B\n")
	if len(c.Avatars) != 2 {
		t.Errorf("avatars = %v, want two", c.Avatars)
	}
}

func TestLoadConfigReadsAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ConfigName)
	if err := os.WriteFile(path, []byte("avatar = example\ntrusted = Quark Idlemind\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.Path != path {
		t.Errorf("Path = %q, want %q", c.Path, path)
	}
	if !c.Holds("EXAMPLE") {
		t.Error("the avatar it names is not one it holds")
	}
}

// The directory holds the profiles, and the profiles hold credentials.
// Saying so when the configuration is read is saying so before the
// first login rather than after it.
func TestAReadableDirectoryIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ConfigName)
	if err := os.WriteFile(path, []byte("avatar = example\ntrusted = A B\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("err = %v, want one about the directory's mode", err)
	}
}

// A missing file is not an empty configuration.  A daemon that started
// on one would hold nobody and trust nobody, and would look exactly
// like one that was working.
func TestAMissingFileIsAnError(t *testing.T) {
	// A private directory, so that what is refused is the file's being
	// missing and not the directory's mode.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := LoadConfig(filepath.Join(dir, "not-there.conf"))
	if err == nil {
		t.Fatal("a missing configuration was accepted")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %q, want the file's being missing", err)
	}
	// And it says where to find out what goes in one, which is not the
	// file that is missing.
	if !strings.Contains(err.Error(), "doc/guide.md") {
		t.Errorf("err = %q, want it to say where the documentation is", err)
	}
	if strings.Contains(err.Error(), "see "+ConfigName) {
		t.Errorf("err = %q sends the reader to the file that is not there", err)
	}
}

func TestTheConfigPathFollowsTheEnvironment(t *testing.T) {
	t.Setenv(EnvConfig, "/somewhere/else.conf")
	got, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/somewhere/else.conf" {
		t.Errorf("ConfigPath() = %q", got)
	}

	t.Setenv(EnvConfig, "")
	t.Setenv("SLGO_CONFIG_DIR", "/profiles")
	got, err = ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/profiles", ConfigName); got != want {
		t.Errorf("ConfigPath() = %q, want %q", got, want)
	}
}

// Every default the documentation promises, asserted.
//
// This exists because three of them were missing and nothing noticed.
// chat-keep, chat-summary and chat-own were written up in the guide,
// documented on their fields, and absent from DefaultConfig -- so they
// were zero, and zero for chat-keep means a compaction folds the WHOLE
// conversation including the exchange that has just happened.  Every
// test that covered compaction set the value explicitly, so every test
// passed; so did the live run, because the file it used set it too.
//
// A value here is not a strong claim about what the number should be.
// The claim is that there IS one, because a documented setting that
// silently defaults to zero is worse than one that does not exist.
func TestEveryDefaultHasAValue(t *testing.T) {
	c := DefaultConfig()
	for _, tc := range []struct {
		name string
		zero bool
	}{
		{"prefix", c.Prefix == ""},
		{"timeout", c.Timeout == 0},
		{"run-timeout", c.RunTimeout == 0},
		{"jobs", c.Jobs == 0},
		{"reply-limit", c.ReplyLimit == 0},
		{"accept-inventory", c.AcceptInventory == ""},
		{"llm-timeout", c.LLMTimeout == 0},
		{"chat-jobs", c.ChatJobs == 0},
		{"chat-context", c.ChatContext == 0},
		{"chat-reply-limit", c.ChatReply == 0},
		{"chat-max-tokens", c.ChatTokens == 0},
		{"chat-temperature", c.ChatTemp == 0},
		{"chat-keep", c.ChatKeep == 0},
		{"chat-summary", c.ChatSummary == 0},
		{"chat-own", c.ChatOwn == 0},
		{"read-cps", c.ReadCPS == 0},
		{"type-cps", c.TypeCPS == 0},
	} {
		if tc.zero {
			t.Errorf("%s has no default", tc.name)
		}
	}

	// And the ones that have to hold together, which is the check that
	// would have caught the missing chat-summary on its own.
	if c.ChatSummary >= c.ChatContext/2 {
		t.Errorf("chat-summary %d against chat-context %d leaves no room to talk",
			c.ChatSummary, c.ChatContext)
	}
	if c.ChatKeep%2 != 0 {
		t.Errorf("chat-keep is %d, which is half an exchange", c.ChatKeep)
	}
}

// Half a chat configuration is said, loudly, and is not fatal: attending
// avatars is what this daemon is for, and chat is beside it.
func TestHalfAChatConfigurationIsSaidNotFatal(t *testing.T) {
	c := parse(t, "avatar = example\ntrusted = A B\nchat = *\n")
	if err := c.check(); err != nil {
		t.Fatalf("a missing llm-url stopped the daemon: %v", err)
	}
	if why := c.ChatProblem(); why == "" || !strings.Contains(why, "llm-url") {
		t.Errorf("ChatProblem() = %q, want it to name what is missing", why)
	}
	if c.ChatOn() {
		t.Error("chat is on with no model")
	}
}

// chat-bot, chat-own-rest and llm-slots, and what is refused.
func TestBotsRestAndSlots(t *testing.T) {
	c := parse(t, "avatar = example\ntrusted = Quark Idlemind\n"+
		"chat-bot = Stranger Bot\nchat-bot = 19d17e57-7e57-c0de-11be-a4325a5080a2\n"+
		"chat-own-rest = 45m\nllm-slots = 4\n")
	if !c.ChatBot(msg.UUID{}, "stranger BOT") || !c.ChatBot(testStranger, "") || c.ChatBot(testSender, "Trusted Resident") {
		t.Error("chat-bot did not match by name and by id, and only those")
	}
	if c.ChatOwnRest != 45*time.Minute || c.LLMSlots != 4 {
		t.Errorf("rest %v, slots %d", c.ChatOwnRest, c.LLMSlots)
	}
	if got := strings.Join(c.ChatBots(), ","); got != "19d17e57-7e57-c0de-11be-a4325a5080a2,Stranger Bot" {
		t.Errorf("ChatBots = %q", got)
	}

	d := parse(t, "avatar = example\ntrusted = Quark Idlemind\n")
	if d.ChatOwnRest != 30*time.Minute || d.LLMSlots != 0 {
		t.Errorf("defaults: rest %v, slots %d", d.ChatOwnRest, d.LLMSlots)
	}

	for _, bad := range []string{"chat-bot =", "chat-own-rest = soon", "chat-own-rest = -1m", "llm-slots = -1", "llm-slots = two"} {
		if _, err := parseConfig(strings.NewReader("avatar = example\ntrusted = A B\n" + bad + "\n")); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}
