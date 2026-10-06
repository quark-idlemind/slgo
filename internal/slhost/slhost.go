// Package slhost finds the machine slgod is running on.
//
// slgod does not always run where the program talking to it runs, and
// the machine it does run on moves between networks, so its address is
// never hardcoded: the sl-host command is the single source of truth
// and picks the right one from its own configuration.  What it prints
// is usually a bare address, and the port is joined here; when it
// prints one with a port -- a second slgod, on another -- that is used
// as it is.
//
// sl-host may keep rules for one profile and not another, so it is told
// which avatar is wanted when that is known, in $SLGO_AGENT: an older
// sl-host ignores a variable it has never heard of, where it would
// refuse a flag.
//
// sl-host is not installed everywhere, and on a machine that runs its
// own slgod there is nothing for it to answer: NOT being on $PATH means
// this machine, which is why that case is a default and not an error.
// Failing while it IS on $PATH is a different matter and is reported.
// Falling back to localhost there would turn "sl-host is misconfigured"
// into a connection refused against this machine, which points the
// reader at the wrong problem entirely.
package slhost

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
)

// Port is the port slgod serves clients on.
const Port = "7807"

// Command is the program asked where slgod is.
const Command = "sl-host"

// Addr returns the address of slgod, on the default port unless
// sl-host names another.
func Addr() (string, error) { return addrFor("", Port) }

// AddrOn returns the host slgod is on, with a port joined to it unless
// sl-host gave one.
//
// It is only for working out a DEFAULT.  An address given on a command
// line or in a configuration file is the operator saying where to go,
// and must not be second-guessed by asking anything.
func AddrOn(port string) (string, error) { return addrFor("", port) }

// AddrFor is Addr for a named profile, whose slgod may be a different
// one: sl-host is asked about that profile.  An empty name asks about
// none, which is the daemon's default avatar.
func AddrFor(profile string) (string, error) { return addrFor(profile, Port) }

func addrFor(profile, port string) (string, error) {
	host, err := HostFor(profile)
	if err != nil {
		return "", err
	}
	return withPort(host, port), nil
}

// withPort joins a port to an address that has none.  An address with
// one is left alone: sl-host said where, port and all.  A bare IPv6
// address has colons and no port, and may come bracketed or not.
func withPort(host, port string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return net.JoinHostPort(host, port)
}

// EnvAddr is the variable that says where slgod is when no flag does.
const EnvAddr = "SLGO_ADDR"

// Resolve fills in an address that was not given.
//
// An address from a command line or a configuration file is returned
// untouched: that is the operator saying where to go, and asking
// anything else would be second-guessing it.  Only the empty string --
// nothing said anywhere -- is worth a question, and the first answer
// is $SLGO_ADDR, the same statement as the flag made once for a whole
// shell.  The order every command that dials slgod follows is: the
// flag (or the configuration file's setting), then $SLGO_ADDR, then
// sl-host, then this machine's port 7807.
// TestNoCommandFindsSlgodItsOwnWay refuses a command that reads the
// variable by another road.
func Resolve(addr string) (string, error) { return ResolveFor(addr, "") }

// ResolveFor is Resolve for a named profile: with no address given,
// $SLGO_ADDR, then sl-host, is asked where that profile's slgod is.
func ResolveFor(addr, profile string) (string, error) {
	if addr != "" {
		return addr, nil
	}
	if env := os.Getenv(EnvAddr); env != "" {
		return env, nil
	}
	return AddrFor(profile)
}

// MustAddr is Resolve for a small command-line tool: it reports a
// failure and exits, because there is nothing such a program can do
// about not knowing where to connect and every one of them would write
// the same three lines.
func MustAddr(addr string) string {
	a, err := Resolve(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return a
}

// Host returns the host slgod is on: what sl-host says, or localhost
// when sl-host is not installed.  What sl-host says may carry a port.
func Host() (string, error) { return HostFor("") }

// HostFor is Host for a named profile.  sl-host is run with $SLGO_AGENT
// set to it, or with $SLGO_AGENT taken out of its environment when no
// profile is named, so that the answer is about exactly the avatar the
// caller is about to ask for -- which the caller has already worked out
// from -a, $SLGO_AGENT and its own settings, in that order.
func HostFor(profile string) (string, error) {
	path, err := exec.LookPath(Command)
	if err != nil {
		// Not installed, so there is nothing to ask and nowhere else
		// slgod could be.
		return "localhost", nil
	}

	cmd := exec.Command(path)
	cmd.Env = withAgent(os.Environ(), profile)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			stderr = ": " + strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("cannot find the host slgod is on: %s failed: %v%s\n"+
			"\t--addr HOST:PORT overrides", Command, err, stderr)
	}

	host := strings.TrimSpace(string(out))
	if host == "" {
		return "", fmt.Errorf("cannot find the host slgod is on: %s printed nothing\n"+
			"\t--addr HOST:PORT overrides", Command)
	}
	// It prints one address; anything after the first word is not it.
	if i := strings.IndexAny(host, " \t\n"); i >= 0 {
		host = host[:i]
	}
	return host, nil
}

// withAgent is env with SLGO_AGENT set to profile, or removed when
// profile is empty.
func withAgent(env []string, profile string) []string {
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "SLGO_AGENT=") {
			out = append(out, kv)
		}
	}
	if profile != "" {
		out = append(out, "SLGO_AGENT="+profile)
	}
	return out
}
