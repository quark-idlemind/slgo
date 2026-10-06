package main

// Settings live in ~/.config/slsh/config, in the same key = value
// form as everything else in this tree:
//
//	addr   = localhost:7807
//	agent  = example
//	escape = ESC
//
//	map_rows          = 16
//	map_span          = 64
//	map_ratio         = 7:3
//	map_friend_colour = green
//
//	viewer_app     = Firestorm-OpenSim
//	viewer_grid    = slgod
//	viewer_launch  = open -a {app} --args --grid {grid} --login {first} {last} {password}
//	viewer_running = pgrep -f {app}.app/Contents
//
// Flags win over the file, and the file over the defaults.
//
// The file is hand-edited and "set" edits it as well -- see set.go for
// the command and settings below for the one table both of them work
// from.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config is what slsh needs to know before it can start.
type Config struct {
	// Addr is the slgod to attach to.  Empty means nobody has said,
	// which is what lets $SLGO_ADDR and then sl-host be asked -- see
	// internal/slhost.
	Addr   string
	Agent  string // the profile: hosted by slgod, or on disk for --direct
	Prefix rune   // the key that leaves chat mode for the command prompt

	// Direct is set when this process holds the session itself,
	// which is worth saying out loud: quitting logs the avatar out.
	Direct bool

	// Chat starts in chat mode rather than at a command prompt.
	Chat bool

	// ViewerApp, ViewerGrid, ViewerLaunch and ViewerRunning are how
	// "viewer --launch" starts a real viewer and hands it the session:
	// which application, which entry in that viewer's own grid list,
	// the command that starts it, and the command that says whether
	// one is up already.  See viewerDefaults.
	ViewerApp     string
	ViewerGrid    string
	ViewerLaunch  string
	ViewerRunning string

	// The shape and scale of the picture "map" draws: how many rows it
	// is given, how much ground the close view covers, the shape of a
	// character cell, how far off this avatar's height still counts as
	// level, and the colour a friend is picked out in.  map.go holds
	// the defaults and does the arithmetic; --rows and --span override
	// the first two for one command, as a flag should.
	MapRows         int
	MapSpan         int
	MapRatio        CellRatio
	MapLevel        int
	MapFriendColour string // a colour by name, never an escape sequence

	// Log says whether to keep a transcript, and LogDir where.  Empty
	// LogDir is the default place; see transcript.go.
	Log    bool
	LogDir string

	// Where "how" finds a model, and what it asks it for: the server's
	// base URL (empty is no model, and how answers from its index
	// alone), the model's name, llama-server's slot or nil for none,
	// how long to wait with zero for the client's default, and a JSON
	// object merged into every request for what one model needs and
	// another would refuse.  See ask.go.  The fields keep the name the
	// command had when they were written; the settings are how_*.
	AskURL     string
	AskModel   string
	AskSlot    *int
	AskTimeout time.Duration
	AskExtra   string

	// NoticeKeep is how long a group notice is kept for "notice".
	NoticeKeep time.Duration
}

// CellRatio is the shape of a character cell in whatever font somebody
// reads a terminal in: how TALL it is against how WIDE, in that order.
//
// It is written the way it is measured -- "7:3" is a cell seven high
// and three wide, which is what the mono font this was measured in
// turned out to be -- and it is the whole reason a map is drawn with
// more columns than rows.
//
// Height first is the one thing here worth saying twice, because a
// ratio carries no units and an inverted one cannot be seen by looking
// at anything: the picture comes out as tall and thin as a doorway and
// reads as a perfectly ordinary picture of somewhere shaped like a
// doorway.  See mapGrid.cols, which is the only place it is used.
type CellRatio struct {
	Tall int
	Wide int
}

// String writes a ratio the way it is written in the file: height
// first, so that what "set" prints is what the file would take back.
func (r CellRatio) String() string { return fmt.Sprintf("%d:%d", r.Tall, r.Wide) }

// ParseCellRatio reads "7:3", height first.
//
// Both parts have to be there.  A bare "7" would have to mean seven to
// one or seven to three depending on who was reading it, and a setting
// whose meaning depends on that is worse than one that is refused.
func ParseCellRatio(s string) (CellRatio, error) {
	tall, wide, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		return CellRatio{}, fmt.Errorf("want height:width, like 7:3, got %q", s)
	}
	var r CellRatio
	for _, part := range []struct {
		text string
		into *int
	}{{tall, &r.Tall}, {wide, &r.Wide}} {
		n, err := strconv.Atoi(strings.TrimSpace(part.text))
		if err != nil {
			return CellRatio{}, fmt.Errorf("want height:width, like 7:3, got %q", s)
		}
		// The upper bound is not a fact about fonts; it is a refusal to
		// turn a typed "70:3" into a picture 373 columns wide, which is
		// a terminal full of frame and nothing a person can read.
		if n < 1 || n > 20 {
			return CellRatio{}, fmt.Errorf("a cell is between 1 and 20 characters either way, got %q", s)
		}
		*part.into = n
	}
	return r, nil
}

// onOff is how a setting that is a yes or a no is printed, so that what
// "set" shows is what "set" would take back.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// DefaultConfig is what an empty file leaves you with.
//
// Addr is deliberately empty rather than localhost: with nothing said
// in a file and nothing on the command line, where slgod runs is a
// question for sl-host, and a default here would answer it first.
func DefaultConfig() Config {
	c := Config{
		Prefix:          27,
		MapRows:         mapDefaultRows,
		MapSpan:         mapDefaultSpan,
		MapRatio:        mapDefaultRatio,
		MapLevel:        mapDefaultLevel,
		MapFriendColour: mapDefaultFriendColour,
		Log:             true,
		NoticeKeep:      noticeDefaultKeep,
	}
	c.ViewerApp, c.ViewerGrid, c.ViewerLaunch, c.ViewerRunning = viewerDefaults(runtime.GOOS)
	return c
}

// viewerDefaults is how to start a real viewer on this kind of machine,
// point it at slgod, and tell whether one is already up.
//
// On macOS that is the OpenSim build of Firestorm, since the build for
// Second Life cannot be pointed at a private grid at all; --grid with a
// nickname that has to be added to that viewer's own grid list once by
// hand, since --loginuri does nothing; open(1), which hands the launch
// off, returns at once and brings the viewer to the front; and a match
// on the bundle path, since the process is called plain "Firestorm"
// whichever build it came from.
//
// Anywhere else it is nothing.  A guessed binary name could fail inside
// a viewer's own startup, or start the wrong program, and empty makes
// "viewer --launch" say that nobody has told it what to run.
// Why: doc/slsh.md#starting-a-viewer-from-slsh
func viewerDefaults(goos string) (app, grid, launch, running string) {
	switch goos {
	case "darwin":
		return "Firestorm-OpenSim", "slgod",
			"open -a {app} --args --grid {grid} --login {first} {last} {password}",
			"pgrep -f {app}.app/Contents"
	}
	return "", "", "", ""
}

// setting is one thing somebody can set: what it is called, what it is
// for, and the two halves of reading and writing it.
//
// There is one row per setting and everything works from it: the
// reader looks a key up here, "set" lists these and nothing else, and
// the file writer is handed one of these rows.  A setting added here
// can be written in the file, listed, and changed, with no other edit.
//
// The reader still refuses a key that is in no row, which is what that
// check has always been for: a misspelled setting that silently did
// nothing is a shell that comes up looking right and behaves as though
// the line were not there.
// Why: doc/slsh.md#one-table-of-settings
type setting struct {
	name string

	// also are the other spellings the file has always taken.  They
	// stay because somebody's file has them in it; the name is what is
	// printed and what a new file gets.
	also []string

	// about is what it is for, in a line, for the listing.
	about string

	// startup is a setting the running shell cannot take, because it
	// had been used before there was a prompt to type "set" at: the
	// session was attached with it, or the terminal was put in raw mode
	// with it.  A flag may have overridden the file for this run as
	// well, so applying one of these now would mean two different
	// things depending on how slsh was started.  The listing marks one
	// "at startup only", and changing one prints startupNote.
	startup bool

	// show is the value as the file would write it, and parse is the
	// same thing backwards.  parse leaves the Config untouched when it
	// refuses, so a bad value cannot half-apply.
	show  func(c *Config) string
	parse func(c *Config, value string) error

	// apply, where there is one, is what a running shell does to take
	// the new value, for a setting read once rather than each time it is
	// wanted.  "set" calls it after the shell's Config has changed.
	apply func(sh *Shell) error
}

// relogNow is "log" and "log_dir" taking effect at once.
func relogNow(sh *Shell) error {
	if err := sh.relog(); err != nil {
		return fmt.Errorf("no transcript: %w", err)
	}
	return nil
}

// startupNote is what a startup-only setting says.  Written out rather
// than implied, because a setting that quietly did not take is worse
// than one that refuses: the picture nobody expected is blamed on the
// setting rather than on the shell not having read it yet.
const startupNote = "this shell keeps the old value; the new one is for the next slsh"

// settings is every setting there is, written in the order they belong
// in -- the shell's own, then the viewer's, then the map's, then how's,
// and within each the order somebody meets them in.
//
// They are LISTED alphabetically, which is not the same thing and is
// deliberate; see sortedSettings.
var settings = []setting{{
	name:    "addr",
	also:    []string{"server"},
	about:   "the slgod to attach to; empty means $SLGO_ADDR, then sl-host",
	startup: true,
	show:    func(c *Config) string { return c.Addr },
	parse:   func(c *Config, v string) error { c.Addr = v; return nil },
}, {
	name:    "agent",
	also:    []string{"profile"},
	about:   "the profile to drive: one slgod holds, or one on disk for --direct",
	startup: true,
	show:    func(c *Config) string { return c.Agent },
	parse:   func(c *Config, v string) error { c.Agent = v; return nil },
}, {
	name:    "escape",
	also:    []string{"prefix", "prefix_key"},
	about:   "the key that leaves chat mode: ESC, ^G, or one character",
	startup: true,
	show:    func(c *Config) string { return KeyName(c.Prefix) },
	parse: func(c *Config, v string) error {
		r, err := ParseKey(v)
		if err != nil {
			return err
		}
		c.Prefix = r
		return nil
	},
}, {
	name:  "log",
	about: "keep a transcript of what is heard, said and run: on or off",
	show:  func(c *Config) string { return onOff(c.Log) },
	parse: func(c *Config, v string) error {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "on", "yes", "true", "1":
			c.Log = true
		case "off", "no", "false", "0":
			c.Log = false
		default:
			return fmt.Errorf("%q is not on or off", v)
		}
		return nil
	},
	apply: relogNow,
}, {
	name:  "log_dir",
	about: "where the transcript goes; empty is the default place",
	show:  func(c *Config) string { return c.LogDir },
	parse: func(c *Config, v string) error { c.LogDir = v; return nil },
	apply: relogNow,
}, {
	name:  "notice_keep",
	about: "how long a group notice is kept for \"notice\", like 15m",
	show:  func(c *Config) string { return durationWord(c.NoticeKeep) },
	parse: func(c *Config, v string) error {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil || d <= 0 {
			return fmt.Errorf("want a length of time, like 15m or 1h, got %q", v)
		}
		c.NoticeKeep = d
		return nil
	},
}, {
	name:  "viewer_app",
	about: "the viewer \"viewer --launch\" starts; the OpenSim build, not the other one",
	show:  func(c *Config) string { return c.ViewerApp },
	parse: func(c *Config, v string) error { c.ViewerApp = v; return nil },
}, {
	name:  "viewer_grid",
	about: "what this grid is called in that viewer's own grid list",
	show:  func(c *Config) string { return c.ViewerGrid },
	parse: func(c *Config, v string) error { c.ViewerGrid = v; return nil },
}, {
	name:  "viewer_launch",
	about: "the command that starts it: {app} {grid} {first} {last} {password}",
	show:  func(c *Config) string { return c.ViewerLaunch },
	parse: func(c *Config, v string) error { c.ViewerLaunch = v; return nil },
}, {
	name:  "viewer_running",
	about: "the command that says whether one is up; empty skips the check",
	show:  func(c *Config) string { return c.ViewerRunning },
	// Deliberately settable to nothing: a person who would rather slsh
	// did not run pgrep can empty it, and the launch then goes ahead
	// without the check.
	parse: func(c *Config, v string) error { c.ViewerRunning = v; return nil },
}, {
	name:  "map_rows",
	about: "how many rows \"map\" draws in, unless --rows says otherwise",
	show:  func(c *Config) string { return strconv.Itoa(c.MapRows) },
	parse: func(c *Config, v string) error {
		n, err := settingNumber(v, 2)
		if err != nil {
			return err
		}
		// The same bound the command refuses at, and for the same
		// reason: past this the picture is wider than any terminal.
		if n > mapMaxRows {
			return fmt.Errorf("%d is more than the %d rows map will draw", n, mapMaxRows)
		}
		c.MapRows = n
		return nil
	},
}, {
	name:  "map_span",
	about: "how much ground the close picture covers, in metres",
	show:  func(c *Config) string { return strconv.Itoa(c.MapSpan) },
	parse: func(c *Config, v string) error {
		n, err := settingNumber(v, 2)
		if err != nil {
			return err
		}
		c.MapSpan = n
		return nil
	},
}, {
	name: "map_ratio",
	// The only setting with a word of its own where a value goes, and
	// the listing is where somebody would find that out.  See cmdSet,
	// which is where "auto" is enforced against this one name.
	about: "the shape of a character cell, height first; \"auto\" measures it",
	show:  func(c *Config) string { return c.MapRatio.String() },
	parse: func(c *Config, v string) error {
		r, err := ParseCellRatio(v)
		if err != nil {
			return err
		}
		c.MapRatio = r
		return nil
	},
}, {
	name:  "map_level",
	about: "how far above or below you still counts as level, in metres",
	show:  func(c *Config) string { return strconv.Itoa(c.MapLevel) },
	parse: func(c *Config, v string) error {
		n, err := settingNumber(v, 1)
		if err != nil {
			return err
		}
		c.MapLevel = n
		return nil
	},
}, {
	name:  "map_friend_colour",
	about: "the colour a friend is drawn in, by name; \"man map\" names them",
	show:  func(c *Config) string { return c.MapFriendColour },
	parse: func(c *Config, v string) error {
		name, err := ParseColour(v)
		if err != nil {
			return err
		}
		c.MapFriendColour = name
		return nil
	},
}, {
	name:  "how_url",
	about: "the model server \"how\" uses, like http://127.0.0.1:8080; empty is none",
	show:  func(c *Config) string { return c.AskURL },
	parse: func(c *Config, v string) error {
		v = strings.TrimSpace(v)
		if err := askCheckURL(v); err != nil {
			return err
		}
		c.AskURL = v
		return nil
	},
}, {
	name:  "how_model",
	about: "the model name sent to that server; llama-server ignores it, Ollama needs it",
	show:  func(c *Config) string { return c.AskModel },
	parse: func(c *Config, v string) error { c.AskModel = strings.TrimSpace(v); return nil },
}, {
	name:  "how_slot",
	about: "the llama-server slot \"how\" uses; empty is whichever is free",
	show: func(c *Config) string {
		if c.AskSlot == nil {
			return ""
		}
		return strconv.Itoa(*c.AskSlot)
	},
	parse: func(c *Config, v string) error {
		if strings.TrimSpace(v) == "" {
			c.AskSlot = nil
			return nil
		}
		n, err := settingNumber(v, 0)
		if err != nil {
			return err
		}
		c.AskSlot = &n
		return nil
	},
}, {
	name:  "how_timeout",
	about: "how long \"how\" waits for the model, like 45s; empty is two minutes",
	show: func(c *Config) string {
		if c.AskTimeout == 0 {
			return ""
		}
		return c.AskTimeout.String()
	},
	parse: func(c *Config, v string) error {
		if strings.TrimSpace(v) == "" {
			c.AskTimeout = 0
			return nil
		}
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil || d <= 0 {
			return fmt.Errorf("want a length of time, like 45s or 2m, got %q", v)
		}
		c.AskTimeout = d
		return nil
	},
}, {
	name: "how_extra",
	// A JSON object rather than a setting per option, because what goes
	// here is a fact about one model -- a "thinking" model told not to
	// think -- and there is no end to those.  Checked when it is set, so
	// that a typo is found then and not at the next question.
	about: "a JSON object added to every request \"how\" makes; see \"man how\"",
	show:  func(c *Config) string { return c.AskExtra },
	parse: func(c *Config, v string) error {
		v = strings.TrimSpace(v)
		if _, err := askParseExtra(v); err != nil {
			return err
		}
		c.AskExtra = v
		return nil
	},
}}

// findSetting is the row for a key, under its name or any spelling of
// it the file has ever taken.
func findSetting(key string) (setting, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, s := range settings {
		if s.name == key {
			return s, true
		}
		for _, a := range s.also {
			if a == key {
				return s, true
			}
		}
	}
	return setting{}, false
}

// sortedSettings is every setting in the order they are printed in.
//
// Alphabetical, as every list of names in this shell is: these are read
// by somebody looking for a name they already have in mind, and a list
// in an order only its author can predict is one they have to read all
// of.  See sortedNames in groups.go, which says the rest of it.  The
// curation is still in the table, where it costs nothing and documents
// what belongs with what.
func sortedSettings() []setting {
	out := append([]setting(nil), settings...)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// settingNames is every setting, for a refusal that has to say what
// there is.
func settingNames() []string {
	out := make([]string, 0, len(settings))
	for _, s := range sortedSettings() {
		out = append(out, s.name)
	}
	return out
}

// settingNumber reads a whole number that has to be at least something.
//
// There is no upper bound to give here in general: a map fifty metres
// across and one four hundred metres across are both pictures somebody
// might want, and the two settings that do have a ceiling say so
// themselves.  The floor is where the setting stops meaning anything --
// a picture one row tall, a span narrower than the avatar in the middle
// of it -- and is worth refusing rather than drawing.
func settingNumber(v string, low int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("want a whole number, got %q", v)
	}
	if n < low {
		return 0, fmt.Errorf("want %d or more, got %d", low, n)
	}
	return n, nil
}

// ConfigDir is where slsh keeps its settings.  SLSH_CONFIG_DIR
// names it outright; otherwise it is slsh under XDG_CONFIG_HOME, or
// under ~/.config when that is unset -- the same rule the profiles
// follow.
func ConfigDir() (string, error) {
	if d := os.Getenv("SLSH_CONFIG_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "slsh"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("slsh: no home directory: %w", err)
	}
	return filepath.Join(home, ".config", "slsh"), nil
}

// ConfigPath is the settings file itself, which "set" writes to and
// names when it has.
func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config"), nil
}

// LoadConfig reads the settings file, treating a missing one as empty.
func LoadConfig() (Config, error) {
	c := DefaultConfig()
	path, err := ConfigPath()
	if err != nil {
		return c, err
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, fmt.Errorf("slsh: %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return c, fmt.Errorf("slsh: %s line %d: want key = value, got %q", path, n, line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		s, ok := findSetting(key)
		if !ok {
			// A misspelled key would otherwise be a setting that
			// silently does nothing.
			return c, fmt.Errorf("slsh: %s line %d: unknown setting %q", path, n, key)
		}
		if err := s.parse(&c, value); err != nil {
			// Named, because a file has several settings in it and the
			// value being complained about is often one somebody
			// copied from another line.
			return c, fmt.Errorf("slsh: %s line %d: %s: %w", path, n, s.name, err)
		}
	}
	return c, sc.Err()
}

// saveSetting writes one setting into the file and hands back the path
// it wrote to.
//
// # Why this is not SaveProfile
//
// agent.SaveProfile rewrites a profile wholesale out of the struct,
// which is right there: what it writes is made from a Login and read
// by programs.  A settings file is different in the one way that matters
// -- it is hand-edited -- and the comments in it are somebody's notes
// about why a viewer needs that particular grid nickname, or which
// address was tried and did not answer.  Rewriting the file from the
// struct would throw all of that away, silently, the first time
// anybody typed "set".
//
// So the line for this setting changes and nothing else does: not the
// other lines, not the comments, not the blank lines, not the spacing
// on the line itself -- only the text after the "=".  A file that does
// not mention the setting gets one line appended, and a file that is
// not there at all is created, with its directory.
func saveSetting(s setting, value string) (string, error) {
	path, err := ConfigPath()
	if err != nil {
		return "", err
	}

	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return path, err
	}

	// The LAST line for this key, not the first.  The reader takes the
	// file from the top and lets each line overwrite what came before,
	// so in a file that mentions a setting twice it is the last one
	// that is in force -- and changing any other would be a "set" that
	// wrote the file and changed nothing anybody could see.
	lines := strings.Split(string(body), "\n")
	at := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			// A commented-out setting is a note, and notes are kept.
			continue
		}
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if got, ok := findSetting(key); ok && got.name == s.name {
			at = i
		}
	}

	text := string(body)
	switch {
	case at >= 0:
		// Everything up to and including the "=" is left exactly as it
		// was, which keeps the indentation, the column the values are
		// lined up in, and whichever spelling of the name the file
		// already used.
		eq := strings.Index(lines[at], "=")
		lines[at] = lines[at][:eq+1] + " " + value
		text = strings.Join(lines, "\n")
	default:
		if text != "" && !strings.HasSuffix(text, "\n") {
			// A file whose last line has no newline on it would
			// otherwise have the new setting run onto the end of it.
			text += "\n"
		}
		text += s.name + " = " + value + "\n"
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return path, err
	}
	// The mode the file already has, so that somebody who made theirs
	// private keeps it private.  There is nothing secret in it -- the
	// passwords are in the profiles, which agent.SaveProfile writes
	// 0600 -- so a new one is an ordinary file.
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}

	// Through a temporary file in the same directory, so that a failure
	// half way cannot leave somebody with a settings file that has been
	// truncated and not written.  Same directory because a rename is
	// only atomic within one filesystem.
	tmp, err := os.CreateTemp(dir, ".config.*")
	if err != nil {
		return path, err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return path, err
	}
	if _, err := tmp.WriteString(text); err != nil {
		tmp.Close()
		return path, err
	}
	if err := tmp.Close(); err != nil {
		return path, err
	}
	return path, os.Rename(tmp.Name(), path)
}

// ParseKey reads the name of a key.
//
// A prefix key has to be able to be anything, including the control
// characters that are the only keys a terminal has left over, so all of
// these mean something:
//
//	ESC  escape  ^[      the escape key, the default
//	^G   C-g     ctrl-g  a control character
//	!    ~               an ordinary character, if you never type it
//	0x07 7               a number, for anything not named above
//
// Enter and TAB are refused however they are spelled: Shell.key takes
// both before it looks for the escape key, so either would leave chat
// with no way out.
func ParseKey(s string) (rune, error) {
	r, err := keyNamed(s)
	if err != nil {
		return 0, err
	}
	switch r {
	case '\r', '\n':
		return 0, fmt.Errorf("%q would leave no way to send a message", s)
	case '\t':
		return 0, fmt.Errorf("%q already moves between conversations, so it would never leave chat", s)
	}
	return r, nil
}

// keyNamed is ParseKey without the refusals.
func keyNamed(s string) (rune, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("no key given")
	}

	switch strings.ToLower(s) {
	case "esc", "escape", "^[", "\\e":
		return 27, nil
	case "tab", "\\t":
		return '\t', nil
	case "space":
		return ' ', nil
	case "enter", "return":
		return '\r', nil
	}

	// ^X and its spellings, for a control character.
	var letter string
	switch {
	case len(s) == 2 && s[0] == '^':
		letter = s[1:]
	case len(s) == 3 && (strings.EqualFold(s[:2], "c-")):
		letter = s[2:]
	case len(s) == 6 && strings.EqualFold(s[:5], "ctrl-"):
		letter = s[5:]
	}
	if letter != "" {
		c := strings.ToUpper(letter)[0]
		if c < '@' || c > '_' {
			return 0, fmt.Errorf("%q is not a control key", s)
		}
		return rune(c - '@'), nil
	}

	// A number, in any of Go's bases.
	if n, err := strconv.ParseInt(s, 0, 32); err == nil {
		if n <= 0 || n > 0x10FFFF {
			return 0, fmt.Errorf("%q is not a key", s)
		}
		return rune(n), nil
	}

	// Anything else has to be exactly one character.
	rs := []rune(s)
	if len(rs) != 1 {
		return 0, fmt.Errorf("%q is not a key: give one character, ESC, or ^X", s)
	}
	return rs[0], nil
}

// KeyName is ParseKey backwards, for telling the user which key to
// press.
func KeyName(r rune) string {
	switch {
	case r == 27:
		return "ESC"
	case r == ' ':
		return "SPACE"
	case r < 32:
		return "^" + string(rune('@'+r))
	default:
		return string(r)
	}
}
