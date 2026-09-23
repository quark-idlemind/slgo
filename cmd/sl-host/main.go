// Command sl-host prints the address of the machine slgod is on, as seen
// from whichever network this machine is attached to now.
//
//	sl-host       print the address for the current network
//	sl-host -v    and explain, on standard error, which rule matched and why
//	sl-host -l    list every rule with its match state, and exit
//
// slgod runs on one machine and is reached from others, and which of its
// addresses is right depends on where THIS machine is, not on anything
// remote.  The programs that talk to it ask this command rather than carry
// an address (see internal/slhost), so nobody has to edit one when a
// laptop moves between networks.
//
// # The rules
//
// ~/.config/sl-host -- or $SL_HOSTFILE, or $RSSH_HOSTFILE -- holds one rule
// a line:
//
//	<network>  <address>  [label]
//
//	192.168.1.0/24   192.168.1.20   home
//	0/0              192.168.9.1    anywhere else
//
// Blank lines and #-comments are ignored, and the first rule whose network
// one of this machine's addresses sits in wins, so a catch-all goes last.
// The label is free text, used only in what this says about itself.  A
// network is a CIDR, and an IPv4 one may leave off trailing zero octets --
// 0/0, 10/8, 192.168/16 -- or a bare address, which is that one address.
// The address is printed as written, so a host name works too.  A line
// that cannot be read is reported and skipped, as the rest of the file is
// still worth having.
//
// # Several addresses
//
// A machine usually has more than one address: wired and wireless at once,
// a VPN, a bridge for virtual machines.  Each rule is tried against every
// one of them, separately.
//
// This was a ksh script, and that is where it broke: it collected the
// addresses one a line and handed the lot to awk as a single -v string,
// which awk refuses when it holds a newline -- so on any machine with two
// addresses no rule was ever tried, and the caller was left with an empty
// host.  The same script also required a network to be four dotted octets,
// so "0/0" was rejected as a bad network.  Here the addresses are a list
// from start to finish, and the short forms are read.
//
// # Overrides and exit status
//
// $SL_HOST, or $RSSH_HOST, is the answer outright, for a one-off -- ahead
// of the file, so it works when the file is missing.  -l ignores it, since
// listing the rules is the point of -l.
//
// Exit status is 0 on a match; 1 when nothing matches, with this machine's
// addresses and the rules on standard error, because that is what says
// which of the two is wrong; and 2 for a usage or configuration error.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	verbose := flag.Bool("v", false, "explain on standard error which rule matched and why")
	list := flag.Bool("l", false, "list every rule with its match state, and exit")
	flag.Usage = func() { fmt.Fprintln(os.Stderr, "usage: sl-host [-v] [-l]") }
	flag.Parse()
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}

	override := firstSet("SL_HOST", "RSSH_HOST")
	if override != "" && !*list {
		if *verbose {
			fmt.Fprintln(os.Stderr, "sl-host: SL_HOST/RSSH_HOST override")
		}
		fmt.Println(override)
		return
	}

	path := rulesPath()
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sl-host: cannot read %s\n", path)
		os.Exit(2)
	}
	rules := parseRules(f, path, os.Stderr)
	f.Close()
	if len(rules) == 0 {
		fmt.Fprintf(os.Stderr, "sl-host: no rules in %s; each line is a network and the address to use on it, e.g. \"0/0 192.168.1.20\"\n", path)
		os.Exit(2)
	}

	addrs, err := localAddrs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "sl-host: cannot read this machine's addresses: %v\n", err)
		os.Exit(1)
	}

	if *list {
		listRules(os.Stdout, rules, addrs)
		return
	}
	if len(addrs) == 0 {
		fmt.Fprintln(os.Stderr, "sl-host: no address found on any interface -- is the network up?")
		os.Exit(1)
	}

	m, ok := choose(rules, addrs)
	if !ok {
		explain(os.Stderr, path, rules, addrs)
		os.Exit(1)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "sl-host: %s (network %s matched our %s)%s\n",
			m.rule.host, m.rule.text, m.addr, bracketed(m.rule.label))
	}
	fmt.Println(m.rule.host)
}

func firstSet(names ...string) string {
	for _, n := range names {
		if v := strings.TrimSpace(os.Getenv(n)); v != "" {
			return v
		}
	}
	return ""
}

func bracketed(label string) string {
	if label == "" {
		return ""
	}
	return " [" + label + "]"
}

// rulesPath is where the rules are kept.
func rulesPath() string {
	if p := firstSet("SL_HOSTFILE", "RSSH_HOSTFILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "sl-host")
	}
	return filepath.Join(home, ".config", "sl-host")
}

// A rule is one line of the file: a network, the address to use on it,
// and a label that is only ever shown.
type rule struct {
	network *net.IPNet
	host    string
	label   string
	line    int
	text    string // the network as it was written, for saying so
}

// parseRules reads the rules, reporting to warn and skipping any line it
// cannot read.
func parseRules(r io.Reader, path string, warn io.Writer) []rule {
	var rules []rule
	s := bufio.NewScanner(r)
	for n := 1; s.Scan(); n++ {
		line := s.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			fmt.Fprintf(warn, "sl-host: %s:%d: a network and no address to use on it; skipped\n", path, n)
			continue
		}
		network, err := parseNetwork(fields[0])
		if err != nil {
			fmt.Fprintf(warn, "sl-host: %s:%d: %v; skipped\n", path, n, err)
			continue
		}
		rules = append(rules, rule{
			network: network, host: fields[1], label: strings.Join(fields[2:], " "),
			line: n, text: fields[0],
		})
	}
	return rules
}

// parseNetwork reads a CIDR, the short IPv4 forms people write -- 0/0,
// 10/8, 192.168/16 -- or a bare address, which is that address alone.
func parseNetwork(s string) (*net.IPNet, error) {
	addr, bits, hasBits := strings.Cut(s, "/")
	if !strings.Contains(addr, ":") {
		// IPv4 may leave off trailing zero octets.
		if parts := strings.Split(addr, "."); len(parts) < 4 {
			for len(parts) < 4 {
				parts = append(parts, "0")
			}
			addr = strings.Join(parts, ".")
		}
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return nil, fmt.Errorf("%q is not a network", s)
	}
	if !hasBits {
		if ip.To4() != nil {
			bits = "32"
		} else {
			bits = "128"
		}
	}
	_, network, err := net.ParseCIDR(addr + "/" + bits)
	if err != nil {
		return nil, fmt.Errorf("%q is not a network", s)
	}
	return network, nil
}

// A match is the rule chosen and the address of ours that it matched.
type match struct {
	rule rule
	addr net.IP
}

// choose is the first rule whose network one of these addresses is in.
//
// 0/0 contains every IPv4 address and ::/0 every IPv6 one, so a
// catch-all needs no special case -- only an address of the right family.
func choose(rules []rule, addrs []net.IP) (match, bool) {
	for _, r := range rules {
		if a := firstIn(r.network, addrs); a != nil {
			return match{rule: r, addr: a}, true
		}
	}
	return match{}, false
}

func firstIn(network *net.IPNet, addrs []net.IP) net.IP {
	for _, a := range addrs {
		if network.Contains(a) {
			return a
		}
	}
	return nil
}

// listRules prints every rule and whether it matches, which is -l.
func listRules(w io.Writer, rules []rule, addrs []net.IP) {
	for _, r := range rules {
		state, note := "-", r.label
		if a := firstIn(r.network, addrs); a != nil {
			state = "MATCH"
			if note == "" {
				note = "via " + a.String()
			} else {
				note += " (via " + a.String() + ")"
			}
		}
		fmt.Fprintln(w, strings.TrimRight(fmt.Sprintf("%-20s %-16s %-8s %s", r.text, r.host, state, note), " "))
	}
}

// localAddrs is every address this machine has on an interface that is
// up, apart from loopback and link-local ones, which say nothing about
// where the machine is.
func localAddrs() ([]net.IP, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []net.IP
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			out = append(out, ip)
		}
	}
	return out, nil
}

// explain says why nothing matched.
func explain(w io.Writer, path string, rules []rule, addrs []net.IP) {
	fmt.Fprintf(w, "sl-host: no rule in %s matches this network.\n", path)
	fmt.Fprint(w, "  our addresses:")
	if len(addrs) == 0 {
		fmt.Fprint(w, " (none)")
	}
	for _, a := range addrs {
		fmt.Fprint(w, " ", a)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  rules:")
	if len(rules) == 0 {
		fmt.Fprintln(w, "    (none)")
	}
	for _, r := range rules {
		fmt.Fprintln(w, strings.TrimRight(fmt.Sprintf("    %-18s %-16s %s", r.text, r.host, r.label), " "))
	}
	fmt.Fprintln(w, "  add a rule for this network, or set SL_HOST=<address> for a one-off.")
}
