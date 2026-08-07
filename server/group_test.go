package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// counted is how many times the sim saw a message of that name.
func counted(sim *fakeSim, name string) int {
	n := 0
	for _, s := range sim.got() {
		if s == name {
			n++
		}
	}
	return n
}

// waitFor polls until f is true, and fails the test if it never is.
func waitFor(t *testing.T, within time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestReconnectRestoresTheActiveGroup is the regression test for a
// fault found in the field: an avatar that could build stopped being
// able to after its session was re-established.
//
// A reconnect is a fresh login, and a fresh login has NO active group,
// so a group settled once at startup is silently lost -- and the
// symptom is the simulator refusing to let the avatar rez, blaming the
// parcel.  Nothing says the daemon dropped something.
func TestReconnectRestoresTheActiveGroup(t *testing.T) {
	sim := newSim(t)
	defer sim.close()

	var logins atomic.Int64
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-9eeb-1bd0e1ec6995</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-f4f4-19d29d124acf</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-d169-d9847afe641e</string></value></member>
		  <member><name>circuit_code</name><value><int>%d</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`,
			4000+logins.Load(), sim.addr().IP, sim.addr().Port)
	}))
	defer hs.Close()

	saved := ReconnectDelays
	ReconnectDelays = []time.Duration{20 * time.Millisecond}
	defer func() { ReconnectDelays = saved }()

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := srv.Host(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}

	group := msg.MustParseUUID("33a57e57-7e57-c0de-e7ea-9cad48757549")
	if err := h.SetGroup(ctx, group); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, "the group to be activated", func() bool {
		return counted(sim, "ActivateGroup") >= 1
	})
	if got := h.Group(); got != group {
		t.Errorf("Group() = %v, want %v", got, group)
	}

	// The session dies the way a lost circuit does.
	first := h.Agent()
	first.Close()

	waitFor(t, 10*time.Second, "the session to be re-established", func() bool {
		a := h.Agent()
		return a != nil && a != first
	})

	// The whole point: the new session is acting as the group too.
	waitFor(t, 5*time.Second, "the group to be activated again", func() bool {
		return counted(sim, "ActivateGroup") >= 2
	})
	if got := h.Group(); got != group {
		t.Errorf("after reconnecting, Group() = %v, want %v", got, group)
	}
}

// TestNoGroupSendsNothing: no group is a legitimate answer -- it is
// what a login starts with -- and must not put an ActivateGroup for the
// zero uuid on the wire, which would be a request to leave whatever
// group the avatar is in.
func TestNoGroupSendsNothing(t *testing.T) {
	sim := newSim(t)
	defer sim.close()

	var logins atomic.Int64
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-9eeb-1bd0e1ec6995</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-f4f4-19d29d124acf</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-d169-d9847afe641e</string></value></member>
		  <member><name>circuit_code</name><value><int>4321</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`,
			sim.addr().IP, sim.addr().Port)
	}))
	defer hs.Close()

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := srv.Host(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}

	if err := h.SetGroup(ctx, msg.UUID{}); err != nil {
		t.Fatal(err)
	}
	// Nothing to wait for, so give it a moment to be wrong in.
	time.Sleep(200 * time.Millisecond)
	if n := counted(sim, "ActivateGroup"); n != 0 {
		t.Errorf("%d ActivateGroup sent for no group, want none", n)
	}
}
