package agent

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/quark-idlemind/slgo/msg"
)

// TestAnAlertIsLoggedWithItsInfo: the text, and each AlertInfo block's
// message and extra parameters, so that a restart warning's count is
// in the log.  The parameters are invented, in the shape the viewer
// reads (attempt_standard_notification, llviewermessage.cpp).
// Why: doc/avatar-state.md#alertmessage
func TestAnAlertIsLoggedWithItsInfo(t *testing.T) {
	a, _ := offlineSession(t)
	var mu sync.Mutex
	var lines []string
	a.opts.Log = func(format string, v ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, v...))
		mu.Unlock()
	}

	m := &msg.AlertMessage{
		AlertData: msg.AlertMessage_AlertData{Message: []byte("This region will restart in 5 minutes.\x00")},
		AlertInfo: []msg.AlertMessage_AlertInfo{{
			Message:     []byte("RegionRestartMinutes\x00"),
			ExtraParams: []byte("<llsd><map><key>MINUTES</key><integer>5</integer></map></llsd>\x00"),
		}},
	}
	feed(t, a, m)

	mu.Lock()
	defer mu.Unlock()
	want := `alert: "This region will restart in 5 minutes." info "RegionRestartMinutes" "<llsd><map><key>MINUTES</key><integer>5</integer></map></llsd>"`
	for _, l := range lines {
		if l == want {
			return
		}
	}
	t.Errorf("the alert was logged as:\n%s\nwant:\n%s", strings.Join(lines, "\n"), want)
}

// TestAnAlertWithoutInfoIsLoggedAsItsText: most alerts carry no
// AlertInfo, and are logged as their text alone.
func TestAnAlertWithoutInfoIsLoggedAsItsText(t *testing.T) {
	got := alertLine(&msg.AlertMessage{AlertData: msg.AlertMessage_AlertData{Message: []byte("You can't sit there.\x00")}})
	if want := `alert: "You can't sit there."`; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
}
