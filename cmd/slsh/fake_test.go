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
// newDaemonShell is the other half: the commands that ask slgod
// rather than the grid -- agents, login, logout, status, viewer -- reach
// it through client.Conn, which is a gRPC client and cannot be faked at
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
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/internal/auth"
	"github.com/quark-idlemind/slgo/llsd"
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
	testScripts   = msg.MustParseUUID("aa8f7e57-7e57-c0de-e8da-278417da2fea")
	testTrash     = msg.MustParseUUID("ab6c7e57-7e57-c0de-e0ce-9f8b1da5cee8")
	testLamp      = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000001")
	testNote      = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000002")
	testProbe     = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000003")
	testSomebody  = msg.MustParseUUID("d22b7e57-7e57-c0de-0e4e-000000000001")
	testOutfit    = msg.MustParseUUID("abab7e57-7e57-c0de-bb52-1c1cafc8878a")
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

	// land is what Land answers, and landErr a daemon that will not.
	land    *sl.Land
	landErr error

	// controls is every control flag the session has asked for, and
	// onControl is the same hook for them that onSend is for messages.
	//
	// They are kept apart because they are apart: a control flag is not
	// a message and does not go through Send.  An AgentUpdate carries
	// the camera as well as the flags, so only whoever owns the camera
	// can build one, and a client asks for the bit instead -- which is
	// why sitting on the ground and standing up leave nothing at all in
	// sent.  See sl.controlUntil and agent.Control.
	controls  []uint32
	onControl func(uint32)

	// move is how the fake answers a walk, and faced and halts are the
	// turns and stops it was asked for.  See walk_test.go.
	move  func(sl.MoveRequest, func(sl.MoveProgress)) sl.MoveProgress
	faced []float32
	halts int

	presence *sl.Presence
	region   *sl.Region
	objects  []*sl.Seen
	friends  []sl.Friend

	// inv is the inventory tree, served over the capability rather
	// than answered from here: everything that reads inventory goes
	// through AIS, so a fake that short-circuited it would be testing
	// a path the shell does not take.  The UDP moves change it too, as
	// they change the grid's, unless ignoreMoves makes this the grid
	// that takes a move into Trash and does nothing; see moveLocked.
	inv         *invDir
	ignoreMoves bool

	// caps maps a capability name to the base URL serving it, and
	// lockedBy is who TryLock should say holds one rather than handing
	// it over.
	caps     map[string]string
	lockedBy string

	// neighbours is what the daemon holds of the regions around this
	// one, changed by a set the way the real backends change it:
	// turning them off drops the circuits rather than only refusing
	// the next offer.
	neighbours    sl.Neighbours
	neighboursErr error

	// regionDetails is what the region says to RequestRegionInfo,
	// regionHeard counts what it has said, and regionSilent makes it
	// say nothing.
	regionDetails *sl.RegionDetails
	regionHeard   uint64
	regionSilent  bool

	// simStats is what SimStats answers; nil is none heard yet.
	simStats    *sl.SimStats
	simStatsErr error

	presenceErr, objectsErr, regionErr, friendsErr, sendErr, capErr error

	// how is what Descriptions says of each object, by id.
	how map[msg.UUID][]sl.Description

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

	// links counts the links created through AIS, and is what gives
	// each one an id a test can name.
	links int

	// baked counts the rebake requests, which is the only evidence
	// from this side that wearing a wearable finished.
	baked int

	// sim is the simulator's attachment list, as SimAttachments
	// answers it; nil is one never heard.  simOnBake makes each rebake
	// list what the region has described, as the simulator does, from
	// the Current Outfit folder as it is then.
	sim       *sl.SimAttachments
	simOnBake bool

	// later is what the region describes late: attachments that are
	// on and have not been described yet, which appear in the listing
	// once it has been asked for laterAt times.
	later   []*sl.Seen
	laterAt int

	// endedWith is what Err says once the stream is over: nil for one
	// that closed, and slgod's status for one it ended.
	endedWith error
}

// invDir is a folder in the fake inventory, and invItem a thing in one.
type invDir struct {
	ID    msg.UUID
	Name  string
	Type  int
	Dirs  []*invDir
	Items []*invItem

	// Version is what AIS reports for the folder.  It matters for the
	// Current Outfit folder and nowhere else: a rebake request carries
	// the version, and a request carrying the wrong one is refused.
	Version int
}

type invItem struct {
	ID      msg.UUID
	Name    string
	Type    int
	InvType int
	Asset   msg.UUID
	Desc    string
	Created int64

	// Flags is the item's flag word.  Its low byte is the slot a
	// system wearable occupies, which is the only place that is
	// recorded: a skin and a shape are both body parts and are told
	// apart by nothing else.
	Flags uint32

	// IsLink puts this under "links" rather than "items", where its
	// Asset goes out as linked_id.  That is the shape AIS sends and
	// the whole of what makes an outfit folder different from any
	// other folder full of names.
	IsLink bool

	// Masks are the permission masks, base, owner, group, everyone and
	// next owner, in the order sl.PermsJSON has them.  Nil is an item
	// its owner may do anything with, and nothing said about the rest.
	Masks *sl.PermsJSON
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
			ID:          msg.MustParseUUID("a4fd7e57-7e57-c0de-559f-7a9b7da6044a"),
			Name:        "Test Region",
			Handle:      1099511628032,
			Access:      13,
			WaterHeight: 20,
			ProductName: "Estate / Full Region",
		},
		regionDetails: &sl.RegionDetails{
			Name: "Test Region", AgentLimit: 40, ObjectBonus: 1.5,
			TerrainRaiseLimit: 80, TerrainLowerLimit: -40,
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
				{ID: testOutfit, Name: "Current Outfit", Type: sl.FolderCurrentOutfit},
			},
			Items: []*invItem{
				{ID: testNote, Name: "readme", Type: int(sl.AssetNotecard), Created: 1754000200},
			},
		},
	}
	t.Cleanup(func() { f.Close() })
	f.serveInventory(t)
	f.serveAppearance(t)
	return f
}

// serveInventory puts the tree behind the AIS capability.
//
// Reading is a GET, and the ways of changing something are a DELETE, a
// PATCH and a POST: rm, emptytrash, renaming and linking all go over
// AIS rather than UDP, because the UDP messages for them are accepted
// and ignored -- the one for linking is refused outright, with "Cannot
// create requested inventory."  So a fake that only answered GET would
// leave every command that changes inventory failing for want of a
// route, and each of them would pass its test for the wrong reason.
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

// serveAppearance answers the rebake request.
//
// Wearing a system wearable is two steps -- a link into the Current
// Outfit folder and then this -- and only the second says the wearing
// finished.  A fake without it would leave every such test failing at
// the last line for want of a capability.
func (f *fakeGrid) serveAppearance(t *testing.T) {
	t.Helper()
	f.ServeCap(t, "UpdateAvatarAppearance", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.baked++
		if f.simOnBake {
			// What is attached, described or not: the simulator knows
			// what it rezzed whether or not the region has said so.
			var on []sl.SimAttachment
			for _, o := range append(append([]*sl.Seen(nil), f.objects...), f.later...) {
				if !o.AttachItem.IsZero() && !sl.IsHUDPoint(o.AttachPoint) {
					on = append(on, sl.SimAttachment{Object: o.ID, Point: o.AttachPoint})
				}
			}
			f.sim = &sl.SimAttachments{CofVersion: f.cofVersionLocked(), Heard: time.Now(), Objects: on}
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, `<llsd><map><key>success</key><boolean>1</boolean></map></llsd>`)
	})
}

// simLists sets the simulator's attachment list, baked from the Current
// Outfit folder as it is now unless stale.
func (f *fakeGrid) simLists(stale bool, on ...sl.SimAttachment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.cofVersionLocked()
	if stale {
		v--
	}
	f.sim = &sl.SimAttachments{CofVersion: v, Heard: time.Now(), Objects: on}
}

// describeLater has the region describe an attachment only once it has
// been asked what is in it this many more times.
func (f *fakeGrid) describeLater(item, object msg.UUID, local uint32, point, calls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.later = append(f.later, &sl.Seen{
		Object: sl.Object{ID: object, Local: local}, PCode: 9,
		Parent: 1, AttachItem: item, AttachPoint: point,
	})
	f.laterAt = f.objectsCalls + calls
}

func (f *fakeGrid) cofVersionLocked() int {
	var walk func(d *invDir) int
	walk = func(d *invDir) int {
		if d == nil {
			return 0
		}
		if d.Type == sl.FolderCurrentOutfit {
			return d.Version
		}
		for _, c := range d.Dirs {
			if v := walk(c); v != 0 {
				return v
			}
		}
		return 0
	}
	return walk(f.inv)
}

func (f *fakeGrid) SimAttachments(ctx context.Context, avatar msg.UUID) (*sl.SimAttachments, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.sim == nil || (!avatar.IsZero() && avatar != testMe) {
		return nil, nil
	}
	c := *f.sim
	c.Objects = append([]sl.SimAttachment(nil), f.sim.Objects...)
	return &c, nil
}

// Baked is how many rebakes have been asked for.
func (f *fakeGrid) Baked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.baked
}

// inventoryRequest answers one AIS request against the tree, and is
// where the tree is changed.
func (f *fakeGrid) inventoryRequest(r *http.Request) (string, int) {
	const empty = `<?xml version="1.0" ?><llsd><map/></llsd>`
	kind, id, children := invPath(r.URL.Path)

	f.mu.Lock()
	defer f.mu.Unlock()

	switch r.Method {
	case "POST":
		// Creating inventory in a folder.  Only links are made this
		// way here, which is how a wearable is worn: the request names
		// what to point at and the folder it goes in.
		dir := findDir(f.inv, id)
		if dir == nil {
			return "no such folder", http.StatusNotFound
		}
		v, err := llsd.Decode(r.Body)
		if err != nil {
			return "unreadable body", http.StatusBadRequest
		}
		m := llsd.Map(v)
		links, _ := m["links"].([]any)
		if len(links) == 0 {
			return "no links in the request", http.StatusBadRequest
		}
		for _, l := range links {
			lm := llsd.Map(l)
			to, err := msg.ParseUUID(llsd.String(lm, "linked_id"))
			if err != nil {
				return "no linked_id", http.StatusBadRequest
			}
			dir.Items = append(dir.Items, &invItem{
				ID: f.nextLinkID(), Name: llsd.String(lm, "name"),
				Desc: llsd.String(lm, "desc"), Asset: to, IsLink: true,
				Type: int(sl.AssetLink), InvType: int(llsd.Int(lm, "inv_type")),
			})
		}
		dir.Version++
		return empty, http.StatusOK

	case "DELETE":
		switch {
		case kind == "item":
			if !removeItem(f.inv, id) {
				return "no such item", http.StatusNotFound
			}
			// Taking something off is a change to the folder too, and
			// the rebake that follows carries the new version.
			if cof := findDirOfType(f.inv, sl.FolderCurrentOutfit); cof != nil {
				cof.Version++
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
		// applied; the rest of what AIS takes here, a description and
		// permissions, nothing in this package sets.
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

// moveLocked applies a move to the tree, as the grid does, so that the
// read-back that follows one finds it; a move of something the tree does
// not hold, or into a folder it does not have, changes nothing.  With mu
// held.
func (f *fakeGrid) moveLocked(m msg.Message) {
	if f.ignoreMoves {
		return
	}
	switch v := m.(type) {
	case *msg.MoveInventoryItem:
		for _, d := range v.InventoryData {
			it, to := findItem(f.inv, d.ItemID), findDir(f.inv, d.FolderID)
			if it == nil || to == nil {
				continue
			}
			removeItem(f.inv, d.ItemID)
			if name := strings.TrimSuffix(string(d.NewName), "\x00"); name != "" {
				it.Name = name
			}
			to.Items = append(to.Items, it)
		}
	case *msg.MoveInventoryFolder:
		for _, d := range v.InventoryData {
			dir, to := findDir(f.inv, d.FolderID), findDir(f.inv, d.ParentID)
			if dir == nil || to == nil || findDir(dir, d.ParentID) != nil {
				continue
			}
			removeDir(f.inv, d.FolderID)
			to.Dirs = append(to.Dirs, dir)
		}
	}
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

// nextLinkID gives each link the fake creates an id of its own, in
// order, so that a test can say which one it means.
func (f *fakeGrid) nextLinkID() msg.UUID {
	f.links++
	return msg.MustParseUUID(fmt.Sprintf("f3a47e57-7e57-c0de-be8b-%012d", f.links))
}

// findDirOfType is how the Current Outfit folder and the trash are
// found: by what the grid says they are for, not by their names.
func findDirOfType(d *invDir, kind int) *invDir {
	if d == nil {
		return nil
	}
	if d.Type == kind {
		return d
	}
	for _, sub := range d.Dirs {
		if got := findDirOfType(sub, kind); got != nil {
			return got
		}
	}
	return nil
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
	version := d.Version
	if version == 0 {
		version = 1
	}
	fmt.Fprintf(&b, `<key>version</key><integer>%d</integer>`, version)
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
	b.WriteString(`</map>`)
	invItemsLLSD(&b, d, "items", false)
	invItemsLLSD(&b, d, "links", true)
	b.WriteString(`</map></map>`)
	return b.String()
}

// invItemsLLSD writes one of the two maps a folder's contents arrive
// in.  A link goes in the second and names what it points at with
// linked_id, where an item names its asset with asset_id; everything
// else about the two is written the same way, which is the point --
// only the key and that one field tell them apart.
func invItemsLLSD(b *strings.Builder, d *invDir, key string, links bool) {
	fmt.Fprintf(b, `<key>%s</key><map>`, key)
	for _, it := range d.Items {
		if it.IsLink != links {
			continue
		}
		fmt.Fprintf(b, `<key>%s</key><map>`, it.ID)
		fmt.Fprintf(b, `<key>item_id</key><string>%s</string>`, it.ID)
		fmt.Fprintf(b, `<key>parent_id</key><string>%s</string>`, d.ID)
		if links {
			fmt.Fprintf(b, `<key>linked_id</key><string>%s</string>`, it.Asset)
		} else {
			fmt.Fprintf(b, `<key>asset_id</key><string>%s</string>`, it.Asset)
		}
		fmt.Fprintf(b, `<key>name</key><string>%s</string>`, xmlText(it.Name))
		fmt.Fprintf(b, `<key>desc</key><string>%s</string>`, xmlText(it.Desc))
		fmt.Fprintf(b, `<key>type</key><integer>%d</integer>`, it.Type)
		fmt.Fprintf(b, `<key>inv_type</key><integer>%d</integer>`, it.InvType)
		fmt.Fprintf(b, `<key>created_at</key><integer>%d</integer>`, it.Created)
		fmt.Fprintf(b, `<key>flags</key><integer>%d</integer>`, it.Flags)
		b.WriteString(`<key>permissions</key><map>`)
		if m := it.Masks; m != nil {
			for _, f := range []struct {
				key  string
				mask uint32
			}{
				{"base_mask", m.Base}, {"owner_mask", m.Owner}, {"group_mask", m.Group},
				{"everyone_mask", m.Everyone}, {"next_owner_mask", m.Next},
			} {
				fmt.Fprintf(b, `<key>%s</key><integer>%d</integer>`, f.key, f.mask)
			}
		} else {
			b.WriteString(`<key>owner_mask</key><integer>581632</integer>`)
		}
		b.WriteString(`</map></map>`)
	}
	b.WriteString(`</map>`)
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

// Controls is every control flag the session has asked for, in order.
//
// There may be more of them than a command asked for and that is not a
// fault: a flag is held rather than sent once, so a confirmation that
// takes longer than half a second is answered with the same flag again.
// See sl.controlUntil.
func (f *fakeGrid) Controls() []uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]uint32(nil), f.controls...)
}

// Relay hands a message to the session as though the grid had sent it.
//
// Unlike the one in package sl this does not wait for the reader to
// finish with it: a test here waits for what the message leads to
// instead -- a line on the terminal, or the command that was waiting
// for it returning.
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
// the shape a live grid sent.  See
// doc/scripts.md#whether-a-script-is-running for the one captured on
// Agni.
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
	f.RelayRegionBy(t, region, handle, 0)
}

// RelayRegionBy is RelayRegion for a teleport that said why, in the
// TeleportStart's flags.
func (f *fakeGrid) RelayRegionBy(t *testing.T, region string, handle uint64, flags uint32) {
	t.Helper()
	select {
	case f.regions <- &sl.RegionChange{Region: region, Handle: handle, TeleportFlags: flags}:
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

// AnswerLocalTeleport stands the avatar where a teleport inside this
// region asked it to stand.
//
// A move within a region is answered by the position agreeing and by
// nothing else -- there is no reply to a TeleportLocationRequest that
// stays here -- so a fake that recorded the message and left the
// presence alone would make every such teleport wait out its whole
// timeout.  Most tests avoid it by asking for the position the fake is
// already at; one that moves the avatar wants this.
func (f *fakeGrid) AnswerLocalTeleport() {
	f.mu.Lock()
	defer f.mu.Unlock()
	before := f.onSend
	f.onSend = func(m msg.Message) {
		if before != nil {
			before(m)
		}
		tp, ok := m.(*msg.TeleportLocationRequest)
		if !ok {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if tp.Info.RegionHandle == f.presence.RegionHandle {
			f.presence.Position = tp.Info.Position
		}
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
// Agni, and recorded in doc/slsh.md -- so a fake that
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
		// The region gains the object, which is where everything that
		// asks "what is worn" looks: the session's own relay knows it
		// from the update below, and the daemon's object store knows
		// it from being in the region.  A fake that only relayed left
		// the second empty, so anything reading the region saw an
		// avatar wearing nothing however much it had just put on.
		f.attached(r.ObjectData.ItemID, id, local, point)
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

// attached puts a worn object into the region, parented to this
// avatar, the way a simulator does when it rezzes one.
func (f *fakeGrid) attached(item, object msg.UUID, local uint32, point int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	const avatar = uint32(1)
	described := false
	for _, o := range f.objects {
		described = described || o.ID == testMe
	}
	if !described {
		f.objects = append(f.objects,
			&sl.Seen{Object: sl.Object{ID: testMe, Local: avatar}, PCode: 47})
	}
	for _, o := range f.objects {
		if o.AttachItem == item {
			return // already on; a replace puts it back where it was
		}
	}
	f.objects = append(f.objects, &sl.Seen{
		Object: sl.Object{ID: object, Local: local}, PCode: 9,
		Parent: avatar, AttachItem: item, AttachPoint: point,
	})
}

// takeOff stops the fake listing the attachments worn from some items,
// which is all that coming off looks like from outside: the object goes
// away and the inventory item it was worn from does not.  The simulator
// also kills the attachment, which is what sl.TakeOff waits to hear.
// Why: doc/slsh.md#deleting-straight-after-a-take-off
func (f *fakeGrid) takeOff(items ...msg.UUID) { f.remove(true, items...) }

// forget is takeOff as a reconnect does it: the region stops listing the
// attachments and the session is told nothing.
func (f *fakeGrid) forget(items ...msg.UUID) { f.remove(false, items...) }

func (f *fakeGrid) remove(kill bool, items ...msg.UUID) {
	if len(items) == 0 {
		return
	}
	var kills []msg.KillObject_ObjectData
	defer func() {
		if len(kills) == 0 {
			return
		}
		m := &msg.KillObject{ObjectData: kills}
		body, err := m.Encode()
		if err != nil {
			return
		}
		select {
		case f.msgs <- &sl.Message{ID: msg.IDOf(m), Name: m.MsgInfo().Name, Body: body, At: time.Now()}:
		case <-time.After(5 * time.Second):
		}
	}()
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
		} else {
			if kill {
				kills = append(kills, msg.KillObject_ObjectData{ID: o.Local})
			}
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

// AnswerGroupChat makes the fake answer the start of a group's chat as the
// grid does, on the event queue: a ChatterBoxSessionStartReply, successful
// or, when reason is set, refused with that reason.  Nothing answers a
// leave or a message.  The shape is the viewer's handler's, not one seen.
func (f *fakeGrid) AnswerGroupChat(t *testing.T, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		im, ok := m.(*msg.ImprovedInstantMessage)
		if !ok || im.MessageBlock.Dialog != sl.DialogSessionGroupStart {
			return
		}
		group := im.MessageBlock.ID
		body := fmt.Sprintf(`<?xml version="1.0" ?><llsd><map><key>success</key><boolean>1</boolean>`+
			`<key>temp_session_id</key><uuid>%s</uuid><key>session_id</key><uuid>%s</uuid></map></llsd>`, group, group)
		if reason != "" {
			body = fmt.Sprintf(`<?xml version="1.0" ?><llsd><map><key>success</key><boolean>0</boolean>`+
				`<key>temp_session_id</key><uuid>%s</uuid><key>error</key><string>%s</string></map></llsd>`, group, reason)
		}
		f.RelayEvent(t, "ChatterBoxSessionStartReply", body)
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

// AnswerPermissions makes the fake keep an object's permission masks
// the way a simulator does: each ObjectPermissions turns bits on or off
// and grant says what the mask becomes, and every selection is answered
// with the masks in full, which is where they are read back from.  The
// masks start as a fresh prim's: everything for the base, the owner and
// the next owner, and nothing for the group or everyone.
func (f *fakeGrid) AnswerPermissions(t *testing.T, grant func(who uint8, mask uint32) uint32) {
	masks := map[uint8]uint32{sl.WhoBase: sl.PermAll, sl.WhoOwner: sl.PermAll, sl.WhoNextOwner: sl.PermAll}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		switch r := m.(type) {
		case *msg.ObjectPermissions:
			for _, d := range r.ObjectData {
				mask := masks[d.Field]
				if d.Set == 1 {
					mask |= d.Mask
				} else {
					mask &^= d.Mask
				}
				masks[d.Field] = grant(d.Field, mask)
			}
		case *msg.ObjectSelect:
			reply := &msg.ObjectProperties{}
			for _, d := range r.ObjectData {
				f.mu.Lock()
				var id msg.UUID
				for _, o := range f.objects {
					if o.Local == d.ObjectLocalID {
						id = o.ID
					}
				}
				f.mu.Unlock()
				reply.ObjectData = append(reply.ObjectData, msg.ObjectProperties_ObjectData{
					ObjectID: id, OwnerID: testMe, CreatorID: testMe,
					BaseMask: masks[sl.WhoBase], OwnerMask: masks[sl.WhoOwner],
					GroupMask: masks[sl.WhoGroup], EveryoneMask: masks[sl.WhoEveryone],
					NextOwnerMask: masks[sl.WhoNextOwner],
				})
			}
			f.Relay(t, reply)
		}
	}
}

// AnswerDeletes makes the fake kill every object a derez names, which is
// how a region says a delete happened: a KillObject naming its local id.
func (f *fakeGrid) AnswerDeletes(t *testing.T) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.onSend = func(m msg.Message) {
		d, ok := m.(*msg.DeRezObject)
		if !ok {
			return
		}
		k := &msg.KillObject{}
		for _, o := range d.ObjectData {
			k.ObjectData = append(k.ObjectData, msg.KillObject_ObjectData{ID: o.ObjectLocalID})
		}
		f.Relay(t, k)
	}
}

// AnswerPosture makes the fake sit the avatar down and stand it up
// again, in the three ways a simulator does it.
//
// Three, because the two kinds of sit share nothing on the wire.  An
// object sit is a message, and the answer to it is this avatar's own
// object update coming back with the seat's local id in it: there is no
// reply of any other kind, and that reparenting is what a sit waits
// for.  A ground sit is a control flag answered by nothing whatever
// except the animation list, so a fake that spoke only the first would
// leave every ground sit waiting out its timeout and passing for a
// reason that has nothing to do with the command.  A stand is another
// flag, which un-parents from an object sit and stops the animation
// from a ground one.
//
// mine is the local id the region has given this avatar.  It rides in
// that same update, which is why nothing need have described the avatar
// beforehand: a session that has not been told which object it is
// cannot read its own parent, and the update that seats it says both
// things at once.
func (f *fakeGrid) AnswerPosture(t *testing.T, mine uint32) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	before := f.onSend
	f.onSend = func(m msg.Message) {
		if before != nil {
			before(m)
		}
		r, ok := m.(*msg.AgentRequestSit)
		if !ok {
			return
		}
		f.mu.Lock()
		var seat uint32
		for _, o := range f.objects {
			if o.ID == r.TargetObject.TargetID {
				seat = o.Local
			}
		}
		f.mu.Unlock()
		// An id that names nothing here is answered with nothing at
		// all, which is not what a simulator does -- it refuses in
		// words, and AnswerSitRefused is that -- but it is what a
		// request nobody answered looks like.
		if seat == 0 {
			return
		}
		f.Relay(t, parentedTo(mine, seat))
	}

	f.onControl = func(flags uint32) {
		switch {
		case flags&agent.ControlSitOnGround != 0:
			f.Relay(t, animating(agent.AnimSitGroundConstrained))
		case flags&agent.ControlStandUp != 0:
			// Both halves of getting up, since one flag does it from
			// either kind of sit and this does not track which the
			// avatar is in: the parent goes back to nothing, and the
			// animation list comes back with the stand in it and no sit.
			f.Relay(t, parentedTo(mine, 0))
			f.Relay(t, animating(agent.AnimStand))
		}
	}
}

// AnswerSitRefused makes the fake refuse a sit the way Agni does: an
// alert, in its own words, ending in the NUL byte that has to be
// trimmed off before anybody reads it.
func (f *fakeGrid) AnswerSitRefused(t *testing.T, said string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	before := f.onSend
	f.onSend = func(m msg.Message) {
		if before != nil {
			before(m)
		}
		if _, ok := m.(*msg.AgentRequestSit); !ok {
			return
		}
		r := &msg.AlertMessage{}
		r.AlertData.Message = append([]byte(said), 0)
		f.Relay(t, r)
	}
}

// parentedTo is this avatar's own object update, saying what it is
// sitting on: a seat's local id, or zero for the ground and for
// standing.
func parentedTo(mine, seat uint32) *msg.ObjectUpdate {
	return &msg.ObjectUpdate{ObjectData: []msg.ObjectUpdate_ObjectData{{
		FullID: testMe, ID: mine, ParentID: seat,
	}}}
}

// animating is the animation list for this avatar.
//
// The whole list every time, because that is how it arrives: an
// animation that has stopped is simply absent from the next one, and
// that absence is the only way standing up from a ground sit is ever
// heard about.
func animating(ids ...msg.UUID) *msg.AvatarAnimation {
	m := &msg.AvatarAnimation{}
	m.Sender.ID = testMe
	for i, id := range ids {
		m.AnimationList = append(m.AnimationList, msg.AvatarAnimation_AnimationList{
			AnimID: id, AnimSequenceID: int32(i + 1),
		})
	}
	return m
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

	// Text is what reading it hands back, for cat --in.
	Text string

	// Owner and the two masks are the item's permissions, for fetch:
	// a zero Owner is the avatar the shell is, and a zero OwnerMask
	// is every right, so an item left alone is one that is copied.
	Owner                   msg.UUID
	OwnerMask, EveryoneMask uint32
}

// owner is who the contents file says owns it.
func (h *heldItem) owner() msg.UUID {
	if h.Owner.IsZero() {
		return testMe
	}
	return h.Owner
}

// ownerMask is the rights the contents file gives its owner.
func (h *heldItem) ownerMask() uint32 {
	if h.OwnerMask == 0 {
		return sl.PermAll
	}
	return h.OwnerMask
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

		case *msg.TransferRequest:
			// A read of something inside.  The parameters are the
			// agent, the session, the owner, the object and then the
			// item; see internal/xfer/transfer.go.
			item := msg.UUID(r.TransferInfo.Params[64:80])
			mu.Lock()
			var text *string
			refused := false
			for _, h := range held {
				if h.ID == item {
					text = &h.Text
					// A notecard that may not be copied may not be
					// read, which is what Agni says about one.
					refused = h.Kind == "notecard" && h.ownerMask()&sl.PermCopy == 0
				}
			}
			mu.Unlock()
			info := &msg.TransferInfo{}
			info.TransferInfo.TransferID = r.TransferInfo.TransferID
			info.TransferInfo.ChannelType = 2
			if refused {
				info.TransferInfo.Status = -3 // insufficient permissions
				f.Relay(t, info)
				return
			}
			if text == nil {
				info.TransferInfo.Status = -2 // unknown source
				f.Relay(t, info)
				return
			}
			info.TransferInfo.Size = int32(len(*text))
			f.Relay(t, info)
			p := &msg.TransferPacket{}
			p.TransferData.TransferID = r.TransferInfo.TransferID
			p.TransferData.ChannelType = 2
			p.TransferData.Status = 1
			p.TransferData.Data = []byte(*text)
			f.Relay(t, p)

		case *msg.MoveTaskInventory:
			// Into the folder it names, as a new item: copied when the
			// owner may copy it, and gone from the object when not.
			mu.Lock()
			var moved *heldItem
			kept := held[:0]
			for _, h := range held {
				if h.ID == r.InventoryData.ItemID {
					moved = h
					if h.ownerMask()&sl.PermCopy == 0 {
						continue
					}
				}
				kept = append(kept, h)
			}
			held = kept
			mu.Unlock()
			if moved == nil {
				return
			}
			kind := sl.AssetLSLText
			if moved.Kind == "notecard" {
				kind = sl.AssetNotecard
			}
			f.mu.Lock()
			if dir := findDir(f.inv, r.AgentData.FolderID); dir != nil {
				dir.Items = append(dir.Items, &invItem{
					ID: f.nextLinkID(), Name: moved.Name,
					Type: int(kind), InvType: int(kind),
				})
				dir.Version++
			}
			f.mu.Unlock()

		case *msg.UpdateTaskInventory:
			// A rename, which is all mv --in sends: the item under its
			// own id takes the name, and the next read says so.
			mu.Lock()
			for _, h := range held {
				if h.ID == r.InventoryData.ItemID {
					h.Name = strings.TrimRight(string(r.InventoryData.Name), "\x00")
				}
			}
			mu.Unlock()

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
// marks the end of a value that may have spaces in it.  The sale info
// is there because a rename sends it back, and refuses an item whose
// file did not give it.
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
		fmt.Fprintf(&b, "\t\towner_id\t%s\n", h.owner())
		fmt.Fprintf(&b, "\t\tpermissions 0\n\t\t{\n")
		fmt.Fprintf(&b, "\t\t\towner_mask\t%08x\n", h.ownerMask())
		fmt.Fprintf(&b, "\t\t\teveryone_mask\t%08x\n", h.EveryoneMask)
		fmt.Fprintf(&b, "\t\t}\n")
		fmt.Fprintf(&b, "\t\tsale_info\t0\n\t\t{\n")
		fmt.Fprintf(&b, "\t\t\tsale_type\tnot\n\t\t\tsale_price\t10\n")
		fmt.Fprintf(&b, "\t\t}\n")
		fmt.Fprintf(&b, "\t}\n")
	}
	return []byte(b.String())
}

// ------------------------------------------------------------- backend

func (f *fakeGrid) Info() *sl.Info { return f.info }

// Refresh is Backend's; nothing here rebuilds a session underneath, so
// the identity it hands back is the one it has.
func (f *fakeGrid) Refresh(context.Context) (*sl.Info, error) { return f.Info(), nil }

// Control takes a control flag the way a daemon does: it is recorded,
// and whatever the test has arranged to happen next happens outside the
// lock, since answering usually means relaying something back.
//
// The flag itself is not turned into an AgentUpdate here.  What builds
// one is the thing that owns the camera, and nothing in this package
// does; the point of the hook is that a ground sit and a stand have no
// other way of being answered at all.
func (f *fakeGrid) Control(ctx context.Context, flags uint32) error {
	f.mu.Lock()
	f.controls = append(f.controls, flags)
	on := f.onControl
	f.mu.Unlock()
	if on != nil {
		on(flags)
	}
	return nil
}

func (f *fakeGrid) Send(ctx context.Context, m msg.Message, reliable bool) error {
	f.mu.Lock()
	err, onSend := f.sendErr, f.onSend
	if err == nil {
		f.sent = append(f.sent, m)
		f.moveLocked(m)
		if _, ok := m.(*msg.RequestRegionInfo); ok && !f.regionSilent && f.regionDetails != nil {
			f.regionHeard++
		}
	}
	f.mu.Unlock()
	if err != nil {
		return err
	}
	if onSend != nil {
		onSend(m)
	}
	// After the test's own answer, so that one is heard first.
	f.answerNames(m)
	return nil
}

// answerNames answers a request for names the way a simulator does,
// from the names the fake holds: a root to a family request, anything
// to a selection.  A lookup by name asks again for names that are
// already known and waits for an answer, so a fake that never answered
// would cost every lookup the quiet rounds.
// Why: doc/objects.md#a-name-a-script-changed
func (f *fakeGrid) answerNames(m msg.Message) {
	var replies []msg.Message
	f.mu.Lock()
	switch m := m.(type) {
	case *msg.RequestObjectPropertiesFamily:
		for _, o := range f.objects {
			if o.ID == m.ObjectData.ObjectID && o.Parent == 0 && !o.IsAvatar() && o.Name != "" {
				r := &msg.ObjectPropertiesFamily{}
				r.ObjectData.ObjectID, r.ObjectData.OwnerID = o.ID, o.Owner
				r.ObjectData.Name = append([]byte(o.Name), 0)
				replies = append(replies, r)
			}
		}
	case *msg.ObjectSelect:
		for _, d := range m.ObjectData {
			for _, o := range f.objects {
				if o.Local == d.ObjectLocalID && !o.IsAvatar() && o.Name != "" {
					r := &msg.ObjectProperties{ObjectData: []msg.ObjectProperties_ObjectData{{
						ObjectID: o.ID, OwnerID: o.Owner, Name: append([]byte(o.Name), 0),
					}}}
					replies = append(replies, r)
				}
			}
		}
	}
	f.mu.Unlock()
	for _, r := range replies {
		body, err := r.Encode()
		if err != nil {
			continue
		}
		// Followed by a number the template does not have, which the
		// reader discards: its taking that means it has finished with
		// the answer, so the lookup waiting for it sees it at once.
		for _, raw := range []*sl.Message{
			{ID: msg.IDOf(r), Name: r.MsgInfo().Name, Body: body, At: time.Now()},
			{ID: msg.MakeID(msg.FreqLow, 65530), Name: "relay barrier", At: time.Now()},
		} {
			select {
			case f.msgs <- raw:
			case <-f.done:
				return
			case <-time.After(5 * time.Second):
				return
			}
		}
	}
}

// sentButNameAsks is Sent without the asking for names a lookup by name
// makes: it asks again for the names of the objects that have the name
// looked up, and that is not what a test of the command is about.
// Why: doc/objects.md#a-name-a-script-changed
func (f *fakeGrid) sentButNameAsks() []msg.Message {
	var out []msg.Message
	for _, m := range f.Sent() {
		switch m.(type) {
		case *msg.RequestObjectPropertiesFamily, *msg.ObjectSelect, *msg.ObjectDeselect:
		default:
			out = append(out, m)
		}
	}
	return out
}

func (f *fakeGrid) Messages() <-chan *sl.Message  { return f.msgs }
func (f *fakeGrid) Events() <-chan *sl.QueueEvent { return f.events }
func (f *fakeGrid) Done() <-chan struct{}         { return f.done }
func (f *fakeGrid) Err() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.endedWith
}

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
	if len(f.later) > 0 && f.objectsCalls >= f.laterAt {
		f.objects = append(f.objects, f.later...)
		f.later = nil
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

// Descriptions is how the fake grid says each object was described: what
// a test put in how, and nothing for the rest.
func (f *fakeGrid) Descriptions(ctx context.Context) (map[msg.UUID][]sl.Description, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objectsErr != nil {
		return nil, f.objectsErr
	}
	out := map[msg.UUID][]sl.Description{}
	for id, ds := range f.how {
		out[id] = ds
	}
	return out, nil
}

// land is what the fake session was told about the ground it is on,
// and landErr is a daemon that will not answer.  Both are set by the
// tests that care; the default is a session that arrived before
// anything was listening, which is the ordinary case for a shell.
func (f *fakeGrid) Land(ctx context.Context) (*sl.Land, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.landErr != nil {
		return nil, f.landErr
	}
	if f.land != nil {
		return f.land, nil
	}
	return &sl.Land{Overlay: agent.OverlayFrom(nil, 0)}, nil
}

// Ground knows no land: none has arrived.
func (f *fakeGrid) Ground(ctx context.Context, west, south, east, north float32) (float32, bool, error) {
	return 0, false, nil
}

func (f *fakeGrid) Region(ctx context.Context) (*sl.Region, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.regionErr != nil {
		return nil, false, f.regionErr
	}
	return f.region, true, nil
}

func (f *fakeGrid) LastRegionDetails(ctx context.Context) (*sl.RegionDetails, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.regionDetails, f.regionHeard, nil
}

func (f *fakeGrid) SimStats(ctx context.Context) (*sl.SimStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.simStatsErr != nil {
		return nil, f.simStatsErr
	}
	if f.simStats == nil {
		return &sl.SimStats{Read: time.Now()}, nil
	}
	return f.simStats, nil
}

func (f *fakeGrid) Neighbours(ctx context.Context, set *bool) (*sl.Neighbours, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.neighboursErr != nil {
		return nil, f.neighboursErr
	}
	if set != nil {
		f.neighbours.On = *set
		if !*set {
			f.neighbours.Held = nil
		}
	}
	n := f.neighbours
	n.Held = append([]sl.Neighbour(nil), f.neighbours.Held...)
	return &n, nil
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

// SlotsWithin, TrySlots and ReleaseSlots are the shared objects, which
// the daemon hands out a number at a time.  lockedBy stands in for somebody
// else having them, as it does for a lock: what is being checked is that
// a command which must not run while they are in use finds them in use.
func (f *fakeGrid) SlotsWithin(ctx context.Context, n int, d, wait time.Duration, agent string) (*client.Grant, error) {
	return f.slots(n)
}

func (f *fakeGrid) TrySlots(ctx context.Context, n int, d time.Duration, agent string) (*client.Grant, error) {
	return f.slots(n)
}

func (f *fakeGrid) ReleaseSlots(id string, clean bool) error { return nil }

func (f *fakeGrid) slots(n int) (*client.Grant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lockedBy != "" {
		return &client.Grant{Why: "in use by " + f.lockedBy}, nil
	}
	g := &client.Grant{ID: "g1", Expires: time.Now().Add(time.Hour)}
	for i := 0; i < n; i++ {
		g.Places = append(g.Places, client.Place{Agent: "quark", Slot: i})
	}
	return g, nil
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

// shortReadBacks has x's session give up a read-back -- a move, a
// permission change, a delete -- at once, for a test that proves one
// runs out rather than waiting the defaults out; see sl.Options.
func shortReadBacks(x *testShell) {
	x.s.SetOptions(sl.Options{
		MoveTimeout:        time.Millisecond,
		PermissionsTimeout: time.Millisecond,
		DeleteTimeout:      100 * time.Millisecond,
	})
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

	// hosts is every Host request, in the order they came, and logouts
	// every Logout request.
	hosts   []*pb.HostRequest
	logouts []*pb.LogoutRequest

	// auth answers the login handshake, for a daemon that is dialled
	// rather than handed a connection, and relay is what to push down
	// the stream once one is open.  ended hears each stream that ends,
	// which is a client letting go of its session.
	auth  *auth.Server
	relay chan *pb.ServerPacket
	ended chan struct{}

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
	defer func() {
		select {
		case d.ended <- struct{}{}:
		default:
		}
	}()
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

func (d *fakeDaemon) Host(_ context.Context, req *pb.HostRequest) (*pb.HostResponse, error) {
	d.hosts = append(d.hosts, req)
	if d.fail != nil {
		return nil, d.fail
	}
	return d.host, nil
}

// Logout answers the way slgod does: a refusal is a status, and who is
// holding the session travels in its details, since a unary call gives
// back a message or a status and never both.
func (d *fakeDaemon) Logout(_ context.Context, req *pb.LogoutRequest) (*pb.LogoutResponse, error) {
	d.logouts = append(d.logouts, req)
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
		ended: make(chan struct{}, 8),
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
