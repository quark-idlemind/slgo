package agent

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Credentials live in one file per account under a private directory:
//
//	~/.config/slgo/          mode 700
//	~/.config/slgo/example      mode 600
//
// and a profile is named by the file:
//
//	acct, err := client.LoginAs(ctx, "example")
//
// The file is key = value lines, # for comments:
//
//	first    = Example
//	last     = Resident
//	password = $1$00157e577e57c0de028f000000000000
//	start    = last
//
// Storing the "$1$" digest rather than the plain password is worth
// doing.  It is the only form that ever goes over the wire, so it loses
// nothing, and it keeps a password that may be used elsewhere off the
// disk.  Plain text works too, and is hashed on the way out.

// ConfigDir returns the directory holding profiles.
//
// SLGO_CONFIG_DIR names it outright.  Otherwise it is slgo under
// XDG_CONFIG_HOME, or under ~/.config when that is unset -- on every
// platform, deliberately, rather than os.UserConfigDir, which on macOS
// points at ~/Library/Application Support.
func ConfigDir() (string, error) {
	if d := os.Getenv("SLGO_CONFIG_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "slgo"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("agent: no home directory: %w", err)
	}
	return filepath.Join(home, ".config", "slgo"), nil
}

// ProfilePath is where the named profile lives.
func ProfilePath(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("agent: profile needs a name")
	}
	// A profile is one file in one directory, never a path.
	if strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return "", fmt.Errorf("agent: %q is not a profile name", name)
	}
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

// LoadProfile reads a profile and returns the login it describes.
//
// It refuses a profile that anyone but its owner can read, and a
// directory that anyone but its owner can list.  A credentials file
// with the wrong permissions is worth stopping for.
func LoadProfile(name string) (Login, error) {
	path, err := ProfilePath(name)
	if err != nil {
		return Login{}, err
	}

	dir := filepath.Dir(path)
	if fi, err := os.Stat(dir); err == nil {
		if m := fi.Mode().Perm(); m&0o077 != 0 {
			return Login{}, fmt.Errorf(
				"agent: %s is mode %04o, wanted 0700: chmod 700 %s", dir, m, dir)
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return Login{}, fmt.Errorf("agent: profile %q: %w", name, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return Login{}, err
	}
	if m := fi.Mode().Perm(); m&0o077 != 0 {
		return Login{}, fmt.Errorf(
			"agent: %s is mode %04o, wanted 0600: chmod 600 %s", path, m, path)
	}

	l, err := parseProfile(f)
	if err != nil {
		return Login{}, fmt.Errorf("agent: %s: %w", path, err)
	}
	if l.First == "" || l.Last == "" {
		return Login{}, fmt.Errorf("agent: %s: needs first and last", path)
	}
	if l.Password == "" {
		return Login{}, fmt.Errorf("agent: %s: needs password", path)
	}
	return l, nil
}

// LoginAs loads a profile and logs in with it.
func LoginAs(ctx context.Context, name string) (*Account, error) {
	l, err := LoadProfile(name)
	if err != nil {
		return nil, err
	}
	return l.Do(ctx)
}

func parseProfile(r *os.File) (Login, error) {
	var l Login
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return l, fmt.Errorf("line %d: want key = value, got %q", n, line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)

		switch key {
		case "first":
			l.First = value
		case "last":
			l.Last = value
		case "password", "passwd":
			l.Password = value
		case "start":
			l.Start = value
		case "url", "login_url":
			l.URL = value
		case "channel":
			l.Channel = value
		case "version":
			l.Version = value
		case "mac":
			l.MAC = value
		case "platform":
			l.Platform = value
		case "options":
			for _, o := range strings.Split(value, ",") {
				if o = strings.TrimSpace(o); o != "" {
					l.Options = append(l.Options, o)
				}
			}
		default:
			// A misspelled key would otherwise be a setting
			// that silently does nothing.
			return l, fmt.Errorf("line %d: unknown setting %q", n, key)
		}
	}
	return l, sc.Err()
}

// SaveProfile writes a profile, creating the directory if it is
// missing, with the permissions LoadProfile insists on.
//
// The password is stored as its "$1$" digest.
func SaveProfile(name string, l Login) error {
	path, err := ProfilePath(name)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll leaves an existing directory alone, so say it again.
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# slgo profile %q\n", name)
	write := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%-9s= %s\n", k, v)
		}
	}
	write("first", l.First)
	write("last", l.Last)
	if l.Password != "" {
		write("password", hashPassword(l.Password))
	}
	write("start", l.Start)
	write("url", l.URL)
	write("channel", l.Channel)
	write("version", l.Version)
	write("mac", l.MAC)
	write("platform", l.Platform)
	if len(l.Options) > 0 {
		write("options", strings.Join(l.Options, ", "))
	}

	// Write through a temporary file so a failure cannot leave a
	// half written profile, and create it 0600 from the start so
	// the secret is never briefly readable.
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// ListProfiles names the profiles that are readable.
func ListProfiles() ([]string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		out = append(out, e.Name())
	}
	return out, nil
}
