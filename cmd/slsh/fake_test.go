package main

// A grid, and a daemon, that are not there.
//
// Every command in this package is a function of a *sl.Session, and a
// session is a function of an sl.Backend -- so a fake backend is the
// difference between testing the commands and testing their flag
// parsing.  Without one a test can reach the option structs and the
// pure helpers and nothing else, which is why so much of this package
// was only ever exercised by hand against a live avatar.
//
// There is a second fake in pty_test.go and the two are not the same
// thing.  That one runs in the harness SUBPROCESS, where there is no
// *testing.T to hang a cleanup on and no coverage to collect; it exists
// so that a real terminal can be driven over a real pty.  This one runs
// in the test process, takes a *testing.T everywhere, and serves
// inventory and the other capabilities from httptest on loopback, which
// is what the commands actually read.
//
// newDaemonShell is the other half: the four commands that ask slgod
// rather than the grid -- agents, host, logout, status -- reach it
// through client.Conn, which is a gRPC client and cannot be faked at
// the Backend level at all.  So there is a real gRPC server, in this
// process, on loopback, exactly as sl/hosted_test.go does it.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/auth"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

var _ sl.Backend = (*fakeGrid)(nil)

// Who the fake says we are.  sl.New refuses a backend with no agent or
// session id, so these are load bearing rather than decoration.
var (
	testMe        = msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	testSessionID = msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	testRoot      = msg.MustParseUUID("23077e57-7e57-c0de-622e-77274d813d21")
	testObjects   = msg.MustParseUUID("a9a87e57-7e57-c0de-b748-062ee08c11ee")
	testScripts   = msg.MustParseUUID("aa8f7e57-7e57-c0de-e8da-278417da2fea")
	testTrash     = msg.MustParseUUID("ab6c7e57-7e57-c0de-e0ce-9f8b1da5cee8")
	testLamp      = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000001")
	testNote      = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000002")
	testProbe     = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000003")
	testSomebody  = msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000001")
)

// fakeGrid is an sl.Backend with nothing behind it.
//
// Every field is read under the lock, so a test may change an answer
// while the session is running -- an object that moves, a call that
// starts failing -- without racing the session's reader goroutine.
type fakeGrid struct {
	mu sync.Mutex

	info *sl.Info

	msgs     chan *sl.Message
	done     chan struct{}
	doneOnce sync.Once

	// sent is everything the session put on the wire, and onSend is
	// called with each one afterwards, without the lock, so that a
	// test can have the far end answer.
	sent   []msg.Message
	onSend func(msg.Message)

	presence *sl.Presence
	region   *sl.Region
	objects  []*sl.Seen
	friends  []sl.Friend

	// inv is the inventory tree, served over the capability rather
	// than answered from here: everything that reads inventory goes
	// through AIS, so a fake that short-circuited it would be testing
	// a path the shell does not take.
	inv *invDir

	// caps maps a capability name to the base URL serving it, and
	// lockedBy is who TryLock should say holds one rather than handing
	// it over.
	caps     map[string]string
	lockedBy string

	presenceErr, objectsErr, regionErr, friendsErr, sendErr, capErr error

	// presenceCalls counts how many times the avatar has been asked
	// where it is, and presenceFailAt is the one to refuse.  A command
	// that asks twice -- tp waits for the move and then reads the
	// position back -- has a second failure that cannot be reached by
	// refusing them all, because the first refusal ends the command.
	presenceCalls  int
	presenceFailAt int
}

// invDir is a folder in the fake inventory, and invItem a thing in one.
type invDir struct {
	ID    msg.UUID
	Name  string
	Type  int
	Dirs  []*invDir
	Items []*invItem
}

type invItem struct {
	ID      msg.UUID
	Name    string
	Type    int
	InvType int
	Asset   msg.UUID
	Desc    string
	Created int64
}

// newFakeGrid builds a backend that answers plausibly and reaches
// nothing.
//
// The answers are the ones a session standing in a region would get,
// because most commands ask for several of them on the way past and a
// test about one of them should not have to say so.
func newFakeGrid(t *testing.T) *fakeGrid {
	t.Helper()
	f := &fakeGrid{
		info: &sl.Info{
			Name:          "fake",
			AgentID:       testMe,
			SessionID:     testSessionID,
			AvatarName:    "Quark Idlemind",
			Region:        "Test Region",
			InventoryRoot: testRoot,
			Channel:       "slsh test 1.0",
			Caps:          []string{"SimulatorFeatures", "ViewerAsset", "LSLSyntax"},
		},
		msgs: make(chan *sl.Message),
		done: make(chan struct{}),
		caps: map[string]string{},
		presence: &sl.Presence{
			Position:     msg.Vector3{X: 128, Y: 128, Z: 25},
			LookAt:       msg.Vector3{X: 1},
			Camera:       msg.Vector3{X: 128, Y: 128, Z: 26},
			Region:       "Test Region",
			RegionHandle: 1099511628032,
		},
		region: &sl.Region{
			ID:          msg.MustParseUUID("a4fd7e57-7e57-c0de-80da-1b63ae00812a"),
			Name:        "Test Region",
			Handle:      1099511628032,
			Access:      13,
			WaterHeight: 20,
			ProductName: "Estate / Full Region",
		},
		inv: &invDir{
			ID: testRoot, Name: "My Inventory",
			Dirs: []*invDir{
				{ID: testObjects, Name: "Objects", Type: 6, Items: []*invItem{
					{ID: testLamp, Name: "a lamp", Type: int(sl.AssetObject), Created: 1754000000},
				}},
				{ID: testScripts, Name: "Scripts", Type: 10, Items: []*invItem{
					{ID: testProbe, Name: "probe", Type: int(sl.AssetLSLText), Created: 1754000100},
				}},
				{ID: testTrash, Name: "Trash", Type: sl.FolderTrash},
			},
			Items: []*invItem{
				{ID: testNote, Name: "readme", Type: int(sl.AssetNotecard), Created: 1754000200},
			},
		},
	}
	t.Cleanup(func() { f.Close() })
	f.serveInventory(t)
	return f
}

// serveInventory puts the tree behind the AIS capability.
func (f *fakeGrid) serveInventory(t *testing.T) {
	t.Helper()
	f.ServeCap(t, agent.InventoryCap, func(w http.ResponseWriter, r *http.Request) {
		id := capFolderID(r.URL.Path)
		depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))

		f.mu.Lock()
		dir := findDir(f.inv, id)
		f.mu.Unlock()
		if dir == nil {
			http.Error(w, "no such folder", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<?xml version="1.0" ?><llsd>`+dirLLSD(dir, testRoot, depth)+`</llsd>`)
	})
}

// ServeCap points a capability at an http server that lives as long as
// the test.  Everything that reaches a capability goes through DoCap,
// so this is how those calls are exercised: httptest listens on
// loopback, which is not the network and does not need one.
func (f *fakeGrid) ServeCap(t *testing.T, name string, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	f.mu.Lock()
	f.caps[name] = s.URL
	f.mu.Unlock()
	return s
}

// capFolderID is the folder an inventory request is about, out of a
// path of the form /category/<folder>/children.
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

// dirLLSD is what AIS says about one folder, nesting its children as
// far as the depth asked for.
//
// The nesting is not decoration: AIS takes a depth on the request and
// answers the whole subtree in one reply, so a fake that flattened it
// would make ls -r fetch a folder at a time and pass for the wrong
// reason.
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
		fmt.Fprintf(&b, `<key>asset_id</key><string>%s</string>`, it.Asset)
		fmt.Fprintf(&b, `<key>name</key><string>%s</string>`, xmlText(it.Name))
		fmt.Fprintf(&b, `<key>desc</key><string>%s</string>`, xmlText(it.Desc))
		fmt.Fprintf(&b, `<key>type</key><integer>%d</integer>`, it.Type)
		fmt.Fprintf(&b, `<key>inv_type</key><integer>%d</integer>`, it.InvType)
		fmt.Fprintf(&b, `<key>created_at</key><integer>%d</integer>`, it.Created)
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

// ---------------------------------------------------------- assertions

// Sent is everything the session has put on the wire, in order.
func (f *fakeGrid) Sent() []msg.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]msg.Message(nil), f.sent...)
}

// Relay hands a message to the session as though the grid had sent it.
//
// Unlike the one in package sl this does not wait for the reader: the
// only thing here that watches the relay is the chat printer, and what
// it does lands on the terminal rather than in the session, so a test
// waits for the line to appear instead.
func (f *fakeGrid) Relay(t *testing.T, m msg.Message) {
	t.Helper()
	body, err := m.Encode()
	if err != nil {
		t.Fatalf("encoding %s: %v", m.MsgInfo().Name, err)
	}
	select {
	case f.msgs <- &sl.Message{ID: msg.IDOf(m), Name: m.MsgInfo().Name, Body: body, At: time.Now()}:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read the relay: is a session attached to this backend?")
	}
}

// AnswerNames makes the fake reply to UUIDNameRequest the way a
// simulator does.  Several calls ask for names and then wait for them,
// so a backend that never answers turns each into a three second pause.
func (f *fakeGrid) AnswerNames(t *testing.T, names map[msg.UUID]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		q, ok := m.(*msg.UUIDNameRequest)
		if !ok {
			return
		}
		r := &msg.UUIDNameReply{}
		for _, b := range q.UUIDNameBlock {
			n, ok := names[b.ID]
			if !ok {
				continue
			}
			first, last, _ := strings.Cut(n, " ")
			r.UUIDNameBlock = append(r.UUIDNameBlock, msg.UUIDNameReply_UUIDNameBlock{
				ID:        b.ID,
				FirstName: append([]byte(first), 0),
				LastName:  append([]byte(last), 0),
			})
		}
		if len(r.UUIDNameBlock) > 0 {
			f.Relay(t, r)
		}
	}
}

// ------------------------------------------------------------- backend

func (f *fakeGrid) Info() *sl.Info { return f.info }

func (f *fakeGrid) Send(ctx context.Context, m msg.Message, reliable bool) error {
	f.mu.Lock()
	err, onSend := f.sendErr, f.onSend
	if err == nil {
		f.sent = append(f.sent, m)
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if onSend != nil {
		onSend(m)
	}
	return nil
}

func (f *fakeGrid) Messages() <-chan *sl.Message { return f.msgs }
func (f *fakeGrid) Done() <-chan struct{}        { return f.done }
func (f *fakeGrid) Err() error                   { return nil }

func (f *fakeGrid) Presence(ctx context.Context, drawDistance float32) (*sl.Presence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.presenceCalls++
	if f.presenceErr != nil {
		return nil, f.presenceErr
	}
	if f.presenceFailAt == f.presenceCalls {
		return nil, fmt.Errorf("the circuit went away mid-command")
	}
	p := *f.presence
	return &p, nil
}

func (f *fakeGrid) Objects(ctx context.Context, named, id string) ([]*sl.Seen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objectsErr != nil {
		return nil, f.objectsErr
	}
	out := make([]*sl.Seen, 0, len(f.objects))
	for _, o := range f.objects {
		if id != "" && !strings.EqualFold(o.ID.String(), id) {
			continue
		}
		if named != "" && o.Name != named {
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

func (f *fakeGrid) Region(ctx context.Context) (*sl.Region, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.regionErr != nil {
		return nil, false, f.regionErr
	}
	return f.region, true, nil
}

func (f *fakeGrid) Flush(ctx context.Context) (int, error) { return 0, nil }

func (f *fakeGrid) Friends(ctx context.Context) ([]sl.Friend, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.friendsErr != nil {
		return nil, f.friendsErr
	}
	return append([]sl.Friend(nil), f.friends...), nil
}

func (f *fakeGrid) NoteFriend(ctx context.Context, id msg.UUID, online bool) error { return nil }
func (f *fakeGrid) Lock(ctx context.Context, name string) error                    { return nil }
func (f *fakeGrid) Unlock(name string) error                                       { return nil }

// TryLock hands over whatever is asked for unless lockedBy says
// somebody else has it, which is how a command that has to wait its
// turn is made to find the turn taken.
func (f *fakeGrid) TryLock(ctx context.Context, name string) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockedBy != "" {
		return false, f.lockedBy, nil
	}
	return true, "", nil
}

func (f *fakeGrid) HasCap(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.caps[name]
	return ok
}

func (f *fakeGrid) DoCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	f.mu.Lock()
	err, base := f.capErr, f.caps[r.Cap]
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	url := r.URL
	if url == "" {
		if base == "" {
			return nil, fmt.Errorf("fake: no %s capability", r.Cap)
		}
		url = base + r.Path
	}
	method := r.Method
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(r.Body))
	if err != nil {
		return nil, err
	}
	if r.Type != "" {
		req.Header.Set("Content-Type", r.Type)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return &agent.CapResponse{Status: resp.StatusCode, Body: body}, nil
}

func (f *fakeGrid) Close() error {
	f.doneOnce.Do(func() {
		close(f.msgs)
		close(f.done)
	})
	return nil
}

// --------------------------------------------------------- the shell

// screenBuffer is what the terminal wrote, safe to read while the
// shell's own goroutines are still writing to it.
type screenBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *screenBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *screenBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func (s *screenBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}

// testShell is one shell over a grid that is not there, with what the
// terminal printed kept in a buffer.
//
// The terminal is a plain one -- a pipe, as far as it is concerned --
// because the escape codes a real one writes would have to be stripped
// out of every assertion.  term_test.go covers the drawing; this is for
// what the commands say.
type testShell struct {
	*Shell
	grid *fakeGrid
	keys chan rune
	out  *screenBuffer
}

func newTestShell(t *testing.T) *testShell {
	t.Helper()
	return newTestShellOn(t, newFakeGrid(t), Config{Addr: "fake:7807", Prefix: 27})
}

func newTestShellOn(t *testing.T, b sl.Backend, cfg Config) *testShell {
	t.Helper()
	s, err := sl.New(b)
	if err != nil {
		t.Fatalf("sl.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	out := &screenBuffer{}
	keys := make(chan rune, 64)
	term := &Term{out: out, keys: keys, done: make(chan struct{}), plain: true, width: 80, height: 24}
	t.Cleanup(term.Close)

	x := &testShell{Shell: NewShell(cfg, term, s), keys: keys, out: out}
	x.grid, _ = b.(*fakeGrid)
	return x
}

// do runs a command line the way Enter would and returns everything
// that reached the terminal, which is what somebody watching would have
// seen -- output and complaint alike.
func (x *testShell) do(t *testing.T, line string) string {
	t.Helper()
	x.out.Reset()
	x.Do(context.Background(), line)
	return x.out.String()
}

// ------------------------------------------------------------- daemon

// fakeDaemon is a slgod with nothing behind it.
//
// It answers the four management calls from fields, which is all a
// client can tell about a daemon anyway: every answer it gives came
// from a simulator it is not obliged to have.
type fakeDaemon struct {
	pb.UnimplementedGridServer

	info   *pb.AgentInfo
	agents []*pb.AgentInfo
	status *pb.StatusResponse

	host   *pb.HostResponse
	logout *pb.LogoutResponse

	// auth answers the login handshake, for a daemon that is dialled
	// rather than handed a connection, and relay is what to push down
	// the stream once one is open.
	auth  *auth.Server
	relay chan *pb.ServerPacket

	// hangUp ends the stream as soon as it is attached, which is a
	// daemon going away under a client that is watching it, and
	// attachFail refuses the attach outright -- a daemon that is not
	// holding the session that was asked for.
	hangUp     bool
	attachFail error

	// fail, when set, is what Host, Logout, ListAgents and Status
	// answer with, which is how the error half of each is reached.
	fail error
}

func (d *fakeDaemon) Stream(s grpc.BidiStreamingServer[pb.ClientPacket, pb.ServerPacket]) error {
	first, err := s.Recv()
	if err != nil {
		return err
	}
	if first.GetAttach() == nil {
		return fmt.Errorf("the first packet was not an attach")
	}
	if d.attachFail != nil {
		return d.attachFail
	}
	if err := s.Send(&pb.ServerPacket{Body: &pb.ServerPacket_Attached{
		Attached: &pb.Attached{Agent: d.info},
	}}); err != nil {
		return err
	}
	if d.hangUp {
		return nil
	}
	// Everything the client says is read and thrown away, and
	// anything the test wants relayed goes down the stream.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := s.Recv(); err != nil {
				return
			}
		}
	}()
	for {
		select {
		case p := <-d.relay:
			if err := s.Send(p); err != nil {
				return err
			}
		case <-done:
			return nil
		case <-s.Context().Done():
			return s.Context().Err()
		}
	}
}

func (d *fakeDaemon) ListAgents(context.Context, *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return &pb.ListAgentsResponse{Agents: d.agents}, nil
}

func (d *fakeDaemon) Status(context.Context, *pb.StatusRequest) (*pb.StatusResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return d.status, nil
}

func (d *fakeDaemon) Host(context.Context, *pb.HostRequest) (*pb.HostResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return d.host, nil
}

// Logout answers the way slgod does, response and error together, which
// is the shape TestLogoutRefusalCannotNameWhoIsUsingIt is about.
func (d *fakeDaemon) Logout(context.Context, *pb.LogoutRequest) (*pb.LogoutResponse, error) {
	if d.fail != nil {
		return d.logout, d.fail
	}
	return d.logout, nil
}

func (d *fakeDaemon) Presence(context.Context, *pb.PresenceRequest) (*pb.PresenceResponse, error) {
	return &pb.PresenceResponse{Region: "Test Region"}, nil
}

func (d *fakeDaemon) Objects(context.Context, *pb.ObjectsRequest) (*pb.ObjectsResponse, error) {
	return &pb.ObjectsResponse{}, nil
}

// newDaemonShell is a shell whose session is held by a daemon running
// in this process, for the commands that ask slgod rather than the grid.
//
// The dial options are what skip the shared-secret handshake, which
// needs a file on disk that a test has no business reading.
func newDaemonShell(t *testing.T) (*testShell, *fakeDaemon) {
	t.Helper()
	d := &fakeDaemon{
		info: &pb.AgentInfo{
			Name: "fake", AgentId: testMe.String(), SessionId: testSessionID.String(),
			AvatarName: "Quark Idlemind", Region: "Test Region",
			InventoryRoot: testRoot.String(), ChannelVersion: "slgo test 1.0",
		},
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening on loopback: %v", err)
	}
	srv := grpc.NewServer()
	pb.RegisterGridServer(srv, d)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := client.Dial(ctx, lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })

	h, err := sl.AttachConn(ctx, conn, "fake")
	if err != nil {
		t.Fatalf("AttachConn: %v", err)
	}
	t.Cleanup(func() { h.Close() })

	return newTestShellOn(t, h, Config{Addr: lis.Addr().String(), Prefix: 27}), d
}

// ---------------------------------------------------- a daemon to dial

// newAuthDaemon starts a daemon a client may DIAL rather than one
// handed an open connection, and answers with the address to dial.
//
// The difference is the handshake.  client.Dial with no options does
// the real thing -- TLS, and a challenge each way over a shared secret
// read from disk -- and the two places that dial rather than being
// given a connection are the two that cannot be reached without it:
// watch, which opens a second connection of its own, and run, which
// opens the first.  So there is a secret in a home directory of the
// test's own and a server that knows the same one.
func newAuthDaemon(t *testing.T) (*fakeDaemon, string) {
	t.Helper()

	home := t.TempDir()
	dir := filepath.Join(home, ".config", "slrun")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "a shared secret for a test"
	if err := os.WriteFile(filepath.Join(dir, "secret"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)

	a, err := auth.New(secret)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{
		auth: a,
		info: &pb.AgentInfo{
			Name: "fake", AgentId: testMe.String(), SessionId: testSessionID.String(),
			AvatarName: "Quark Idlemind", Region: "Test Region",
			InventoryRoot: testRoot.String(),
		},
		relay: make(chan *pb.ServerPacket, 8),
	}

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

	return d, lis.Addr().String()
}

// Login is the daemon's half of the handshake, which is the server's
// own code with the connection bookkeeping left out.
func (d *fakeDaemon) Login(ctx context.Context, req *pb.LoginRequest) (*pb.LoginResponse, error) {
	binding, err := auth.BindingFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if len(req.GetProof()) == 0 {
		challenge, err := d.auth.Begin(req.GetClient())
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
