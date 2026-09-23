package sl

// Setting the maturity preference, and telling the two numbers apart.
//
// The whole point of this call is the ANSWER: what the grid granted,
// which may be lower than what was asked for and is the only way
// anything here learns the account's ceiling.  So every test is about
// what came back rather than what went out.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// grantedAccess is the capability's answer, in the shape it comes in.
func grantedAccess(max string) string {
	return `<llsd><map><key>access_prefs</key><map>` +
		`<key>max</key><string>` + max + `</string></map></map></llsd>`
}

// TestSetMaturityAsksForWhatWasWantedAndReportsWhatWasGranted.
func TestSetMaturityAsksForWhatWasWantedAndReportsWhatWasGranted(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	var asked string
	f.ServeCap(t, MaturityCap, func(rw http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		asked = string(b)
		rw.Write([]byte(grantedAccess(MaturityAdult)))
	})

	got, err := w.SetMaturity(context.Background(), "adult")
	if err != nil {
		t.Fatalf("SetMaturity: %v", err)
	}
	if got != MaturityAdult {
		t.Errorf("SetMaturity = %q, want %q", got, MaturityAdult)
	}
	if !strings.Contains(asked, "access_prefs") || !strings.Contains(asked, MaturityAdult) {
		t.Errorf("the request was %q", asked)
	}
}

// TestSetMaturityReportsACeilingRatherThanTheRequest.
//
// An account that may not go adult is granted something lower, and the
// answer is the only place that shows.  Handing the caller back what it
// asked for would leave it believing an avatar may go somewhere it will
// be refused from -- and the refusal arrives much later, on a teleport,
// naming two possible causes and neither of them this.
func TestSetMaturityReportsACeilingRatherThanTheRequest(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.ServeCap(t, MaturityCap, func(rw http.ResponseWriter, r *http.Request) {
		rw.Write([]byte(grantedAccess(MaturityModerate)))
	})

	got, err := w.SetMaturity(context.Background(), "adult")
	if err != nil {
		t.Fatalf("SetMaturity: %v", err)
	}
	if got != MaturityModerate {
		t.Errorf("SetMaturity = %q, and the grid granted %q", got, MaturityModerate)
	}
}

// TestSetMaturityWillNotReadAnAnswerItDoesNotUnderstand: an answer that
// does not carry a rating is an error, never the rating that was asked
// for.
func TestSetMaturityWillNotReadAnAnswerItDoesNotUnderstand(t *testing.T) {
	t.Parallel()
	for _, body := range []string{
		`<llsd><map><key>access_prefs</key><map></map></map></llsd>`,
		`<llsd><map><key>something_else</key><string>A</string></map></llsd>`,
		`<llsd><string>A</string></llsd>`,
		`not llsd at all`,
	} {
		w, f := newFakeSession(t)
		f.ServeCap(t, MaturityCap, func(rw http.ResponseWriter, r *http.Request) {
			rw.Write([]byte(body))
		})
		if got, err := w.SetMaturity(context.Background(), "adult"); err == nil {
			t.Errorf("%q was read as the rating %q", body, got)
		}
	}
}

// TestSetMaturityRefusesARatingTheGridWouldNotKnow, before any request
// goes out: the set is Linden Lab's three and a fourth word is a typo,
// which is worth saying rather than sending.
func TestSetMaturityRefusesARatingTheGridWouldNotKnow(t *testing.T) {
	t.Parallel()
	w, _ := newFakeSession(t)
	if _, err := w.SetMaturity(context.Background(), "adultish"); err == nil {
		t.Error("a rating nobody has heard of was sent")
	}
}

// TestParseMaturityTakesBothVocabularies: the words the interface uses
// and the letters the protocol does, since a person reading "regions"
// has seen the words and a person reading this package has seen the
// letters.
func TestParseMaturityTakesBothVocabularies(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ in, want string }{
		{"general", MaturityGeneral}, {"PG", MaturityGeneral}, {"g", MaturityGeneral},
		{"moderate", MaturityModerate}, {"Mature", MaturityModerate}, {"m", MaturityModerate},
		{"adult", MaturityAdult}, {"A", MaturityAdult}, {" Adult ", MaturityAdult},
	} {
		got, err := ParseMaturity(c.in)
		if err != nil || got != c.want {
			t.Errorf("ParseMaturity(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"", "x", "everything", "18+"} {
		if got, err := ParseMaturity(bad); err == nil {
			t.Errorf("ParseMaturity(%q) = %q, want a refusal", bad, got)
		}
	}
}

// TestAHostedPresenceCarriesBothMaturities: they cross from the daemon
// as two strings side by side, and a translation that crossed them over
// would print the ceiling as the preference -- which reads as an avatar
// shown adult when it has only asked for moderate, and is found out on a
// refused teleport.
func TestAHostedPresenceCarriesBothMaturities(t *testing.T) {
	t.Parallel()
	h, d := newFakeDaemon(t)
	d.presence = &pb.PresenceResponse{
		Region:             "Test Region",
		MaturityPreference: MaturityModerate,
		MaturityCeiling:    MaturityAdult,
	}
	p, err := h.Presence(context.Background(), 0)
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.MaturityPreference != MaturityModerate || p.MaturityCeiling != MaturityAdult {
		t.Errorf("the daemon said M and A, and the presence says %q and %q",
			p.MaturityPreference, p.MaturityCeiling)
	}
}

// TestADirectPresenceReadsTheMaturityOffItsOwnLogin: a direct session
// holds the login response itself, and has to hand on the same two
// numbers a daemon would, or everything above it would have to know
// which kind of session it was given.
func TestADirectPresenceReadsTheMaturityOffItsOwnLogin(t *testing.T) {
	d := aDirectSession(t)
	d.a = &agent.Agent{Account: &agent.Account{Raw: map[string]any{
		"agent_region_access": MaturityModerate,
		"agent_access_max":    MaturityAdult,
	}}}
	p, err := d.Presence(context.Background(), 0)
	if err != nil {
		t.Fatalf("Presence: %v", err)
	}
	if p.MaturityPreference != MaturityModerate || p.MaturityCeiling != MaturityAdult {
		t.Errorf("the login said M and A, and the presence says %q and %q",
			p.MaturityPreference, p.MaturityCeiling)
	}
}
