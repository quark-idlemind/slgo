package main

import (
	"bytes"
	"net"
	"strings"
	"testing"
)

func mustRules(t *testing.T, text string) []rule {
	t.Helper()
	var warn bytes.Buffer
	rules := parseRules(strings.NewReader(text), "rules", &warn)
	if warn.Len() > 0 {
		t.Fatalf("warned reading rules that should read cleanly:\n%s", warn.String())
	}
	return rules
}

func ips(ss ...string) []net.IP {
	var out []net.IP
	for _, s := range ss {
		out = append(out, net.ParseIP(s))
	}
	return out
}

// TestEveryAddressIsTriedOnItsOwn is the fault this command replaced: a
// machine with two addresses, whose second one is the one a rule is for.
// The ksh script before it passed both to awk as one string, awk refused
// the newline between them, and no rule was tried at all.
func TestEveryAddressIsTriedOnItsOwn(t *testing.T) {
	rules := mustRules(t, "192.168.9.0/24  192.168.9.20\n")
	m, ok := choose(rules, ips("192.168.1.166", "192.168.9.5"))
	if !ok || m.rule.host != "192.168.9.20" {
		t.Fatalf("chose %+v, %v; want the rule the second address matches", m.rule, ok)
	}
	if !m.addr.Equal(net.ParseIP("192.168.9.5")) {
		t.Errorf("credited %v, want the address that matched", m.addr)
	}
}

// TestTheFirstMatchingRuleWins, as the script documented: a catch-all
// goes last, and a file that puts one first means it.
func TestTheFirstMatchingRuleWins(t *testing.T) {
	addrs := ips("192.168.1.166", "192.168.9.5")
	for _, c := range []struct{ text, want string }{
		{"192.168.1.0/24 192.168.1.20\n0/0 192.168.50.1\n", "192.168.1.20"},
		{"0/0 192.168.50.1\n192.168.1.0/24 192.168.1.20\n", "192.168.50.1"},
		{"192.168.9.0/24 192.168.9.20\n192.168.1.0/24 192.168.1.20\n", "192.168.9.20"},
	} {
		if m, _ := choose(mustRules(t, c.text), addrs); m.rule.host != c.want {
			t.Errorf("with\n%s chose %s, want %s", c.text, m.rule.host, c.want)
		}
	}
}

// TestACatchAllIsReadAndMatches: "0/0" is what people write, and the
// script refused it as a bad network, since it wanted four octets.
func TestACatchAllIsReadAndMatches(t *testing.T) {
	rules := mustRules(t, "192.168.1.0/24 192.168.1.20\n0/0 192.168.50.1\n")
	if m, ok := choose(rules, ips("192.168.77.3")); !ok || m.rule.host != "192.168.50.1" {
		t.Errorf("chose %+v, %v; want the catch-all", m.rule, ok)
	}
}

func TestNothingMatchingIsNoAnswer(t *testing.T) {
	rules := mustRules(t, "192.168.1.0/24 192.168.1.20\n")
	if m, ok := choose(rules, ips("192.168.9.5")); ok {
		t.Errorf("chose %+v for an address no rule mentions", m.rule)
	}
	if _, ok := choose(nil, ips("192.168.9.5")); ok {
		t.Error("an empty file chose something")
	}
}

// TestNetworksAreReadTheWayPeopleWriteThem: the short IPv4 forms, a bare
// address, and IPv6.
func TestNetworksAreReadTheWayPeopleWriteThem(t *testing.T) {
	for _, c := range []struct {
		in, want string
	}{
		{"0/0", "0.0.0.0/0"},
		{"0.0.0.0/0", "0.0.0.0/0"},
		{"10/8", "10.0.0.0/8"},
		{"192.168/16", "192.168.0.0/16"},
		{"192.168.1/24", "192.168.1.0/24"},
		{"192.168.1.0/24", "192.168.1.0/24"},
		{"192.168.1.20", "192.168.1.20/32"},
		{"fd00::/8", "fd00::/8"},
	} {
		n, err := parseNetwork(c.in)
		if err != nil || n.String() != c.want {
			t.Errorf("%q read as %v, %v; want %s", c.in, n, err, c.want)
		}
	}
	for _, bad := range []string{"home", "192.168.1.0/33", "1.2.3.4.5/8"} {
		if _, err := parseNetwork(bad); err == nil {
			t.Errorf("%q was accepted as a network", bad)
		}
	}
}

// TestALabelIsKeptAndABadLineIsSkipped: the label is free text of any
// number of words, and one unreadable line is reported without losing the
// rest of the file.
func TestALabelIsKeptAndABadLineIsSkipped(t *testing.T) {
	var warn bytes.Buffer
	rules := parseRules(strings.NewReader(
		"# where slgod is\n\n"+
			"192.168.1.0/24  192.168.1.20  the house, wired   # comment\n"+
			"garden  192.168.3.3\n"+
			"192.168.9.0/24\n"+
			"0/0  192.168.50.1\n"), "rules", &warn)
	if len(rules) != 2 {
		t.Fatalf("kept %d rules, want the two good ones: %+v", len(rules), rules)
	}
	if rules[0].label != "the house, wired" || rules[0].line != 3 {
		t.Errorf("first rule %+v; want its whole label and its line", rules[0])
	}
	for _, want := range []string{"rules:4", "rules:5"} {
		if !strings.Contains(warn.String(), want) {
			t.Errorf("no warning naming %s:\n%s", want, warn.String())
		}
	}
}

// TestTheListSaysWhichRulesMatch is -l: every rule, MATCH or not, and the
// address that matched.
func TestTheListSaysWhichRulesMatch(t *testing.T) {
	var b bytes.Buffer
	listRules(&b, mustRules(t,
		"192.168.1.0/24 192.168.1.20 home\n192.168.9.0/24 192.168.9.20\n0/0 192.168.50.1 anywhere\n"),
		ips("192.168.9.5"))
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("listed %d lines:\n%s", len(lines), b.String())
	}
	for i, want := range []string{"-        home", "MATCH    via 192.168.9.5", "MATCH    anywhere (via 192.168.9.5)"} {
		if !strings.HasSuffix(lines[i], want) {
			t.Errorf("line %d is %q, want it to end %q", i+1, lines[i], want)
		}
	}
}

// TestAFailureSaysWhatWasTried: every address and every rule, so that the
// person reading it can see which of the two is wrong.
func TestAFailureSaysWhatWasTried(t *testing.T) {
	var b bytes.Buffer
	explain(&b, "/home/example/.config/sl-host",
		mustRules(t, "192.168.1.0/24 192.168.1.20 home\n"), ips("192.168.9.5", "192.168.44.2"))
	got := b.String()
	for _, want := range []string{
		"no rule in /home/example/.config/sl-host matches this network",
		"our addresses: 192.168.9.5 192.168.44.2\n",
		"192.168.1.0/24",
		"home",
		"SL_HOST=<address>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the explanation lacks %q:\n%s", want, got)
		}
	}
}
