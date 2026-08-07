package main

// Options for the commands inside the shell.
//
// The same getopt the programs themselves use, so that a flag means the
// same thing in "slsh -c ..." as at the prompt, and clusters like -lrT
// work without every combination being spelled out by hand.  The usage
// text comes from the option declarations too, so a flag cannot be
// added without appearing in --help.

import (
	"io"

	"github.com/pborman/getopt/v2"
	"github.com/pborman/options"
)

// subOptions parses one command's flags, and prints its usage if that
// is what was asked for.
//
// Each call gets a set of its own, which matters twice over: nothing a
// command registers is still registered for the next one, and --help
// can print THIS command's options.  The shell runs many commands in
// one process, which is the case a package-level set gets wrong.
//
// done is true when the usage was printed and the command has nothing
// further to do.
//
// options.Help is deliberately not used for the --help flag. It calls
// os.Exit(0), which in a shell would take the whole session down when
// somebody typed "ls -h"; and it prints getopt's GLOBAL set, which here
// holds slsh's own program flags, so "ls -h" answered with --addr and
// --agent. Printing this set to the command's own writer is what was
// wanted from it, and redirects with > like any other output.
func subOptions(name, params string, opts any, out io.Writer, args []string) (rest []string, done bool, err error) {
	set := getopt.New()
	set.SetProgram(name)
	set.SetParameters(params)
	if err := options.RegisterSet(name, opts, set); err != nil {
		return nil, false, err
	}

	argv := make([]string, 0, len(args)+1)
	argv = append(argv, name)
	argv = append(argv, args...)
	if err := set.Getopt(argv, nil); err != nil {
		return nil, false, err
	}
	if set.IsSet("help") {
		set.PrintUsage(out)
		return nil, true, nil
	}
	return set.Args(), false, nil
}
