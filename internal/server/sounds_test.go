package server

// slgod relays the four sound messages by name to a client that asked
// for them, and to none that did not.
// Why: doc/sounds.md#what-is-heard

import (
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

func TestTheSoundMessagesAreRelayedToAClientThatAskedForThem(t *testing.T) {
	t.Parallel()

	h := &Hosted{Name: "example", clients: map[*Client]bool{}}
	asked := &Client{out: make(chan *pb.ServerPacket, 8), subs: map[msg.ID]bool{}, names: map[string]bool{}}
	asked.setSubs(&pb.Subscribe{Set: []string{"SoundTrigger", "AttachedSound", "AttachedSoundGainChange", "PreloadSound"}})
	other := &Client{out: make(chan *pb.ServerPacket, 8), subs: map[msg.ID]bool{}, names: map[string]bool{}}
	other.setSubs(&pb.Subscribe{Set: []string{"ChatFromSimulator"}})
	h.attach(asked)
	h.attach(other)

	for _, m := range []msg.Message{
		&msg.SoundTrigger{}, &msg.AttachedSound{}, &msg.AttachedSoundGainChange{},
		&msg.PreloadSound{DataBlock: []msg.PreloadSound_DataBlock{{}}},
	} {
		h.relay(&msg.Packet{ID: msg.IDOf(m), Message: m})
		select {
		case p := <-asked.out:
			if got, want := p.GetMessage().GetName(), m.MsgInfo().Name; got != want {
				t.Errorf("relayed %q, want %q", got, want)
			}
		default:
			t.Errorf("%s was not relayed to a client that asked for it", m.MsgInfo().Name)
		}
		select {
		case p := <-other.out:
			t.Errorf("%s was relayed to a client that did not ask: %v", m.MsgInfo().Name, p)
		default:
		}
	}
}
