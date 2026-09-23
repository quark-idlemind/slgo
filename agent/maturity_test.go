package agent

// Which of the login response's maturity fields is which, and what
// becomes of the preference after the session asks to change it.
//
// Three fields in that response have maturity in their meaning and two
// of them are the ones wanted.  A mix-up between them does not fail
// anywhere: it prints a plausible rating, and the first sign of it is an
// avatar turned away from land the shell said it was allowed onto, or
// told it may not go somewhere it could.  So each test gives the three
// different values, and a reading of the wrong one shows.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// loggedInWith is an agent whose login response said these three.
func loggedInWith(access, regionAccess, accessMax string) *Agent {
	return &Agent{Account: &Account{Raw: map[string]any{
		"agent_access":        access,
		"agent_region_access": regionAccess,
		"agent_access_max":    accessMax,
	}}}
}

// grantMaturity is a capability that answers every request the same
// way, whatever it asked for.
func grantMaturity(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(hs.Close)
	return hs
}

// granted is a grant of this rating, in the shape
// UpdateAgentInformation answers with -- and asks in, which is the same.
func granted(rating string) string {
	return `<llsd><map><key>access_prefs</key><map><key>max</key><string>` +
		rating + `</string></map></map></llsd>`
}

// TestTheMaturityIsReadFromTheFieldsAViewerReads.
//
// agent_access is the field whose name says it is the answer, and it is
// the one that is not: it predates the adult rating, and a real login
// response carries M there beside an adult preference.  Read as the
// preference it would report moderate for an avatar shown adult.  The
// preference is agent_region_access and the ceiling agent_access_max,
// which is how the viewer reads them.
func TestTheMaturityIsReadFromTheFieldsAViewerReads(t *testing.T) {
	a := loggedInWith("M", "PG", "A")
	pref, ceiling := a.Maturity()
	if pref != "PG" || ceiling != "A" {
		t.Errorf("Maturity = %q, %q; want the preference PG and the ceiling A", pref, ceiling)
	}
}

// TestAnAgentNothingToldHasNoMaturity: an agent with no login response,
// or one without the fields, says nothing rather than general.  General
// is a real preference, and reporting it for an avatar shown adult is
// telling somebody their avatar will be refused land it would be let
// onto.
func TestAnAgentNothingToldHasNoMaturity(t *testing.T) {
	for _, a := range []*Agent{{}, {Account: &Account{}}} {
		if pref, ceiling := a.Maturity(); pref != "" || ceiling != "" {
			t.Errorf("an agent told nothing reports %q, %q", pref, ceiling)
		}
	}
}

// TestAGrantedPreferenceReplacesTheLogins.
//
// The preference is changed by a request whose answer is the only
// place the new one is ever stated.  A session that went on reporting
// the login's afterwards would say, a moment after being granted adult,
// that the avatar is shown general -- and would go on saying it for as
// long as it stayed logged in.  The ceiling is not the request's to
// change and stays as the login gave it.
func TestAGrantedPreferenceReplacesTheLogins(t *testing.T) {
	hs := grantMaturity(t, http.StatusOK, granted("M"))
	a := loggedInWith("M", "PG", "A")
	a.SetCaps(Caps{MaturityCap: hs.URL + "/cap/maturity"})

	resp, err := a.DoCap(context.Background(), CapRequest{
		Cap: MaturityCap, Method: "POST", Body: []byte(granted("A")),
	})
	if err != nil || !resp.OK() {
		t.Fatalf("DoCap = %v, %v", resp, err)
	}
	if pref, ceiling := a.Maturity(); pref != "M" || ceiling != "A" {
		t.Errorf("after a grant of M, Maturity = %q, %q", pref, ceiling)
	}
}

// TestAnAnswerThatGrantsNothingLeavesThePreferenceAlone: a refusal, or
// an answer nobody can read, says nothing about the preference now, and
// is not taken as a change to it.
func TestAnAnswerThatGrantsNothingLeavesThePreferenceAlone(t *testing.T) {
	for _, answer := range []struct {
		status int
		body   string
	}{
		{http.StatusInternalServerError, granted("A")},
		{http.StatusOK, `not llsd at all`},
		{http.StatusOK, `<llsd><map><key>access_prefs</key><map></map></map></llsd>`},
	} {
		hs := grantMaturity(t, answer.status, answer.body)
		a := loggedInWith("M", "PG", "A")
		a.SetCaps(Caps{MaturityCap: hs.URL + "/cap/maturity"})
		if _, err := a.DoCap(context.Background(), CapRequest{
			Cap: MaturityCap, Method: "POST", Body: []byte(granted("A")),
		}); err != nil {
			t.Fatal(err)
		}
		if pref, _ := a.Maturity(); pref != "PG" {
			t.Errorf("an answer of %d %q made the preference %q", answer.status, answer.body, pref)
		}
	}
}
