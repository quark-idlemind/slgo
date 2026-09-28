package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// A command given up on while it waits for what it asked for is handed
// that thing beside the error when it turns up all the same, and says
// so: nobody else has its id.  Each of these cancels as the request goes
// out, so only the last look, which the cancel does not reach, finds it.

// givenUp runs a line on a context that onSend's act may cancel, and
// returns the command's error.
func givenUp(t *testing.T, line string, f *fakeGrid, d *daemon, b *bot,
	act func(m msg.Message, cancel context.CancelFunc)) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.mu.Lock()
	f.onSend = func(m msg.Message) { act(m, cancel) }
	f.mu.Unlock()
	r := &req{d: d, bot: b, from: testSender, who: "Trusted Resident", base: context.Background()}
	var out strBuilder
	err := r.Run(ctx, &out, line)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("%s given up on returned %v, having printed %q", line, err, out.String())
	}
	return err
}

func TestAMkdirGivenUpOnSaysWhatItMade(t *testing.T) {
	d, b, f := newTestDaemon(t)
	f.folders = map[msg.UUID][]fakeEntry{testRoot: nil}
	var made msg.UUID
	err := givenUp(t, `mkdir "given up on"`, f, d, b, func(m msg.Message, cancel context.CancelFunc) {
		c, ok := m.(*msg.CreateInventoryFolder)
		if !ok {
			return
		}
		cancel()
		f.mu.Lock()
		defer f.mu.Unlock()
		made = c.FolderData.FolderID
		f.folders[testRoot] = append(f.folders[testRoot], fakeEntry{ID: made, Name: "given up on", Folder: true})
	})
	if !strings.Contains(err.Error(), "made all the same") || !strings.Contains(err.Error(), made.String()) {
		t.Errorf("a mkdir given up on said %q, which does not give the folder it made, %s", err, made)
	}
}

func TestACopyGivenUpOnSaysWhatItMade(t *testing.T) {
	d, b, f := newTestDaemon(t)
	copied := msg.MustParseUUID("c75d7e57-7e57-c0de-b372-0000000000c2")
	f.folders = map[msg.UUID][]fakeEntry{
		testRoot: {{ID: testLamp, Name: "a lamp", Type: int(sl.AssetObject)}},
	}
	err := givenUp(t, `cp "a lamp" / "a second lamp"`, f, d, b, func(m msg.Message, cancel context.CancelFunc) {
		if _, ok := m.(*msg.CopyInventoryItem); !ok {
			return
		}
		cancel()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.folders[testRoot] = append(f.folders[testRoot],
			fakeEntry{ID: copied, Name: "a second lamp", Type: int(sl.AssetObject)})
	})
	if !strings.Contains(err.Error(), "copied all the same") || !strings.Contains(err.Error(), copied.String()) {
		t.Errorf("a cp given up on said %q, which does not give the copy it made, %s", err, copied)
	}
}

func TestAPlaceGivenUpOnSaysWhatItRezzed(t *testing.T) {
	d, b, f := newTestDaemon(t)
	placed := msg.MustParseUUID("f3a47e57-7e57-c0de-be8b-000000000032")
	f.folders = map[msg.UUID][]fakeEntry{
		testRoot: {{ID: testLamp, Name: "a lamp", Type: int(sl.AssetObject)}},
	}
	err := givenUp(t, `place "a lamp"`, f, d, b, func(m msg.Message, cancel context.CancelFunc) {
		if _, ok := m.(*msg.RezObject); !ok {
			return
		}
		cancel()
		f.mu.Lock()
		defer f.mu.Unlock()
		f.objects = append(f.objects, &sl.Seen{
			Object: sl.Object{ID: placed, Local: 32, Name: "a lamp"},
			PCode:  9, Owner: testMe, Position: msg.Vector3{X: 130, Y: 64, Z: 25},
		})
	})
	if !strings.Contains(err.Error(), "rezzed all the same") || !strings.Contains(err.Error(), placed.String()) {
		t.Errorf("a place given up on said %q, which does not give the object it rezzed, %s", err, placed)
	}
}
