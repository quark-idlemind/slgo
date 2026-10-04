package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/internal/xmlrpc"
)

// TestViewerOptionsMatchTheCapture holds the list against the request a
// real viewer sent.
//
// The list cannot be derived and cannot be checked at run time: a login
// server answers what it was asked and says nothing about what it was
// not, so a block left out comes back missing rather than wrong, and a
// viewer given the result fails somewhere else entirely.  The capture is
// the only thing that knows, so a newer viewer asking for more should
// fail here rather than in a grey screen months later.
func TestViewerOptionsMatchTheCapture(t *testing.T) {
	f, err := os.Open("testdata/firestorm-login.xml")
	if err != nil {
		t.Fatalf("testdata/firestorm-login.xml is tracked and must be readable: %v", err)
	}
	defer f.Close()

	_, params, err := xmlrpc.DecodeCall(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := params["options"].([]any)
	if !ok {
		t.Fatalf("options is %T", params["options"])
	}
	want := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("option %v is %T, want a string", v, v)
		}
		want = append(want, s)
	}

	if len(ViewerOptions) != len(want) {
		t.Fatalf("ViewerOptions has %d entries, the viewer asked for %d\nours:   %v\ntheirs: %v",
			len(ViewerOptions), len(want), ViewerOptions, want)
	}
	for i := range want {
		if ViewerOptions[i] != want[i] {
			t.Errorf("option %d is %q, the viewer asked for %q",
				i, ViewerOptions[i], want[i])
		}
	}
}

// TestViewerOptionsSurviveTheRequest: the two blocks this package always
// asks for are also in ViewerOptions, so a login using it must not ask
// for either of them twice.
func TestViewerOptionsSurviveTheRequest(t *testing.T) {
	l := Login{First: "A", Last: "B", Password: "x", Options: ViewerOptions}
	body, err := l.body()
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeMethodCallForTest(body)
	if err != nil {
		t.Fatal(err)
	}

	var asked []string
	for k, v := range got {
		if strings.HasPrefix(k, "options[") {
			asked = append(asked, v)
		}
	}
	if len(asked) != len(ViewerOptions) {
		t.Errorf("asked for %d option blocks, want %d: %v", len(asked), len(ViewerOptions), asked)
	}
	seen := map[string]int{}
	for _, a := range asked {
		seen[a]++
	}
	for k, n := range seen {
		if n > 1 {
			t.Errorf("asked for %q %d times", k, n)
		}
	}
	for _, want := range []string{"inventory-root", "buddy-list", "inventory-skeleton"} {
		if seen[want] == 0 {
			t.Errorf("did not ask for %q", want)
		}
	}
}

// TestDefaultLoginIsUnchanged: widening the request is opt in.  A client
// that only wants a circuit should not pay for an inventory skeleton it
// will never read.
func TestDefaultLoginIsUnchanged(t *testing.T) {
	l := Login{First: "A", Last: "B", Password: "x"}
	body, err := l.body()
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeMethodCallForTest(body)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for k := range got {
		if strings.HasPrefix(k, "options[") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("a plain login asks for %d option blocks, want 2", n)
	}
}
