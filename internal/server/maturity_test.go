package server

// The maturity preference and ceiling, which the login response carries
// and nothing afterwards repeats.
//
// Only the daemon ever sees that response, so a client that wants
// either number has to be handed it -- and the one way a client had of
// learning them before, asking the capability, sets the preference as
// it answers.  These go the whole way: a login response decoded by the
// real login code, a session built from it, and the presence a client
// is given.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// TestThePresenceCarriesTheMaturityTheLoginGave, and then what a
// request through the daemon was granted.
//
// The login response carries three fields with maturity in their
// meaning, set here to three different values so that passing on the
// wrong one shows.  A daemon that passed on none leaves a client with
// no way to read either number short of asking for one; a daemon that
// kept the login's after a grant would report, a moment after granting
// adult, that the avatar is shown general.
func TestThePresenceCarriesTheMaturityTheLoginGave(t *testing.T) {
	sim := newSim(t)
	defer sim.close()

	login := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-8597-66b760a8cb5f</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-3bf6-2277c65663be</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-2a9a-c37f17e64c61</string></value></member>
		  <member><name>circuit_code</name><value><int>4322</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		  <member><name>agent_access</name><value><string>M</string></value></member>
		  <member><name>agent_region_access</name><value><string>PG</string></value></member>
		  <member><name>agent_access_max</name><value><string>A</string></value></member>
		</struct></value></param></params></methodResponse>`,
			sim.addr().IP, sim.addr().Port)
	}))
	defer login.Close()

	grant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<llsd><map><key>access_prefs</key><map>`+
			`<key>max</key><string>A</string></map></map></llsd>`)
	}))
	defer grant.Close()

	srv := New()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	h, err := srv.StartAgent(ctx, "example",
		agent.Login{First: "Example", Last: "Resident", Password: "x", URL: login.URL},
		agent.Options{Timeout: 10 * time.Second, SkipCaps: true, Idle: -1})
	if err != nil {
		t.Fatal(err)
	}

	p, err := srv.Presence(ctx, &pb.PresenceRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if p.GetMaturityPreference() != "PG" || p.GetMaturityCeiling() != "A" {
		t.Errorf("the presence says preference %q and ceiling %q; the login said PG and A",
			p.GetMaturityPreference(), p.GetMaturityCeiling())
	}

	// A client asks for adult through the daemon, and is granted it.
	h.Agent().SetCaps(agent.Caps{agent.MaturityCap: grant.URL + "/cap/maturity"})
	resp, err := srv.Cap(ctx, &pb.CapRequest{
		Cap: agent.MaturityCap, Method: "POST",
		Body: []byte(`<llsd><map><key>access_prefs</key><map>` +
			`<key>max</key><string>A</string></map></map></llsd>`),
	})
	if err != nil || resp.GetStatus() != http.StatusOK {
		t.Fatalf("Cap = %v, %v", resp, err)
	}

	if p, err = srv.Presence(ctx, &pb.PresenceRequest{}); err != nil {
		t.Fatal(err)
	}
	if p.GetMaturityPreference() != "A" || p.GetMaturityCeiling() != "A" {
		t.Errorf("after a grant of A the presence says preference %q and ceiling %q",
			p.GetMaturityPreference(), p.GetMaturityCeiling())
	}
}
