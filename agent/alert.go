package agent

// The region's alerts, logged.
//
// An AlertMessage is the simulator's general channel for telling the
// avatar something: a refusal, a notice, and the warning that the region
// is about to restart, which carries its count in AlertInfo (Firestorm
// reads RegionRestartMinutes and RegionRestartSeconds there, in
// attempt_standard_notification, llviewermessage.cpp).  Each one is
// logged as it came, AlertInfo included, so that such a warning is on
// record when one happens.  Clients still get every alert as before:
// they are relayed by the tap, whatever handles them here.
// Why: doc/avatar-state.md#alertmessage

import (
	"fmt"
	"strings"

	"github.com/quark-idlemind/slgo/msg"
)

// logAlerts registers the handler that logs them.
func (a *Agent) logAlerts() {
	a.Disp.MustHandle("AlertMessage", func(p *msg.Packet) {
		a.logf("%s", alertLine(p.Message.(*msg.AlertMessage)))
	}, msg.Inline())
}

// alertLine is an alert as the log prints it: its text, then each
// AlertInfo block's message and its extra parameters, quoted.
func alertLine(m *msg.AlertMessage) string {
	var b strings.Builder
	fmt.Fprintf(&b, "alert: %q", trimNul(m.AlertData.Message))
	for _, in := range m.AlertInfo {
		fmt.Fprintf(&b, " info %q", trimNul(in.Message))
		if extra := trimNul(in.ExtraParams); extra != "" {
			fmt.Fprintf(&b, " %q", extra)
		}
	}
	return b.String()
}
