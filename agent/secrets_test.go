package agent

// Capability URLs in errors.  Every one of them is a bearer credential,
// and Go's HTTP client writes the whole URL it was asked for into every
// error it returns; these errors are what a daemon logs when a login
// has to be retried or a session ends.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// secretPath is the part of a capability URL that is the credential.
// Invented; it has the shape of one.
const secretPath = "/cap/5b747e57-7e57-c0de-bd47-c17eb0d6747f"

// deadURL is the address of a server that has gone, so that a request
// to it fails in the client rather than with an answer.
func deadURL(t *testing.T) string {
	t.Helper()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close()
	return dead.URL
}

// TestAFailedSeedRequestDoesNotCarryTheSeed: the seed is the credential
// every other capability is got with.  A failure to reach it is a login
// to retry, and slgod logs the reason -- so the log held the seed of
// every login that failed on it.  The host stays, because it says which
// simulator would not answer.
func TestAFailedSeedRequestDoesNotCarryTheSeed(t *testing.T) {
	t.Parallel()
	base := deadURL(t)
	_, err := RequestCaps(context.Background(), base+secretPath, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secretPath) {
		t.Errorf("the error names the seed: %v", err)
	}
	if !strings.Contains(err.Error(), strings.TrimPrefix(base, "http://")) {
		t.Errorf("the error lost the host as well: %v", err)
	}

	// A refusal that quotes the URL it refused is cut the same way.
	hs := seedServer(t, "no such capability: https://sim.example.net"+secretPath, http.StatusNotFound)
	_, err = RequestCaps(context.Background(), hs.URL+secretPath, nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secretPath) {
		t.Errorf("the error quotes the seed from the refusal: %v", err)
	}
}

// TestAFailedCapRequestDoesNotCarryTheURL: a capability request's error
// goes wherever its caller sends it, and the event queue's is the reason
// a session ends, which slgod logs.  Both are cut to the host, and still
// say which capability it was.
func TestAFailedCapRequestDoesNotCarryTheURL(t *testing.T) {
	t.Parallel()
	base := deadURL(t)
	a := capAgent(Caps{"FetchInventory2": base + secretPath})

	_, err := a.DoCap(context.Background(), CapRequest{Cap: "FetchInventory2", Method: "POST"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secretPath) {
		t.Errorf("the capability request's error names the URL: %v", err)
	}
	if !strings.Contains(err.Error(), "FetchInventory2") {
		t.Errorf("the error no longer says which capability failed: %v", err)
	}

	_, _, err = a.postEventQueue(context.Background(), base+secretPath, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secretPath) {
		t.Errorf("the event queue's error names the URL: %v", err)
	}
}
