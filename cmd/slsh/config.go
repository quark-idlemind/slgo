package main

// Settings live in ~/.config/slsh/config, in the same key = value
// form as everything else in this tree:
//
//	addr   = localhost:7807
//	agent  = example
//	escape = ESC
//
//	viewer_app     = Firestorm-OpenSim
//	viewer_grid    = slgod
//	viewer_launch  = open -a {app} --args --grid {grid} --login {first} {last} {password}
//	viewer_running = pgrep -f {app}.app/Contents
//
// Flags win over the file, and the file over the defaults.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Config is what slsh needs to know before it can start.
type Config struct {
	// Addr is the slgod to attach to.  Empty means nobody has said,
	// which is what lets sl-host be asked -- see internal/slhost.
	Addr   string
	Agent  string // the profile: hosted by slgod, or on disk for --direct
	Prefix rune   // the key that starts a command

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
}

// DefaultConfig is what an empty file leaves you with.
//
// Addr is deliberately empty rather than localhost: with nothing said
// in a file and nothing on the command line, where slgod runs is a
// question for sl-host, and a default here would answer it first.
func DefaultConfig() Config {
	c := Config{Prefix: 27}
	c.ViewerApp, c.ViewerGrid, c.ViewerLaunch, c.ViewerRunning = viewerDefaults(runtime.GOOS)
	return c
}

// viewerDefaults is how to start a real viewer on this kind of machine,
// point it at slgod, and tell whether one is already up.
//
// # Why the OpenSim build and not the one already installed
//
// This is the trap, and it is worth the paragraph.  A Firestorm built
// for Second Life CANNOT be pointed at a private grid at all.  That
// flavour compiles LLGridManager from llviewernetwork.cpp, whose
// grid-file block -- the one that would read the viewer's own
// grids.user.xml -- is compiled out (llviewernetwork.cpp:149-201,
// "#if 0 <FS:AW disabled for meeting havok sublicense requirements/>"),
// so the only grids it has are the two it hardcodes.  Driven live, the
// SL build at /Applications/Firestorm-Releasex64.app logged
//
//	WARNING #GridManager# llviewernetwork.cpp(214) initialize :
//	Unknown grid 'slgod'
//
// and then llviewernetwork.cpp:244, "Default grid to
// util.agni.lindenlab.com" -- an Agni login screen, with nothing on it
// to suggest why.  Somebody who reaches for the viewer they already
// have gets exactly that, which is why the default names the other one.
//
// The OpenSim flavour compiles fsgridhandler.cpp instead, which does
// read grids.user.xml, and it is at ~/Applications/Firestorm-OpenSim.app
// here.  "open -a Firestorm-OpenSim" resolves it by name; both the bare
// name and the full path were tried live and both attached.
//
// # Why the grid is a NICKNAME and a setting of its own
//
// --grid takes a nickname out of the viewer's own grid list, not a URL.
// Passing the login URI where the nickname goes was tried on the chance
// that the auto-add path would take it, and did not work: "Unknown grid
// 'http://127.0.0.1:9000/'", then Agni again.  --loginuri, which would
// be the obvious flag, is read into CmdLineLoginURI
// (app_settings/cmd_line.xml:201-208) and then never looked at by
// anything but its own unit tests.
//
// So the grid has to exist in the viewer BEFORE any of this works:
// Preferences -> OpenSim, add the login URI that "viewer" prints, and
// give it the nickname this setting names.  That is a one-off piece of
// local setup, which is exactly the sort of thing that belongs in a
// setting rather than buried in a command template where nobody would
// find it.
//
// # Why macOS goes through open(1)
//
// A macOS application is a bundle, not an executable: the binary here
// is Firestorm-OpenSim.app/Contents/MacOS/Firestorm, and running it
// directly is not the same as launching the app.  "open -a" is the
// supported way and is what the recovery script on this machine already
// uses (~/bin/sl-restart, which launches with open -a "$APP" --args
// --autologin), so it is copied from there.
//
// It also brings the viewer to the FRONT, which is deliberate and is
// what was asked for: open activates the application it launches unless
// -g is given (man open), and somebody who has just typed "viewer
// --launch" wants to be looking at it.
//
// open returns as soon as the launch has been handed off rather than
// waiting for the application to exit -- that is what -W is for, and it
// is not passed -- so the prompt comes back at once.
//
// # Why the running check is a process match
//
// It has to answer before the viewer has a window, a port or a session,
// and a process is the only thing it has by then.  The pattern is the
// bundle path rather than the process name because the process is
// called plain "Firestorm" whichever build it came from -- which is
// also why it must follow the app setting, since the two flavours are
// told apart only by their bundles.  sl-restart reaps orphans with the
// same shape of pattern (pkill -f "Firestorm-Releasex64.app/Contents").
//
// # Why an unknown platform gets nothing
//
// Guessing a binary name would produce a command that fails somewhere
// inside a viewer's own startup, or worse, starts the wrong program.
// Empty means "viewer --launch" says nobody has told it what to run,
// which is a sentence somebody can act on.
func viewerDefaults(goos string) (app, grid, launch, running string) {
	switch goos {
	case "darwin":
		return "Firestorm-OpenSim", "slgod",
			"open -a {app} --args --grid {grid} --login {first} {last} {password}",
			"pgrep -f {app}.app/Contents"
	}
	return "", "", "", ""
}

// ConfigDir is where slchat keeps its settings.  SLSH_CONFIG_DIR
// names it outright; otherwise it is slchat under XDG_CONFIG_HOME, or
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

// LoadConfig reads the settings file, treating a missing one as empty.
func LoadConfig() (Config, error) {
	c := DefaultConfig()
	dir, err := ConfigDir()
	if err != nil {
		return c, err
	}
	path := filepath.Join(dir, "config")
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
		switch key {
		case "addr", "server":
			c.Addr = value
		case "agent", "profile":
			c.Agent = value
		case "viewer_app":
			c.ViewerApp = value
		case "viewer_grid":
			c.ViewerGrid = value
		case "viewer_launch":
			c.ViewerLaunch = value
		case "viewer_running":
			// Deliberately settable to nothing: a person who would
			// rather slsh did not run pgrep can empty it, and the
			// launch then goes ahead without the check.
			c.ViewerRunning = value
		case "escape", "prefix", "prefix_key":
			r, err := ParseKey(value)
			if err != nil {
				return c, fmt.Errorf("slsh: %s line %d: %w", path, n, err)
			}
			c.Prefix = r
		default:
			// A misspelled key would otherwise be a setting that
			// silently does nothing.
			return c, fmt.Errorf("slsh: %s line %d: unknown setting %q", path, n, key)
		}
	}
	return c, sc.Err()
}

// ParseKey reads the name of a key.
//
// A prefix key has to be able to be anything, including the control
// characters that are the only keys a terminal has left over, so all of
// these mean something:
//
//	ESC  escape  ^[      the escape key, the default
//	^G   C-g     ctrl-g  a control character
//	TAB  ^I              tab, though it already cycles sessions
//	!    ~               an ordinary character, if you never type it
//	0x07 7               a number, for anything not named above
func ParseKey(s string) (rune, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("no key given")
	}

	switch strings.ToLower(s) {
	case "esc", "escape", "^[", "\\e":
		return 27, nil
	case "tab", "^i", "\\t":
		return 9, nil
	case "space":
		return ' ', nil
	case "enter", "return", "^m":
		return 0, fmt.Errorf("%q would leave no way to send a message", s)
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
	case r == 9:
		return "TAB"
	case r == ' ':
		return "SPACE"
	case r < 32:
		return "^" + string(rune('@'+r))
	default:
		return string(r)
	}
}
