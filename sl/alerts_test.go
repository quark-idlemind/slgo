package sl

import "testing"

// TestQuotingAlertsLeavesThemAsTheyWere: a timeout quotes the alerts
// heard since it started, and quoting them must not change what is kept,
// or Alerts returns them quoted and the next timeout quotes them twice.
func TestQuotingAlertsLeavesThemAsTheyWere(t *testing.T) {
	w := &Session{alerts: []string{"no room here", "try later"}}
	want := `; the simulator said "try later"`
	for i := 0; i < 2; i++ {
		if got := w.alertsSince(1); got != want {
			t.Fatalf("timeout %d quoted %s, want %s", i+1, got, want)
		}
	}
	if got := w.Alerts(); len(got) != 2 || got[0] != "no room here" || got[1] != "try later" {
		t.Errorf("Alerts() = %q after two timeouts, want them as they were heard", got)
	}
}
