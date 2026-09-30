package agent

// The avatar's health.
//
// A region with damage enabled sends HealthMessage, one float: the
// viewer puts it in the status bar as a whole number and does nothing
// else with it (process_health_message, llviewermessage.cpp:5113, which
// casts to S32).  So it is kept as it came, with whether one has come
// at all, since 100 is the health of an avatar nothing has hurt and a
// session that has heard nothing is not that.
// Why: doc/avatar-state.md#healthmessage

import (
	"sync"

	"github.com/quark-idlemind/slgo/msg"
)

type health struct {
	mu    sync.Mutex
	value float32
	known bool
}

// Health is the latest health the simulator said, and whether it has
// said any: false until the first HealthMessage.  It is what the
// simulator last sent, from whichever region, and is not reset by a
// move.
func (a *Agent) Health() (health float32, known bool) {
	a.health.mu.Lock()
	defer a.health.mu.Unlock()
	return a.health.value, a.health.known
}

// keepHealth registers the handler that keeps it.
func (a *Agent) keepHealth() {
	a.Disp.MustHandle("HealthMessage", func(p *msg.Packet) {
		m := p.Message.(*msg.HealthMessage)
		a.health.mu.Lock()
		a.health.value, a.health.known = m.HealthData.Health, true
		a.health.mu.Unlock()
	}, msg.Inline())
}
