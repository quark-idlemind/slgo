package main

// The daemon, actually run.
//
// main is one long piece of wiring -- flags, a machine identity, a
// login per profile, the group each session has to be settled into, the
// authentication, the listener -- and none of it is reachable by
// calling anything smaller: it is a func main() with no seam in it.  So
// this file calls main, in this process, with a grid made out of a
// login server that answers and a simulator that replies to the
// handshake, and takes it down again with the signal a real one is
// stopped with.
//
// Everything it touches is redirected first: the profiles, the machine
// identity file and the shared secret all live in directories the test
// owns.  The developer running this has live slgod credentials on disk
// and a test that read or wrote them would be a fault, not a result.
//
// What cannot be reached this way is anything that ends in log.Fatal.
// Those call os.Exit, which would take the test binary with them, so
// the refusals -- no profiles named, no session that came up, no shared
// secret -- are left to coverage-notes/daemon.md rather than tested
// here.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// ------------------------------------------------------------ a grid

var theGroup = msg.MustParseUUID("33a57e57-7e57-c0de-e7ea-9cad48757549")

// fakeSim answers the handshake, volunteers a group membership, and
// records what it was sent.  The group list matters: settling the
// active group waits fifteen seconds for it, so a simulator that never
// sent one would make every run of this file take that long.
type fakeSim struct {
	conn *net.UDPConn

	mu     sync.Mutex
	seen   []string
	peer   *net.UDPAddr
	seq    uint32
	groups []msg.AgentGroupDataUpdate_GroupData
}

func newSim(t *testing.T) *fakeSim {
	t.Helper()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Skipf("no loopback UDP: %v", err)
	}
	s := &fakeSim{conn: c, groups: []msg.AgentGroupDataUpdate_GroupData{
		{GroupID: theGroup, GroupName: []byte("Builders\x00")},
	}}
	go s.run()
	t.Cleanup(func() { c.Close() })
	return s
}

func (f *fakeSim) addr() *net.UDPAddr { return f.conn.LocalAddr().(*net.UDPAddr) }

func (f *fakeSim) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *fakeSim) saw(name string) bool {
	for _, s := range f.got() {
		if s == name {
			return true
		}
	}
	return false
}

func (f *fakeSim) run() {
	buf := make([]byte, 8192)
	for {
		n, peer, err := f.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.peer = peer
		f.mu.Unlock()

		h, off, err := msg.DecodeHeader(buf[:n])
		if err != nil {
			continue
		}
		body := buf[off:n]
		if h.HasAcks() {
			if body, _, err = msg.SplitAcks(body); err != nil {
				continue
			}
		}
		if h.Zerocoded() {
			if body, err = msg.ZeroExpand(nil, body); err != nil {
				continue
			}
		}
		if len(body) == 0 {
			continue
		}
		id, _, err := msg.DecodeID(body)
		if err != nil {
			continue
		}
		name := id.String()
		f.mu.Lock()
		f.seen = append(f.seen, name)
		f.mu.Unlock()

		if h.Reliable() {
			ack := &msg.PacketAck{Packets: []msg.PacketAck_Packets{{ID: h.Sequence}}}
			f.send(ack, 0)
		}
		switch name {
		case "UseCircuitCode":
			rh := &msg.RegionHandshake{}
			rh.RegionInfo.SimName = []byte("Testville\x00")
			f.send(rh, msg.FlagReliable)
		case "CompleteAgentMovement":
			amc := &msg.AgentMovementComplete{}
			amc.Data.Position = msg.Vector3{X: 1, Y: 2, Z: 3}
			amc.SimData.ChannelVersion = []byte("Fake Server\x00")
			f.send(amc, msg.FlagReliable)

			// The membership list, which is what slgod needs before it
			// can decide which group to act as.
			f.mu.Lock()
			gs := f.groups
			f.mu.Unlock()
			f.sendGroups(gs...)

			// A message the daemon has no handler for, which is how a
			// protocol change announces itself, and one that will not
			// decode at all, which is how a bad build does.
			chat := &msg.ChatFromSimulator{}
			chat.ChatData.Message = []byte("nobody is listening\x00")
			f.send(chat, 0)
			f.sendRaw(msg.IDOf(chat), []byte{1, 2, 3}, 0)
		case "LogoutRequest":
			f.send(&msg.LogoutReply{}, msg.FlagReliable)
		}
	}
}

// setGroups changes what the simulator will say the avatar belongs to,
// which is what decides whether the group can be settled without being
// told.
func (f *fakeSim) setGroups(gs ...msg.AgentGroupDataUpdate_GroupData) {
	f.mu.Lock()
	f.groups = gs
	f.mu.Unlock()
}

// sendGroups volunteers a membership list, which is where the group
// list comes from: it is not in the login response.
func (f *fakeSim) sendGroups(gs ...msg.AgentGroupDataUpdate_GroupData) {
	gd := &msg.AgentGroupDataUpdate{GroupData: gs}
	f.send(gd, msg.FlagReliable)
}

func (f *fakeSim) send(m msg.Message, flags uint8) {
	b, err := m.Encode()
	if err != nil {
		return
	}
	f.sendRaw(msg.IDOf(m), b, flags)
}

// sendRaw puts a number and a body on the wire without encoding
// anything, so that a test can send what no message would produce.
func (f *fakeSim) sendRaw(id msg.ID, body []byte, flags uint8) {
	f.mu.Lock()
	peer := f.peer
	f.seq++
	seq := f.seq
	f.mu.Unlock()
	if peer == nil {
		return
	}
	out := msg.AppendHeader(nil, &msg.Header{Flags: flags, Sequence: seq})
	out = msg.AppendID(out, id)
	out = append(out, body...)
	f.conn.WriteToUDP(out, peer)
}

// loginServer answers every login, pointing at the simulator.
func loginServer(t *testing.T, sim *fakeSim) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>true</string></value></member>
		  <member><name>agent_id</name><value><string>876e7e57-7e57-c0de-9eeb-1bd0e1ec6995</string></value></member>
		  <member><name>session_id</name><value><string>8d1b7e57-7e57-c0de-f4f4-19d29d124acf</string></value></member>
		  <member><name>secure_session_id</name><value><string>95507e57-7e57-c0de-d169-d9847afe641e</string></value></member>
		  <member><name>circuit_code</name><value><int>4242</int></value></member>
		  <member><name>sim_ip</name><value><string>%s</string></value></member>
		  <member><name>sim_port</name><value><int>%d</int></value></member>
		  <member><name>first_name</name><value><string>"Example"</string></value></member>
		  <member><name>last_name</name><value><string>Resident</string></value></member>
		</struct></value></param></params></methodResponse>`, sim.addr().IP, sim.addr().Port)
	}))
	t.Cleanup(hs.Close)
	return hs
}

// ------------------------------------------------------- the daemon

// daemon is one run of main, with everything it reads redirected into
// directories the test owns and everything it says captured.
type daemon struct {
	log  *logCapture
	done chan struct{}
}

// logCapture is where the daemon's output goes, so that a test can wait
// for something to have happened.  The daemon says what it is doing and
// says nothing else, so its log is the only account of its progress
// there is.
type logCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.Write(p)
}

func (c *logCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

// waitForLog blocks until the daemon has said something matching, and
// answers with the whole line.
func (d *daemon) waitForLog(t *testing.T, re *regexp.Regexp, what string) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if m := re.FindStringSubmatch(d.log.String()); m != nil {
			return m[len(m)-1]
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon never said %s:\n%s", what, d.log.String())
		}
		select {
		case <-d.done:
			t.Fatalf("the daemon stopped before saying %s:\n%s", what, d.log.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

var serving = regexp.MustCompile(`serving gRPC on (\S+) for`)

// profileDir writes profiles that log in against hs, in a directory of
// the test's own.  Returning to the real ~/.config/slgo would be a
// test reading somebody's credentials.
func profileDir(t *testing.T, hs *httptest.Server, names ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "slgo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		body := fmt.Sprintf("first = Example\nlast = Resident\npassword = secret\nurl = %s\n", hs.URL)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("SLGO_CONFIG_DIR", dir)
	return dir
}

// runDaemon starts main with these arguments and hands back something
// that can wait for it and stop it.
//
// It cannot run in parallel with anything: main parses the process's
// flags, writes the process's log, and is told to stop by a signal sent
// to the process.
func runDaemon(t *testing.T, args ...string) *daemon {
	t.Helper()

	// Its own machine identity and its own home, so that neither the
	// real ~/.config/slgod nor the real shared secret is touched.
	t.Setenv("SLGOD_CONFIG_DIR", filepath.Join(t.TempDir(), "slgod"))

	savedArgs, savedFlags := os.Args, flag.CommandLine
	os.Args = append([]string{"slgod"}, args...)
	// A fresh flag set, because main registers its flags on the
	// process's and registering the same one twice panics.
	flag.CommandLine = flag.NewFlagSet("slgod", flag.ContinueOnError)

	capture := &logCapture{}
	savedFlagsOnLog := log.Flags()
	log.SetOutput(capture)

	d := &daemon{log: capture, done: make(chan struct{})}
	go func() {
		defer close(d.done)
		main()
	}()

	t.Cleanup(func() {
		d.stop(t)
		log.SetOutput(os.Stderr)
		log.SetFlags(savedFlagsOnLog)
		os.Args, flag.CommandLine = savedArgs, savedFlags
	})
	return d
}

// stop sends the signal a daemon is stopped with and waits for it to
// finish logging out.
//
// The signal goes to this process, which is safe only because main has
// already installed its handler by the time anything calls this -- the
// caller has seen the daemon say it is serving.  Sent earlier it would
// kill the test binary.
func (d *daemon) stop(t *testing.T) {
	t.Helper()
	select {
	case <-d.done:
		return
	default:
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("signalling the daemon: %v", err)
	}
	select {
	case <-d.done:
	case <-time.After(40 * time.Second):
		t.Fatal("the daemon did not stop when it was signalled")
	}
}

// --------------------------------------------------------- the tests

// TestTheDaemonHostsWhatItWasNamedAndServesIt is main from end to end:
// a machine identity invented and written, a profile logged in, the
// active group settled, a listener up, and a signal taking it all down
// again with a logout on the way.
func TestTheDaemonHostsWhatItWasNamedAndServesIt(t *testing.T) {
	sim := newSim(t)
	hs := loginServer(t, sim)
	dir := profileDir(t, hs, "example", "other")
	writeSecret(t, "a shared secret for a test")

	d := runDaemon(t, "-listen", "127.0.0.1:0", "-v", "-start", "last", "example")
	addr := d.waitForLog(t, serving, "that it is serving")

	// The machine identity is invented once and written down, because a
	// pair that changes every login looks like a different computer
	// every time -- which is what an abuser looks like.
	if !strings.Contains(d.log.String(), "machine ") {
		t.Errorf("the daemon did not say which machine it claims to be:\n%s", d.log.String())
	}

	// The active group is settled from the membership list, without
	// which a parcel that only lets a group build refuses the avatar
	// and blames the land.
	waitFor(t, 10*time.Second, "the group to be activated", func() bool {
		return sim.saw("ActivateGroup")
	})
	if !strings.Contains(d.log.String(), "acting as group Builders") {
		t.Errorf("the daemon did not say which group it settled on:\n%s", d.log.String())
	}

	// Authentication is on unless it is turned off, so a client dialling
	// it does the real handshake against the secret on disk.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, addr)
	if err != nil {
		t.Fatalf("dialling the daemon: %v", err)
	}

	info, err := c.Attach(ctx, "example")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if info.GetAvatarName() != "Example Resident" || info.GetRegion() != "Testville" {
		t.Errorf("attached to %+v", info)
	}

	// A profile that was not named on the command line can still be
	// started, and gets exactly the same settling -- otherwise an
	// avatar could build or not depending on how its session came to
	// exist.
	if _, err := c.Host(ctx, "other", false); err != nil {
		t.Fatalf("starting a profile on request: %v", err)
	}
	waitFor(t, 10*time.Second, "the session started on request to be settled", func() bool {
		return strings.Contains(d.log.String(), "other: started on request")
	})
	if !strings.Contains(d.log.String(), "other: acting as group") {
		t.Errorf("a session started on request was not settled:\n%s", d.log.String())
	}

	// Both are listed, and both are hosted.
	agents, err := c.ListAgents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Errorf("agents = %v, want both", agents)
	}

	// -v says what arrived that nothing was waiting for, which is the
	// only warning there is that the protocol has moved, and complains
	// about what would not decode whether -v was given or not.
	waitFor(t, 10*time.Second, "an unhandled message to be reported", func() bool {
		return strings.Contains(d.log.String(), "no handler for")
	})
	waitFor(t, 10*time.Second, "an undecodable packet to be reported", func() bool {
		return strings.Contains(d.log.String(), "undecodable packet")
	})

	// A config directory that has stopped being one leaves the daemon
	// serving what it already holds: what could be started is a
	// question it can fail to answer without failing.
	os.RemoveAll(dir)
	if err := os.WriteFile(dir, []byte("not a directory any more"), 0o600); err != nil {
		t.Fatal(err)
	}
	if agents, err = c.ListAgents(ctx); err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 {
		t.Errorf("agents = %v; the hosted sessions should still be listed", agents)
	}

	// Hung up on before the daemon is told to stop, so that what the
	// stop does is the only thing left to explain what follows.
	c.Close()

	// And the signal it is stopped with logs the avatars out rather
	// than dropping the circuits and leaving them to time out.
	d.stop(t)
	if !sim.saw("LogoutRequest") {
		t.Errorf("the daemon did not log out on the way down: %v", sim.got())
	}
	if !strings.Contains(d.log.String(), "done") {
		t.Errorf("the daemon did not finish tidily:\n%s", d.log.String())
	}
	// The session ending is noticed and said, which is what makes a
	// daemon left running overnight worth reading the log of.
	waitFor(t, 10*time.Second, "the session ending to be reported", func() bool {
		return strings.Contains(d.log.String(), "example: connection ended")
	})

	// The usage message names the flags, which is all -help has to go
	// on.  It is set by main, so it can only be looked at afterwards.
	if usage := usageText(t); !strings.Contains(usage, "usage: slgod") ||
		!strings.Contains(usage, "-listen") {
		t.Errorf("usage message = %q", usage)
	}
}

// usageText runs what main installed as the usage message, with the
// standard error it writes to redirected somewhere a test can read.
func usageText(t *testing.T) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stderr
	os.Stderr = w
	flag.CommandLine.SetOutput(w)
	flag.Usage()
	os.Stderr = saved
	w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestTheDaemonWillServeWithoutAuthentication: -no-auth is for a
// loopback-only run, and it has to say so loudly, because the thing it
// is serving is a live Second Life session.
func TestTheDaemonWillServeWithoutAuthentication(t *testing.T) {
	sim := newSim(t)
	// Two memberships, which is the case slgod cannot settle on its
	// own: with several joined and nothing said, picking one would
	// silently choose the wrong land rights.
	other := msg.MustParseUUID("7f6e7e57-7e57-c0de-1eac-672833278ea7")
	sim.setGroups(
		msg.AgentGroupDataUpdate_GroupData{GroupID: theGroup, GroupName: []byte("Builders\x00")},
		msg.AgentGroupDataUpdate_GroupData{GroupID: other, GroupName: []byte("Testers\x00")},
	)

	hs := loginServer(t, sim)
	dir := profileDir(t, hs, "example", "nogroup")
	// Deliberately no secret written: -no-auth must not need one.
	t.Setenv("HOME", t.TempDir())

	// A profile whose account the login server will not have, and one
	// asking to act as a group this avatar has not joined.  Neither is
	// fatal: one expired password must not take down the sessions that
	// did come up, and a group that cannot be resolved is a reason this
	// avatar cannot build rather than a reason to stop.
	writeProfile(t, dir, "refused", fmt.Sprintf(
		"first = Example\nlast = Resident\npassword = secret\nurl = %s\n", refusingLoginServer(t).URL))
	writeProfile(t, dir, "wronggroup", fmt.Sprintf(
		"first = Example\nlast = Resident\npassword = secret\nurl = %s\ngroup = Nonexistent\n", hs.URL))

	// A group named on the command line beats the profile's, and a uuid
	// needs no membership list at all.
	d := runDaemon(t, "-listen", "127.0.0.1:0", "-no-auth",
		"-group", "example="+theGroup.String(),
		"example", "wronggroup", "nogroup", "refused", "notaprofile")
	addr := d.waitForLog(t, serving, "that it is serving")

	if !strings.Contains(d.log.String(), "WARNING: serving without authentication") {
		t.Errorf("serving unauthenticated was not said loudly:\n%s", d.log.String())
	}
	// A profile that will not load is not fatal, and neither is one
	// whose login is refused; both are reported and the rest go on.
	if !strings.Contains(d.log.String(), "notaprofile: NOT hosted") {
		t.Errorf("a profile that does not exist was not reported:\n%s", d.log.String())
	}
	if !strings.Contains(d.log.String(), "refused: NOT hosted") {
		t.Errorf("a login the grid refused was not reported:\n%s", d.log.String())
	}
	if !strings.Contains(d.log.String(), "wronggroup: no active group: no group named") {
		t.Errorf("a group that could not be resolved was not reported:\n%s", d.log.String())
	}
	// And one that could have been resolved but was not asked about is
	// warned about rather than guessed at: the avatar will be refused
	// by any parcel that only lets a group build, and the refusal
	// blames the land.
	if !strings.Contains(d.log.String(), "nogroup: no active group (2 joined, none chosen") {
		t.Errorf("an unsettled group was not warned about:\n%s", d.log.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	if _, err := c.Attach(ctx, "example"); err != nil {
		t.Fatalf("attach: %v", err)
	}
	waitFor(t, 10*time.Second, "the group named on the command line to be activated", func() bool {
		return sim.saw("ActivateGroup")
	})
	// Hung up on while the session is healthy and nothing is being
	// relayed: a client closed while the daemon is sending to it races
	// inside the client.  See coverage-notes/daemon.md.
	c.Close()

	// The grid throwing a session off is the one ending the daemon does
	// not put right, and the only account of it is the line it logs.
	kick := &msg.KickUser{}
	kick.UserInfo.Reason = []byte("You have been logged out because you logged in from another location.\x00")
	sim.send(kick, msg.FlagReliable)
	waitFor(t, 10*time.Second, "the session ending to be reported with its reason", func() bool {
		return strings.Contains(d.log.String(), "connection ended: ")
	})

	// Having said so it keeps watching, in case the supervisor puts a
	// session back underneath it -- which for a session the grid ended
	// deliberately it never will, so this is the daemon waiting rather
	// than spinning.
	time.Sleep(1100 * time.Millisecond)
	select {
	case <-d.done:
		t.Fatal("the daemon stopped when one of its sessions was thrown off")
	default:
	}
}

// writeProfile puts one profile in the directory, for the cases where
// what is in it is the point.
func writeProfile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// refusingLoginServer says no, the way a login server does when the
// password has expired.
func refusingLoginServer(t *testing.T) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<?xml version="1.0"?><methodResponse><params><param><value><struct>
		  <member><name>login</name><value><string>false</string></value></member>
		  <member><name>reason</name><value><string>key</string></value></member>
		  <member><name>message</name><value><string>nope</string></value></member>
		</struct></value></param></params></methodResponse>`)
	}))
	t.Cleanup(hs.Close)
	return hs
}

// writeSecret puts the shared secret where slgod and its clients will
// look for it, in a home directory belonging to the test.
func writeSecret(t *testing.T, secret string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "slrun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
}

func waitFor(t *testing.T, within time.Duration, what string, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ------------------------------------------------- choosing a group

// TestChoosingAGroup: the simulator lists what the avatar has joined,
// so the choice can usually be made without being told.  Several
// memberships and no instruction is the one case that cannot be
// resolved -- guessing there picks the wrong land rights, and building
// nothing is better than building in the wrong place under the wrong
// group.
func TestChoosingAGroup(t *testing.T) {
	t.Parallel()

	// A context that is already over, so that waiting for a membership
	// list returns what the session has this instant.  Every case here
	// is about the decision, not about the wait.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// A session that has been told nothing.
	none := &agent.Agent{}

	// A uuid needs no list, and so works even if one never arrives.
	if id, why, err := chooseGroup(ctx, none, theGroup.String()); err != nil ||
		id != theGroup || why != theGroup.String() {
		t.Errorf("a uuid gave %v, %q, %v", id, why, err)
	}

	// No list and no instruction is a legitimate answer: it is what a
	// login starts with.
	id, why, err := chooseGroup(ctx, none, "")
	if err != nil || !id.IsZero() || why != "none joined" {
		t.Errorf("no groups gave %v, %q, %v", id, why, err)
	}

	// A name with nothing to match it against is an error rather than a
	// shrug: somebody asked for that group and did not get it.
	if _, _, err := chooseGroup(ctx, none, "Builders"); err == nil {
		t.Error("a group that could not be found was accepted")
	} else if !strings.Contains(err.Error(), "(none)") {
		t.Errorf("the error should list what was joined: %v", err)
	}
}

// TestChoosingBetweenGroupsThatWereJoined needs a session that has been
// told what it belongs to, which means a real one: the membership list
// arrives as a message and there is no other way in.
func TestChoosingBetweenGroupsThatWereJoined(t *testing.T) {
	sim := newSim(t)
	hs := loginServer(t, sim)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	login := agent.Login{First: "Example", Last: "Resident", Password: "x", URL: hs.URL}
	acct, err := login.Do(ctx)
	if err != nil {
		t.Fatal(err)
	}
	a, err := agent.Connect(ctx, acct, agent.Options{
		Timeout: 20 * time.Second, SkipCaps: true, Idle: -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	waitFor(t, 10*time.Second, "the membership list", func() bool {
		return len(a.Groups()) > 0
	})

	// One membership is no ambiguity to resolve, so it is chosen
	// without being asked for.
	id, why, err := chooseGroup(ctx, a, "")
	if err != nil || id != theGroup || why != "Builders" {
		t.Errorf("one group gave %v, %q, %v", id, why, err)
	}

	// Named, and matched without regard to case, because a group name
	// is typed by a person.
	if id, why, err := chooseGroup(ctx, a, "builders"); err != nil || id != theGroup || why != "Builders" {
		t.Errorf("naming the group gave %v, %q, %v", id, why, err)
	}

	// And a name that is not among them says what is, so that the
	// person who typed it can see the difference.
	_, _, err = chooseGroup(ctx, a, "Testers")
	if err == nil || !strings.Contains(err.Error(), `"Builders"`) {
		t.Errorf("the refusal should list the memberships: %v", err)
	}

	// Two memberships, and both called the same thing -- which is
	// allowed on the grid, where a group is its uuid.
	other := msg.MustParseUUID("7f6e7e57-7e57-c0de-1eac-672833278ea7")
	sim.sendGroups(
		msg.AgentGroupDataUpdate_GroupData{GroupID: theGroup, GroupName: []byte("Builders\x00")},
		msg.AgentGroupDataUpdate_GroupData{GroupID: other, GroupName: []byte("Builders\x00")},
	)
	waitFor(t, 10*time.Second, "the second membership", func() bool {
		return len(a.Groups()) == 2
	})

	// Nothing asked for and more than one joined is not derivable, and
	// picking one would silently choose the wrong land rights.  It is
	// not an error -- the session is fine, it just cannot build -- so
	// it comes back as no group and a reason.
	id, why, err = chooseGroup(ctx, a, "")
	if err != nil || !id.IsZero() || !strings.Contains(why, "2 joined, none chosen") {
		t.Errorf("two groups and no instruction gave %v, %q, %v", id, why, err)
	}

	// A name that matches both is the one case that IS an error: the
	// person asked for something and there is no answer to give them
	// but the uuid.
	if _, _, err := chooseGroup(ctx, a, "Builders"); err == nil {
		t.Error("an ambiguous group name was resolved anyway")
	} else if !strings.Contains(err.Error(), "use the uuid") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}
}

// TestGroupNamesAreQuotedForReading: the list goes into an error a
// person reads, and a group called "Builders 2" would otherwise be
// indistinguishable from two groups.
func TestGroupNamesAreQuotedForReading(t *testing.T) {
	t.Parallel()

	if got := groupNames(nil); got != "(none)" {
		t.Errorf("groupNames(nil) = %q", got)
	}
	got := groupNames([]agent.Group{{Name: "Builders"}, {Name: "Testers 2"}})
	if got != `"Builders", "Testers 2"` {
		t.Errorf("groupNames = %s", got)
	}
}

// TestTheGroupFlagReportsItself: flag prints the value in its usage
// message, so a flag that cannot say what it holds makes -help wrong.
func TestTheGroupFlagReportsItself(t *testing.T) {
	t.Parallel()

	var none *groupFlag
	if got := none.String(); got != "" {
		t.Errorf("an absent flag reports %q", got)
	}

	var g groupFlag
	if got := g.String(); got != "" {
		t.Errorf("an unset flag reports %q", got)
	}
	g.Set("Builders")
	if got := g.String(); got != "Builders" {
		t.Errorf("a bare value reports %q", got)
	}

	// Several are reported in a settled order, because a map's is not
	// one and usage text that changed between runs would be noise.
	var each groupFlag
	each.Set("qi=Testers")
	each.Set("example=Builders")
	if got := each.String(); got != "example=Builders,qi=Testers" {
		t.Errorf("named values report %q", got)
	}
}

// TestAnUnnamedRegionIsStillSaidToBeARegion: the region name arrives in
// a message that may not have come yet, and "in " with nothing after it
// reads like a bug in the daemon rather than a fact about the grid.
func TestAnUnnamedRegionIsStillSaidToBeARegion(t *testing.T) {
	t.Parallel()

	if got := orUnknown(""); got != "an unnamed region" {
		t.Errorf("orUnknown(\"\") = %q", got)
	}
	if got := orUnknown("Testville"); got != "Testville" {
		t.Errorf("orUnknown = %q", got)
	}
}
