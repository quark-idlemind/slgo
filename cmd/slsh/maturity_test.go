package main

// What the maturity command prints, which is the grant and not the
// request.
//
// The two are the same on an account that may have what it asked for
// and different on one that may not, and the difference is the whole
// reason this command exists: a refused teleport names two possible
// causes -- the preference and the account's age verification -- and
// picks neither.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/sl"
)

// answerMaturity makes the fake's capability grant a rating, whatever
// was asked for, and says what the request carried.
func answerMaturity(t *testing.T, x *testShell, grant string) *string {
	t.Helper()
	asked := new(string)
	x.grid.ServeCap(t, sl.MaturityCap, func(w http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		*asked = string(b)
		w.Header().Set("Content-Type", "application/llsd+xml")
		w.Write([]byte(`<llsd><map><key>access_prefs</key><map><key>max</key>` +
			`<string>` + grant + `</string></map></map></llsd>`))
	})
	return asked
}

// TestMaturityGrantedWhatItAskedForSaysSoOnce.
func TestMaturityGrantedWhatItAskedForSaysSoOnce(t *testing.T) {
	x := newTestShell(t)
	asked := answerMaturity(t, x, sl.MaturityAdult)

	got := x.do(t, "maturity adult")
	if !strings.Contains(got, "shown land rated adult and below") {
		t.Errorf("maturity printed %q", got)
	}
	if strings.Contains(got, "as high as this account may go") {
		t.Errorf("a granted request was reported as a ceiling: %q", got)
	}
	if !strings.Contains(*asked, sl.MaturityAdult) {
		t.Errorf("the request carried %q", *asked)
	}
}

// TestMaturityGrantedLessSaysWhichHalfIsTheProblem.
//
// This is the case the command is for.  What came back is the
// account's ceiling, and the line has to say that it is not a setting
// anything here can change -- otherwise the next thing somebody does
// is type the command again.
func TestMaturityGrantedLessSaysWhichHalfIsTheProblem(t *testing.T) {
	x := newTestShell(t)
	answerMaturity(t, x, sl.MaturityModerate)

	got := x.do(t, "maturity adult")
	if !strings.Contains(got, "asked for adult and was granted moderate") {
		t.Errorf("maturity printed %q", got)
	}
	if !strings.Contains(got, "as high as this account may go") {
		t.Errorf("the ceiling was not named as one: %q", got)
	}
	if !strings.Contains(got, "account page") {
		t.Errorf("nothing says where it can be changed: %q", got)
	}
}

// TestABareMaturitySaysBothNumbersAndAsksNothing.
//
// "This avatar is shown moderate and below, and this account may go as
// high as adult" is the sentence somebody puzzling over a refused
// teleport wants, and it used to be unsayable: the capability's only
// question is "set it to this", and the one rating safe to ask for is
// general, which would quietly lower a preference somebody had set
// higher.  So the numbers come from the presence, and nothing is sent.
func TestABareMaturitySaysBothNumbersAndAsksNothing(t *testing.T) {
	x := newTestShell(t)
	asked := answerMaturity(t, x, sl.MaturityGeneral)
	x.grid.presence.MaturityPreference = sl.MaturityModerate
	x.grid.presence.MaturityCeiling = sl.MaturityAdult

	got := x.do(t, "maturity")
	if !strings.Contains(got, "shown land rated moderate and below, and this account may go as high as adult") {
		t.Errorf("a bare maturity printed %q", got)
	}
	if *asked != "" {
		t.Errorf("a bare maturity set the preference to %q", *asked)
	}
}

// TestABareMaturityAtTheCeilingSaysThereIsNoHigher: an avatar already
// shown all it may be is the case where asking for more is pointless,
// and the line says so rather than leaving somebody to try.
func TestABareMaturityAtTheCeilingSaysThereIsNoHigher(t *testing.T) {
	x := newTestShell(t)
	x.grid.presence.MaturityPreference = sl.MaturityModerate
	x.grid.presence.MaturityCeiling = sl.MaturityModerate

	got := x.do(t, "maturity")
	if !strings.Contains(got, "shown land rated moderate and below, which is as high as this account may go") {
		t.Errorf("a bare maturity printed %q", got)
	}
}

// TestABareMaturityToldNothingDoesNotSayGeneral.
//
// A daemon built before these were passed on, or a login response
// without them, leaves both empty.  Printed as general, empty would
// tell somebody their avatar will be refused land it would be let onto;
// printed as anything, an empty ceiling is an account's age
// verification made up.  Each missing half is said to be missing, and
// still nothing is asked for to fill it.
func TestABareMaturityToldNothingDoesNotSayGeneral(t *testing.T) {
	x := newTestShell(t)
	asked := answerMaturity(t, x, sl.MaturityGeneral)

	got := x.do(t, "maturity")
	if !strings.Contains(got, "did not say") || strings.Contains(got, "general") {
		t.Errorf("a bare maturity told nothing printed %q", got)
	}

	x.grid.presence.MaturityPreference = sl.MaturityAdult
	got = x.do(t, "maturity")
	if !strings.Contains(got, "shown land rated adult and below; the login did not say how high") {
		t.Errorf("a bare maturity told only the preference printed %q", got)
	}

	x.grid.presence.MaturityPreference = ""
	x.grid.presence.MaturityCeiling = sl.MaturityAdult
	got = x.do(t, "maturity")
	if !strings.Contains(got, "may go as high as adult; the login did not say what this avatar is shown") {
		t.Errorf("a bare maturity told only the ceiling printed %q", got)
	}

	if *asked != "" {
		t.Errorf("a bare maturity set the preference to %q", *asked)
	}
}

// TestMaturityRefusesAWordTheGridWouldNotKnow, before anything is sent.
func TestMaturityRefusesAWordTheGridWouldNotKnow(t *testing.T) {
	x := newTestShell(t)
	asked := answerMaturity(t, x, sl.MaturityAdult)

	if got := x.do(t, "maturity adultish"); !strings.Contains(got, "not a maturity rating") {
		t.Errorf("maturity printed %q", got)
	}
	if *asked != "" {
		t.Errorf("a rating nobody has heard of was sent as %q", *asked)
	}
}
