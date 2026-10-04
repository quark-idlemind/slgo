package server

// What the server says about an endpoint it does not own.
//
// The endpoint itself is cmd/slgod's, and its tests are there.  What is
// pinned here is the seam: that a daemon which serves no viewer logins
// -- the ordinary one -- answers so in a way a client can act on, and
// that a minting call is refused rather than half answered.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// errNoViewerPassword is the refusal the real endpoint gives for a
// profile that was never set up to be handed over, in miniature.
var errNoViewerPassword = errors.New("example has no viewer_password, so it cannot be handed to a viewer")

// fakeViewer is an endpoint with nothing behind it.
type fakeViewer struct {
	uri      string
	attached bool

	minted string
	life   time.Duration
	err    error

	mintedFor []string
}

func (f *fakeViewer) LoginURI() string             { return f.uri }
func (f *fakeViewer) Attached(profile string) bool { return f.attached }

func (f *fakeViewer) Mint(profile string) (string, time.Duration, error) {
	f.mintedFor = append(f.mintedFor, profile)
	return f.minted, f.life, f.err
}

// TestADaemonWithNoViewerEndpointSaysSoAndSaysHow: -viewer is not the
// default and the daemon here runs without it, so this is the ordinary
// answer rather than an edge.  An empty address with nothing beside it
// would leave somebody unable to tell "no endpoint" from "an endpoint
// nobody is on", which are two different problems with two different
// answers.
func TestADaemonWithNoViewerEndpointSaysSoAndSaysHow(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()

	st, err := r.srv.Status(ctx, &pb.StatusRequest{Agent: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if st.GetViewer() == nil {
		t.Fatal("no viewer field at all; a client cannot tell that from a daemon that never answers")
	}
	if uri := st.GetViewer().GetLoginUri(); uri != "" {
		t.Errorf("login uri = %q on a daemon started without -viewer", uri)
	}

	_, err = r.srv.ViewerCredential(ctx, &pb.ViewerCredentialRequest{Agent: "example"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("minting on a daemon with no endpoint: %v; want FailedPrecondition", err)
	}
	if !strings.Contains(errText(err), "-viewer") {
		t.Errorf("the refusal does not say how to get an endpoint: %v", err)
	}
}

// TestTheEndpointAndWhoIsOnItTravelWithTheStatus: the address appeared
// in one line of the daemon's log and nowhere else, so a shell attached
// afterwards could not ask.  This is that answer crossing.
func TestTheEndpointAndWhoIsOnItTravelWithTheStatus(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()
	v := &fakeViewer{uri: "http://127.0.0.1:9000/"}
	r.srv.SetViewer(v)

	st, err := r.srv.Status(ctx, &pb.StatusRequest{Agent: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if got := st.GetViewer().GetLoginUri(); got != v.uri {
		t.Errorf("login uri = %q, want %q", got, v.uri)
	}
	if st.GetViewer().GetAttached() {
		t.Error("a viewer is attached when none has logged in")
	}

	v.attached = true
	st, err = r.srv.Status(ctx, &pb.StatusRequest{Agent: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if !st.GetViewer().GetAttached() {
		t.Error("a viewer that has taken the session is not reported")
	}
}

// TestAMintedCredentialCarriesTheAvatarsNameAndTheAddressItIsGoodFor:
// a viewer's login box wants a name, and the profile name slgod files a
// session under is not one.  The address travels with the password so
// that a client cannot pair a credential with an endpoint it read at
// some other moment.
func TestAMintedCredentialCarriesTheAvatarsNameAndTheAddressItIsGoodFor(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()
	v := &fakeViewer{uri: "http://127.0.0.1:9000/", minted: "0123456789abcdef", life: time.Minute}
	r.srv.SetViewer(v)

	got, err := r.srv.ViewerCredential(ctx, &pb.ViewerCredentialRequest{Agent: "example"})
	if err != nil {
		t.Fatal(err)
	}
	if got.GetFirst() != "Example" || got.GetLast() != "Resident" {
		t.Errorf("name = %q %q, want the avatar's", got.GetFirst(), got.GetLast())
	}
	if got.GetLoginUri() != v.uri {
		t.Errorf("login uri = %q, want %q", got.GetLoginUri(), v.uri)
	}
	if got.GetPassword() != v.minted {
		t.Errorf("password = %q, want what the endpoint minted", got.GetPassword())
	}
	if got.GetExpirySeconds() != 60 {
		t.Errorf("expiry = %ds, want the minute the endpoint gave", got.GetExpirySeconds())
	}
	// By profile, since that is what the endpoint files digests under.
	if len(v.mintedFor) != 1 || v.mintedFor[0] != "example" {
		t.Errorf("minted for %v, want the profile once", v.mintedFor)
	}
}

// TestARefusedMintingIsPassedOnWhole: the endpoint refuses for reasons
// a person can act on -- a profile with no viewer_password, above all
// -- and a refusal reworded on the way through is a refusal nobody can
// act on.
func TestARefusedMintingIsPassedOnWhole(t *testing.T) {
	r := newRig(t, agent.Caps{})
	ctx := context.Background()
	r.srv.SetViewer(&fakeViewer{
		uri: "http://127.0.0.1:9000/",
		err: errNoViewerPassword,
	})

	_, err := r.srv.ViewerCredential(ctx, &pb.ViewerCredentialRequest{Agent: "example"})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("a refused minting: %v; want FailedPrecondition", err)
	}
	if !strings.Contains(errText(err), errNoViewerPassword.Error()) {
		t.Errorf("the endpoint's reason did not survive: %v", err)
	}
}
