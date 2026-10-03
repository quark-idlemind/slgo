package main

// This avatar's own account details: the email address on file and
// whether the directory shows the avatar.
//
//	account        ask the grid, and print them
//
// They are private.  This is the only command that asks for them or
// prints them, and nothing else here -- status, who, profile, a log --
// holds them.  The retired IM-to-email flag is not printed: the viewer
// ignores it on Second Life, and so does this.
// Why: doc/account.md

import (
	"context"
	"fmt"
	"io"
)

var accountCommands = map[string]*command{
	"account": {
		flags:    func() any { return new(helpOnly) },
		brief:    "this avatar's own email address and directory visibility; private, printed only here",
		keywords: "email address mail directory visibility search listed private settings",
		man:      "account",
		run:      cmdAccount,
	},
}

// cmdAccount asks the grid for the details and prints two lines.  Errors
// come from sl.UserInfo, which keeps the address out of them.
func cmdAccount(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("account", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 0 {
		return usageError("account")
	}
	u, err := sh.s.UserInfo(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%-10s  %s\n", "email", u.Email)
	fmt.Fprintf(out, "%-10s  %s\n", "directory", u.DirectoryVisibility)
	return nil
}
