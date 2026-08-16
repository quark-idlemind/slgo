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
// rather than the grid -- agents, login, logout, status -- reach it
// through client.Conn, which is a gRPC client and cannot be faked at
// the Backend level at all.  So there is a real gRPC server, in this
// process, on loopback, exactly as sl/hosted_test.go does it.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/auth"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/llsd"
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

// testRegionHandle is where the fake says the avatar is standing: grid
// square (1, 1), packed the way every message that names a region packs
// one.  Named because a teleport is addressed by handle, so a test about
// one has to be able to say which region it means.
const testRegionHandle = 1099511628032

// fakeGrid is an sl.Backend with nothing behind it.
//
// Every field is read under the lock, so a test may change an answer
// while the session is running -- an object that moves, a call that
// starts failing -- without racing the session's reader goroutine.
type fakeGrid struct {
	mu sync.Mutex

	info *sl.Info

	msgs     chan *sl.Message
	events   chan *sl.QueueEvent
	regions  chan *sl.RegionChange
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

	// objectsCalls counts how many times the region has been asked what
	// is in it, which is how a test tells a step that was taken from one
	// that was not needed: resolving a name the session already knows
	// must not cost a round trip.
	objectsCalls int
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
		msgs:    make(chan *sl.Message),
		events:  make(chan *sl.QueueEvent),
		regions: make(chan *sl.RegionChange),
		done:    make(chan struct{}),
		caps:    map[string]string{},
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
//
// Reading is a GET, and the two ways of changing something are a DELETE
// and a PATCH: rm, emptytrash and renaming all go over AIS rather than
// UDP, because the UDP messages for them are accepted and ignored.  So a
// fake that only answered GET would leave every command that changes
// inventory failing for want of a route, and each of them would pass its
// test for the wrong reason.
func (f *fakeGrid) serveInventory(t *testing.T) {
	t.Helper()
	f.ServeCap(t, agent.InventoryCap, func(w http.ResponseWriter, r *http.Request) {
		body, status := f.inventoryRequest(r)
		if status != http.StatusOK {
			http.Error(w, body, status)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, body)
	})
}

// inventoryRequest answers one AIS request against the tree, and is
// where the tree is changed.
func (f *fakeGrid) inventoryRequest(r *http.Request) (string, int) {
	const empty = `<?xml version="1.0" ?><llsd><map/></llsd>`
	kind, id, children := invPath(r.URL.Path)

	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.Method {
	case "DELETE":
		switch {
		case kind == "item":
			if !removeItem(f.inv, id) {
				return "no such item", http.StatusNotFound
			}
		case children:
			dir := findDir(f.inv, id)
			if dir == nil {
				return "no such folder", http.StatusNotFound
			}
			dir.Dirs, dir.Items = nil, nil
		default:
			if !removeDir(f.inv, id) {
				return "no such folder", http.StatusNotFound
			}
		}
		return empty, http.StatusOK

	case "PATCH":
		// Only the name is read back out again, so only the name is
		// applied; the rest of what AIS takes here is permissions,
		// which nothing in this package sets.
		var name string
		if v, err := llsd.Decode(bytes.NewReader(readAll(r))); err == nil {
			name, _ = llsd.Map(v)["name"].(string)
		}
		if name == "" {
			return "nothing to change", http.StatusBadRequest
		}
		if kind == "item" {
			it := findItem(f.inv, id)
			if it == nil {
				return "no such item", http.StatusNotFound
			}
			it.Name = name
			return empty, http.StatusOK
		}
		dir := findDir(f.inv, id)
		if dir == nil {
			return "no such folder", http.StatusNotFound
		}
		dir.Name = name
		return empty, http.StatusOK
	}

	dir := findDir(f.inv, capFolderID(r.URL.Path))
	if dir == nil {
		return "no such folder", http.StatusNotFound
	}
	depth, _ := strconv.Atoi(r.URL.Query().Get("depth"))
	return `<?xml version="1.0" ?><llsd>` + dirLLSD(dir, testRoot, depth) + `</llsd>`, http.StatusOK
}

func readAll(r *http.Request) []byte {
	b, _ := io.ReadAll(r.Body)
	return b
}

// invPath is what an AIS request names: the kind, the id, and whether it
// is about the contents rather than the thing itself.  The forms are
// /item/<id>, /category/<id> and /category/<id>/children.
func invPath(path string) (kind string, id msg.UUID, children bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return "", msg.UUID{}, false
	}
	id, _ = msg.ParseUUID(parts[1])
	return parts[0], id, len(parts) > 2 && parts[2] == "children"
}

func findItem(d *invDir, id msg.UUID) *invItem {
	if d == nil {
		return nil
	}
	for _, it := range d.Items {
		if it.ID == id {
			return it
		}
	}
	for _, sub := range d.Dirs {
		if got := findItem(sub, id); got != nil {
			return got
		}
	}
	return nil
}

// removeItem takes an item out of wherever in the tree it is, and says
// whether there was one.
func removeItem(d *invDir, id msg.UUID) bool {
	if d == nil {
		return false
	}
	for i, it := range d.Items {
		if it.ID == id {
			d.Items = append(d.Items[:i], d.Items[i+1:]...)
			return true
		}
	}
	for _, sub := range d.Dirs {
		if removeItem(sub, id) {
			return true
		}
	}
	return false
}

// removeDir is removeItem for a folder, which takes what is inside it
// with it.
func removeDir(d *invDir, id msg.UUID) bool {
	if d == nil {
		return false
	}
	for i, sub := range d.Dirs {
		if sub.ID == id {
			d.Dirs = append(d.Dirs[:i], d.Dirs[i+1:]...)
			return true
		}
		if removeDir(sub, id) {
			return true
		}
	}
	return false
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

// AskedTheRegion is how many times the region has been asked what is in
// it.  See objectsCalls.
func (f *fakeGrid) AskedTheRegion() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.objectsCalls
}

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

// RelayEvent hands one entry to the session as though it had come off
// the grid's event queue.
//
// The body is LLSD text rather than a struct, because an event has no
// template and no generated type: what a test asserts against has to be
// the shape a live grid sent.  See sl.ScriptRunning for the one captured
// on Agni.
func (f *fakeGrid) RelayEvent(t *testing.T, name, body string) {
	t.Helper()
	select {
	case f.events <- &sl.QueueEvent{Name: name, Body: []byte(body), At: time.Now()}:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read the event relay: is a session attached to this backend?")
	}
}

// RelayRegion tells the session the avatar is in another region, as the
// daemon does once it has followed a teleport somewhere.
//
// It is the only way the news arrives.  Polling for the position would
// answer with the new region as well, so a shell that has to act at the
// moment -- print a line, drop what it holds -- has to be told rather
// than to look.
func (f *fakeGrid) RelayRegion(t *testing.T, region string, handle uint64) {
	t.Helper()
	select {
	case f.regions <- &sl.RegionChange{Region: region, Handle: handle}:
	case <-time.After(5 * time.Second):
		t.Fatal("nothing read the region relay: is a session attached to this backend?")
	}
}

// AnswerTeleport makes the fake take the avatar to another region when
// it is asked to.
//
// Two things have to happen or a teleport would never return.  The
// finish arrives on the EVENT QUEUE rather than on the circuit, which is
// where Second Life sends it and what a fake answering on the circuit
// would let a session get away with reading.  And the presence has to
// start answering with the new region afterwards, because the finish
// says only that the simulator being left has let go: what says the
// avatar arrived is this session reporting the region the finish named.
//
// Whatever the fake was already answering is answered as well, since a
// teleport by name asks the map first and there is one hook between
// them.
func (f *fakeGrid) AnswerTeleport(t *testing.T, region string, handle uint64) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	before := f.onSend
	f.onSend = func(m msg.Message) {
		if before != nil {
			before(m)
		}
		// Both ways of asking, since an accepted lure is answered with
		// the same finish and differs only in what was sent.
		switch m.(type) {
		case *msg.TeleportLocationRequest, *msg.TeleportLureRequest:
		default:
			return
		}
		f.RelayEvent(t, "TeleportFinish", teleportFinish(handle))
		f.mu.Lock()
		f.presence.Region, f.presence.RegionHandle = region, handle
		f.mu.Unlock()
	}
}

// AnswerTeleportWith makes the fake refuse instead, with a body of the
// shape Agni sends: a key in one block and a sentence in the other.
func (f *fakeGrid) AnswerTeleportWith(t *testing.T, body string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	before := f.onSend
	f.onSend = func(m msg.Message) {
		if before != nil {
			before(m)
		}
		switch m.(type) {
		case *msg.TeleportLocationRequest, *msg.TeleportLureRequest:
			f.RelayEvent(t, "TeleportFailed", body)
		}
	}
}

// teleportFinish is the grid saying the region it has handed this avatar
// to.  The handle is LLSD binary, eight bytes big endian; the address
// and the seed beside it in a real one are for whoever moves the
// circuit, which is the daemon, and nothing here reads them.
func teleportFinish(handle uint64) string {
	h := binary.BigEndian.AppendUint64(nil, handle)
	return `<llsd><map><key>Info</key><array><map>` +
		`<key>AgentID</key><uuid>` + testMe.String() + `</uuid>` +
		`<key>RegionHandle</key><binary>` +
		base64.StdEncoding.EncodeToString(h) + `</binary>` +
		`<key>SimAccess</key><integer>13</integer>` +
		`<key>SimPort</key><integer>13032</integer>` +
		`</map></array></map></llsd>`
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

// AnswerProfile makes the fake answer an AvatarPropertiesRequest with
// the replies it is given, in the order it is given them.
//
// A simulator sends three and sends the groups FIRST, twenty
// milliseconds ahead of the properties, so a test that wants to catch
// the mistake that ordering predicts has to hand them over the same way
// round.  Handing over the groups alone is the other measured case: an
// avatar the grid has never heard of gets the empty group row and
// nothing else, ever.
//
// Whatever the fake was already answering is answered as well, because a
// profile with a partner in it needs the names answered too and there is
// one hook between them.
func (f *fakeGrid) AnswerProfile(t *testing.T, replies ...msg.Message) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	before := f.onSend
	f.onSend = func(m msg.Message) {
		if before != nil {
			before(m)
		}
		if _, ok := m.(*msg.AvatarPropertiesRequest); !ok {
			return
		}
		for _, r := range replies {
			f.Relay(t, r)
		}
	}
}

// AnswerMap makes the fake answer a MapNameRequest with these blocks and
// the end of the list after them.
//
// The end marker is added here rather than left to the caller because
// every reply has one and nothing returns without it: a fake that
// answered with regions alone would leave every lookup waiting out its
// deadline.  Its shape is the measured one -- no coordinates, an access
// code of 255, and the query lowercased with its last character taken
// off -- so that a command reading it as a region would be caught.
func (f *fakeGrid) AnswerMap(t *testing.T, blocks ...msg.MapBlockReply_Data) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		q, ok := m.(*msg.MapNameRequest)
		if !ok {
			return
		}
		asked := strings.ToLower(strings.TrimRight(string(q.NameData.Name), "\x00"))
		if asked != "" {
			asked = asked[:len(asked)-1]
		}
		r := &msg.MapBlockReply{Data: append(append([]msg.MapBlockReply_Data(nil), blocks...),
			msg.MapBlockReply_Data{Name: append([]byte(asked), 0), Access: 255})}
		r.AgentData.AgentID = testMe
		f.Relay(t, r)
	}
}

// mapBlock is one region as the map describes it, with the fields Second
// Life sends as zero left as zero.
func mapBlock(name string, x, y uint16, access uint8) msg.MapBlockReply_Data {
	return msg.MapBlockReply_Data{
		X: x, Y: y, Name: append([]byte(name), 0), Access: access,
	}
}

// avatarProperties is the reply that carries the profile itself.
func avatarProperties(who msg.UUID, born, about string, partner msg.UUID, flags uint32) *msg.AvatarPropertiesReply {
	m := &msg.AvatarPropertiesReply{}
	m.AgentData.AgentID, m.AgentData.AvatarID = testMe, who
	m.PropertiesData.BornOn = append([]byte(born), 0)
	m.PropertiesData.AboutText = append([]byte(about), 0)
	m.PropertiesData.PartnerID = partner
	m.PropertiesData.CharterMember = []byte{0}
	m.PropertiesData.Flags = flags
	return m
}

// avatarGroups is the reply that carries the groups an avatar lists.
// With no rows it is what an avatar who lists none really sends: one row
// of all zeros, which is not a group.
func avatarGroups(who msg.UUID, rows ...msg.AvatarGroupsReply_GroupData) *msg.AvatarGroupsReply {
	m := &msg.AvatarGroupsReply{}
	m.AgentData.AgentID, m.AgentData.AvatarID = testMe, who
	if len(rows) == 0 {
		rows = []msg.AvatarGroupsReply_GroupData{{GroupName: []byte{0}, GroupTitle: []byte{0}}}
	}
	m.GroupData = rows
	return m
}

func aGroup(id msg.UUID, name, title string) msg.AvatarGroupsReply_GroupData {
	return msg.AvatarGroupsReply_GroupData{
		GroupID:   id,
		GroupName: append([]byte(name), 0), GroupTitle: append([]byte(title), 0),
	}
}

func avatarInterests(who msg.UUID, wantTo, skills, languages string) *msg.AvatarInterestsReply {
	m := &msg.AvatarInterestsReply{}
	m.AgentData.AgentID, m.AgentData.AvatarID = testMe, who
	m.PropertiesData.WantToText = append([]byte(wantTo), 0)
	m.PropertiesData.SkillsText = append([]byte(skills), 0)
	m.PropertiesData.LanguagesText = append([]byte(languages), 0)
	return m
}

// AnswerAttach makes the fake put something on when it is asked to.
//
// A wear is not finished when the request goes out: the session waits
// for the region to describe a brand new object carrying the item it
// came from, since that is the only thing tying the two together.  So a
// fake that only accepted the request would leave every wear timing out
// after forty seconds.
//
// The point is the fake's to choose rather than an echo of the request,
// because the two differ in the case that matters: a wear with no point
// named asks for 0, meaning "wherever the object itself says", and what
// it lands on is known only from the answer.
//
// What comes off is the caller's to say for the same reason, and it is
// said as items rather than worked out from the point.  A replacing wear
// displaces ONE attachment however many are on the point -- measured on
// Agni, and recorded at the head of cmd/slsh/wear.go -- so a fake that
// cleared the point would agree with a command that named everything
// that had been there, which is the bug this argument exists to catch.
func (f *fakeGrid) AnswerAttach(t *testing.T, id msg.UUID, local uint32, point int, off ...msg.UUID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		r, ok := m.(*msg.RezSingleAttachmentFromInv)
		if !ok {
			return
		}
		f.takeOff(off...)
		f.Relay(t, &msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{
			FullID: id,
			ID:     local,
			// The point rides in the State byte with its nibbles
			// swapped, which is how the session reads it back.
			State:     uint8((point&0x0f)<<4 | (point>>4)&0x0f),
			NameValue: []byte("AttachItemID STRING RW DS " + r.ObjectData.ItemID.String() + "\n"),
		}}})
	}
}

// AnswerDetach makes the fake take something off when it is asked to,
// but not at once.
//
// The delay is the whole point of the helper.  Nothing replies to a
// detach: what says the thing came off is the object no longer being
// among what is worn, and on a real region that is true some time after
// the request rather than during it.  A fake that dropped the object
// inside the send would let a command which never waited at all pass.
func (f *fakeGrid) AnswerDetach(after time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		d, ok := m.(*msg.DetachAttachmentIntoInv)
		if !ok {
			return
		}
		item := d.ObjectData.ItemID
		time.AfterFunc(after, func() { f.takeOff(item) })
	}
}

// takeOff stops the fake listing the attachments worn from some items,
// which is all that coming off looks like from outside: the object goes
// away and the inventory item it was worn from does not.
func (f *fakeGrid) takeOff(items ...msg.UUID) {
	if len(items) == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var keep []*sl.Seen
	for _, o := range f.objects {
		gone := false
		for _, item := range items {
			gone = gone || o.AttachItem == item
		}
		if !gone {
			keep = append(keep, o)
		}
	}
	f.objects = keep
}

// AnswerActivateGroup makes the fake act as whatever group it is asked
// to.
//
// Nothing replies to ActivateGroup.  What says it took effect is the
// active group in a later AgentDataUpdate, which is why sl.ActivateGroup
// reads the presence back rather than trusting the send -- so a fake
// that recorded the message and left the presence alone would leave
// every activation waiting out its timeout for a change nobody was
// going to make.
func (f *fakeGrid) AnswerActivateGroup() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		a, ok := m.(*msg.ActivateGroup)
		if !ok {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.presence.ActiveGroup = a.AgentData.GroupID
	}
}

// AnswerLinking makes the fake join and take apart what it is asked to.
//
// Neither message is replied to.  What says a link happened is the
// children naming the root as their parent in an ordinary object update,
// and what says a delink happened is the same update naming nobody -- so
// a fake that took the request and said nothing would leave every link
// and every unlink waiting twenty seconds for a confirmation nothing was
// going to send.
//
// What the region has described is changed as well as relayed, because
// both commands read the linkset back to say what it is now: a fake that
// went on reporting the old shape would have the report contradict the
// update that arrived a moment before it.
func (f *fakeGrid) AnswerLinking(t *testing.T) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		var parent uint32
		var locals []uint32
		switch r := m.(type) {
		case *msg.ObjectLink:
			// The first block is the root and the rest go under it,
			// which is the order sl.Link packs them in.
			if len(r.ObjectData) < 2 {
				return
			}
			parent = r.ObjectData[0].ObjectLocalID
			for _, d := range r.ObjectData[1:] {
				locals = append(locals, d.ObjectLocalID)
			}
		case *msg.ObjectDelink:
			for _, d := range r.ObjectData {
				locals = append(locals, d.ObjectLocalID)
			}
		default:
			return
		}

		f.mu.Lock()
		var said []msg.ObjectUpdate_ObjectData
		for _, l := range locals {
			for _, o := range f.objects {
				if o.Local != l {
					continue
				}
				o.Parent = parent
				said = append(said, msg.ObjectUpdate_ObjectData{
					FullID: o.ID, ID: o.Local, ParentID: parent,
				})
			}
		}
		f.mu.Unlock()

		for _, u := range said {
			f.Relay(t, &msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{u}})
		}
	}
}

// heldItem is one thing inside a fake object and, when it is a script,
// what it will do about being started or stopped.
type heldItem struct {
	Name string
	ID   msg.UUID

	// Kind is the word the contents file uses for it, and "" is
	// "lsltext", a script.  Anything else is there to be filtered out.
	Kind string

	Running bool

	// Deaf answers no question at all, which is the region that has
	// taken the request and said nothing about it since.  Stuck answers
	// every question and never changes state, which is a script the
	// simulator will not start.
	Deaf, Stuck bool
}

// AnswerInside makes the fake serve what an object holds, and answer for
// the scripts inside it.
//
// Three protocols in one helper, because start and stop need all three
// and a fake that spoke any two of them would leave the commands waiting
// on the third: an object's contents arrive as a filename and then a
// file over xfer, SetScriptRunning is answered by nothing whatever, and
// the only way to learn whether it took is to ask with GetScriptRunning.
//
// The running state is kept here and changed by the request rather than
// echoed back at the asker, because that difference is what the commands
// are about: a script that was already running has to answer the
// question asked before anything is sent, and a stuck one has to go on
// answering with the state it began in however often it is told
// otherwise.
func (f *fakeGrid) AnswerInside(t *testing.T, task msg.UUID, held ...*heldItem) {
	var mu sync.Mutex
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		switch r := m.(type) {
		case *msg.RequestTaskInventory:
			f.Relay(t, replyTaskInventory(task, "inventory_37c9.tmp"))

		case *msg.RequestXfer:
			mu.Lock()
			file := taskInventoryFile(task, held)
			mu.Unlock()
			f.Relay(t, xferPacket(r.XferID.ID, 0, true, file))

		case *msg.SetScriptRunning:
			mu.Lock()
			for _, h := range held {
				if h.ID == r.Script.ItemID && !h.Stuck {
					h.Running = r.Script.Running
				}
			}
			mu.Unlock()

		case *msg.GetScriptRunning:
			mu.Lock()
			say := ""
			for _, h := range held {
				if h.ID != r.Script.ItemID || h.Deaf {
					continue
				}
				say = scriptRunningBody(r.Script.ObjectID, h.ID, h.Running)
			}
			mu.Unlock()
			if say != "" {
				// The event queue and not the circuit, which is what
				// Agni does: measured there, nothing came back on the
				// circuit at all.  See sl.ScriptRunning.
				f.RelayEvent(t, "ScriptRunningReply", say)
			}
		}
	}
}

// scriptRunningBody is what Agni answers GetScriptRunning with, on the
// event queue.
//
// Copied from a capture rather than composed, because its shape is the
// whole reason the answer goes over the queue at all: the Script block
// arrives as an ARRAY where the template declares a single block, and
// the map carries Mono and -- since some time before August 2026 -- Luau
// and LuauLanguage, none of which the template has.  The unknown ones
// are here to be ignored; a decoder that choked on them would fail on
// the next field Linden Lab adds.
func scriptRunningBody(object, item msg.UUID, running bool) string {
	n := 0
	if running {
		n = 1
	}
	return fmt.Sprintf(`<llsd><map><key>Script</key><array><map>`+
		`<key>Running</key><boolean>%d</boolean>`+
		`<key>ItemID</key><string>%s</string>`+
		`<key>Luau</key><boolean>0</boolean><key>LuauLanguage</key><boolean>0</boolean>`+
		`<key>Mono</key><boolean>1</boolean>`+
		`<key>ObjectID</key><string>%s</string>`+
		`</map></array></map></llsd>`, n, item, object)
}

// replyTaskInventory is the simulator naming the file it has written an
// object's contents to.
func replyTaskInventory(task msg.UUID, filename string) *msg.ReplyTaskInventory {
	m := &msg.ReplyTaskInventory{}
	m.InventoryData.TaskID = task
	m.InventoryData.Serial = 1
	m.InventoryData.Filename = append([]byte(filename), 0)
	return m
}

// xferPacket is one packet of a file arriving over the xfer protocol.
// The first carries a four byte length prefix that is not part of the
// file, and the last is marked in the top bit of its number.
func xferPacket(id uint64, seq uint32, last bool, data []byte) *msg.SendXferPacket {
	m := &msg.SendXferPacket{}
	m.XferID.ID = id
	m.XferID.Packet = seq
	if last {
		m.XferID.Packet |= 0x80000000
	}
	if seq == 0 {
		m.DataPacket.Data = binary.LittleEndian.AppendUint32(nil, uint32(len(data)))
	}
	m.DataPacket.Data = append(m.DataPacket.Data, data...)
	return m
}

// taskInventoryFile is the contents file for an object: the nested
// braces sl.TaskInventory parses, with only the fields anything here
// reads filled in.  A name ends with a bar, which is how the format
// marks the end of a value that may have spaces in it.
func taskInventoryFile(task msg.UUID, held []*heldItem) []byte {
	var b strings.Builder
	for _, h := range held {
		kind := h.Kind
		if kind == "" {
			kind = "lsltext"
		}
		fmt.Fprintf(&b, "\tinv_item\t0\n\t{\n")
		fmt.Fprintf(&b, "\t\titem_id\t%s\n", h.ID)
		fmt.Fprintf(&b, "\t\tparent_id\t%s\n", task)
		fmt.Fprintf(&b, "\t\ttype\t%s\n", kind)
		fmt.Fprintf(&b, "\t\tinv_type\t%s\n", kind)
		fmt.Fprintf(&b, "\t\tname\t%s|\n", h.Name)
		fmt.Fprintf(&b, "\t}\n")
	}
	return []byte(b.String())
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

func (f *fakeGrid) Messages() <-chan *sl.Message  { return f.msgs }
func (f *fakeGrid) Events() <-chan *sl.QueueEvent { return f.events }
func (f *fakeGrid) Done() <-chan struct{}         { return f.done }
func (f *fakeGrid) Err() error                    { return nil }

func (f *fakeGrid) RegionChanges() <-chan *sl.RegionChange { return f.regions }

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
	f.objectsCalls++
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
		close(f.events)
		close(f.regions)
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

	// presence is what to answer the where-am-I call with, for the
	// commands whose answer is state slgod holds and a client can only
	// be handed.  Unset means the least a session can say: a region
	// name and nothing else.
	presence *pb.PresenceResponse

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

	// credential is what a viewer credential request is answered with,
	// and credentialFail is the refusal instead -- a daemon serving no
	// viewer logins, or a profile that may not be handed over.  Both
	// are what the shell has to make readable.
	credential     *pb.ViewerCredentialResponse
	credentialFail error

	// credentials counts how many were asked for, since minting is the
	// one call here with an effect: asking twice for one launch would
	// leave a live password behind.
	credentials int
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

func (d *fakeDaemon) ViewerCredential(context.Context, *pb.ViewerCredentialRequest) (*pb.ViewerCredentialResponse, error) {
	d.credentials++
	if d.credentialFail != nil {
		return nil, status.Error(codes.FailedPrecondition, d.credentialFail.Error())
	}
	return d.credential, nil
}

func (d *fakeDaemon) Host(context.Context, *pb.HostRequest) (*pb.HostResponse, error) {
	if d.fail != nil {
		return nil, d.fail
	}
	return d.host, nil
}

// Logout answers the way slgod does: a refusal is a status, and who is
// holding the session travels in its details, since a unary call gives
// back a message or a status and never both.
func (d *fakeDaemon) Logout(context.Context, *pb.LogoutRequest) (*pb.LogoutResponse, error) {
	if d.fail != nil {
		st := status.New(codes.FailedPrecondition, d.fail.Error())
		if len(d.logout.GetClients()) > 0 {
			if with, err := st.WithDetails(d.logout); err == nil {
				st = with
			}
		}
		return nil, st.Err()
	}
	return d.logout, nil
}

func (d *fakeDaemon) Presence(context.Context, *pb.PresenceRequest) (*pb.PresenceResponse, error) {
	if d.presence != nil {
		return d.presence, nil
	}
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
