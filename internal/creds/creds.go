package creds

// Package creds works out who to log in as, when a program is doing
// the logging in itself rather than attaching to slgod.
//
// The credentials may already be on disk: profiles live one file per
// account under ~/.config/slgo, and one of them may be named outright
// or found by the avatar's name.  Whatever is still missing is asked
// for, and the password is asked for without echo, because a terminal
// that shows it puts it in the scrollback of whoever walks past.

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/quark-idlemind/slgo/agent"
	"golang.org/x/term"
)

// Resolve assembles a login from what was asked for, what is on
// disk, and what the person at the keyboard can be asked.
//
// Order matters and is the order of least surprise: an explicitly named
// profile wins, then a profile whose names match the ones given, then
// what is typed in.  A password already stored is used as it stands --
// it is the "$1$" digest, which is the only form that ever goes over
// the wire anyway.
func Resolve(in *os.File, out io.Writer, profile, first, last, start string) (agent.Login, error) {
	var l agent.Login

	switch {
	case profile != "":
		p, err := agent.LoadProfile(profile)
		if err != nil {
			return l, err
		}
		l = p
	case first != "" && last != "":
		// A profile for these names saves asking for the password.
		if p, ok := profileFor(first, last); ok {
			l = p
		}
	default:
		// Nothing named at all: one profile means there is nothing
		// to choose between.
		if names, err := agent.ListProfiles(); err == nil && len(names) == 1 {
			if p, err := agent.LoadProfile(names[0]); err == nil {
				l = p
				fmt.Fprintf(out, "using the %s profile\n", names[0])
			}
		}
	}

	if first != "" {
		l.First = first
	}
	if last != "" {
		l.Last = last
	}
	if start != "" {
		l.Start = start
	}

	// Whatever is still missing has to be asked for.
	r := bufio.NewReader(in)
	if l.First == "" {
		s, err := ask(r, out, "First name: ")
		if err != nil {
			return l, err
		}
		l.First = s
	}
	if l.Last == "" {
		s, err := ask(r, out, "Last name: ")
		if err != nil {
			return l, err
		}
		// Second Life's own accounts have no last name and are
		// "Resident" to everything that asks for one.
		if s == "" {
			s = "Resident"
		}
		l.Last = s
	}
	if l.Password == "" {
		s, err := askSecret(in, r, out, fmt.Sprintf("Password for %s %s: ", l.First, l.Last))
		if err != nil {
			return l, err
		}
		l.Password = s
	}

	if l.First == "" || l.Password == "" {
		return l, fmt.Errorf("a name and a password are needed to log in")
	}
	return l, nil
}

// profileFor finds a stored profile for an avatar by the names in it,
// since profiles are filed under a short name of the operator's
// choosing rather than under the avatar's.
func profileFor(first, last string) (agent.Login, bool) {
	names, err := agent.ListProfiles()
	if err != nil {
		return agent.Login{}, false
	}
	for _, n := range names {
		p, err := agent.LoadProfile(n)
		if err != nil {
			continue
		}
		if strings.EqualFold(p.First, first) && strings.EqualFold(p.Last, last) {
			return p, true
		}
	}
	return agent.Login{}, false
}

func ask(r *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fmt.Fprint(out, prompt)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("nothing to read: %w", err)
	}
	return strings.TrimSpace(line), nil
}

// askSecret reads a password without echoing it.
//
// Down a pipe there is no terminal to turn echo off on, and no terminal
// to have shown it either, so the line is simply read -- through the
// same reader as everything else, since a second one over the same file
// would find the first had already buffered the lot.  That is what
// makes a scripted login possible without a special case elsewhere.
func askSecret(in *os.File, r *bufio.Reader, out io.Writer, prompt string) (string, error) {
	fd := int(in.Fd())
	if !term.IsTerminal(fd) {
		return ask(r, out, prompt)
	}

	fmt.Fprint(out, prompt)
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(out)
	if err != nil {
		return "", fmt.Errorf("could not read the password: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}
