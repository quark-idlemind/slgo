package main

// Settings live in ~/.config/slchat/config, in the same key = value
// form as everything else in this tree:
//
//	addr   = localhost:7807
//	agent  = example
//	prefix = ESC
//
// Flags win over the file, and the file over the defaults.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Config is what slchat needs to know before it can start.
type Config struct {
	Addr   string // the slgod to attach to
	Agent  string // the profile it hosts; empty means the only one
	Prefix rune   // the key that starts a command
}

// DefaultConfig is what an empty file leaves you with.
func DefaultConfig() Config {
	return Config{Addr: "localhost:7807", Prefix: 27}
}

// ConfigDir is where slchat keeps its settings.  SLCHAT_CONFIG_DIR
// names it outright; otherwise it is slchat under XDG_CONFIG_HOME, or
// under ~/.config when that is unset -- the same rule the profiles
// follow.
func ConfigDir() (string, error) {
	if d := os.Getenv("SLCHAT_CONFIG_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "slchat"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("slchat: no home directory: %w", err)
	}
	return filepath.Join(home, ".config", "slchat"), nil
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
		return c, fmt.Errorf("slchat: %s: %w", path, err)
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
			return c, fmt.Errorf("slchat: %s line %d: want key = value, got %q", path, n, line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "addr", "server":
			c.Addr = value
		case "agent", "profile":
			c.Agent = value
		case "prefix", "prefix_key":
			r, err := ParseKey(value)
			if err != nil {
				return c, fmt.Errorf("slchat: %s line %d: %w", path, n, err)
			}
			c.Prefix = r
		default:
			// A misspelled key would otherwise be a setting that
			// silently does nothing.
			return c, fmt.Errorf("slchat: %s line %d: unknown setting %q", path, n, key)
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
