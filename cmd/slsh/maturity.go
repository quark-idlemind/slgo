package main

// What this avatar is willing to be shown, which decides where it may
// go.
//
//	maturity                 the preference in force, and the account's ceiling
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
// tell them apart, and does it two ways.  A bare "maturity" prints both
// numbers as the login response gave them.  And asking for adult makes
// the grid say it: granted adult, and the preference was the problem
// and is now fixed; granted something lower, and that is the ceiling,
// and the rest of the job is on a web page.
//
// The refused avatar above was granted adult and made the same journey
// a minute later, so the first half is measured.  The second half --
// a grant lower than the request -- is not: neither account this has
// run against was capped.
//
// # Reading it without asking
//
// The capability behind the set form has one question, "set it to
// this", so it cannot be what a bare "maturity" uses: the only rating
// always safe to ask for is general, and asking for it would quietly
// LOWER a preference somebody had set higher.  Both numbers arrive in
// the login response instead, which the daemon hands on with the
// presence, and a bare "maturity" prints them and asks nothing.
//
// What it prints is what the daemon was last told, and there is a gap
// in that which is worth knowing about: a preference changed through
// this shell is followed, but one changed on the account's web page or
// in a viewer attached to the session is not, because neither passes
// through the daemon.  See agent.Maturity.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/quark-idlemind/slgo/sl"
)

var maturityCommands = map[string]*command{
	"maturity": {
		params: "[RATING]",
		flags:  func() any { return new(helpOnly) },
		brief:  "what land this avatar is shown and may be; with a RATING, ask for that one and print what was granted",
		man:    "maturity",
		run:    cmdMaturity,
	},
}

// cmdMaturity asks for a maturity preference and prints what came back,
// or with no rating prints the one in force and the account's ceiling.
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
		return showMaturity(ctx, sh, out)
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

// showMaturity says what this avatar is shown and how high it may go,
// which is the sentence somebody puzzling over a refused teleport wants
// and could not previously be given without asking for adult and
// reading the refusal.
//
// Either half can be missing, and each is said to be missing rather
// than filled in.  An empty preference printed as general would tell
// somebody their avatar will be turned away from land it would have
// been let onto; an empty ceiling printed as anything at all would be
// an account's age verification made up.
func showMaturity(ctx context.Context, sh *Shell, out io.Writer) error {
	p, err := sh.s.Where(ctx)
	if err != nil {
		return err
	}
	pref, ceiling := p.MaturityPreference, p.MaturityCeiling
	switch {
	case pref != "" && ceiling == pref:
		fmt.Fprintf(out, "this avatar is shown land rated %s and below, "+
			"which is as high as this account may go\n", sl.MaturityName(pref))
	case pref != "" && ceiling != "":
		fmt.Fprintf(out, "this avatar is shown land rated %s and below, "+
			"and this account may go as high as %s\n",
			sl.MaturityName(pref), sl.MaturityName(ceiling))
	case pref != "":
		fmt.Fprintf(out, "this avatar is shown land rated %s and below; "+
			"the login did not say how high this account may go\n", sl.MaturityName(pref))
	case ceiling != "":
		fmt.Fprintf(out, "this account may go as high as %s; "+
			"the login did not say what this avatar is shown now\n", sl.MaturityName(ceiling))
	default:
		// Nothing is asked for to fill the gap: the set form is named
		// as the way to learn it, and it is not taken on anybody's
		// behalf.
		fmt.Fprintf(out, "the daemon did not say what this avatar is shown or how high "+
			"it may go: the login did not carry them, or the daemon was built before "+
			"it passed them on; \"maturity RATING\" asks for one and prints what was granted\n")
	}
	return nil
}
