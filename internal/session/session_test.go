package session

// A grid, and a daemon, that are not there.
//
// Everything here is a function of a *sl.Session, and a session is a
// function of an sl.Backend -- so a fake backend is the difference
// between testing this package and testing its arithmetic.  Without one
// a test can reach AutoName and nothing else, which
// is why the whole of it was only ever exercised by slbench against a
// live avatar.
//
// There are two fakes and they are not the same thing.  fakeGrid is an
// sl.Backend: it answers what the grid would answer, serves inventory
// from httptest because inventory is AIS and not a message, and is what
// everything taking a session is tested through.  fakeDaemon is a real
// gRPC server on loopback, for the one call that cannot be faked at the
// Backend level at all -- Connect, which dials.  UseAutoAnywhere dials
// too, once per avatar it considers, and its tests replace dialFor.
//
// Neither can stand in for a grid.  What is checked is the decisions
// this package makes, not what a simulator would have done about them.

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
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

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/auth"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

var _ sl.Backend = (*fakeGrid)(nil)

// Who the fake says we are.  sl.New refuses a backend with no agent or
// session id, so these are load bearing rather than decoration.
var (
	testMe        = msg.MustParseUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")
	testSessionID = msg.MustParseUUID("72427e57-7e57-c0de-39d2-e78c47465eb6")
	testRoot      = msg.MustParseUUID("23077e57-7e57-c0de-245c-d6b83f1a8b6d")
	testObjects   = msg.MustParseUUID("a9a87e57-7e57-c0de-b748-062ee08c11ee")
	testTrash     = msg.MustParseUUID("aa8f7e57-7e57-c0de-e8da-278417da2fea")
	thePrim       = msg.MustParseUUID("89ad7e57-7e57-c0de-08a1-04b25f97cc85")

	// testRegion names the region the fake is in.  sl sends a local id
	// found by looking an object up only while the region it was found
	// in is named and still the avatar's.
	testRegion = msg.MustParseUUID("a4fd7e57-7e57-c0de-559f-7a9b7da6044a")
)

// autoItemID is the inventory id of the nth auto object, made up rather
// than random so that a failure names the same thing twice.
func autoItemID(n int) msg.UUID {
	return msg.MustParseUUID(fmt.Sprintf("c75d7e57-7e57-c0de-b372-%012d", n))
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
	ID   msg.UUID
	Name string
	Type int
}

// fakeGrid is an sl.Backend with nothing behind it.
//
// Every field is read under the lock, so a test may change an answer
// while the session is running -- an item that appears, a send that
// starts failing -- without racing the session's reader goroutine.
type fakeGrid struct {
	mu sync.Mutex

	info *sl.Info

	msgs     chan *sl.Message
	done     chan struct{}
	doneOnce sync.Once

	// sent is everything the session put on the wire, and onSend is
	// called with each one afterwards, without the lock, so that a test
	// can have the far end answer.
	sent   []msg.Message
	onSend func(msg.Message)

	presence *sl.Presence
	objects  []*sl.Seen

	// afterObjects, when set, is called with the lock held once each
	// answer to Objects has been made: what changes there is seen by
	// the next look and not this one.
	afterObjects func()

	presenceErr, objectsErr, sendErr, capErr error

	// inv is the inventory tree, served over the capability rather than
	// answered from here: everything that reads inventory goes through
	// AIS, so a fake that short-circuited it would be testing a path
	// this package does not take.
	inv *invDir

	// hideRead makes a chosen reading of a folder answer empty, counted
	// from one.  An account that has never had an auto object has to be
	// told apart from one that has, and the difference is only visible
	// in which look at the folder is which.
	hideRead func(folder msg.UUID, nth int) bool
	reads    map[msg.UUID]int

	caps map[string]string

	// capCalls counts the capability requests made, answered or not.
	capCalls int
}

// newFakeGrid builds a backend that answers plausibly and reaches
// nothing.
func newFakeGrid(t *testing.T) *fakeGrid {
	t.Helper()
	f := &fakeGrid{
		info: &sl.Info{
			Name:          "quark",
			AgentID:       testMe,
			SessionID:     testSessionID,
			AvatarName:    "Quark Idlemind",
			Region:        "Test Region",
			InventoryRoot: testRoot,
		},
		msgs:  make(chan *sl.Message),
		done:  make(chan struct{}),
		reads: map[msg.UUID]int{},
		caps:  map[string]string{},
		presence: &sl.Presence{
			Position: msg.Vector3{X: 128, Y: 128, Z: 25},
			LookAt:   msg.Vector3{X: 1},
			Camera:   msg.Vector3{X: 128, Y: 128, Z: 26},
			Region:   "Test Region",
		},
		inv: &invDir{
			ID: testRoot, Name: "My Inventory",
			Dirs: []*invDir{
				{ID: testObjects, Name: "Objects", Type: 6},
				{ID: testTrash, Name: "Trash", Type: sl.FolderTrash},
			},
		},
	}
	t.Cleanup(func() { f.Close() })
	f.serveInventory(t)
	return f
}

// newFakeSession is a real sl.Session over a fake backend, with the
// reader goroutine running.
func newFakeSession(t *testing.T) (*sl.Session, *fakeGrid) {
	t.Helper()
	f := newFakeGrid(t)
	s, err := sl.New(f)
	if err != nil {
		t.Fatalf("sl.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, f
}

// stock fills the Objects folder with the first n auto items and puts
// each of them on, which is the state an avatar is in after a setup and
// so the state most of these start from.
func (f *fakeGrid) stock(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dir := findDir(f.inv, testObjects)
	dir.Items = nil
	f.objects = nil
	for i := 0; i < n; i++ {
		id := autoItemID(i)
		dir.Items = append(dir.Items, &invItem{ID: id, Name: AutoName(i), Type: int(sl.AssetObject)})
		f.objects = append(f.objects, &sl.Seen{
			Object:      sl.Object{ID: msg.UUID(id), Local: uint32(100 + i)},
			AttachItem:  id,
			AttachPoint: AutoPoints[i] &^ sl.AttachAdd,
		})
	}
}

// serveInventory puts the tree behind the AIS capability.
func (f *fakeGrid) serveInventory(t *testing.T) {
	t.Helper()
	f.ServeCap(t, agent.InventoryCap, func(w http.ResponseWriter, r *http.Request) {
		id := capFolderID(r.URL.Path)
		depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))

		f.mu.Lock()
		f.reads[id]++
		dir := findDir(f.inv, id)
		if dir != nil && f.hideRead != nil && f.hideRead(id, f.reads[id]) {
			dir = &invDir{ID: dir.ID, Name: dir.Name, Type: dir.Type}
		}
		body := ""
		if dir != nil {
			body = dirLLSD(dir, testRoot, depth)
		}
		f.mu.Unlock()

		if body == "" {
			http.Error(w, "no such folder", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<?xml version="1.0" ?><llsd>`+body+`</llsd>`)
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

// dirLLSD is what AIS says about one folder, nesting its children as far
// as the depth asked for.
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
		fmt.Fprintf(&b, `<key>type</key><integer>%d</integer>`, it.Type)
		fmt.Fprintf(&b, `<key>inv_type</key><integer>%d</integer>`, it.Type)
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

// ------------------------------------------------------------- backend

func (f *fakeGrid) Info() *sl.Info { return f.info }

// Refresh is Backend's; nothing here rebuilds a session underneath, so
// the identity it hands back is the one it has.
func (f *fakeGrid) Refresh(context.Context) (*sl.Info, error) { return f.Info(), nil }

// Control is nothing here.  Nothing this fake stands in for sits
// down or stands up; the method exists because sl.Backend has it,
// so that the one place an AgentUpdate is built stays the one place
// that owns the camera.
func (f *fakeGrid) Control(ctx context.Context, flags uint32) error { return nil }

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

// Sent is everything the session has put on the wire, in order.
func (f *fakeGrid) Sent() []msg.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]msg.Message(nil), f.sent...)
}

func (f *fakeGrid) Messages() <-chan *sl.Message { return f.msgs }

// Events is nothing.  A nil channel blocks for ever, which is what a
// backend with no event queue behind it honestly offers: the session
// reads it alongside the circuit and must not take silence there for
// the session ending.
func (f *fakeGrid) Events() <-chan *sl.QueueEvent { return nil }
func (f *fakeGrid) Done() <-chan struct{}         { return f.done }
func (f *fakeGrid) Err() error                    { return nil }

// RegionChanges is never told of one: nothing here teleports.
func (f *fakeGrid) RegionChanges() <-chan *sl.RegionChange { return nil }

// SimAttachments: this fake has never heard an appearance.
func (f *fakeGrid) SimAttachments(ctx context.Context, avatar msg.UUID) (*sl.SimAttachments, error) {
	return nil, nil
}

func (f *fakeGrid) Presence(ctx context.Context, drawDistance float32) (*sl.Presence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.presenceErr != nil {
		return nil, f.presenceErr
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
	if f.afterObjects != nil {
		f.afterObjects()
	}
	return out, nil
}

func (f *fakeGrid) Land(ctx context.Context) (*sl.Land, error) {
	return &sl.Land{Overlay: agent.OverlayFrom(nil, 0)}, nil
}

// Ground knows no land: none has arrived.
func (f *fakeGrid) Ground(ctx context.Context, west, south, east, north float32) (float32, bool, error) {
	return 0, false, nil
}

func (f *fakeGrid) Region(ctx context.Context) (*sl.Region, bool, error) {
	return &sl.Region{ID: testRegion, Name: "Test Region"}, true, nil
}

func (f *fakeGrid) Neighbours(ctx context.Context, set *bool) (*sl.Neighbours, error) {
	return &sl.Neighbours{}, nil
}

func (f *fakeGrid) Flush(ctx context.Context) (int, error)           { return 0, nil }
func (f *fakeGrid) Friends(ctx context.Context) ([]sl.Friend, error) { return nil, nil }

func (f *fakeGrid) NoteFriend(ctx context.Context, id msg.UUID, online bool) error { return nil }

// Lock, Unlock and TryLock are an sl.Backend's, and nothing in this
// package takes a lock: the daemon hands out places instead.
func (f *fakeGrid) Lock(ctx context.Context, name string) error { return nil }
func (f *fakeGrid) Unlock(name string) error                    { return nil }
func (f *fakeGrid) TryLock(ctx context.Context, name string) (bool, string, error) {
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
	f.capCalls++
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

// listsSessions is a fakeGrid that also answers what else the daemon is
// holding, which is what makes it a HOSTED session as far as
// sessionNames is concerned.  A direct one is not, and must not be
// mistaken for a daemon with one avatar.
type listsSessions struct {
	*fakeGrid
	names []string
	err   error
}

func (l *listsSessions) Sessions(context.Context) ([]string, error) { return l.names, l.err }

// ------------------------------------------------------ a real daemon

// fakeDaemon is a slgod with nothing behind it, on loopback, for
// Connect, which dials.
//
// It has to be dialled rather than handed a connection, so it does the
// whole handshake: TLS, and a challenge each way over a secret in a home
// directory the test owns.  Everything else it answers is the least a
// session needs to get as far as finding its objects already worn --
// which is the state this package is usually in.
type fakeDaemon struct {
	pb.UnimplementedGridServer

	mu sync.Mutex

	auth *auth.Server

	// agents is who it is holding, in its own order, and info describes
	// one of them by name.
	agents []string

	// refuseAttach names avatars this daemon will not attach to, which
	// is a session it is not holding.
	refuseAttach map[string]bool

	// cap is where capability requests are sent, so that inventory is
	// read over http exactly as it is in the world, and capFail is a
	// session whose inventory cannot be read at all.
	cap     string
	capFail bool

	// objects is what every session says is worn, keyed by nothing: the
	// avatars here are interchangeable.
	objects []*pb.ObjectInfo
}

// agentInfo describes one avatar.  The ids are the same for all of them
// because nothing here tells avatars apart by id, only by name.
func (d *fakeDaemon) agentInfo(name string) *pb.AgentInfo {
	if name == "" {
		d.mu.Lock()
		if len(d.agents) > 0 {
			name = d.agents[0]
		}
		d.mu.Unlock()
	}
	return &pb.AgentInfo{
		Name: name, AgentId: testMe.String(), SessionId: testSessionID.String(),
		AvatarName: "Quark Idlemind", Region: "Test Region",
		InventoryRoot: testRoot.String(), Caps: []string{agent.InventoryCap},
	}
}

// newFakeDaemon starts one and answers with the address to dial.
//
// It writes to $HOME, because that is where the shared secret has to be
// for the client to find it, so nothing using this may run in parallel.
func newFakeDaemon(t *testing.T) (*fakeDaemon, string) {
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
	// these tests are about what happens when nobody names one.
	t.Setenv(sl.EnvAgent, "")

	a, err := auth.New(secret)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{
		auth:         a,
		agents:       []string{"quark"},
		refuseAttach: map[string]bool{},
	}
	d.serveInventory(t)

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

// serveInventory gives the daemon an Objects folder holding a full set
// of auto items, all of them worn.  That is the state an avatar that has
// been set up is in, and it is what makes taking objects cost nothing
// but the asking.
func (d *fakeDaemon) serveInventory(t *testing.T) {
	t.Helper()
	root := &invDir{
		ID: testRoot, Name: "My Inventory",
		Dirs: []*invDir{{ID: testObjects, Name: "Objects", Type: 6}},
	}
	objects := root.Dirs[0]
	for i := range AutoPoints {
		id := autoItemID(i)
		objects.Items = append(objects.Items,
			&invItem{ID: id, Name: AutoName(i), Type: int(sl.AssetObject)})
		d.objects = append(d.objects, &pb.ObjectInfo{
			Id: id.String(), Local: uint32(100 + i),
			AttachItem: id.String(), AttachPoint: uint32(AutoPoints[i] &^ sl.AttachAdd),
		})
	}

	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := capFolderID(r.URL.Path)
		depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
		dir := findDir(root, id)
		if dir == nil {
			http.Error(w, "no such folder", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<?xml version="1.0" ?><llsd>`+dirLLSD(dir, testRoot, depth)+`</llsd>`)
	}))
	t.Cleanup(s.Close)
	d.cap = s.URL
}

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

func (d *fakeDaemon) Stream(s grpc.BidiStreamingServer[pb.ClientPacket, pb.ServerPacket]) error {
	first, err := s.Recv()
	if err != nil {
		return err
	}
	att := first.GetAttach()
	if att == nil {
		return fmt.Errorf("the first packet was not an attach")
	}
	d.mu.Lock()
	refused := d.refuseAttach[att.Agent]
	d.mu.Unlock()
	if refused {
		return fmt.Errorf("no session called %q is hosted here", att.Agent)
	}

	who := d.agentInfo(att.Agent)
	if err := s.Send(&pb.ServerPacket{Body: &pb.ServerPacket_Attached{
		Attached: &pb.Attached{Agent: who},
	}}); err != nil {
		return err
	}

	// Held open until the client goes: nothing it sends needs an answer.
	for {
		if _, err := s.Recv(); err != nil {
			return nil
		}
	}
}

func (d *fakeDaemon) ListAgents(context.Context, *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]*pb.AgentInfo, 0, len(d.agents))
	for _, n := range d.agents {
		out = append(out, &pb.AgentInfo{Name: n})
	}
	return &pb.ListAgentsResponse{Agents: out}, nil
}

func (d *fakeDaemon) Presence(context.Context, *pb.PresenceRequest) (*pb.PresenceResponse, error) {
	return &pb.PresenceResponse{
		Region:   "Test Region",
		Position: &pb.Vector3{X: 128, Y: 128, Z: 25},
	}, nil
}

func (d *fakeDaemon) Objects(context.Context, *pb.ObjectsRequest) (*pb.ObjectsResponse, error) {
	return &pb.ObjectsResponse{Objects: d.objects}, nil
}

func (d *fakeDaemon) Region(context.Context, *pb.RegionRequest) (*pb.RegionInfo, error) {
	return &pb.RegionInfo{Name: "Test Region", Known: true}, nil
}

// Cap does the http itself, exactly as slgod does: it holds the URL and
// makes the request, and has no idea that what comes back is inventory.
func (d *fakeDaemon) Cap(ctx context.Context, r *pb.CapRequest) (*pb.CapResponse, error) {
	if d.capFail {
		return nil, fmt.Errorf("the capability is not answering")
	}
	if r.Cap != agent.InventoryCap {
		return nil, fmt.Errorf("no %s capability", r.Cap)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", d.cap+r.Path, nil)
	if err != nil {
		return nil, err
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
	return &pb.CapResponse{Status: int32(resp.StatusCode), Body: body}, nil
}

// ------------------------------------------------------- what is where

// TestAgentNameIsAskedRatherThanTested: this package is the one place
// that decides what "which avatar" means, and it defers to sl so that no
// two commands can disagree about whether the environment counts.
func TestAgentNameIsAskedRatherThanTested(t *testing.T) {
	t.Setenv(sl.EnvAgent, "  from the environment  ")
	if got := AgentName("named"); got != "named" {
		t.Errorf("AgentName = %q, want what was named to win", got)
	}
	if got := AgentName(""); got != "from the environment" {
		t.Errorf("AgentName = %q, want the environment's answer, trimmed", got)
	}
}

// TestTheFirstAutoObjectKeepsTheBareName: an account that has only ever
// run one benchmark at a time already has an object called "auto", and
// numbering it retrospectively would ask that account to grow a second
// object it will never use.
func TestTheFirstAutoObjectKeepsTheBareName(t *testing.T) {
	t.Parallel()
	if got := AutoName(0); got != AutoObject {
		t.Errorf("AutoName(0) = %q, want the bare name", got)
	}
	// The numbers a person sees count from one, so the second object is
	// "auto 2" rather than "auto 1".
	if got := AutoName(1); got != "auto 2" {
		t.Errorf("AutoName(1) = %q", got)
	}
}

// TestThePoolIsAsBigAsThePointsThereAre: a place is a number, and the
// numbers run as far as there are points to wear an object on.  A daemon
// that believed in more places than this would hand out one nothing here
// can wear.
func TestThePoolIsAsBigAsThePointsThereAre(t *testing.T) {
	t.Parallel()
	if AutoPool() != len(AutoPoints) {
		t.Errorf("AutoPool = %d over %d points", AutoPool(), len(AutoPoints))
	}
}

// TestAutoPointsIsAppendOnly: a slot is identified by its INDEX -- that
// is what slgod grants and what one program tells another -- so
// reordering this list makes two versions disagree about which object
// slot five is, and nothing anywhere detects it.
func TestAutoPointsIsAppendOnly(t *testing.T) {
	t.Parallel()
	want := []int{
		sl.HUDBottomLeft, sl.HUDBottom, sl.HUDBottomRight, sl.HUDTopLeft,
		sl.HUDTop, sl.HUDTopRight, sl.HUDCenter1, sl.HUDCenter2,
		sl.HUDBottomLeft, sl.HUDBottom, sl.HUDBottomRight, sl.HUDTopLeft,
	}
	if len(AutoPoints) < len(want) {
		t.Fatalf("AutoPoints has shrunk to %d entries", len(AutoPoints))
	}
	for i, p := range want {
		if AutoPoints[i] != p {
			t.Errorf("slot %d is attachment point %d, want %d: this list is append-only",
				i, AutoPoints[i], p)
		}
	}
	if len(AutoPoints)%AutoGroupSize != 0 {
		t.Errorf("%d points do not divide into groups of %d, so the last group is short",
			len(AutoPoints), AutoGroupSize)
	}
}

// ------------------------------------------------------------- connect

// TestConnectRefusesAnAvatarNameWithoutDirect: through slgod the session
// already knows who it is, so --first and --last are not a way of
// choosing one -- and silently ignoring them would attach to whatever
// the daemon felt like and report on the wrong avatar.
func TestConnectRefusesAnAvatarNameWithoutDirect(t *testing.T) {
	t.Parallel()
	for _, o := range []Options{{First: "Quark"}, {Last: "Idlemind"}} {
		if _, err := Connect(context.Background(), o); err == nil {
			t.Errorf("Connect accepted %+v without --direct", o)
		} else if !strings.Contains(err.Error(), "--direct") {
			t.Errorf("Connect = %v, want it to say where those flags belong", err)
		}
	}
}

// TestConnectAttachesToTheDaemonItWasPointedAt: an address given on a
// command line is the operator saying where to go, so nothing else is
// asked -- and what comes back is a session that knows which avatar it
// got.
func TestConnectAttachesToTheDaemonItWasPointedAt(t *testing.T) {
	d, addr := newFakeDaemon(t)
	d.agents = []string{"quark"}

	s, err := Connect(context.Background(), Options{Addr: addr})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer s.Close()
	if s.Info().Name != "quark" {
		t.Errorf("attached to %q", s.Info().Name)
	}
}

// TestConnectSaysThatDirectIsTheAlternative: a daemon that is not
// running is the ordinary way this fails, and the reader is one command
// away from not needing it at all -- which is worth saying, because
// "connection refused" does not suggest it.
func TestConnectSaysThatDirectIsTheAlternative(t *testing.T) {
	// A home directory with a secret in it, so that the failure is the
	// connection rather than the missing file.
	newFakeDaemon(t)

	_, err := Connect(context.Background(), Options{Addr: "127.0.0.1:1"})
	if err == nil {
		t.Fatal("Connect reached a daemon on a port nothing is listening on")
	}
	if !strings.Contains(err.Error(), "--direct") {
		t.Errorf("Connect = %v, want it to mention the alternative", err)
	}
}

// TestConnectWithNoAddressAsksSlHost: slgod does not always run where
// the program talking to it runs, so with nothing said anywhere the
// question goes to sl-host -- and sl-host failing is reported rather
// than turned into a connection refused against this machine, which
// would point the reader at the wrong problem entirely.
func TestConnectWithNoAddressAsksSlHost(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "sl-host")
	body := "#!/bin/sh\necho 'no configuration for this network' >&2\nexit 1\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	_, err := Connect(context.Background(), Options{})
	if err == nil {
		t.Fatal("Connect went somewhere without being told where")
	}
	if !strings.Contains(err.Error(), "sl-host") {
		t.Errorf("Connect = %v, want it to name what it asked", err)
	}
}

// TestConnectDirectLogsInFromThisProcess: --direct is the other half of
// the package doc -- whether to attach or to log in is a decision a
// command line makes -- and it goes through the credentials on disk, so
// a profile that is not there is a refusal before anything is dialled.
func TestConnectDirectLogsInFromThisProcess(t *testing.T) {
	// A login server that refuses everything, so that the login is
	// reached and answered without a grid being involved, and counts
	// what reaches it.
	var mu sync.Mutex
	logins := 0
	asked := func() int {
		mu.Lock()
		defer mu.Unlock()
		return logins
	}
	grid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		logins++
		mu.Unlock()
		io.WriteString(w, "this is not an xml-rpc response")
	}))
	defer grid.Close()

	dir := filepath.Join(t.TempDir(), "slgo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SLGO_CONFIG_DIR", dir)
	profile := "first = Quark\nlast = Idlemind\npassword = $1$0c3e7e577e57c0de1b49b982fc5bae19\nurl = " + grid.URL + "\n"
	if err := os.WriteFile(filepath.Join(dir, "qi"), []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}

	in, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()

	o := Options{Direct: true, Agent: "qi", Channel: "slgo test", In: in, Out: io.Discard}
	_, err = Connect(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "login response") || asked() == 0 {
		t.Errorf("Connect = %v after %d requests, want the login server's answer refused",
			err, asked())
	}

	// Saying nothing about where to ask means the terminal, which is
	// the ordinary case and asks nothing at all when the profile is
	// complete.
	before := asked()
	o.In, o.Out = nil, nil
	_, err = Connect(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), "login response") || asked() == before {
		t.Errorf("Connect = %v, and the login server was not asked again", err)
	}

	// A profile that is not there is the caller's mistake and is
	// refused without anything being dialled at all.
	before = asked()
	o.Agent = "nobody"
	_, err = Connect(context.Background(), o)
	if err == nil || !strings.Contains(err.Error(), `profile "nobody"`) {
		t.Errorf("Connect = %v, want the missing profile named", err)
	}
	if asked() != before {
		t.Error("a profile that does not exist got as far as the login server")
	}
}

// ---------------------------------------------------------- the items

// TestEveryAutoObjectIsACopyOfTheFirst: an avatar may not be allowed to
// rez -- a parcel grants building to a group, and an avatar in none is
// refused -- but copying an item it already owns asks the land nothing
// at all.  It is also quicker: rez, name and take is eight seconds and a
// copy is under two.
func TestEveryAutoObjectIsACopyOfTheFirst(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(1)

	answerCopies(f)

	if err := EnsureAutoItems(context.Background(), s, testObjects, 4); err != nil {
		t.Fatalf("EnsureAutoItems: %v", err)
	}

	copies := 0
	for _, m := range f.Sent() {
		if c, ok := m.(*msg.CopyInventoryItem); ok {
			copies++
			if c.InventoryData[0].OldItemID != autoItemID(0) {
				t.Errorf("copied %s, want the first object", c.InventoryData[0].OldItemID)
			}
		}
	}
	if copies != 3 {
		t.Errorf("%d copies were made, want one for each object after the first", copies)
	}

	// Asking again makes none: what is already there is left alone,
	// which is what makes this safe to call on every run.
	f.mu.Lock()
	f.sent = nil
	f.mu.Unlock()
	if err := EnsureAutoItems(context.Background(), s, testObjects, 4); err != nil {
		t.Fatalf("EnsureAutoItems: %v", err)
	}
	if got := len(f.Sent()); got != 0 {
		t.Errorf("%d messages went out for items that already exist", got)
	}
}

// TestTwoFirstAutoObjectsAreRefused: every copy is made from the item
// called auto, and two of them is two different things either could be
// a copy of.  One name has to be one thing, so it is refused with both
// ids and the way out, and nothing is copied.
func TestTwoFirstAutoObjectsAreRefused(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(1)
	answerCopies(f)
	other := autoItemID(50)
	f.mu.Lock()
	dir := findDir(f.inv, testObjects)
	dir.Items = append(dir.Items, &invItem{ID: other, Name: AutoObject, Type: int(sl.AssetObject)})
	f.mu.Unlock()

	err := EnsureAutoItems(context.Background(), s, testObjects, 4)
	var ne *sl.NameError
	if !errors.As(err, &ne) || len(ne.IDs) != 2 {
		t.Fatalf("EnsureAutoItems = %v, want both items called %q refused", err, AutoObject)
	}
	for _, id := range []msg.UUID{autoItemID(0), other} {
		if !strings.Contains(err.Error(), id.String()) {
			t.Errorf("the refusal does not name %s: %v", id, err)
		}
	}
	// And it says the way out, since nothing here will take one.
	if !strings.Contains(err.Error(), "delete the rest by id") {
		t.Errorf("the refusal does not say what to do: %v", err)
	}
	if got := len(f.Sent()); got != 0 {
		t.Errorf("%d messages went out, want nothing copied", got)
	}
}

// TestOneAutoObjectNeedsNothingCopied: an account that has never had one
// and only wants one is left to EnsureAttached, which will build it the
// slow way -- there is nothing here to copy from and nothing to do.
func TestOneAutoObjectNeedsNothingCopied(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)

	if err := EnsureAutoItems(context.Background(), s, testObjects, 1); err != nil {
		t.Fatalf("EnsureAutoItems: %v", err)
	}
	if got := len(f.Sent()); got != 0 {
		t.Errorf("%d messages went out with nothing to copy", got)
	}
}

// TestTheFirstAutoObjectHasToComeFromSomewhere: with nothing to copy and
// several wanted, one has to be built -- and an avatar that may not rez
// cannot, which is worth saying in the failure because the grid's own
// refusal blames the land.
func TestTheFirstAutoObjectHasToComeFromSomewhere(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)

	// Nothing in the folder and no way to find out where we are, so
	// building the first one gets no further than trying.
	f.mu.Lock()
	f.presenceErr = fmt.Errorf("the parcel will not have it")
	f.mu.Unlock()

	err := EnsureAutoItems(context.Background(), s, testObjects, 4)
	if err == nil {
		t.Fatal("EnsureAutoItems made objects out of nothing")
	}
	if !strings.Contains(err.Error(), "may not rez") {
		t.Errorf("EnsureAutoItems = %v, want it to say what such an avatar needs", err)
	}
}

// TestWhatTheWearingLeavesIsTheSeed: with nothing to copy from,
// EnsureAutoItems leaves the first object to EnsureAttached, then looks
// the folder up again and copies everything else from what is there.
// That is what makes every auto object the same object, exactly what a
// benchmark wants.
//
// It builds nothing: the folder looks empty the first time it is read
// and holds the object afterwards, which is an account that has just
// been given one, so EnsureAttached finds it and puts it on.  The build
// itself -- rez, name, take, wear -- is sl's
// TestEnsureAttachedMakesOneWhenThereIsNone.
func TestWhatTheWearingLeavesIsTheSeed(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(1)
	answerCopies(f)

	f.hide(func(folder msg.UUID, nth int) bool { return folder == testObjects && nth == 1 })

	if err := EnsureAutoItems(context.Background(), s, testObjects, 2); err != nil {
		t.Fatalf("EnsureAutoItems: %v", err)
	}
	items, err := s.FolderItems(context.Background(), testObjects)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Errorf("the folder holds %d items, want the seed and one copy", len(items))
	}
	copies := 0
	for _, m := range f.Sent() {
		switch m := m.(type) {
		case *msg.ObjectAdd:
			t.Error("something was rezzed for an object that was already there")
		case *msg.CopyInventoryItem:
			copies++
			if m.InventoryData[0].OldItemID != autoItemID(0) {
				t.Errorf("copied %s, want the one the wearing found", m.InventoryData[0].OldItemID)
			}
		}
	}
	if copies != 1 {
		t.Errorf("%d copies were made, want one", copies)
	}
}

// TestASeedThatVanishedBetweenBeingMadeAndBeingFound: the object is
// built and then looked up again, because building says nothing about
// the inventory item it became -- and an inventory that has stopped
// answering there must not be carried on past with a nil seed, which
// would be a nil dereference on the first copy.
func TestASeedThatVanishedBetweenBeingMadeAndBeingFound(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(1)
	// Empty, then holding the object for the wearing, then empty again
	// for the lookup that follows it.
	f.hide(func(folder msg.UUID, nth int) bool {
		return folder == testObjects && nth != 2
	})

	if err := EnsureAutoItems(context.Background(), s, testObjects, 2); err == nil {
		t.Error("EnsureAutoItems carried on with nothing to copy from")
	}
}

// TestAnObjectThatCannotBeCopiedIsFewerObjectsAndNotAFailure: a no-copy
// item simply is not answered by the grid, and the wearing that follows
// builds what is missing the slow way, or makes do with fewer -- so
// stopping here would turn a slower benchmark into no benchmark.
func TestAnObjectThatCannotBeCopiedIsFewerObjectsAndNotAFailure(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(1)

	// The copy never goes out, which is as far as a no-copy item gets
	// without waiting out the whole minute the real one is given.
	f.mu.Lock()
	f.sendErr = fmt.Errorf("the circuit is gone")
	f.mu.Unlock()

	if err := EnsureAutoItems(context.Background(), s, testObjects, 2); err != nil {
		t.Errorf("EnsureAutoItems = %v, want fewer objects rather than a failure", err)
	}
	// And the one object there was is still there: nothing was undone
	// on the way out.
	items, err := s.FolderItems(context.Background(), testObjects)
	if err != nil || len(items) != 1 {
		t.Errorf("the folder holds %d items, %v", len(items), err)
	}
}

// TestReadingTheFolderIsWhereThisGivesUp: everything after it needs the
// list, so an inventory that will not answer is not something to carry
// on past with an empty one -- that would look exactly like an account
// with no objects and quietly build a second set.
//
// Every later step reads inventory too, and would fail the same way, so
// what shows it stopped here is that nothing after the one read was
// asked or sent, and the error is the read's own.
func TestReadingTheFolderIsWhereThisGivesUp(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.capErr = fmt.Errorf("the capability is not answering")
	f.mu.Unlock()

	err := EnsureAutoItems(context.Background(), s, testObjects, 2)
	if err == nil || !strings.Contains(err.Error(), "the capability is not answering") ||
		strings.Contains(err.Error(), "making the first") {
		t.Errorf("EnsureAutoItems = %v, want the read's own failure", err)
	}
	f.mu.Lock()
	calls := f.capCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Errorf("inventory was asked %d times, want the one read that failed", calls)
	}
	if sent := f.Sent(); len(sent) != 0 {
		t.Errorf("%d messages went out after the read failed", len(sent))
	}
}

// hide makes chosen readings of a folder answer empty; see hideRead.
func (f *fakeGrid) hide(when func(folder msg.UUID, nth int) bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hideRead = when
}

// answerCopies makes the grid confirm a copy the way it really does: by
// the item appearing in the folder.  The new id is the grid's to choose,
// so a copy is found by looking for its name rather than by knowing it
// in advance.
//
// The copy arrives already worn, which a real one does not.  Putting one
// on is ten seconds of taking it off and waiting to be told it went back
// on, and none of that is what these tests are about -- sl/attach_test.go
// holds the wearing to account.
func answerCopies(f *fakeGrid) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		c, ok := m.(*msg.CopyInventoryItem)
		if !ok {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		dir := findDir(f.inv, testObjects)
		for _, d := range c.InventoryData {
			n := len(dir.Items)
			id := autoItemID(n)
			dir.Items = append(dir.Items, &invItem{
				ID:   id,
				Name: strings.TrimSuffix(string(d.NewName), "\x00"),
				Type: int(sl.AssetObject),
			})
			f.objects = append(f.objects, &sl.Seen{
				Object:      sl.Object{ID: msg.UUID(id), Local: uint32(100 + n)},
				AttachItem:  id,
				AttachPoint: AutoPoints[n%len(AutoPoints)] &^ sl.AttachAdd,
			})
		}
	}
}

// ------------------------------------------------------------- setup

// TestSetupTakesEveryPlaceBeforeMovingAnything: wearing an item that
// cannot be found worn takes it off and puts it back on, and a run whose
// object went away reports nothing useful about why -- so setup takes
// the avatar's whole pool, without waiting, before it copies or wears
// anything, and gives it back afterwards marked as not left clean.
func TestSetupTakesEveryPlaceBeforeMovingAnything(t *testing.T) {
	s, g := newGranting(t, "quark")
	g.fakeGrid.stock(1)
	answerCopies(g.fakeGrid)

	objs, err := SetupAuto(context.Background(), s, 4)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if len(objs) != 4 {
		t.Fatalf("SetupAuto made %d objects ready", len(objs))
	}
	want := fmt.Sprintf("quark:%d:true", AutoPool())
	if got := g.askedFor(); len(got) != 1 || got[0] != want {
		t.Errorf("setup asked for %v, want [%s]: the whole pool, tried", got, want)
	}
	if len(g.sentAtAsk) != 1 || g.sentAtAsk[0] != 0 {
		t.Errorf("messages sent before the pool was asked for: %v", g.sentAtAsk)
	}
	if len(g.fakeGrid.Sent()) == 0 {
		t.Error("setup copied nothing, so the order it did things in says nothing")
	}
	if got := g.gaveBack(); len(got) != 1 || got[0] != "g1:false" {
		t.Errorf("setup gave back %v, want the one grant, not clean", got)
	}
}

// TestSetupRefusesUnderARunningBenchmark: setting up moves attachments
// about, and an object moving under a running benchmark is a wrong
// number rather than a failure.  The refusal carries what the daemon
// said, because "in use" on its own leaves the reader nothing to do
// about it -- and it is a refusal rather than a wait, because somebody
// at a terminal wants to be told, not hung.
func TestSetupRefusesUnderARunningBenchmark(t *testing.T) {
	s, g := newGranting(t, "quark")
	g.free["quark"] = 2 // not the whole pool: somebody is running

	_, err := SetupAuto(context.Background(), s, 4)
	if err == nil {
		t.Fatal("SetupAuto moved attachments about under a running benchmark")
	}
	if !strings.Contains(err.Error(), "only 2 free") {
		t.Errorf("SetupAuto = %v, want it to carry what the daemon said", err)
	}

	// The whole pool free, and it goes ahead -- and gives the places
	// back afterwards, since it holds them only while it moves things.
	g.free["quark"] = SlotsPerAgentForTest
	if _, err := SetupAuto(context.Background(), s, 4); err != nil {
		t.Fatalf("SetupAuto with nothing running: %v", err)
	}
	if got := g.gaveBack(); len(got) != 1 {
		t.Errorf("setup gave the objects back %d times: %v", len(got), got)
	}
}

// TestSetupAsksForAtLeastOneAndAtMostThePool: the number comes off a
// command line, and neither nought objects nor a hundred is a thing the
// pool can be asked for -- clamping is better than a refusal because
// there is an obvious right answer to both.
func TestSetupAsksForAtLeastOneAndAtMostThePool(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))

	objs, err := SetupAuto(context.Background(), s, 0)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if len(objs) != 1 {
		t.Errorf("asking for none made %d ready, want one", len(objs))
	}

	if objs, err = SetupAuto(context.Background(), s, len(AutoPoints)+50); err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if len(objs) != len(AutoPoints) {
		t.Errorf("asking for more than the pool made %d ready, want %d", len(objs), len(AutoPoints))
	}
}

// TestSetupStopsAtTheFirstObjectAndCarriesOnAfterIt: failing on the
// first is a setup that did nothing, which is worth reporting; failing
// on the fifth leaves four usable objects, and a benchmark can run on
// those.
func TestSetupStopsAtTheFirstObjectAndCarriesOnAfterIt(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)

	// Only two objects exist, none can be copied, and none can be built
	// either -- so the third slot is where this stops.
	f.stock(2)
	f.mu.Lock()
	f.sendErr = fmt.Errorf("the circuit is gone")
	f.presenceErr = fmt.Errorf("the parcel will not have it")
	f.mu.Unlock()

	objs, err := SetupAuto(context.Background(), s, 4)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if len(objs) != 2 {
		t.Errorf("SetupAuto made %d ready, want the two that exist", len(objs))
	}

	// Nothing at all is a different matter: there is no benchmark to
	// run, so it is reported rather than mentioned.  The first object is
	// there to copy from, so this gets as far as the wearing, but it is
	// not on and cannot be put on.
	f.stock(1)
	f.mu.Lock()
	f.objects = nil
	f.mu.Unlock()
	objs, err = SetupAuto(context.Background(), s, 4)
	if err == nil {
		t.Errorf("SetupAuto made %d ready and reported success, with none of them worn", len(objs))
	} else if strings.Contains(err.Error(), "making the first") {
		t.Errorf("SetupAuto = %v, want it to fail wearing the first, not making it", err)
	}
}

// TestSetupWithNoFirstObjectIsASetupThatDidNothing: failing on the
// first is not a benchmark that will run slowly, it is one that will not
// run -- so it is reported rather than mentioned in passing.
func TestSetupWithNoFirstObjectIsASetupThatDidNothing(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(4)

	// The items are all there, so nothing is copied -- but nothing can
	// be told about what is worn, and putting one back on cannot be
	// said either.
	f.mu.Lock()
	f.objectsErr = fmt.Errorf("the circuit went away")
	f.sendErr = fmt.Errorf("the circuit is gone")
	f.mu.Unlock()

	if _, err := SetupAuto(context.Background(), s, 4); err == nil {
		t.Error("SetupAuto reported success without its first object")
	}
}

// TestSetupNeedsToKnowWhereObjectsGo: the folder is where a taken object
// lands and so where the auto object is kept, and an inventory that will
// not answer is not something to guess past.  Everything after the guess
// would read inventory too and fail the same way, so what shows it was
// not made is that nothing more was asked.
func TestSetupNeedsToKnowWhereObjectsGo(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.capErr = fmt.Errorf("the capability is not answering")
	f.mu.Unlock()

	_, err := SetupAuto(context.Background(), s, 1)
	if err == nil || !strings.Contains(err.Error(), "the capability is not answering") {
		t.Errorf("SetupAuto = %v, want the failure to find the folder", err)
	}
	f.mu.Lock()
	calls := f.capCalls
	f.mu.Unlock()
	if calls != 1 {
		t.Errorf("inventory was asked %d times, want the one look for the folder", calls)
	}
	if sent := f.Sent(); len(sent) != 0 {
		t.Errorf("%d messages went out without a folder to put anything in", len(sent))
	}
}

// TestSetupWithADaemonTooOldToGrant: the pool is what makes several
// runs at once safe, so a daemon that cannot be asked for it is not
// something to carry on without -- two benchmarks would quietly share an
// object and both report plausible numbers.
func TestSetupWithADaemonTooOldToGrant(t *testing.T) {
	s, g := newGranting(t, "quark")
	g.refuse = fmt.Errorf("unknown method")

	_, err := SetupAuto(context.Background(), s, 1)
	if err == nil || !strings.Contains(err.Error(), "asking for the "+AutoObject+" objects") ||
		!strings.Contains(err.Error(), "unknown method") {
		t.Errorf("SetupAuto = %v, want it to say what it was asking for and why it failed", err)
	}
	if sent := g.fakeGrid.Sent(); len(sent) != 0 {
		t.Errorf("setup went on to send %d messages", len(sent))
	}
}

// TestTheObjectsFolderFallsBackToTheRoot: a take lands in Objects, and
// an inventory that has no such folder is unusual rather than broken --
// the root will do, and stopping would be a worse answer than a slightly
// untidy inventory.
func TestTheObjectsFolderFallsBackToTheRoot(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)

	got, err := objectsFolder(context.Background(), s)
	if err != nil {
		t.Fatalf("objectsFolder: %v", err)
	}
	if got != testObjects {
		t.Errorf("objectsFolder = %s, want the Objects folder", got)
	}

	f.mu.Lock()
	f.inv.Dirs = nil
	f.mu.Unlock()
	if got, err = objectsFolder(context.Background(), s); err != nil {
		t.Fatalf("objectsFolder: %v", err)
	}
	if got != testRoot {
		t.Errorf("objectsFolder = %s, want the root", got)
	}
}

// ------------------------------------------------------- somewhere to run

// TestRunInANamedObjectLeavesItExactlyAsItWasFound: it is somebody's
// object, and the script that ran in it is the only trace -- so there is
// nothing to undo and nothing is rezzed.
func TestRunInANamedObjectLeavesItExactlyAsItWasFound(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.objects = []*sl.Seen{{Object: sl.Object{ID: thePrim, Local: 77, Name: "workbench"}}}
	f.mu.Unlock()

	obj, undo, err := RunIn(context.Background(), s, "workbench", false)
	if err != nil {
		t.Fatalf("RunIn: %v", err)
	}
	if obj.ID != thePrim {
		t.Errorf("RunIn = %+v", obj)
	}
	if undo != nil {
		t.Error("RunIn offered to undo something it did not do to somebody else's object")
	}
	if got := len(f.Sent()); got != 0 {
		t.Errorf("%d messages went out for an object that was already there", got)
	}
}

// TestRunInAChildPrimIsRefusedRatherThanRedirected: the script would
// run, but in the linkset's root, which is not where it was asked for.
func TestRunInAChildPrimIsRefusedRatherThanRedirected(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.objects = []*sl.Seen{{
		Object: sl.Object{ID: thePrim, Local: 78, Name: "workbench"},
		Parent: 77,
	}}
	f.mu.Unlock()

	_, _, err := RunIn(context.Background(), s, "workbench", false)
	if err == nil || !strings.Contains(err.Error(), "child prim") {
		t.Errorf("RunIn = %v, want it to say what was named", err)
	}
}

// TestRunInSaysWhenTheNamedObjectIsNotThere: an object out of range is
// the ordinary way this fails -- the avatar walked away, or the name is
// a typo -- and it must not be confused with one that is there and
// unusable.
func TestRunInSaysWhenTheNamedObjectIsNotThere(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)

	_, _, err := RunIn(context.Background(), s, "workbench", false)
	if err == nil || !strings.Contains(err.Error(), "in range") {
		t.Errorf("RunIn = %v, want it to say the object was not found", err)
	}

	// A session that could not answer has not said the object is not
	// there, and is not reported as though it had.
	f.mu.Lock()
	f.objectsErr = fmt.Errorf("the circuit went away")
	f.mu.Unlock()
	_, _, err = RunIn(context.Background(), s, "workbench", false)
	if err == nil || !strings.Contains(err.Error(), "the circuit went away") ||
		strings.Contains(err.Error(), "in range") {
		t.Errorf("RunIn = %v, want the session's failure rather than an object not found", err)
	}
}

// TestRunInRezzesBesideTheAvatarAndTidiesUpAfter: one rezzed here is
// ours, so it goes in the trash afterwards.
func TestRunInRezzesBesideTheAvatarAndTidiesUpAfter(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	confirmRez(t, f)

	obj, undo, err := RunIn(context.Background(), s, "", false)
	if err != nil {
		t.Fatalf("RunIn: %v", err)
	}
	if obj.ID != thePrim {
		t.Errorf("RunIn = %+v", obj)
	}
	if undo == nil {
		t.Fatal("RunIn rezzed something and offered no way to be rid of it")
	}

	// Beside the avatar rather than on top of it, and high enough that
	// the ground is not in the way.
	add := firstOf[*msg.ObjectAdd](t, f)
	if add.ObjectData.RayStart.X != 129.5 || add.ObjectData.RayStart.Z != 25.5 {
		t.Errorf("rezzed at %v, want it beside the avatar", add.ObjectData.RayStart)
	}
	// The name is so that a fault header reads well, and it says when.
	name := firstOf[*msg.ObjectName](t, f)
	if !strings.HasPrefix(string(name.ObjectData[0].Name), "slgo run ") {
		t.Errorf("named it %q", name.ObjectData[0].Name)
	}

	undo()
	if got := firstOf[*msg.DeRezObject](t, f).AgentBlock.DestinationID; got != testTrash {
		t.Errorf("sent it to %s, want the trash", got)
	}
}

// TestRunInKeepsWhatItWasAskedToKeep: --keep is for looking at what the
// script did afterwards, and an object thrown away before anybody could
// is the whole of what that flag prevents.
func TestRunInKeepsWhatItWasAskedToKeep(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	confirmRez(t, f)

	_, undo, err := RunIn(context.Background(), s, "", true)
	if err != nil {
		t.Fatalf("RunIn: %v", err)
	}
	if undo != nil {
		t.Error("RunIn offered to throw away an object it was told to keep")
	}
}

// TestRunInCarriesOnWhenTheNameWillNotTake: the name is so that a fault
// header reads well, which is not worth abandoning a run over -- the
// object is perfectly usable unnamed.
func TestRunInCarriesOnWhenTheNameWillNotTake(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	confirmRez(t, f)

	// The rename goes out and everything after it fails, which is the
	// circuit going away between one message and the next.
	f.mu.Lock()
	prev := f.onSend
	f.onSend = func(m msg.Message) {
		prev(m)
		if _, ok := m.(*msg.ObjectName); ok {
			f.mu.Lock()
			f.sendErr = fmt.Errorf("the circuit is gone")
			f.mu.Unlock()
		}
	}
	f.mu.Unlock()

	obj, _, err := RunIn(context.Background(), s, "", true)
	if err != nil {
		t.Fatalf("RunIn = %v, want the naming to be survivable", err)
	}
	if obj.ID != thePrim {
		t.Errorf("RunIn = %+v", obj)
	}
}

// TestRunInNeedsToKnowWhereTheAvatarIs: the object goes beside it, so a
// session that cannot say where it is has nowhere to put one -- and
// rezzing at the origin would put it in somebody else's parcel.
func TestRunInNeedsToKnowWhereTheAvatarIs(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.presenceErr = fmt.Errorf("the circuit went away")
	f.mu.Unlock()

	if _, _, err := RunIn(context.Background(), s, "", false); err == nil {
		t.Error("RunIn rezzed something without knowing where the avatar was")
	}
}

// TestRunInReportsARezThatWasRefused: a parcel that grants building to a
// group refuses an avatar in none, and the refusal blames the land --
// so the message has to say what was being attempted.
func TestRunInReportsARezThatWasRefused(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.sendErr = fmt.Errorf("the parcel will not have it")
	f.mu.Unlock()

	_, _, err := RunIn(context.Background(), s, "", false)
	if err == nil || !strings.Contains(err.Error(), "rezzing") {
		t.Errorf("RunIn = %v, want it to say what it was doing", err)
	}
}

// TestRunInClearsAwayARezGivenUpOn: a run given up on while its object
// is being looked for may have one standing by then, and Rez hands it
// back.  Nobody else holds its id, so it goes in the trash before the
// cancel is returned.
func TestRunInClearsAwayARezGivenUpOn(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The prim appears as the first look after the rez is answered, so
	// only the last look sees it; its delete is answered with its kill.
	looked := false
	f.mu.Lock()
	f.afterObjects = func() {
		for _, m := range f.sent {
			add, ok := m.(*msg.ObjectAdd)
			if !ok || looked {
				continue
			}
			looked = true
			at := add.ObjectData.RayEnd
			at.Z += add.ObjectData.Scale.Z / 2
			f.objects = append(f.objects, &sl.Seen{
				Object: sl.Object{ID: thePrim, Local: 77}, PCode: 9, Owner: testMe, Position: at,
			})
			cancel()
		}
	}
	f.onSend = func(m msg.Message) {
		if d, ok := m.(*msg.DeRezObject); ok {
			go f.relay(t, &msg.KillObject{ObjectData: []msg.KillObject_ObjectData{{ID: d.ObjectData[0].ObjectLocalID}}})
		}
	}
	f.mu.Unlock()

	obj, undo, err := RunIn(ctx, s, "", false)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("RunIn = %v, want the caller's cancel", err)
	}
	if obj != nil || undo != nil {
		t.Errorf("RunIn handed back %v with its error", obj)
	}
	d := firstOf[*msg.DeRezObject](t, f)
	if d.ObjectData[0].ObjectLocalID != 77 || d.AgentBlock.DestinationID != testTrash {
		t.Errorf("sent %+v; want the prim that was made deleted into the trash", d)
	}
}

// TestATidyUpThatFailsSaysWhatIsStillThere: the object is in the world
// and somebody has to know, since the run's own context may well be why
// the tidying is happening at all.  It is said on standard error, which
// this test borrows, so it does not run alongside the others.
func TestATidyUpThatFailsSaysWhatIsStillThere(t *testing.T) {
	s, f := newFakeSession(t)
	confirmRez(t, f)

	_, undo, err := RunIn(context.Background(), s, "", false)
	if err != nil {
		t.Fatalf("RunIn: %v", err)
	}

	// No trash folder, so there is nowhere to put it.
	f.mu.Lock()
	f.inv.Dirs = nil
	f.mu.Unlock()

	said := stderrOf(t, undo)
	for _, m := range f.Sent() {
		if _, ok := m.(*msg.DeRezObject); ok {
			t.Error("the object was sent to a trash folder that does not exist")
		}
	}
	if !strings.Contains(said, thePrim.String()) || !strings.Contains(said, "is still there") {
		t.Errorf("the failed tidy-up said %q, want the object named as still there", said)
	}
}

// stderrOf is what fn wrote to standard error.
func stderrOf(t *testing.T, fn func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stderr")
	w, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	save := os.Stderr
	os.Stderr = w
	fn()
	os.Stderr = save
	w.Close()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---------------------------------------------------------- test tools

// confirmRez plays the simulator's side of a rez and of the rename that
// follows it: the region describes something new, answers the question
// of whose it is, and then answers what it is called.  A delete of it is
// answered too, with the KillObject that says it has gone.
//
// It is driven off what the session sends rather than relayed from the
// test goroutine, because RunIn does not return until all of it has
// happened -- so the answers have to come from inside the send.
func confirmRez(t *testing.T, f *fakeGrid) {
	t.Helper()
	var named string
	f.mu.Lock()
	f.onSend = func(m msg.Message) {
		switch v := m.(type) {
		case *msg.ObjectAdd:
			// Where the simulator puts it: the prim's bottom on the
			// point asked for, so its centre half its height above.
			at := v.ObjectData.RayEnd
			at.Z += v.ObjectData.Scale.Z / 2
			f.mu.Lock()
			f.objects = append(f.objects, &sl.Seen{
				Object: sl.Object{ID: thePrim, Local: 77}, PCode: 9, Position: at,
			})
			f.mu.Unlock()
			go f.relay(t, &msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{
				{FullID: thePrim, ID: 77},
			}})
		case *msg.ObjectName:
			f.mu.Lock()
			named = strings.TrimSuffix(string(v.ObjectData[0].Name), "\x00")
			f.mu.Unlock()
		case *msg.RequestObjectPropertiesFamily:
			f.mu.Lock()
			name := named
			f.mu.Unlock()
			if name == "" {
				name = "Object"
			}
			reply := &msg.ObjectPropertiesFamily{}
			reply.ObjectData.ObjectID = v.ObjectData.ObjectID
			reply.ObjectData.OwnerID = testMe
			reply.ObjectData.Name = append([]byte(name), 0)
			go f.relay(t, reply)
		case *msg.DeRezObject:
			kill := &msg.KillObject{}
			for _, d := range v.ObjectData {
				kill.ObjectData = append(kill.ObjectData, msg.KillObject_ObjectData{ID: d.ObjectLocalID})
			}
			go f.relay(t, kill)
		}
	}
	f.mu.Unlock()
}

// relay hands a message to the session as though the grid had sent it.
func (f *fakeGrid) relay(t *testing.T, m msg.Message) {
	body, err := m.Encode()
	if err != nil {
		t.Errorf("encoding %s: %v", m.MsgInfo().Name, err)
		return
	}
	select {
	case f.msgs <- &sl.Message{ID: msg.IDOf(m), Name: m.MsgInfo().Name, Body: body, At: time.Now()}:
	case <-f.done:
	case <-time.After(30 * time.Second):
		t.Error("nothing read the relay: is a session attached to this backend?")
	}
}

// firstOf returns the first message of one kind that went out, which is
// what an assertion usually wants: not what was sent, but what
// ObjectName was sent.
func firstOf[T msg.Message](t *testing.T, f *fakeGrid) T {
	t.Helper()
	for _, m := range f.Sent() {
		if v, ok := m.(T); ok {
			return v
		}
	}
	var zero T
	t.Fatalf("no %T went out", zero)
	return zero
}
