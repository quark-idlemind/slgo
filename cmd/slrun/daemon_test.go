package main

// A whole run of the program, over a daemon that is not there.
//
// main_test.go can reach everything slrun decides ABOUT a script,
// because that is a function of a session and a session can be faked at
// the backend.  What it cannot reach is how slrun gets one: both ways
// of finding somewhere to run -- the shared auto object, and an object
// named on the command line -- go through session.Connect, which DIALS
// slgod.  There is no seam there and there should not be one: dialling
// is the whole of what those functions do.
//
// So this is a real gRPC daemon in this process, doing the real
// handshake against a secret in a home directory the test owns, with
// fake_test.go's grid behind it.  The daemon is a proxy and nothing
// more, which is what slgod is: capability requests go out over http
// exactly as they do in the world, messages the client sends are handed
// to the grid, and what the grid says comes back down the stream.
//
// Nothing here runs in parallel.  The flags are package level, the
// arguments are the process's, and the secret is in $HOME -- three
// things two tests at once would be sharing.
//
// What cannot be reached is main's failure half: it ends in os.Exit,
// which would take the test binary with it, so the message it prints
// first cannot be reached either.  See coverage-notes/last-commands.md.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/auth"
	"github.com/quark-idlemind/slgo/internal/session"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// The Objects folder and the inventory item the shared auto object is
// worn from.  The item is what ties a worn object back to inventory --
// the object itself is rezzed afresh with a new id every time it goes on
// -- so it is the only durable name the auto object has.
var (
	testObjects = msg.MustParseUUID("a9a87e57-7e57-c0de-b748-062ee08c11ee")
	theAutoItem = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000010")
)

// ------------------------------------------------------- an inventory

// invDir is a folder in the fake inventory and invItem a thing in one.
//
// A tree rather than a flat answer, because finding the auto object
// means reading the top of inventory for Objects and then reading
// Objects itself: a fake that answered both alike would let a program
// that had confused the two pass.
type invDir struct {
	ID    msg.UUID
	Name  string
	Type  int
	Dirs  []*invDir
	Items []*invItem
}

type invItem struct {
	ID   msg.UUID
	Name string
}

func findDir(d *invDir, id msg.UUID) *invDir {
	if d == nil {
		return nil
	}
	if d.ID == id {
		return d
	}
	for _, sub := range d.Dirs {
		if got := findDir(sub, id); got != nil {
			return got
		}
	}
	return nil
}

// capFolderID is the folder an AIS request is about, out of a path of
// the form /category/<folder>/children.
func capFolderID(path string) msg.UUID {
	var id msg.UUID
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, p := range parts {
		if p == "category" && i+1 < len(parts) {
			id, _ = msg.ParseUUID(parts[i+1])
		}
	}
	return id
}

// dirLLSD is what AIS says about one folder, nesting its children as far
// as the depth asked for: AIS takes a depth on the request and answers
// the whole subtree at once.
func dirLLSD(d *invDir, parent msg.UUID, depth int) string {
	var b strings.Builder
	b.WriteString(`<map>`)
	fmt.Fprintf(&b, `<key>category_id</key><string>%s</string>`, d.ID)
	fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, parent)
	fmt.Fprintf(&b, `<key>name</key><string>%s</string>`, xmlText(d.Name))
	fmt.Fprintf(&b, `<key>type_default</key><integer>%d</integer>`, d.Type)
	b.WriteString(`<key>version</key><integer>1</integer>`)
	b.WriteString(`<key>_embedded</key><map><key>categories</key><map>`)
	for _, sub := range d.Dirs {
		fmt.Fprintf(&b, `<key>%s</key>`, sub.ID)
		if depth > 0 {
			b.WriteString(dirLLSD(sub, d.ID, depth-1))
			continue
		}
		// Named but not opened: the folder itself, with nothing in it.
		b.WriteString(dirLLSD(&invDir{ID: sub.ID, Name: sub.Name, Type: sub.Type}, d.ID, 0))
	}
	b.WriteString(`</map><key>items</key><map>`)
	for _, it := range d.Items {
		fmt.Fprintf(&b, `<key>%s</key><map>`, it.ID)
		fmt.Fprintf(&b, `<key>item_id</key><string>%s</string>`, it.ID)
		fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, d.ID)
		fmt.Fprintf(&b, `<key>asset_id</key><string>%s</string>`, it.ID)
		fmt.Fprintf(&b, `<key>name</key><string>%s</string>`, xmlText(it.Name))
		fmt.Fprintf(&b, `<key>type</key><integer>%d</integer>`, int(sl.AssetObject))
		fmt.Fprintf(&b, `<key>inv_type</key><integer>%d</integer>`, int(sl.AssetObject))
		b.WriteString(`<key>permissions</key><map>`)
		b.WriteString(`<key>owner_mask</key><integer>581632</integer>`)
		b.WriteString(`</map></map>`)
	}
	b.WriteString(`</map><key>links</key><map/></map></map>`)
	return b.String()
}

func xmlText(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// ---------------------------------------------------------- the daemon

// fakeDaemon is an slgod holding one session, with fake_test.go's grid
// behind it.
//
// It answers nothing itself.  Every question is passed to the grid and
// every answer translated, which is what slgod does: the daemon holds
// the circuit and has no idea what any of the traffic means.
type fakeDaemon struct {
	pb.UnimplementedGridServer

	grid *fakeGrid
	auth *auth.Server

	// grants names what it has handed out, so that a run can give back
	// what it was given.
	grants int

	// sendMu is because a stream has two writers -- the relay pump and
	// the answers to what the client asked -- and a gRPC stream may not
	// be sent on by two goroutines at once.
	sendMu sync.Mutex
}

// newFakeDaemon starts one and answers with the grid behind it and the
// address to dial.
//
// The shared secret goes in a home directory of the test's own.
// client.Dial does the real handshake and looks the secret up in $HOME,
// and the developer running this has a real one there with a live daemon
// behind it.
func newFakeDaemon(t *testing.T) (*fakeGrid, string) {
	t.Helper()

	const secret = "a shared secret for a test"
	home := t.TempDir()
	dir := filepath.Join(home, ".config", "slrun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	// An avatar named by the environment is still a named avatar, and
	// which avatar was chosen is one of the things being checked here.
	t.Setenv(sl.EnvAgent, "")

	f := newFakeGrid(t, flags.Script)

	// The one object is both things at once: the prim a --object run
	// names, and the shared auto object a default run finds, which is
	// worn and so is found by the item it came from rather than by name.
	f.mu.Lock()
	f.seen = []*sl.Seen{{
		Object:      f.obj,
		AttachItem:  theAutoItem,
		AttachPoint: session.AutoPoints[0],
	}}
	f.mu.Unlock()

	root := &invDir{
		ID: testRoot, Name: "My Inventory",
		Dirs: []*invDir{{ID: testObjects, Name: "Objects", Type: 6, Items: []*invItem{
			{ID: theAutoItem, Name: session.AutoObject},
		}}},
	}
	f.ServeCap(t, agent.InventoryCap, func(w http.ResponseWriter, r *http.Request) {
		depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
		d := findDir(root, capFolderID(r.URL.Path))
		if d == nil {
			http.Error(w, "no such folder", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<?xml version="1.0" ?><llsd>`+dirLLSD(d, testRoot, depth)+`</llsd>`)
	})

	a, err := auth.New(secret)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{grid: f, auth: a}

	creds, err := auth.ServerTLS()
	if err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(creds))
	pb.RegisterGridServer(srv, d)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	return f, lis.Addr().String()
}

// agentInfo is who the daemon says it is holding.  The capabilities have
// to be named here and nowhere else: a client knows what a session can
// do from the attach frame, so a capability the daemon does not mention
// is one no call will even try.
func (d *fakeDaemon) agentInfo() *pb.AgentInfo {
	d.grid.mu.Lock()
	defer d.grid.mu.Unlock()
	caps := make([]string, 0, len(d.grid.caps))
	for name := range d.grid.caps {
		caps = append(caps, name)
	}
	return &pb.AgentInfo{
		Name: "quark", AgentId: testMe.String(), SessionId: testSessionID.String(),
		AvatarName: "Quark Idlemind", Region: "Test Region",
		InventoryRoot: testRoot.String(), Caps: caps,
	}
}

// Login is the daemon's half of the handshake, which is the server's own
// code with the connection bookkeeping left out.
func (d *fakeDaemon) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	binding, err := auth.BindingFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetProof()) == 0 {
		challenge, err := d.auth.Begin(auth.UnpackName([4]uint64{
			req.GetClient_0(), req.GetClient_1(), req.GetClient_2(), req.GetClient_3(),
		}))
		if err != nil {
			return nil, err
		}
		return &pb.LoginResponse{Challenge: challenge}, nil
	}
	proof, _, err := d.auth.Answer(req.GetChallenge(), req.GetProof(), binding)
	if err != nil {
		return nil, err
	}
	return &pb.LoginResponse{Proof: proof}, nil
}

// Stream is the relay, which is the only way a line a script said can
// reach the program that started it.
//
// The pump stops when the client hangs up, which is why the grid is only
// ever made to talk while a run is waiting for it: a client closes the
// channels its own receiving goroutine sends on, so a line arriving
// while it closes is a panic on send to a closed channel.  See
// coverage-notes/last-commands.md.
func (d *fakeDaemon) Stream(s grpc.BidiStreamingServer[pb.ClientPacket, pb.ServerPacket]) error {
	first, err := s.Recv()
	if err != nil {
		return err
	}
	if first.GetAttach() == nil {
		return fmt.Errorf("the first packet was not an attach")
	}
	if err := d.send(s, &pb.ServerPacket{Body: &pb.ServerPacket_Attached{
		Attached: &pb.Attached{Agent: d.agentInfo()},
	}}); err != nil {
		return err
	}

	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case m := <-d.grid.msgs:
				d.send(s, &pb.ServerPacket{Body: &pb.ServerPacket_Message{
					Message: &pb.InboundMessage{
						Id: uint32(m.ID), Name: m.Name, Body: m.Body,
					},
				}})
			case <-done:
				return
			}
		}
	}()

	for {
		p, err := s.Recv()
		if err != nil {
			return nil
		}
		switch {
		case p.GetMessage() != nil:
			out := p.GetMessage()
			in := msg.New(msg.ID(out.Id))
			if in == nil || in.Decode(out.Body) != nil {
				continue
			}
			// On a goroutine of its own, as the grid's answers are: the
			// answer goes back through the relay, and answering inline
			// would be this loop waiting for the pump it is not running.
			go d.grid.answer(in)

		case p.GetLock() != nil:
			// Handed over without argument.  Whose turn it is belongs to
			// internal/session's tests; here the lock is on the way to
			// somewhere to run and nothing else.
			l := p.GetLock()
			d.send(s, &pb.ServerPacket{Body: &pb.ServerPacket_Locked{
				Locked: &pb.Locked{Name: l.Name, Held: true},
			}})

		case p.GetSlots() != nil:
			// One object, which is what this grid has, and it is handed
			// over without argument for the same reason a lock is:
			// whose turn it is belongs to the daemon's own tests.
			//
			// Clean, so that a run here is not made to clear an object
			// before it uses it.  What a dirty one costs is a script
			// write and a wait, which is grid work this fake would have
			// to play out to no purpose.
			want := int(p.GetSlots().GetWant())
			g := &pb.SlotsGranted{Expires: time.Now().Add(time.Hour).Unix()}
			if want == 1 {
				d.grants++
				g.Grant = fmt.Sprintf("g%d", d.grants)
				g.Held = []*pb.SlotHeld{{Agent: "quark", Slot: 0}}
			} else {
				g.Why = fmt.Sprintf("this grid has one object and %d were asked for", want)
			}
			d.send(s, &pb.ServerPacket{Body: &pb.ServerPacket_Granted{Granted: g}})

		case p.GetRenewSlots() != nil:
			d.send(s, &pb.ServerPacket{Body: &pb.ServerPacket_Granted{
				Granted: &pb.SlotsGranted{
					Grant:   p.GetRenewSlots().GetGrant(),
					Expires: time.Now().Add(time.Hour).Unix(),
				},
			}})
		}
	}
}

func (d *fakeDaemon) send(s grpc.BidiStreamingServer[pb.ClientPacket, pb.ServerPacket], p *pb.ServerPacket) error {
	d.sendMu.Lock()
	defer d.sendMu.Unlock()
	return s.Send(p)
}

func (d *fakeDaemon) ListAgents(context.Context, *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	return &pb.ListAgentsResponse{Agents: []*pb.AgentInfo{{Name: "quark"}}}, nil
}

func (d *fakeDaemon) Objects(ctx context.Context, r *pb.ObjectsRequest) (*pb.ObjectsResponse, error) {
	seen, err := d.grid.Objects(ctx, r.Named, r.Id)
	if err != nil {
		return nil, err
	}
	out := make([]*pb.ObjectInfo, 0, len(seen))
	for _, s := range seen {
		out = append(out, &pb.ObjectInfo{
			Id: s.Object.ID.String(), Local: s.Object.Local, Name: s.Object.Name,
			AttachItem: s.AttachItem.String(), AttachPoint: uint32(s.AttachPoint),
		})
	}
	return &pb.ObjectsResponse{Objects: out}, nil
}

func (d *fakeDaemon) Presence(ctx context.Context, r *pb.PresenceRequest) (*pb.PresenceResponse, error) {
	p, err := d.grid.Presence(ctx, r.DrawDistance)
	if err != nil {
		return nil, err
	}
	return &pb.PresenceResponse{
		Region:   p.Region,
		Position: &pb.Vector3{X: p.Position.X, Y: p.Position.Y, Z: p.Position.Z},
	}, nil
}

func (d *fakeDaemon) Region(context.Context, *pb.RegionRequest) (*pb.RegionInfo, error) {
	return &pb.RegionInfo{Name: "Test Region", Known: true}, nil
}

func (d *fakeDaemon) Flush(context.Context, *pb.FlushRequest) (*pb.FlushResponse, error) {
	return &pb.FlushResponse{}, nil
}

// Cap makes the request itself, exactly as slgod does: it holds the URL
// and has no idea that what comes back is inventory or a compiler's
// verdict.
func (d *fakeDaemon) Cap(ctx context.Context, r *pb.CapRequest) (*pb.CapResponse, error) {
	resp, err := d.grid.DoCap(ctx, agent.CapRequest{
		Cap: r.Cap, Path: r.Path, URL: r.Url, Method: r.Method,
		Type: r.ContentType, Body: r.Body,
	})
	if err != nil {
		return nil, err
	}
	return &pb.CapResponse{Status: int32(resp.Status), Body: resp.Body}, nil
}

// ------------------------------------------------------------ running

// commandLine is what the program was invoked with, put back
// afterwards: the arguments are the process's, and a test that left its
// own behind would be the next test's command line.
func commandLine(t *testing.T, args ...string) {
	t.Helper()
	os.Args = append([]string{"slrun"}, args...)
	t.Cleanup(func() { os.Args = []string{"slrun"} })
}

// script writes one to a file of the test's own and answers with the
// path, which is what slrun takes on its command line.
func script(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "a.lsl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// bothOf collects what a call printed on each stream.  slrun's result
// is stdout, one line per line the script said; standard error is where
// it says which avatar it chose, which is a remark and not a result, and
// keeping them apart is what makes that distinction testable.
func bothOf(t *testing.T, fn func()) (out, errOut string) {
	t.Helper()
	dir := t.TempDir()
	o, e := create(t, filepath.Join(dir, "stdout")), create(t, filepath.Join(dir, "stderr"))
	savedOut, savedErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = o, e
	fn()
	os.Stdout, os.Stderr = savedOut, savedErr
	o.Close()
	e.Close()
	return read(t, filepath.Join(dir, "stdout")), read(t, filepath.Join(dir, "stderr"))
}

func create(t *testing.T, path string) *os.File {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ------------------------------------------------------------- tests

// TestTheSharedObjectIsTakenAndSaidToHaveBeen: the default is the auto
// object, and the reason it is the default is seconds -- it is worn, so
// finding it costs a lookup, and the script inside it already exists,
// which is the eight seconds installing one into an empty object takes.
//
// Which avatar it ran as goes to standard error when nobody named one,
// because the daemon's default depends on its own history and nothing on
// disk records it: a reader who did not say cannot otherwise tell.
func TestTheSharedObjectIsTakenAndSaidToHaveBeen(t *testing.T) {
	reset(t)
	f, addr := newFakeDaemon(t)
	f.says = []string{"hello from the shared object"}
	commandLine(t, "--addr", addr, "-vv", script(t, "default {}"))

	var err error
	out, errOut := bothOf(t, func() { err = run() })

	if err != nil {
		t.Fatalf("run = %v", err)
	}
	if !strings.Contains(out, "hello from the shared object") {
		t.Errorf("what the script said was lost:\n%s", out)
	}
	if !strings.Contains(errOut, "running as quark") {
		t.Errorf("a run that chose its own avatar did not say which:\n%s", errOut)
	}
	if f.ran != 1 {
		t.Errorf("%d scripts ran", f.ran)
	}
}

// TestInterruptingARunSaysSoOnceAndFails: ^C during a run.
//
// The signal is raised from inside the fake as the script is uploaded,
// which is the one moment the program is certainly listening for one:
// run has installed its handler and has not taken it down again.  The
// script never says DONE, so without the interrupt this would sit here
// until the timeout.
//
// What is being pinned is the reporting, not the exit.  slrun already
// stopped and already exited non-zero -- measured, against the version
// before this -- but it said "context canceled" under the name of every
// script including the ones that never started, and nothing at all
// about having been interrupted.
func TestInterruptingARunSaysSoOnceAndFails(t *testing.T) {
	reset(t)
	f, addr := newFakeDaemon(t)
	f.silent = true // never says DONE, so the run would wait
	f.onRun = func() { syscall.Kill(os.Getpid(), syscall.SIGINT) }
	commandLine(t, "--addr", addr, script(t, "default {}"))

	var err error
	out, errOut := bothOf(t, func() { err = run() })

	if !errors.Is(err, errInterrupted) {
		t.Fatalf("run = %v, want errInterrupted", err)
	}
	if errors.Is(err, errScript) {
		t.Error("an interrupt was reported as a script failing")
	}
	for _, said := range []string{out, errOut} {
		if strings.Contains(said, "context canceled") {
			t.Errorf("the cancellation was reported as a script error:\n%s", said)
		}
	}
}

// TestOneScriptSaysWhatItSaidAndNothingElse: the plain run, which is
// the one almost every run is.  No name in front of the line, because
// there is nothing to tell it apart from, and nothing about which
// avatar lent the object.
func TestOneScriptSaysWhatItSaidAndNothingElse(t *testing.T) {
	reset(t)
	f, addr := newFakeDaemon(t)
	f.says = []string{"hello from the shared object"}
	path := script(t, "default {}")
	commandLine(t, "--addr", addr, path)

	var err error
	out, errOut := bothOf(t, func() { err = run() })

	if err != nil {
		t.Fatalf("run = %v", err)
	}
	if got := strings.TrimRight(out, "\n"); got != "hello from the shared object" {
		t.Errorf("a lone script printed %q, want the line and nothing else", got)
	}
	if strings.Contains(errOut, "running as") {
		t.Errorf("a run nobody asked to say more said which avatar anyway:\n%s", errOut)
	}
}

// TestTheFirstVAsksForTheNameAndNotForTheAvatar: the two levels are
// about different things -- the name is for reading the output, and
// which avatar is for how the run was arranged -- so one does not drag
// the other in with it.
func TestTheFirstVAsksForTheNameAndNotForTheAvatar(t *testing.T) {
	reset(t)
	f, addr := newFakeDaemon(t)
	f.says = []string{"hello from the shared object"}
	path := script(t, "default {}")
	commandLine(t, "--addr", addr, "-v", path)

	var err error
	out, errOut := bothOf(t, func() { err = run() })

	if err != nil {
		t.Fatalf("run = %v", err)
	}
	if !strings.Contains(out, filepath.Base(path)+":") {
		t.Errorf("-v did not put the script name in front of the line:\n%s", out)
	}
	if strings.Contains(errOut, "running as") {
		t.Errorf("one -v brought the avatar in as well:\n%s", errOut)
	}
}

// TestANamedObjectIsUsedAsItIsAndNotTidiedAway: --object is somebody
// else's object, so there is nothing to undo when the run ends -- and
// --keep says where the run happened, which is the only way to go and
// look at what it left behind.
func TestANamedObjectIsUsedAsItIsAndNotTidiedAway(t *testing.T) {
	reset(t)
	f, addr := newFakeDaemon(t)
	f.says = []string{"hello from a named prim"}
	commandLine(t, "--addr", addr, "--object", "a prim", "--keep", script(t, "default {}"))

	var err error
	out, _ := bothOf(t, func() { err = run() })

	if err != nil {
		t.Fatalf("run = %v", err)
	}
	if !strings.Contains(out, "running in ") {
		t.Errorf("--keep did not say where the run happened:\n%s", out)
	}
	if !strings.Contains(out, "hello from a named prim") {
		t.Errorf("what the script said was lost:\n%s", out)
	}
}

// TestAFailedScriptIsAFailedRunWhateverElseSucceeded: the original
// always exited 0, which left a caller no way to tell without scraping
// stdout -- and one script of several failing is still a run that did
// not do what it was asked.
func TestAFailedScriptIsAFailedRunWhateverElseSucceeded(t *testing.T) {
	reset(t)
	f, addr := newFakeDaemon(t)
	f.refuse = []string{"(1,1) : ERROR : Syntax error"}
	commandLine(t, "--addr", addr, "--object", "a prim", script(t, "not lsl at all"))

	var err error
	out, _ := bothOf(t, func() { err = run() })

	if err == nil {
		t.Fatal("a script that would not compile was reported as a run that worked")
	}
	if !strings.Contains(out, "ERROR : Syntax error") {
		t.Errorf("what Second Life said was not printed:\n%s", out)
	}
}

// TestAnObjectThatIsNotThereClosesTheSessionItOpened: the session is
// opened before the object is looked for, and one that turned out to be
// no use belongs to nobody -- leaving it open holds a stream on the
// daemon for as long as the program runs.
func TestAnObjectThatIsNotThereClosesTheSessionItOpened(t *testing.T) {
	reset(t)
	_, addr := newFakeDaemon(t)

	flags.Object = "no such workbench"
	_, _, err := runIn(context.Background(), session.Options{Addr: addr, Channel: "slrun"}, 1)
	if err == nil {
		t.Fatal("runIn found an object that is not in the region")
	}
	if !strings.Contains(err.Error(), "no such workbench") {
		t.Errorf("runIn = %v, want it to name what it could not find", err)
	}
}
