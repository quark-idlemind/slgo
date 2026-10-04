package main

// The experiment itself, run against a daemon that is not there.
//
// main is one straight line and not one step of it can be reached
// through an sl.Backend.  It DIALS slgod, so there has to be one to
// dial; it copies an inventory item and reads the folder back until the
// copy appears, because that is the only confirmation a copy gets; and
// its Wear waits for the simulator to describe the new attachment,
// which arrives as an ObjectUpdate relayed down the daemon's stream.
// So this is a real gRPC daemon in this process, with the real
// handshake against a secret in a home directory the test owns, and
// enough of a grid behind it to answer those three things.
//
// What is being pinned is the verdict.  The whole program is a count of
// what is on one attachment point before and after, and one of its three
// answers is a claim about the grid that the rest of this tree is built
// on: session.AutoPoints puts every object on one point BECAUSE this
// printed YES.  A run that printed the wrong one would be believed, and
// two benchmarks would quietly share an object ever afterwards.
//
// Nothing here runs in parallel.  main parses the process's flags and
// writes the process's standard output, and the daemon is found through
// a secret in $HOME, so two runs at once would be two runs sharing all
// three.
//
// What cannot be reached is every log.Fatal in the file: they call
// os.Exit and would take the test binary with them.

import (
	"context"
	"flag"
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

	"google.golang.org/grpc"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/auth"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"

	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// The seed item, and the probe when a test says it already exists.
var (
	theSeed  = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000010")
	theProbe = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000011")
)

// ------------------------------------------------------- an inventory

// invDir is a folder in the daemon's inventory and invItem a thing in
// one.
//
// A tree rather than a flat answer, because the program reads the top of
// inventory to find Objects and then reads Objects itself: a fake that
// answered both with the same thing would let a program that had
// confused the two pass.
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
// as the depth asked for.  The nesting is not decoration: AIS takes a
// depth on the request and answers the whole subtree at once.
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

// ---------------------------------------------------------- the daemon

// fakeDaemon is an slgod with nothing behind it: an inventory to copy
// into, a list of what is worn, and an attach answered the way the grid
// answers one.
type fakeDaemon struct {
	pb.UnimplementedGridServer

	mu   sync.Mutex
	auth *auth.Server

	root   *invDir
	worn   []*pb.ObjectInfo
	capURL string

	// drop is how many of the objects already on the point an attach
	// throws off, which is the whole question this program asks: none is
	// the add bit working, one is the replacement it does without the
	// bit, and two is the answer that is neither.
	drop int

	// made numbers the objects and items the grid invents, so that
	// nothing gets the same id twice.
	made int
}

// newFakeDaemon starts one and answers with the address to dial.
//
// The secret goes in a home directory of the test's own: client.Dial
// does the real handshake, and the developer running this has a real
// secret on disk with a live daemon behind it.
func newFakeDaemon(t *testing.T, items ...*invItem) (*fakeDaemon, string) {
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
	// the developer running this has one set.
	t.Setenv(sl.EnvAgent, "")

	a, err := auth.New(secret)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDaemon{
		auth: a,
		root: &invDir{
			ID: testRoot, Name: "My Inventory",
			Dirs: []*invDir{{ID: testObjects, Name: "Objects", Type: 6, Items: items}},
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

// serveInventory puts the tree behind http, which is where inventory
// really lives: it is AIS and not a message, and the daemon is a proxy
// for the request rather than the answer.
func (d *fakeDaemon) serveInventory(t *testing.T) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
		d.mu.Lock()
		dir := findDir(d.root, capFolderID(r.URL.Path))
		body := ""
		if dir != nil {
			body = dirLLSD(dir, testRoot, depth)
		}
		d.mu.Unlock()
		if body == "" {
			http.Error(w, "no such folder", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<?xml version="1.0" ?><llsd>`+body+`</llsd>`)
	}))
	t.Cleanup(s.Close)
	d.capURL = s.URL
}

// wear puts one object on a point, throwing off as many of the ones
// already there as drop says, and answers with the update the simulator
// would send about it.
//
// The object gets a brand new id, as a real one does: attaching rezzes
// it afresh, and the only thing tying it back to the item it came from
// is the AttachItemID in its NameValue.
func (d *fakeDaemon) wear(item msg.UUID, point int) msg.Message {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.made++
	id := msg.MustParseUUID(fmt.Sprintf("88fa7e57-7e57-c0de-af42-813fbc8c%04d", d.made))

	left := d.worn[:0]
	dropped := 0
	for _, o := range d.worn {
		if int(o.AttachPoint) == point && dropped < d.drop {
			dropped++
			continue
		}
		left = append(left, o)
	}
	d.worn = append(left, &pb.ObjectInfo{
		Id: id.String(), Local: uint32(200 + d.made),
		AttachItem: item.String(), AttachPoint: uint32(point),
	})

	// attachPoint reads the point back out of the State byte with its
	// nibbles swapped, which is how the wire carries it.
	return &msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{
		FullID:    id,
		ID:        uint32(200 + d.made),
		State:     uint8((point&0x0f)<<4 | (point>>4)&0x0f),
		NameValue: []byte("AttachItemID STRING RW DS " + item.String() + "\n"),
	}}}
}

// answer is the simulator's reply to something the client sent, which
// here is only ever a copy or an attach.
func (d *fakeDaemon) answer(m msg.Message) msg.Message {
	switch v := m.(type) {
	case *msg.CopyInventoryItem:
		// A copy is confirmed by the item appearing in the folder and by
		// nothing else, so this is the whole of the answer.
		d.mu.Lock()
		defer d.mu.Unlock()
		for _, it := range v.InventoryData {
			dir := findDir(d.root, it.NewFolderID)
			if dir == nil {
				continue
			}
			d.made++
			dir.Items = append(dir.Items, &invItem{
				ID:   msg.MustParseUUID(fmt.Sprintf("c75d7e57-7e57-c0de-b372-%012d", d.made)),
				Name: strings.TrimSuffix(string(it.NewName), "\x00"),
			})
		}
		return nil

	case *msg.RezSingleAttachmentFromInv:
		return d.wear(v.ObjectData.ItemID, int(v.ObjectData.AttachmentPt)&^sl.AttachAdd)
	}
	return nil
}

func (d *fakeDaemon) agentInfo() *pb.AgentInfo {
	return &pb.AgentInfo{
		Name: "quark", AgentId: testMe.String(), SessionId: testSessionID.String(),
		AvatarName: "Quark Idlemind", Region: "Test Region",
		InventoryRoot: testRoot.String(), Caps: []string{agent.InventoryCap},
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

// Stream answers the attach and then relays, which is the only way an
// ObjectUpdate can reach a client and so the only way Wear can finish.
//
// It replies on the goroutine that read the request rather than from
// one of its own, so a reply is only ever an answer to something and
// this end has nothing to say once the program stops asking.
func (d *fakeDaemon) Stream(s grpc.BidiStreamingServer[pb.ClientPacket, pb.ServerPacket]) error {
	first, err := s.Recv()
	if err != nil {
		return err
	}
	if first.GetAttach() == nil {
		return fmt.Errorf("the first packet was not an attach")
	}
	if err := s.Send(&pb.ServerPacket{Body: &pb.ServerPacket_Attached{
		Attached: &pb.Attached{Agent: d.agentInfo()},
	}}); err != nil {
		return err
	}

	for {
		p, err := s.Recv()
		if err != nil {
			return nil
		}
		out := p.GetMessage()
		if out == nil {
			continue
		}
		in := msg.New(msg.ID(out.Id))
		if in == nil || in.Decode(out.Body) != nil {
			continue
		}
		reply := d.answer(in)
		if reply == nil {
			continue
		}
		body, err := reply.Encode()
		if err != nil {
			continue
		}
		if err := s.Send(&pb.ServerPacket{Body: &pb.ServerPacket_Message{
			Message: &pb.InboundMessage{
				Id: uint32(msg.IDOf(reply)), Name: reply.MsgInfo().Name, Body: body,
			},
		}}); err != nil {
			return err
		}
	}
}

func (d *fakeDaemon) Objects(context.Context, *pb.ObjectsRequest) (*pb.ObjectsResponse, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &pb.ObjectsResponse{Objects: append([]*pb.ObjectInfo(nil), d.worn...)}, nil
}

func (d *fakeDaemon) Presence(context.Context, *pb.PresenceRequest) (*pb.PresenceResponse, error) {
	return &pb.PresenceResponse{Region: "Test Region"}, nil
}

func (d *fakeDaemon) Region(context.Context, *pb.RegionRequest) (*pb.RegionInfo, error) {
	return &pb.RegionInfo{Name: "Test Region", Known: true}, nil
}

// Cap does the http itself, exactly as slgod does: it holds the URL and
// makes the request, and has no idea that what comes back is inventory.
func (d *fakeDaemon) Cap(ctx context.Context, r *pb.CapRequest) (*pb.CapResponse, error) {
	if r.Cap != agent.InventoryCap {
		return nil, fmt.Errorf("no %s capability", r.Cap)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", d.capURL+r.Path, nil)
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

// wearing puts an object on a point as though it had been on since
// login, which is the state the seed is in when the program starts.
func (d *fakeDaemon) wearing(item msg.UUID, local uint32, point int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.worn = append(d.worn, &pb.ObjectInfo{
		Id:          msg.MustParseUUID(fmt.Sprintf("88fa7e57-7e57-c0de-af42-813fbc8c%04d", local)).String(),
		Local:       local,
		AttachItem:  item.String(),
		AttachPoint: uint32(point),
	})
}

// ------------------------------------------------------------ the run

// runMain is one run of the program, with its flags, its arguments and
// everything it printed belonging to the test.
//
// A fresh flag set, because main registers its flags on the process's
// and registering the same one twice panics.
func runMain(t *testing.T, args ...string) string {
	t.Helper()

	savedArgs, savedFlags := os.Args, flag.CommandLine
	os.Args = append([]string{"slgo-multiattach"}, args...)
	flag.CommandLine = flag.NewFlagSet("slgo-multiattach", flag.ContinueOnError)
	defer func() { os.Args, flag.CommandLine = savedArgs, savedFlags }()

	path := filepath.Join(t.TempDir(), "stdout")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := os.Stdout
	os.Stdout = f
	main()
	os.Stdout = saved
	f.Close()

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// --------------------------------------------------------- the answers

// TestAPointHoldingSeveralObjectsIsTheAnswerTheToolWasWrittenFor: this
// is the reading the rest of the tree is built on.  session.AutoPoints
// wears twenty-four objects on one point because AttachAdd was measured
// to work, and a run that had printed NO would have left the object pool
// capped at the eight HUD points for no reason at all.
//
// It also takes the path where the probe has to be MADE, which is the
// ordinary first run: the copy is confirmed by the item appearing in the
// folder and by nothing else, so the program has to read it back.
func TestAPointHoldingSeveralObjectsIsTheAnswerTheToolWasWrittenFor(t *testing.T) {
	d, addr := newFakeDaemon(t, &invItem{ID: theSeed, Name: "auto"})
	d.wearing(theSeed, 100, sl.HUDBottomLeft)
	// Something on another point, which must not be counted: a filter
	// that let the whole avatar through would print YES whatever the
	// attach did.
	d.wearing(msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000099"), 101, sl.HUDTopRight)

	got := runMain(t, "-addr", addr, "-agent", "quark")

	if !strings.Contains(got, `made "auto probe"`) {
		t.Errorf("the probe was not made and said to have been:\n%s", got)
	}
	if !strings.Contains(got, "before: 1 object(s) on HUD bottom left") {
		t.Errorf("what was on the point before was miscounted:\n%s", got)
	}
	if !strings.Contains(got, "after: 2 object(s) on HUD bottom left") {
		t.Errorf("what was on the point after was miscounted:\n%s", got)
	}
	if !strings.Contains(got, "YES: a point holds several objects") {
		t.Errorf("the verdict was not the one the count supports:\n%s", got)
	}
}

// TestAnAttachThatReplacedWhatWasThereIsTheOtherAnswer: without the add
// bit an attach replaces, and reporting that as YES would put the object
// pool's ceiling at 38 when it is really 8 -- two benchmarks would then
// share an object and both report plausible numbers.
//
// The probe already exists here, which is the second and every later
// run: nothing is copied and nothing is said about making one.
func TestAnAttachThatReplacedWhatWasThereIsTheOtherAnswer(t *testing.T) {
	d, addr := newFakeDaemon(t,
		&invItem{ID: theSeed, Name: "auto"},
		&invItem{ID: theProbe, Name: "auto probe"})
	d.wearing(theSeed, 100, sl.HUDBottomLeft)
	d.drop = 1

	got := runMain(t, "-addr", addr, "-agent", "quark")

	if strings.Contains(got, "made ") {
		t.Errorf("a probe that already existed was made again:\n%s", got)
	}
	if !strings.Contains(got, "after: 1 object(s) on HUD bottom left") {
		t.Errorf("what was on the point after was miscounted:\n%s", got)
	}
	if !strings.Contains(got, "NO: it replaced what was there") {
		t.Errorf("the verdict was not the one the count supports:\n%s", got)
	}
}

// TestFewerThanBeforeIsSaidToBeUnclearRatherThanCalledEitherWay: an
// attach that ended with less on the point than it started with answers
// neither question, and calling it NO would be a measurement reported as
// a conclusion.  The list is printed above it so the reader can see what
// actually happened.
func TestFewerThanBeforeIsSaidToBeUnclearRatherThanCalledEitherWay(t *testing.T) {
	d, addr := newFakeDaemon(t,
		&invItem{ID: theSeed, Name: "auto"},
		&invItem{ID: theProbe, Name: "auto probe"})
	d.wearing(theSeed, 100, sl.HUDBottom)
	d.wearing(theProbe, 101, sl.HUDBottom)
	d.drop = 2

	got := runMain(t, "-addr", addr, "-agent", "quark",
		"-point", strconv.Itoa(sl.HUDBottom), "-first", "auto", "-second", "auto probe")

	if !strings.Contains(got, "before: 2 object(s) on HUD bottom") {
		t.Errorf("what was on the point before was miscounted:\n%s", got)
	}
	if !strings.Contains(got, "after: 1 object(s) on HUD bottom") {
		t.Errorf("what was on the point after was miscounted:\n%s", got)
	}
	if !strings.Contains(got, "UNCLEAR: fewer than before") {
		t.Errorf("a count that answers neither question was called either way:\n%s", got)
	}
}
