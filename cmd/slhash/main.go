// Command slhash writes the hashed form of a password that a viewer sends
// to a Second Life login server.
//
//	slhash             asks for it, without echoing it
//	slhash PASSWORD    hashes the one given
//	slhash < FILE      reads the first line of standard input
//
// The scheme is the grid's own: the plaintext is md5'd whole and sent
// as "$1$" followed by the lowercase hex digits -- llloginhandler.cpp
// md5s --login, llsecapi.cpp prepends "$1$".  It is the same form slgo
// already mints and compares in agent/login and internal/creds, so a
// digest from here logs in wherever a stored viewer_password does.
//
// Asked for at a terminal, the password is typed with echo off, so it is
// on neither the screen nor in the shell's history, which is the reason to
// prefer it.  Given as an argument it is in both, and visible to anybody
// on the machine who lists its processes while this runs -- the same as
// piping it in from echo or printf, which put it on a command line just
// the same, so the argument is offered as what it is rather than pushed
// through a pipe to look safer than it is.
//
// Read from standard input, only the first line counts, and a trailing
// CR/LF is stripped, so a password piped in with or without a newline
// gives the same digest.  An empty password is refused however it
// arrives.  Nothing here prints, logs or keeps the plaintext.  --version
// says which build this is.
package main

import (
	"bufio"
	"crypto/md5"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/quark-idlemind/slgo/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, in *os.File, out, errw io.Writer) int {
	fs := flag.NewFlagSet("slhash", flag.ContinueOnError)
	fs.SetOutput(errw)
	ver := fs.Bool("version", false, "say which build this is, and exit")
	fs.Usage = func() {
		fmt.Fprintln(errw, "usage: slhash [PASSWORD]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *ver {
		fmt.Fprintln(out, version.String("slhash"))
		return 0
	}

	var password string
	switch fs.NArg() {
	case 0:
		p, err := readPassword(in, errw)
		if err != nil {
			fmt.Fprintf(errw, "slhash: %v\n", err)
			return 1
		}
		password = p
	case 1:
		password = fs.Arg(0)
	default:
		fs.Usage()
		return 2
	}
	// Not a password anything will accept: the digest of nothing is
	// the digest of a slip -- return pressed at the prompt, or an empty
	// variable -- and would only be found out when a login failed.
	if password == "" {
		fmt.Fprintln(errw, "slhash: the password is empty")
		return 1
	}
	fmt.Fprintln(out, digest(password))
	return 0
}

// readPassword asks for the password with echo off when standard input is
// a terminal, and otherwise reads its first line.
//
// The prompt is written to the terminal itself, through standard input,
// which a terminal is opened for reading and writing on.  So it is seen
// with standard output and standard error both redirected -- slhash >
// digest 2> errors still asks -- and it never lands in either file.
// Standard error is the fallback, for a terminal that will not take the
// write.
func readPassword(in *os.File, errw io.Writer) (string, error) {
	if fd := int(in.Fd()); term.IsTerminal(fd) {
		var prompt io.Writer = in
		if _, err := io.WriteString(in, "Password: "); err != nil {
			prompt = errw
			io.WriteString(prompt, "Password: ")
		}
		b, err := term.ReadPassword(fd)
		io.WriteString(prompt, "\n")
		if err != nil {
			return "", fmt.Errorf("could not read the password: %w", err)
		}
		return string(b), nil
	}
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("no password on standard input")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// digest is the form the login server is sent.
func digest(password string) string {
	sum := md5.Sum([]byte(password))
	return "$1$" + hex.EncodeToString(sum[:])
}
