package sl

// Fetching an asset's bytes over http.
//
// Which types the content delivery network will serve is the whole
// question here, and it is checked in list_test.go, where
// TestAssetRefusesWhatTheNetworkWillNot holds the refusals.  What is
// here is the request itself: the network serves by asset id, so an
// asset whose id is known can be read without knowing which item it
// belongs to, which inventory it is in, or whether it is in one at all
// -- which is what makes it the right call for a texture on somebody
// else's prim.

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// TestAnAssetIsAskedForByIdAndTypeInTheQuery: the url is the capability
// with <name>_id=<uuid> on it, so a type with no name in the protocol
// cannot be asked for -- and asking under the wrong name gets a 404 for
// an asset that is there.
func TestAnAssetIsAskedForByIdAndTypeInTheQuery(t *testing.T) {
	w, f := newFakeSession(t)
	asked := make(chan string, 4)
	f.ServeCap(t, AssetCap, func(rw http.ResponseWriter, r *http.Request) {
		asked <- r.URL.RequestURI()
		rw.Write([]byte("JPEG2000 or so"))
	})

	body, err := w.Texture(context.Background(), theOther)
	if err != nil {
		t.Fatalf("Texture: %v", err)
	}
	if string(body) != "JPEG2000 or so" {
		t.Errorf("the asset came back as %q", body)
	}
	got := <-asked
	if !strings.Contains(got, "texture_id=") || !strings.Contains(got, theOther.String()) {
		t.Errorf("asked for %q", got)
	}

	// A type of its own, to show the name in the query is the type's
	// and not always "texture".
	if _, err := w.Asset(context.Background(), theOther, AssetAnimation); err != nil {
		t.Fatalf("Asset: %v", err)
	}
	if got := <-asked; !strings.Contains(got, "animatn_id=") {
		t.Errorf("asked for an animation as %q", got)
	}
}

// TestAnAssetNeedsAnIdAndACapability: the id is what the network serves
// by, and a session that never asked for the capability has nowhere to
// send the request -- which is a different problem from an asset that is
// not there.
func TestAnAssetNeedsAnIdAndACapability(t *testing.T) {
	w, f := newFakeSession(t)

	if _, err := w.Asset(context.Background(), msgZero(), AssetTexture); err == nil {
		t.Error("Asset fetched something with no id")
	}
	if _, err := w.Texture(context.Background(), theOther); err == nil {
		t.Error("Texture fetched from a capability the session does not have")
	}

	// There, and refusing: the status is what the caller has to see.
	f.ServeCap(t, AssetCap, func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(404)
	})
	_, err := w.Texture(context.Background(), theOther)
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("Texture = %v, want it to say what came back", err)
	}
}

// TestATypeWithNoNameCannotBeAskedFor: the url is built out of the
// type's protocol name, so a type that has none has no query to put in
// it -- and a request built from an empty name asks the network for
// "_id=", which answers as though the asset were missing.
func TestATypeWithNoNameCannotBeAskedFor(t *testing.T) {
	w, _ := newFakeSession(t)
	unknown := AssetType(99)
	if got := unknown.String(); !strings.Contains(got, "99") {
		t.Errorf("String = %q, want it to name the number", got)
	}
	_, err := w.Asset(context.Background(), theOther, unknown)
	if err == nil || !strings.Contains(err.Error(), "no name in the protocol") {
		t.Errorf("Asset = %v, want it to say the type cannot be asked for", err)
	}
}
