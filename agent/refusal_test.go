package agent

import (
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// agniVIPRefusal is a region that would not have this avatar, in the
// shape Agni sent it off the event queue: a key in one block and a
// sentence for a person in the other.  The AgentID is made up.
const agniVIPRefusal = `<llsd><map>` +
	`<key>AlertInfo</key><array><map>` +
	`<key>ExtraParams</key><string></string>` +
	`<key>Message</key><string>MustHaveVIPStatus</string></map></array>` +
	`<key>Info</key><array><map>` +
	`<key>AgentID</key><uuid>45d57e57-7e57-c0de-d221-6ffd8a188ce4</uuid>` +
	`<key>Reason</key><string>You must be a premium or vip subscriber ` +
	`to enter this region.</string></map></array></map></llsd>`

// TestARefusalOffTheQueueKeepsTheKeyAndTheSentenceApart: the key is
// what a program acts on and the sentence is what a person reads.  A
// reader that returned one string for both -- which is what the two
// packages that read this did before, each in its own way -- leaves a
// caller searching the words for the key.
func TestARefusalOffTheQueueKeepsTheKeyAndTheSentenceApart(t *testing.T) {
	body, err := llsd.Decode(strings.NewReader(agniVIPRefusal))
	if err != nil {
		t.Fatal(err)
	}
	got := ReadTeleportFailedEvent(body)
	if got.Key != KeyMustHaveVIPStatus {
		t.Errorf("key = %q, want %q", got.Key, KeyMustHaveVIPStatus)
	}
	if want := "You must be a premium or vip subscriber to enter this region."; got.Reason != want {
		t.Errorf("reason = %q, want %q", got.Reason, want)
	}
}

// TestARefusalOnTheCircuitComparesEqualToTheKey: on the circuit both
// voices end in the NUL the protocol puts on a variable string.  A key
// read with it still on would compare unequal to every constant above,
// silently, and the refusal would be taken for one nobody here knows.
func TestARefusalOnTheCircuitComparesEqualToTheKey(t *testing.T) {
	m := &msg.TeleportFailed{}
	m.Info.Reason = []byte("Could not teleport closer to destination\x00")
	m.AlertInfo = []msg.TeleportFailed_AlertInfo{{Message: []byte("CouldntTPCloser\x00")}}

	got := ReadTeleportFailed(m)
	want := Refusal{Key: KeyCouldntTPCloser, Reason: "Could not teleport closer to destination"}
	if got != want {
		t.Errorf("read %+v, want %+v", got, want)
	}
}

// TestARefusalWithNothingInItReadsAsNothing: an event body missing
// either block, or both, is a refusal with less to say and not a reason
// to fail -- the caller still has to be told the teleport was refused.
// And a reason with no key beside it stays a reason: it is sometimes a
// sentence, and promoting it would make the key sometimes a sentence.
func TestARefusalWithNothingInItReadsAsNothing(t *testing.T) {
	for _, body := range []any{
		nil,
		map[string]any{},
		map[string]any{"AlertInfo": []any{}},
		map[string]any{"Info": []any{map[string]any{"Reason": "no_host"}}},
	} {
		got := ReadTeleportFailedEvent(body)
		if got.Key != "" {
			t.Errorf("%v gave the key %q", body, got.Key)
		}
	}
	if got := ReadTeleportFailed(&msg.TeleportFailed{}); got != (Refusal{}) {
		t.Errorf("an empty TeleportFailed read as %+v", got)
	}
}
