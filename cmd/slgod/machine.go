package main

// The login server asks a client which computer it is running on: "mac",
// from the network card's address, and "id0", from the first disk's
// serial number.  A viewer reads both off the hardware and sends the
// md5 of each, so what travels is a pair of digests -- the hashing
// happens in agent.Login, and what is kept here is the address itself,
// which is the readable half and the one worth being able to check.
//
// slgod has no hardware to read -- and reading this host's would hand
// Linden Lab an identifier that follows the operator to every other
// program on the machine -- so it makes a pair up once and keeps it in
//
//	~/.config/slgod/config
//
// Keeping it matters more than what it is.  A pair that changes every
// login looks like a different computer every time, which is what an
// abuser looks like; a pair that never changes looks like an ordinary
// resident with one computer.  So the file is written once, on the first
// run, and read on every run after.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// machineID is the computer slgod claims to be.
type machineID struct {
	MAC string // 02:xx:xx:xx:xx:xx, hashed before it is sent
	ID0 string // 32 hex digits, the shape a viewer's serial digest has
}

// machineConfigDir returns the directory holding slgod's own settings.
//
// This is slgod, not slgo: the profiles under ~/.config/slgo belong to
// accounts and are portable between machines, while this describes the
// machine and should not travel with them.
func machineConfigDir() (string, error) {
	if d := os.Getenv("SLGOD_CONFIG_DIR"); d != "" {
		return d, nil
	}
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "slgod"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("slgod: no home directory: %w", err)
	}
	return filepath.Join(home, ".config", "slgod"), nil
}

// useConfigDir makes dir the one directory this daemon keeps
// everything in: the profiles it logs in, which are otherwise
// ~/.config/slgo, and its own files -- the machine identity, the seats,
// the viewer certificate -- which are otherwise ~/.config/slgod.  It is
// what -config does, and what lets a second slgod run beside the first
// with its own accounts and its own state, a development daemon on
// another port, say, without the two reading each other's files.
//
// It sets the two variables that already name each directory outright,
// SLGO_CONFIG_DIR and SLGOD_CONFIG_DIR, so that every lookup in the
// process -- agent's for profiles, this package's for the rest -- lands
// on the same place without a directory being passed through each.
// The path is made absolute first, so a later change of working
// directory cannot move it.
//
// The directory has to exist and be private already, 0700 like the
// profile directory: it holds credentials, and one made here on the
// strength of a typing slip would only hold nobody's.  The shared
// secret is not in it.  That is one file for the whole lab, and a
// daemon that looked for it here would be one its clients could not
// reach.
func useConfigDir(dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", abs)
	}
	if m := fi.Mode().Perm(); m&0o077 != 0 {
		return fmt.Errorf("%s is mode %04o, wanted 0700: chmod 700 %s", abs, m, abs)
	}
	if err := os.Setenv("SLGO_CONFIG_DIR", abs); err != nil {
		return err
	}
	return os.Setenv("SLGOD_CONFIG_DIR", abs)
}

// secretPath is the file slgod reads its secret from: the one named by
// -secret, or a file called secret in the -config directory when there
// is one, or "" for the shared secret everything else uses.
//
// A daemon with a directory of its own may have a secret of its own,
// and one put there is taken to be meant: a second slgod for other
// accounts need not answer to the first one's clients.  Its clients
// find it by address -- see auth.SecretPathsFor.
func secretPath(flagged, configDir string) string {
	if flagged != "" {
		return flagged
	}
	if configDir == "" {
		return ""
	}
	p := filepath.Join(configDir, "secret")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

func machineConfigPath() (string, error) {
	dir, err := machineConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config"), nil
}

// loadMachineID reads the stored identity, inventing and writing
// whichever half of it is missing.  It reports the path, so a first run
// can say where the new file went.
func loadMachineID() (machineID, string, error) {
	path, err := machineConfigPath()
	if err != nil {
		return machineID{}, "", err
	}

	m, err := readMachineID(path)
	if err != nil {
		return machineID{}, path, err
	}

	// A half filled file is worth completing rather than refusing:
	// an operator who wrote a mac by hand should not have to invent
	// an id0 too.
	made := false
	if m.MAC == "" {
		if m.MAC, err = randomMAC(); err != nil {
			return machineID{}, path, err
		}
		made = true
	}
	if m.ID0 == "" {
		if m.ID0, err = randomID0(); err != nil {
			return machineID{}, path, err
		}
		made = true
	}
	if made {
		if err := writeMachineID(path, m); err != nil {
			return machineID{}, path, err
		}
	}
	return m, path, nil
}

// readMachineID parses the config file, treating a missing one as empty.
func readMachineID(path string) (machineID, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return machineID{}, nil
		}
		return machineID{}, fmt.Errorf("slgod: %s: %w", path, err)
	}
	defer f.Close()

	var m machineID
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return machineID{}, fmt.Errorf("slgod: %s line %d: want key = value, got %q", path, n, line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		switch key {
		case "mac":
			m.MAC = value
		case "id0":
			m.ID0 = value
		default:
			// A misspelled key would otherwise be a setting
			// that silently does nothing.
			return machineID{}, fmt.Errorf("slgod: %s line %d: unknown setting %q", path, n, key)
		}
	}
	return m, sc.Err()
}

// writeMachineID stores the identity, through a temporary file so a
// failure cannot leave half of one behind.
func writeMachineID(path string, m machineID) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	var b strings.Builder
	b.WriteString("# slgod machine identity, invented once and kept.\n" +
		"#\n" +
		"# The login server takes these as the computer slgod runs on.  They\n" +
		"# are deliberately not this host's real address or disk serial: what\n" +
		"# matters is that they never change, not that they are true.  Change\n" +
		"# them and Linden Lab sees the accounts move to a new machine.\n")
	fmt.Fprintf(&b, "mac = %s\n", m.MAC)
	fmt.Fprintf(&b, "id0 = %s\n", m.ID0)

	tmp, err := os.CreateTemp(dir, ".config.*")
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

// randomMAC invents an address that cannot collide with a real card's.
//
// The low two bits of the first octet say what kind of address it is:
// locally administered, and unicast.  Set that way it is drawn from the
// range set aside for addresses nobody assigned, so no manufacturer will
// ever ship the same one.
func randomMAC() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("slgod: cannot invent a mac address: %w", err)
	}
	b[0] = b[0]&^0x01 | 0x02
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02X", x)
	}
	return strings.Join(parts, ":"), nil
}

// randomID0 invents a serial digest, in the shape a viewer sends: the
// hex of an md5, which is to say 32 hex digits.
func randomID0() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("slgod: cannot invent an id0: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
