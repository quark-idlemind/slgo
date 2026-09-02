package main

// What this avatar is willing to be shown, which decides where it may
// go.
//
//	maturity                 what the grid says the preference is now
//	maturity RATING          ask for that one
//
// # Two numbers, and only one of them is anybody's to set
//
// Land is rated general, moderate or adult, and an account carries a
// PREFERENCE -- the highest it has asked to be shown -- and a CEILING,
// the highest it is permitted to ask for.  This command sets the first.
// The second comes from age verification on the account and no client
// can move it: there is no message for it, and Linden Lab's own viewer
// sends people to the website.
//
// The distinction is invisible from a refused teleport, which is where
// anybody meets it.  Measured on Agni on 2026-09-02, two avatars a
// moment apart to the same public region on the adult continent -- one
// arrived, and the other got
//
//	RegionTPAccessBlocked: "You aren't allowed in that Region due to
//	your maturity Rating. You may need to validate your age and/or
//	install the latest Viewer. ..."
//
// which names both causes and picks neither.  So this command exists to
// tell them apart: ask for adult, and what comes back is the answer.
// Granted adult, and the preference was the problem and is now fixed.
// Granted something lower, and that is the ceiling, and the rest of the
// job is on a web page.
//
// The refused avatar above was granted adult and made the same journey
// a minute later, so the first half is measured.  The second half --
// a grant lower than the request -- is not: neither account this has
// run against was capped.
//
// # Why there is no reading without asking
//
// The preference arrives in the login response, which the daemon holds
// and does not pass on, so the only way this shell can learn it is to
// ask the capability -- and the capability's question is "set it to
// this".  A bare "maturity" therefore sets the preference to the one
// thing that is always allowed and always a no-op if it is already in
// force: it asks for the rating the account already has, which is not
// something this knows, so it asks for GENERAL.
//
// That would be a lie in the other direction -- it would LOWER a
// preference somebody had set higher -- so it is not what happens.  A
// bare "maturity" says what it cannot do and points at the two forms
// that do something, and that is the honest shape until the login
// response's own fields are plumbed through the daemon.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/quark-idlemind/slgo/sl"
)

var maturityCommands = map[string]*command{
	"maturity": {
		params: "RATING",
		flags:  func() any { return new(helpOnly) },
		brief:  "ask to be shown land rated general, moderate or adult; the grid answers with what it granted",
		man:    "maturity",
		run:    cmdMaturity,
	},
}

// cmdMaturity asks for a maturity preference and prints what came back.
//
// What is printed is the grant and not the request, and the two are
// said apart where they differ: the difference is the account's ceiling
// showing itself, and it is the one thing this command exists to make
// visible.
func cmdMaturity(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o helpOnly
	args, done, err := subOptions("maturity", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) == 0 {
		return usageError("maturity", "which rating to ask for: general, moderate or adult; "+
			"nothing here can read the setting without asking for one, since the "+
			"capability's only question is \"set it to this\"")
	}

	want, err := sl.ParseMaturity(strings.Join(args, " "))
	if err != nil {
		return err
	}

	got, err := sh.s.SetMaturity(ctx, want)
	if err != nil {
		return err
	}

	if got == want {
		fmt.Fprintf(out, "this avatar is shown land rated %s and below\n", sl.MaturityName(got))
		return nil
	}
	// Lower than was asked for, which is the account's ceiling and not
	// a failure of this command.  Said in the terms somebody can act
	// on: what they have, and that the rest is not here.
	fmt.Fprintf(out, "asked for %s and was granted %s\n",
		sl.MaturityName(want), sl.MaturityName(got))
	fmt.Fprintf(out, "%s is as high as this account may go, which is age verification "+
		"rather than a setting: no client can raise it, and it is changed on the "+
		"account page and nowhere else\n", sl.MaturityName(got))
	return nil
}
