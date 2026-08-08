package sl

// What the simulator says it supports.
//
// The reading of the answer is checked in syntax_test.go, where
// TestFeatures holds the accessors to a document built by hand.  What is
// here is the fetch: one http GET whose answers are otherwise only
// discoverable by trying something and seeing it refused, and which
// differs from region to region -- a beta grid region answers
// differently from a main grid one, which is the whole reason the
// capability exists.

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestFeaturesKeepsTheWholeAnswer: the set of keys is Linden Lab's to
// change and has grown several times, so a reader that only kept the
// ones this package names would silently lose whatever was added last.
func TestFeaturesKeepsTheWholeAnswer(t *testing.T) {
	w, f := newFakeSession(t)
	f.ServeCap(t, "SimulatorFeatures", func(rw http.ResponseWriter, r *http.Request) {
		fmt.Fprint(rw, `<llsd><map>`+
			`<key>MeshRezEnabled</key><boolean>1</boolean>`+
			`<key>MeshUploadEnabled</key><boolean>1</boolean>`+
			`<key>MaxAgentAttachments</key><integer>38</integer>`+
			`<key>SomethingNobodyHasHeardOf</key><string>and yet</string>`+
			`</map></llsd>`)
	})

	got, err := w.Features(context.Background())
	if err != nil {
		t.Fatalf("Features: %v", err)
	}
	if !got.MeshRezEnabled() || !got.MeshUploadEnabled() {
		t.Errorf("mesh reads as %v/%v", got.MeshRezEnabled(), got.MeshUploadEnabled())
	}
	if got.MaxAgentAttachments() != 38 {
		t.Errorf("MaxAgentAttachments = %d", got.MaxAgentAttachments())
	}
	if got.String("SomethingNobodyHasHeardOf") != "and yet" {
		t.Error("a key this package does not name was dropped")
	}
	// A region that mentions no syntax id is a region whose cached
	// syntax cannot be checked, which is not an error.
	if !got.LSLSyntaxID().IsZero() {
		t.Errorf("LSLSyntaxID = %s from an answer that named none", got.LSLSyntaxID())
	}
}

// TestFeaturesSaysWhenTheAnswerWasNotOne: this is asked before deciding
// whether something is worth trying at all, so an unreadable answer must
// not read as a region that supports nothing.
func TestFeaturesSaysWhenTheAnswerWasNotOne(t *testing.T) {
	t.Run("no capability to ask", func(t *testing.T) {
		w, _ := newFakeSession(t)
		if _, err := w.Features(context.Background()); err == nil {
			t.Error("Features answered for a session that never got the capability")
		}
	})

	t.Run("an answer that is not LLSD", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.ServeCap(t, "SimulatorFeatures", func(rw http.ResponseWriter, r *http.Request) {
			fmt.Fprint(rw, "not llsd at all")
		})
		if _, err := w.Features(context.Background()); err == nil {
			t.Error("Features parsed something that is not a document")
		}
	})

	t.Run("LLSD that is not a map", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.ServeCap(t, "SimulatorFeatures", func(rw http.ResponseWriter, r *http.Request) {
			fmt.Fprint(rw, `<llsd><array><string>x</string></array></llsd>`)
		})
		_, err := w.Features(context.Background())
		if err == nil || !strings.Contains(err.Error(), "wanted a map") {
			t.Errorf("Features = %v, want it to say what it got", err)
		}
	})
}
