package main

// The daemon and its clients have to agree about how many places an
// avatar has, and nothing in either of them can say so on its own: the
// daemon hands out numbers and the clients turn a number into an object
// worn at a particular point.  This is the only place that sees both.

import (
	"testing"

	"github.com/quark-idlemind/slgo/internal/server"
	"github.com/quark-idlemind/slgo/internal/session"
)

// TestTheDaemonAndItsClientsCountThePoolTheSameWay: a daemon that
// believed in more places than the clients can wear would hand out one
// nothing can be run in, and the run would fail after the grant rather
// than instead of it.  Fewer, and objects an avatar is wearing would
// never be used by anybody.
func TestTheDaemonAndItsClientsCountThePoolTheSameWay(t *testing.T) {
	if server.SlotsPerAgent != session.AutoPool() {
		t.Errorf("slgod hands out %d places per avatar and its clients can wear %d",
			server.SlotsPerAgent, session.AutoPool())
	}
}
