// Command sl-host prints the address of the machine slgod is on, as seen
// from whichever network this machine is attached to now.
//
//	sl-host          print the address for the current network
//	sl-host -a NAME  the address for the current network and this profile
//	sl-host -v       and explain, on standard error, which rule matched and why
//	sl-host -l       list every rule with its match state, and exit
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
//	<network>  <address>[:<port>]  [@profile ...]  [label]
//
//	192.168.1.0/24   192.168.1.20        home
//	0/0              127.0.0.1:7808      @dev  the development slgod
//	0/0              192.168.9.1         anywhere else
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
// # Ports, and which slgod
//
// The address may carry a port -- 192.168.1.20:7808, or [2001:db8::20]:7808
// for IPv6 -- and a client then uses it as given, adding slgod's usual
// :7807 only to an address that has none.  That is what lets a second
// slgod, on another port, be found the same way as the first.
//
// A word after the address that starts with @ names a profile the rule is
// for, and a rule may name several.  Such a rule is tried only when the
// question is about one of them: sl-host -a NAME, or $SLGO_AGENT, which
// is how a client asks, having worked out which avatar it wants.  A rule
// with no @ word is for every profile, and for a question that names
// none, so a file written before profiles could be named means what it
// always meant.  First match still wins: put a profile's own rules above
// the general ones.
//
// The profile travels in the environment rather than as a flag because
// an older sl-host, still installed somewhere on a PATH, refuses a flag
// it does not know and would take every client down with it.  One that
// has never heard of $SLGO_AGENT simply answers as before.
//
// # Several addresses
//
// A machine usually has more than one address: wired and wireless at once,
// a VPN, a bridge for virtual machines.  Each rule is tried against every
// one of them, separately: the addresses are a list from start to finish.
// Why: doc/sl-host.md#the-script-it-replaced
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
	"slices"
	"strconv"
	"strings"

	"github.com/quark-idlemind/slgo/internal/version"
)

func main() {
	verbose := flag.Bool("v", false, "explain on standard error which rule matched and why")
	agent := flag.String("a", "", "the profile being connected as; $SLGO_AGENT if not given")
	list := flag.Bool("l", false, "list every rule with its match state, and exit")
	ver := flag.Bool("version", false, "say which build this is, and exit")
	flag.Usage = func() { fmt.Fprintln(os.Stderr, "usage: sl-host [-v] [-l] [-a PROFILE]") }
	flag.Parse()
	if *ver {
		fmt.Println(version.String("sl-host"))
		return
	}
	if flag.NArg() > 0 {
		flag.Usage()
		os.Exit(2)
	}

	profile := strings.TrimSpace(*agent)
	if profile == "" {
		profile = firstSet("SLGO_AGENT")
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
		listRules(os.Stdout, rules, addrs, profile)
		return
	}
	if len(addrs) == 0 {
		fmt.Fprintln(os.Stderr, "sl-host: no address found on any interface -- is the network up?")
		os.Exit(1)
	}

	m, ok := choose(rules, addrs, profile)
	if !ok {
		explain(os.Stderr, path, rules, addrs, profile)
		os.Exit(1)
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "sl-host: %s (network %s matched our %s%s)%s\n",
			m.rule.host, m.rule.text, m.addr, forProfile(profile), bracketed(m.rule.label))
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

func forProfile(profile string) string {
	if profile == "" {
		return ""
	}
	return ", for " + profile
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
// the profiles it is for (none: every one), and a label that is only
// ever shown.
type rule struct {
	network  *net.IPNet
	host     string
	profiles []string
	label    string
	line     int
	text     string // the network as it was written, for saying so
}

// isFor says whether the rule may answer a question about this profile.
// A rule that names none answers every question; one that names some
// answers only about those, and never a question that names nobody.
func (r rule) isFor(profile string) bool {
	if len(r.profiles) == 0 {
		return true
	}
	for _, p := range r.profiles {
		if p == profile {
			return true
		}
	}
	return false
}

// accounts is the rule's @ words, as written, for saying so.
func (r rule) accounts() string {
	if len(r.profiles) == 0 {
		return ""
	}
	return "@" + strings.Join(r.profiles, " @")
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
		if err := checkAddress(fields[1]); err != nil {
			fmt.Fprintf(warn, "sl-host: %s:%d: %v; skipped\n", path, n, err)
			continue
		}
		// The @ words come straight after the address; the label is
		// whatever follows them.
		rest := fields[2:]
		var profiles []string
		for len(rest) > 0 && strings.HasPrefix(rest[0], "@") {
			profiles = append(profiles, rest[0][1:])
			rest = rest[1:]
		}
		if slices.Contains(profiles, "") {
			fmt.Fprintf(warn, "sl-host: %s:%d: an @ with no profile name after it; skipped\n", path, n)
			continue
		}
		rules = append(rules, rule{
			network: network, host: fields[1], profiles: profiles,
			label: strings.Join(rest, " "), line: n, text: fields[0],
		})
	}
	return rules
}

// checkAddress refuses an address whose port is not one.  The address is
// otherwise printed as written -- a host name is as good as a number --
// but a port a client cannot dial is better said here, against the line
// it is on, than as a connection error somewhere else later.  An IPv6
// address with no port needs no brackets; with one, it has to have them,
// or there is no telling the port from the last group.
func checkAddress(s string) error {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		// No port, which is the usual case.
		return nil
	}
	if host == "" {
		return fmt.Errorf("%q has a port and no host", s)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("%q: the port must be a number from 1 to 65535", s)
	}
	return nil
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
func choose(rules []rule, addrs []net.IP, profile string) (match, bool) {
	for _, r := range rules {
		if !r.isFor(profile) {
			continue
		}
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

// listRules prints every rule and whether it matches, which is -l.  A
// rule for other profiles than the one asked about says so, as "other",
// whatever its network: it would not be tried.
func listRules(w io.Writer, rules []rule, addrs []net.IP, profile string) {
	for _, r := range rules {
		state, note := "-", strings.TrimSpace(r.accounts()+" "+r.label)
		if !r.isFor(profile) {
			state = "other"
		} else if a := firstIn(r.network, addrs); a != nil {
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
func explain(w io.Writer, path string, rules []rule, addrs []net.IP, profile string) {
	if profile == "" {
		fmt.Fprintf(w, "sl-host: no rule in %s matches this network.\n", path)
	} else {
		fmt.Fprintf(w, "sl-host: no rule in %s matches this network for %s.\n", path, profile)
	}
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
		fmt.Fprintln(w, strings.TrimRight(fmt.Sprintf("    %-18s %-16s %s", r.text, r.host,
			strings.TrimSpace(r.accounts()+" "+r.label)), " "))
	}
	fmt.Fprintln(w, "  add a rule for this network, or set SL_HOST=<address> for a one-off.")
}
