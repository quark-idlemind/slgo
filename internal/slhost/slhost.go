// Package slhost finds the machine slgod is running on.
//
// slgod does not always run where the program talking to it runs, and
// the machine it does run on moves between networks, so its address is
// never hardcoded: the sl-host command is the single source of truth
// and picks the right one from its own configuration.  Only the host
// comes from there -- sl-host prints a bare address with no port -- so
// the port is joined here.
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

// Addr returns the address of slgod, on the default port.
func Addr() (string, error) { return AddrOn(Port) }

// AddrOn returns the host slgod is on, with a port joined to it.
//
// It is only for working out a DEFAULT.  An address given on a command
// line or in a configuration file is the operator saying where to go,
// and must not be second-guessed by asking anything.
func AddrOn(port string) (string, error) {
	host, err := Host()
	if err != nil {
		return "", err
	}
	return net.JoinHostPort(host, port), nil
}

// Resolve fills in an address that was not given.
//
// An address from a command line or a configuration file is returned
// untouched: that is the operator saying where to go, and asking
// anything else would be second-guessing it.  Only the empty string --
// nothing said anywhere -- is worth a question.
func Resolve(addr string) (string, error) {
	if addr != "" {
		return addr, nil
	}
	return Addr()
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
// when sl-host is not installed.
func Host() (string, error) {
	path, err := exec.LookPath(Command)
	if err != nil {
		// Not installed, so there is nothing to ask and nowhere else
		// slgod could be.
		return "localhost", nil
	}

	out, err := exec.Command(path).Output()
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
	// It prints one address; anything after the first line is not it.
	if i := strings.IndexAny(host, " \t\n"); i >= 0 {
		host = host[:i]
	}
	return host, nil
}
