package redact

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The capability path below is invented; it has the shape of one, which
// is all these tests need.
const (
	capPath = "/cap/5b747e57-7e57-c0de-bd47-c17eb0d6747f"
	capURL  = "https://sim.example.net:12043" + capPath
	session = "8d1b7e57-7e57-c0de-3bf6-2277c65663be"
)

// unredacted turns redaction off for one test and back on after it, so that a
// test that fails half way does not leave the rest reading full values.
func unredacted(t *testing.T) {
	t.Helper()
	SetFull(true)
	t.Cleanup(func() { SetFull(false) })
}

// TestAnIdIsCutToWhatTellsItApart: the default keeps the first eight
// characters, which tells the sessions of one daemon apart, and marks
// the cut so that nobody takes the stub for a whole id.
func TestAnIdIsCutToWhatTellsItApart(t *testing.T) {
	if got, want := ID(session), "8d1b7e57..."; got != want {
		t.Errorf("ID = %q, want %q", got, want)
	}
	// Something already short enough is not lengthened by the marker.
	if got := ID("abc"); got != "abc" {
		t.Errorf("ID(short) = %q", got)
	}
	unredacted(t)
	if got := ID(session); got != session {
		t.Errorf("with redaction off, ID = %q, want it whole", got)
	}
}

// TestASecretIsNotWrittenAtAll: a circuit code is too short for a
// prefix of it to mean anything but a guess halved.
func TestASecretIsNotWrittenAtAll(t *testing.T) {
	if got := Secret(690139535); got != Hidden {
		t.Errorf("Secret = %q, want %q", got, Hidden)
	}
	unredacted(t)
	if got := Secret(690139535); got != "690139535" {
		t.Errorf("with redaction off, Secret = %q", got)
	}
}

// TestACapabilityURLKeepsItsHostAndLosesItsPath: the host says which
// simulator it was; the path is the credential.
func TestACapabilityURLKeepsItsHostAndLosesItsPath(t *testing.T) {
	for in, want := range map[string]string{
		capURL:                           "https://sim.example.net:12043/...",
		"https://sim.example.net":        "https://sim.example.net",
		"https://sim.example.net/?t=abc": "https://sim.example.net/...",
		"not a url at all":               Hidden,
		"://nonsense":                    Hidden,
	} {
		if got := URL(in); got != want {
			t.Errorf("URL(%q) = %q, want %q", in, got, want)
		}
	}
	unredacted(t)
	if got := URL(capURL); got != capURL {
		t.Errorf("with redaction off, URL = %q", got)
	}
}

// TestAnHTTPErrorLosesTheURLItWasAbout is the case the package exists
// for: Go's client writes the whole URL into its error, wrapped however
// deep, and that error is what a failed login's retry line logs.
func TestAnHTTPErrorLosesTheURLItWasAbout(t *testing.T) {
	made := func() error {
		return fmt.Errorf("agent: seed capability: %w",
			&url.Error{Op: http.MethodPost, URL: capURL, Err: errors.New("connection refused")})
	}

	err := Error(made())
	if strings.Contains(err.Error(), capPath) {
		t.Errorf("the error still names the capability: %v", err)
	}
	if !strings.Contains(err.Error(), "sim.example.net") || !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("the error lost more than the path: %v", err)
	}
	var ue *url.Error
	if !errors.As(err, &ue) {
		t.Error("the error is no longer a *url.Error; a caller testing for one would stop finding it")
	}
	if Error(nil) != nil {
		t.Error("Error(nil) is not nil")
	}
	// And the same error caught before anything wrapped it, which is
	// where the callers here catch it.
	bare := Error(&url.Error{Op: http.MethodGet, URL: capURL, Err: errors.New("EOF")})
	if strings.Contains(bare.Error(), capPath) {
		t.Errorf("the bare error still names the capability: %v", bare)
	}

	unredacted(t)
	if err := Error(made()); !strings.Contains(err.Error(), capPath) {
		t.Errorf("with redaction off the error lost its URL: %v", err)
	}
}

// TestTextCutsAURLWhereverItIs: a refusal's body is the simulator's
// words, and a URL in them is as much a credential as one in an error.
func TestTextCutsAURLWhereverItIs(t *testing.T) {
	in := `no such cap "` + capURL + `" here; see http://other.example.org` + capPath + ` and httpd`
	got := Text(in)
	if strings.Contains(got, capPath) {
		t.Errorf("Text = %q, still holds the path", got)
	}
	want := `no such cap "https://sim.example.net:12043/..." here; see http://other.example.org/... and httpd`
	if got != want {
		t.Errorf("Text = %q\nwant   %q", got, want)
	}
	unredacted(t)
	if got := Text(in); got != in {
		t.Errorf("with redaction off, Text = %q", got)
	}
}
