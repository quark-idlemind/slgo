package session

// A grid, and a daemon, that are not there.
//
// Everything here is a function of a *sl.Session, and a session is a
// function of an sl.Backend -- so a fake backend is the difference
// between testing this package and testing its arithmetic.  Without one
// a test can reach AutoName and autoGroupSlots and nothing else, which
// is why the whole of it was only ever exercised by autobench against a
// live avatar.
//
// There are two fakes and they are not the same thing.  fakeGrid is an
// sl.Backend: it answers what the grid would answer, serves inventory
// from httptest because inventory is AIS and not a message, and is what
// everything taking a session is tested through.  fakeDaemon is a real
// gRPC server on loopback, for the one call that cannot be faked at the
// Backend level at all -- Connect, which dials, and UseAutoAnywhere,
// which dials once per avatar it considers.
//
// Neither can stand in for a grid.  What is checked is the decisions
// this package makes, not what a simulator would have done about them.

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
	testMe        = msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	testSessionID = msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	testRoot      = msg.MustParseUUID("23077e57-7e57-c0de-622e-77274d813d21")
	testObjects   = msg.MustParseUUID("a9a87e57-7e57-c0de-b748-062ee08c11ee")
	testTrash     = msg.MustParseUUID("aa8f7e57-7e57-c0de-e8da-278417da2fea")
	thePrim       = msg.MustParseUUID("88fa7e57-7e57-c0de-af42-813fbc8c4b63")
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
// while the session is running -- a lock that becomes free, an item that
// appears -- without racing the session's reader goroutine.
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

	presenceErr, objectsErr, sendErr, capErr error

	// busy is who holds a lock, by name, lockErr is a daemon too old to
	// know what a lock is at all, and waitErr is one that goes away
	// while somebody is queued on it.
	busy    map[string]string
	lockErr error
	waitErr error

	// waited is every lock that was queued on rather than tried, which
	// is how a test tells "took a free one" from "waited its turn".
	waited []string

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
		busy:  map[string]string{},
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
	return out, nil
}

func (f *fakeGrid) Region(ctx context.Context) (*sl.Region, bool, error) {
	return &sl.Region{Name: "Test Region"}, true, nil
}

func (f *fakeGrid) Neighbours(ctx context.Context, set *bool) (*sl.Neighbours, error) {
	return &sl.Neighbours{}, nil
}

func (f *fakeGrid) Flush(ctx context.Context) (int, error)           { return 0, nil }
func (f *fakeGrid) Friends(ctx context.Context) ([]sl.Friend, error) { return nil, nil }

func (f *fakeGrid) NoteFriend(ctx context.Context, id msg.UUID, online bool) error { return nil }

// Lock is the queueing form and is granted at once: what a test wants
// from it is which name was waited for, since waiting rather than
// trying is the decision this package makes.
func (f *fakeGrid) Lock(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockErr != nil {
		return f.lockErr
	}
	if f.waitErr != nil {
		return f.waitErr
	}
	f.waited = append(f.waited, name)
	delete(f.busy, name)
	return nil
}

func (f *fakeGrid) Unlock(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.busy, name)
	return nil
}

func (f *fakeGrid) TryLock(ctx context.Context, name string) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockErr != nil {
		return false, "", f.lockErr
	}
	if by := f.busy[name]; by != "" {
		return false, by, nil
	}
	f.busy[name] = "us"
	return true, "", nil
}

// Waited is every lock that was queued on rather than tried.
func (f *fakeGrid) Waited() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.waited...)
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

// fakeDaemon is a slgod with nothing behind it, on loopback, for the
// two calls that dial.
//
// It has to be dialled rather than handed a connection, so it does the
// whole handshake: TLS, and a challenge each way over a secret in a home
// directory the test owns.  Everything else it answers is the least a
// session needs to get as far as taking a lock and finding its objects
// already worn -- which is the state this package is usually in.
type fakeDaemon struct {
	pb.UnimplementedGridServer

	mu sync.Mutex

	auth *auth.Server

	// agents is who it is holding, in its own order, and info describes
	// one of them by name.
	agents []string

	// locked answers a lock request for one avatar.  It has to answer
	// SOMETHING: a lock that is not answered is one the client waits on,
	// and the wait is a quarter of an hour.
	locked func(agent string, l *pb.Lock) *pb.Locked

	// refuseAttach names avatars this daemon will not attach to, which
	// is a session it is not holding.
	refuseAttach map[string]bool

	// cap is where capability requests are sent, so that inventory is
	// read over http exactly as it is in the world, and capFail is a
	// session whose inventory cannot be read at all.
	cap     string
	capFail bool

	// objects is what every session says is worn, keyed by nothing: the
	// avatars here are interchangeable except for their locks.
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
		locked: func(_ string, l *pb.Lock) *pb.Locked {
			return &pb.Locked{Name: l.Name, Held: true}
		},
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
// been set up is in, and it is what makes taking a group cost nothing
// but the lock.
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

// busy makes every group on one avatar taken, which is what sends a
// caller on to the next avatar.
func (d *fakeDaemon) busy(names ...string) {
	taken := map[string]bool{}
	for _, n := range names {
		taken[n] = true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.locked = func(who string, l *pb.Lock) *pb.Locked {
		if taken[who] && l.Try {
			return &pb.Locked{Name: l.Name, Held: false, Holder: "somebody else"}
		}
		// A wait is answered by handing it over, since queueing is what
		// this is meant to end up doing.
		return &pb.Locked{Name: l.Name, Held: true}
	}
}

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

	for {
		p, err := s.Recv()
		if err != nil {
			return nil
		}
		l := p.GetLock()
		if l == nil {
			continue
		}
		d.mu.Lock()
		answer := d.locked
		d.mu.Unlock()
		if got := answer(who.Name, l); got != nil {
			s.Send(&pb.ServerPacket{Body: &pb.ServerPacket_Locked{Locked: got}})
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

// TestAGroupIsTakenWholeOrNotAtAll: the pool is divided into fixed
// groups and a run takes all of one, which is what makes several runs at
// once safe -- taking slots one at a time would let two runs each hold
// some and wait for the rest, which is a deadlock rather than a queue.
func TestAGroupIsTakenWholeOrNotAtAll(t *testing.T) {
	t.Parallel()
	if AutoGroups() != len(AutoPoints)/AutoGroupSize {
		t.Errorf("AutoGroups = %d over %d points", AutoGroups(), len(AutoPoints))
	}
	// The lock is NOT the old bare name: a client old enough to take
	// that one would not exclude against these, and the two would
	// quietly share objects.
	if AutoGroupLock(0) == AutoLock {
		t.Error("the group lock is the old bare name, which an old client would not exclude against")
	}
	if got := AutoGroupLock(2); got != "auto/2" {
		t.Errorf("AutoGroupLock(2) = %q", got)
	}

	// Slots belong to exactly one group, and every point is in one.
	seen := map[int]bool{}
	for g := 0; g < AutoGroups(); g++ {
		slots := autoGroupSlots(g, AutoGroupSize)
		if len(slots) != AutoGroupSize {
			t.Fatalf("group %d has %d slots", g, len(slots))
		}
		for _, s := range slots {
			if seen[s] {
				t.Errorf("slot %d is in two groups", s)
			}
			seen[s] = true
		}
	}
	if len(seen) != AutoGroups()*AutoGroupSize {
		t.Errorf("%d slots are in a group, of %d points", len(seen), len(AutoPoints))
	}

	// Asking for fewer than a whole group gives fewer, and asking past
	// the end of the pool gives nothing rather than a slot that is not
	// there.
	if got := autoGroupSlots(0, 2); len(got) != 2 {
		t.Errorf("autoGroupSlots(0, 2) = %v", got)
	}
	if got := autoGroupSlots(AutoGroups(), AutoGroupSize); len(got) != 0 {
		t.Errorf("autoGroupSlots past the end = %v", got)
	}
}

// TestAutoPointsIsAppendOnly: a slot is identified by its INDEX -- that
// is what a lock is taken on and what one program tells another -- so
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
	// reached and answered without a grid being involved.
	grid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	if _, err := Connect(context.Background(), o); err == nil {
		t.Error("a login server answering nonsense was taken for a login")
	}

	// Saying nothing about where to ask means the terminal, which is
	// the ordinary case and asks nothing at all when the profile is
	// complete.
	o.In, o.Out = nil, nil
	if _, err := Connect(context.Background(), o); err == nil {
		t.Error("a login server answering nonsense was taken for a login")
	}

	// A profile that is not there is the caller's mistake and is
	// refused without anything being dialled at all.
	o.Agent = "nobody"
	if _, err := Connect(context.Background(), o); err == nil {
		t.Error("Connect logged in as a profile that does not exist")
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

// TestTheFirstAutoObjectIsBuiltAndThenCopied: the one case in the life
// of an account where something is built rather than copied.  Once it
// exists there is a seed, and everything after it is a copy -- which is
// what makes every auto object the same object, exactly what a benchmark
// wants.
func TestTheFirstAutoObjectIsBuiltAndThenCopied(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(1)
	answerCopies(f)

	// The folder looks empty the first time it is read and has the
	// object in it afterwards, which is an account that has just been
	// given one: EnsureAttached finds it, puts it on, and it becomes the
	// seed everything else is copied from.
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
// item simply is not answered by the grid, and the caller already copes
// with getting fewer than it asked for -- so stopping here would turn a
// slower benchmark into no benchmark.
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
func TestReadingTheFolderIsWhereThisGivesUp(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.capErr = fmt.Errorf("the capability is not answering")
	f.mu.Unlock()

	if err := EnsureAutoItems(context.Background(), s, testObjects, 2); err == nil {
		t.Error("EnsureAutoItems carried on past an inventory it could not read")
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

// TestSetupTakesEveryGroupBeforeMovingAnything: wearing something
// replaces what is on the point, and a run whose object went away
// reports nothing useful about why -- so this refuses outright rather
// than working around a group that is in use.
func TestSetupTakesEveryGroupBeforeMovingAnything(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))

	objs, err := SetupAuto(context.Background(), s, 4)
	if err != nil {
		t.Fatalf("SetupAuto: %v", err)
	}
	if len(objs) != 4 {
		t.Fatalf("SetupAuto made %d objects ready", len(objs))
	}
	// And it gave every one of them back: holding them afterwards would
	// leave the avatar unusable until the program exited.
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.busy) != 0 {
		t.Errorf("setup is still holding %v", f.busy)
	}
}

// TestSetupRefusesUnderARunningBenchmark: the refusal names the group
// and whoever has it, because "in use" without either leaves the reader
// with nothing to do about it.
func TestSetupRefusesUnderARunningBenchmark(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.stock(len(AutoPoints))
	f.mu.Lock()
	f.busy[AutoGroupLock(1)] = "autobench on quark"
	f.mu.Unlock()

	_, err := SetupAuto(context.Background(), s, 4)
	if err == nil {
		t.Fatal("SetupAuto moved attachments about under a running benchmark")
	}
	if !strings.Contains(err.Error(), "autobench on quark") {
		t.Errorf("SetupAuto = %v, want it to name who has the group", err)
	}

	// A daemon that says a group is taken without saying by whom still
	// has to produce a sentence.
	f.mu.Lock()
	f.busy[AutoGroupLock(1)] = ""
	f.busy[AutoGroupLock(0)] = ""
	f.mu.Unlock()
	if got := holderOr(""); got != "something else" {
		t.Errorf("holderOr = %q with nobody named", got)
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
	// run, so it is reported rather than mentioned.
	f.stock(0)
	if _, err := SetupAuto(context.Background(), s, 4); err == nil {
		t.Error("SetupAuto reported success with no objects at all")
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
// not answer is not something to guess past.
func TestSetupNeedsToKnowWhereObjectsGo(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.capErr = fmt.Errorf("the capability is not answering")
	f.mu.Unlock()

	if _, err := SetupAuto(context.Background(), s, 1); err == nil {
		t.Error("SetupAuto found a folder in an inventory it could not read")
	}
}

// TestSetupWithADaemonTooOldToLock: the lock is what makes several runs
// at once safe, so one that cannot be asked for is not something to
// carry on without -- two benchmarks would quietly share an object and
// both report plausible numbers.
func TestSetupWithADaemonTooOldToLock(t *testing.T) {
	t.Parallel()
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.lockErr = fmt.Errorf("unknown method")
	f.mu.Unlock()

	_, err := SetupAuto(context.Background(), s, 1)
	if err == nil || !strings.Contains(err.Error(), AutoObject) {
		t.Errorf("SetupAuto = %v, want it to say what it was asking for", err)
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

// TestRunInAChildPrimIsRefusedRatherThanRedirected: the script would run
// -- in the linkset's root, which is not where it was asked for -- and a
// benchmark reading linkset data from the wrong prim reports numbers
// that are wrong rather than missing.
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

	f.mu.Lock()
	f.objectsErr = fmt.Errorf("the circuit went away")
	f.mu.Unlock()
	if _, _, err := RunIn(context.Background(), s, "workbench", false); err == nil {
		t.Error("RunIn looked for an object on a session that could not answer")
	}
}

// TestRunInRezzesBesideTheAvatarAndTidiesUpAfter: one rezzed here is
// ours, so it goes in the trash afterwards -- and the same object is
// used for every run, which is not merely tidy: a benchmark carries a
// reading from one script to the next through the object's linkset data.
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

// TestATidyUpThatFailsSaysWhatIsStillThere: the object is in the world
// and somebody has to know, since the run's own context may well be why
// the tidying is happening at all.
func TestATidyUpThatFailsSaysWhatIsStillThere(t *testing.T) {
	t.Parallel()
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

	undo()
	for _, m := range f.Sent() {
		if _, ok := m.(*msg.DeRezObject); ok {
			t.Error("the object was sent to a trash folder that does not exist")
		}
	}
}

// ---------------------------------------------------------- test tools

// confirmRez plays the simulator's side of a rez and of the rename that
// follows it: the region describes something new, answers the question
// of whose it is, and then answers what it is called.
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
