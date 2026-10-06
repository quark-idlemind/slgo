package slate

// What the fake grid does over time, for state, give and rez: a prim whose
// texture entry or click byte changes when a timer fires or when the
// runner asks the region to describe it again, roots that appear, the
// properties of an object, instant messages, and an inventory served over
// AIS from httptest the way cmd/slsh's fake serves it.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// Invented ids, made with tools/new-id.
var (
	idScriptsFolder = msg.MustParseUUID("06c77e57-7e57-c0de-5935-9b238feebc68")
	idObjectsFolder = msg.MustParseUUID("10387e57-7e57-c0de-00b9-f0313295b2df")
	idOldCopy       = msg.MustParseUUID("1e517e57-7e57-c0de-9c29-16b36117cace")
	idNewCopy       = msg.MustParseUUID("32437e57-7e57-c0de-c464-8c1657476042")
	idNewCopy2      = msg.MustParseUUID("40eb7e57-7e57-c0de-b1b7-3f8be53f16fb")
	idOffer         = msg.MustParseUUID("656d7e57-7e57-c0de-8cd9-8428d28ae642")
	idOffer2        = msg.MustParseUUID("cc4e7e57-7e57-c0de-ef3c-3b1d2f35b8c2")
	idTexA          = msg.MustParseUUID("6b5e7e57-7e57-c0de-5117-87121399d48f")
	idTexB          = msg.MustParseUUID("71617e57-7e57-c0de-d07c-b0dcc981ecc0")
	idTexC          = msg.MustParseUUID("7e0d7e57-7e57-c0de-f711-dbac083f3001")
	idRootA         = msg.MustParseUUID("80777e57-7e57-c0de-236d-c43b4b83b660")
	idRootB         = msg.MustParseUUID("a17c7e57-7e57-c0de-f613-0fb005dd6a95")
	idRootC         = msg.MustParseUUID("b4e17e57-7e57-c0de-cabc-cf4a7c8fc0f0")
	idRootD         = msg.MustParseUUID("c5ff7e57-7e57-c0de-1e50-efcf22d04cfc")
	idOwnerOther    = msg.MustParseUUID("d0c27e57-7e57-c0de-f6e7-cc9d1cafe61d")
	idChildOfA      = msg.MustParseUUID("e9e77e57-7e57-c0de-9ff0-a8c23feda1a8")
	idNewFolder     = msg.MustParseUUID("0cee7e57-7e57-c0de-194b-d4951dd06a3e")
	idKitItemA      = msg.MustParseUUID("1dd57e57-7e57-c0de-dc5e-ecb2f024a387")
	idKitItemB      = msg.MustParseUUID("45687e57-7e57-c0de-501d-67e54ef6f573")
	idOldFolder     = msg.MustParseUUID("47917e57-7e57-c0de-a775-00a9a2d2a18a")
)

// fakeExtra is the part of the fake that PR 6's tests add. It has its own
// lock, and a mutation of an object takes the grid's.
type fakeExtra struct {
	mu     sync.Mutex
	reqs   []objRequest
	onReq  map[uint32][]func(*sl.Seen) // applied when the prim is described again
	props  map[msg.UUID]fakeProps
	invURL string
	matURL string // the RenderMaterials server, when a test serves one (alphamode_test.go)

	// overrides says the session holds the ModifyMaterialParams
	// capability, which a test of the GLTF expectations grants (gltf_test.go).
	overrides bool
	inv       *fakeInv
	offers    map[msg.UUID]fakeOffer // item delivered when the offer with this transaction is accepted
	acks      []*msg.ImprovedInstantMessage
}

// objRequest is a RequestMultipleObjects the runner sent.
type objRequest struct {
	local uint32
	at    time.Time
}

type fakeProps struct {
	name, desc string
	owner      msg.UUID
}

// fakeOffer is what accepting an offer delivers.
type fakeOffer struct {
	item msg.UUID
	name string
	typ  int

	// A folder offer delivers a new folder, called name, holding these
	// items under the ids in holdIDs, into the folder the accept names.
	folder  msg.UUID
	holds   []string
	holdIDs []msg.UUID
}

// afterSend is what the far end does with what it was sent.
func (f *fakeGrid) afterSend(m msg.Message) {
	switch x := m.(type) {
	case *msg.RequestMultipleObjects:
		for _, d := range x.ObjectData {
			f.ex.mu.Lock()
			f.ex.reqs = append(f.ex.reqs, objRequest{d.ID, time.Now()})
			// One change per request: a test lists the requests that change
			// nothing first, to keep an entry stale through several.
			var fn func(*sl.Seen)
			if fns := f.ex.onReq[d.ID]; len(fns) > 0 {
				fn, f.ex.onReq[d.ID] = fns[0], fns[1:]
			}
			f.ex.mu.Unlock()
			if fn == nil {
				continue
			}
			f.mu.Lock()
			for _, o := range f.objects {
				if o.Local == d.ID {
					fn(o)
				}
			}
			f.mu.Unlock()
		}
	case *msg.ObjectSelect:
		var reply []msg.ObjectProperties_ObjectData
		f.mu.Lock()
		f.ex.mu.Lock()
		for _, d := range x.ObjectData {
			for _, o := range f.objects {
				if p, ok := f.ex.props[o.ID]; ok && o.Local == d.ObjectLocalID {
					reply = append(reply, msg.ObjectProperties_ObjectData{
						ObjectID: o.ID, OwnerID: p.owner,
						Name: []byte(p.name + "\x00"), Description: []byte(p.desc + "\x00"),
					})
				}
			}
		}
		f.ex.mu.Unlock()
		f.mu.Unlock()
		if len(reply) > 0 {
			f.relay(&msg.ObjectProperties{ObjectData: reply})
		}
	case *msg.ImprovedInstantMessage:
		if x.MessageBlock.Dialog != sl.DialogTaskInventoryAccepted {
			return
		}
		f.ex.mu.Lock()
		f.ex.acks = append(f.ex.acks, x)
		offer, ok := f.ex.offers[x.MessageBlock.ID]
		inv := f.ex.inv
		f.ex.mu.Unlock()
		if ok && inv != nil && len(x.MessageBlock.BinaryBucket) == 16 {
			var folder msg.UUID
			copy(folder[:], x.MessageBlock.BinaryBucket)
			if !offer.folder.IsZero() {
				inv.addFolder(folder, offer.folder, offer.name)
				for i, n := range offer.holds {
					inv.add(offer.folder, offer.holdIDs[i], n, 6)
				}
				return
			}
			inv.add(folder, offer.item, offer.name, offer.typ)
		}
	}
}

// requests are the RequestMultipleObjects sent for a prim, in order.
func (f *fakeGrid) requests(local uint32) []time.Time {
	f.ex.mu.Lock()
	defer f.ex.mu.Unlock()
	var out []time.Time
	for _, r := range f.ex.reqs {
		if r.local == local {
			out = append(out, r.at)
		}
	}
	return out
}

// accepts are the dialog-10 messages sent.
func (f *fakeGrid) accepts() []*msg.ImprovedInstantMessage {
	f.ex.mu.Lock()
	defer f.ex.mu.Unlock()
	return append([]*msg.ImprovedInstantMessage(nil), f.ex.acks...)
}

func (f *fakeGrid) setProps(id msg.UUID, name, desc string, owner msg.UUID) {
	f.ex.mu.Lock()
	defer f.ex.mu.Unlock()
	if f.ex.props == nil {
		f.ex.props = map[msg.UUID]fakeProps{}
	}
	f.ex.props[id] = fakeProps{name, desc, owner}
}

// ------------------------------------------------------ changing a prim

// withFace is a change of one face of a prim's texture entry.
func withFace(face int, fn func(*sl.Face)) func(*sl.Seen) {
	return func(o *sl.Seen) {
		n := max(6, face+1)
		if c, ok := o.FaceCount(); ok {
			n = max(n, c)
		}
		faces := sl.PlainFaces(n) // a prim nothing has described yet is plain
		if len(o.TextureEntry) > 0 {
			var err error
			if faces, err = sl.DecodeTextureEntry(o.TextureEntry, n); err != nil {
				panic(err)
			}
		}
		fn(&faces[face])
		te, err := sl.EncodeTextureEntry(faces)
		if err != nil {
			panic(err)
		}
		o.TextureEntry = te
	}
}

func withTexture(face int, id msg.UUID) func(*sl.Seen) {
	return withFace(face, func(f *sl.Face) { f.Texture = id })
}

// withShape gives a prim the shape the grid describes it with, packed as
// the wire carries it; the fake then answers FaceCount as the real
// store does.
func withShape(shape sl.Shape) func(*sl.Seen) {
	return func(o *sl.Seen) {
		p, err := shape.Pack()
		if err != nil {
			panic(err)
		}
		o.Shape = p
	}
}

// asSculpt marks a prim as a sculpt, whose faces no shape gives.
func asSculpt(o *sl.Seen) {
	o.Sculpt = msg.SculptMark{Kind: msg.SculptSphere, ID: idTexA}
}

// withFaces sets every face of the prim to the given values, the entry's
// default being the last one.
func withFaces(faces ...sl.Face) func(*sl.Seen) {
	return func(o *sl.Seen) {
		te, err := sl.EncodeTextureEntry(faces)
		if err != nil {
			panic(err)
		}
		o.TextureEntry = te
	}
}

func withClick(b uint8) func(*sl.Seen) {
	return func(o *sl.Seen) { o.Click, o.ClickKnown = b, true }
}

// change applies a change to the prim with a local id now.
func (f *fakeGrid) change(local uint32, fn func(*sl.Seen)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.objects {
		if o.Local == local {
			fn(o)
		}
	}
}

// changeAfter applies it when d has passed, as a script's change would
// reach the store.
func (f *fakeGrid) changeAfter(t *testing.T, d time.Duration, local uint32, fn func(*sl.Seen)) {
	t.Helper()
	timer := time.AfterFunc(d, func() { f.change(local, fn) })
	t.Cleanup(func() { timer.Stop() })
}

// changeOnRequest applies it when the region is asked to describe the prim
// again, which is the only thing that brings a stale entry up to date.
func (f *fakeGrid) changeOnRequest(local uint32, fn func(*sl.Seen)) {
	f.ex.mu.Lock()
	defer f.ex.mu.Unlock()
	if f.ex.onReq == nil {
		f.ex.onReq = map[uint32][]func(*sl.Seen){}
	}
	f.ex.onReq[local] = append(f.ex.onReq[local], fn)
}

// appear adds a prim to the region now, and appearAfter when d has passed.
func (f *fakeGrid) appear(s ...*sl.Seen) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects = append(f.objects, s...)
}

func (f *fakeGrid) appearAfter(t *testing.T, d time.Duration, s ...*sl.Seen) {
	t.Helper()
	timer := time.AfterFunc(d, func() { f.appear(s...) })
	t.Cleanup(func() { timer.Stop() })
}

// teOf is a texture entry for a prim whose first face has a texture.
func teOf(face0 msg.UUID) []byte {
	faces := sl.PlainFaces(6)
	faces[0].Texture = face0
	te, err := sl.EncodeTextureEntry(faces)
	if err != nil {
		panic(err)
	}
	return te
}

// described makes the prim a described one with a click byte the region
// has said.
func described(s *sl.Seen, click uint8, known bool) *sl.Seen {
	s.Click, s.ClickKnown = click, known
	return s
}

// ------------------------------------------------------ instant messages

// offerIM is an object's give as the grid sent it: the owner as the id,
// the object as the name, the transaction as the id field, the item's name
// in quotes with the place in parentheses, and the asset type as the one
// byte of the bucket.
// Why: doc/im-senders.md#an-objects-give
func offerIM(owner msg.UUID, object string, txn msg.UUID, item string, asset byte) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID = owner
	m.MessageBlock.FromAgentName = append([]byte(object), 0)
	m.MessageBlock.Message = []byte(fmt.Sprintf("'%s'  ( http://example.invalid/Test/128/64/22 )\x00", item))
	m.MessageBlock.Dialog = sl.DialogTaskInventoryOffered
	m.MessageBlock.ID = txn
	m.MessageBlock.BinaryBucket = []byte{asset}
	return m
}

// ------------------------------------------------------------- inventory

// fakeInv is the avatar's inventory, served over AIS. The root is the
// session's inventory root.
type fakeInv struct {
	mu       sync.Mutex
	folders  []invFolder
	items    []invItem
	requests int
}

type invFolder struct {
	id, parent msg.UUID
	name       string
}

type invItem struct {
	id, folder msg.UUID
	name       string
	typ        int

	// An item that says what it is made of: its asset (the item's own id
	// when zero) and the owner's permissions (none are served when zero).
	asset     msg.UUID
	ownerMask uint32
}

func (v *fakeInv) add(folder, id msg.UUID, name string, typ int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.items = append(v.items, invItem{id: id, folder: folder, name: name, typ: typ})
}

// addFolder adds a folder under parent.
func (v *fakeInv) addFolder(parent, id msg.UUID, name string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.folders = append(v.folders, invFolder{id, parent, name})
}

// parentOf is the folder a folder is in.
func (v *fakeInv) parentOf(id msg.UUID) msg.UUID {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, f := range v.folders {
		if f.id == id {
			return f.parent
		}
	}
	return msg.UUID{}
}

// addAsset is add for an item with an asset of its own and the owner's
// permissions.
func (v *fakeInv) addAsset(folder, id msg.UUID, name string, typ int, asset msg.UUID, ownerMask uint32) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.items = append(v.items, invItem{id: id, folder: folder, name: name, typ: typ, asset: asset, ownerMask: ownerMask})
}

// setOwnerMask changes the owner's permissions on an item already added.
func (v *fakeInv) setOwnerMask(id msg.UUID, mask uint32) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for i := range v.items {
		if v.items[i].id == id {
			v.items[i].ownerMask = mask
		}
	}
}

// withInventory serves an inventory with a Scripts and an Objects folder
// under the root, and returns it to add to.
func (f *fakeGrid) withInventory(t *testing.T) *fakeInv {
	t.Helper()
	v := &fakeInv{folders: []invFolder{
		{testInvRoot, msg.UUID{}, "My Inventory"},
		{idScriptsFolder, testInvRoot, "Scripts"},
		{idObjectsFolder, testInvRoot, "Objects"},
	}}
	s := httptest.NewServer(http.HandlerFunc(v.serve))
	t.Cleanup(s.Close)
	f.ex.mu.Lock()
	f.ex.invURL, f.ex.inv = s.URL, v
	f.ex.mu.Unlock()
	return v
}

// offer says what accepting the offer with a transaction delivers.
func (f *fakeGrid) offer(txn, item msg.UUID, name string, typ int) {
	f.ex.mu.Lock()
	defer f.ex.mu.Unlock()
	if f.ex.offers == nil {
		f.ex.offers = map[msg.UUID]fakeOffer{}
	}
	f.ex.offers[txn] = fakeOffer{item: item, name: name, typ: typ}
}

// offerFolder says that accepting the offer with a transaction delivers a
// new folder, folder, called name and holding the named items under ids.
func (f *fakeGrid) offerFolder(txn, folder msg.UUID, name string, holds []string, ids []msg.UUID) {
	f.ex.mu.Lock()
	defer f.ex.mu.Unlock()
	if f.ex.offers == nil {
		f.ex.offers = map[msg.UUID]fakeOffer{}
	}
	f.ex.offers[txn] = fakeOffer{name: name, folder: folder, holds: holds, holdIDs: ids}
}

// serve answers a read of one folder: its folders and its items, with
// empty links, the three that make a reply a whole folder.
func (v *fakeInv) serve(w http.ResponseWriter, r *http.Request) {
	var id msg.UUID
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for i, p := range parts {
		if p == "category" && i+1 < len(parts) {
			id, _ = msg.ParseUUID(parts[i+1])
		}
	}
	_, _ = strconv.Atoi(r.URL.Query().Get("depth"))
	v.mu.Lock()
	defer v.mu.Unlock()
	v.requests++
	var self *invFolder
	for i := range v.folders {
		if v.folders[i].id == id {
			self = &v.folders[i]
		}
	}
	if self == nil {
		http.Error(w, "no such folder", http.StatusNotFound)
		return
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" ?><llsd><map>`)
	fmt.Fprintf(&b, `<key>category_id</key><string>%s</string>`, self.id)
	fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, self.parent)
	fmt.Fprintf(&b, `<key>name</key><string>%s</string>`, self.name)
	b.WriteString(`<key>type_default</key><integer>-1</integer><key>version</key><integer>1</integer>`)
	b.WriteString(`<key>_embedded</key><map><key>categories</key><map>`)
	for _, f := range v.folders {
		if f.parent == id {
			fmt.Fprintf(&b, `<key>%s</key><map><key>category_id</key><string>%s</string><key>parent_id</key><string>%s</string><key>name</key><string>%s</string><key>type_default</key><integer>%d</integer><key>version</key><integer>1</integer></map>`,
				f.id, f.id, id, f.name, folderType(f.name))
		}
	}
	b.WriteString(`</map><key>items</key><map>`)
	for _, it := range v.items {
		if it.folder != id {
			continue
		}
		asset, perms := it.id, ""
		if !it.asset.IsZero() {
			asset = it.asset
		}
		if it.ownerMask != 0 {
			perms = fmt.Sprintf(`<key>permissions</key><map><key>owner_mask</key><integer>%d</integer></map>`, it.ownerMask)
		}
		fmt.Fprintf(&b, `<key>%s</key><map><key>item_id</key><string>%s</string><key>parent_id</key><string>%s</string><key>asset_id</key><string>%s</string><key>name</key><string>%s</string><key>desc</key><string></string><key>type</key><integer>%d</integer><key>inv_type</key><integer>%d</integer><key>flags</key><integer>0</integer><key>created_at</key><integer>1265521621</integer>%s</map>`,
			it.id, it.id, id, asset, it.name, it.typ, it.typ, perms)
	}
	b.WriteString(`</map><key>links</key><map/></map></map></llsd>`)
	w.Header().Set("Content-Type", "application/llsd+xml")
	io.WriteString(w, b.String())
}

// folderType is the preferred type of a folder by its name: the Trash is
// the only one that is told apart.
func folderType(name string) int {
	if name == "Trash" {
		return sl.FolderTrash
	}
	return -1
}

// hasCap and doCap are the capabilities the fake serves: the inventory's.
func (f *fakeGrid) hasCap(name string) bool {
	f.ex.mu.Lock()
	defer f.ex.mu.Unlock()
	return name == agent.InventoryCap && f.ex.invURL != "" || name == sl.MaterialsCap && f.ex.matURL != "" ||
		name == sl.OverridesCap && f.ex.overrides
}

func (f *fakeGrid) doCap(ctx context.Context, r agent.CapRequest) (*agent.CapResponse, error) {
	f.ex.mu.Lock()
	base := f.ex.invURL
	if r.Cap == sl.MaterialsCap {
		base = f.ex.matURL
	}
	f.ex.mu.Unlock()
	if r.Cap != agent.InventoryCap && r.Cap != sl.MaterialsCap || base == "" {
		return nil, fmt.Errorf("the fake grid serves no %s capability", r.Cap)
	}
	method := r.Method
	if method == "" {
		method = "GET"
	}
	req, err := http.NewRequestWithContext(ctx, method, base+r.Path, bytes.NewReader(r.Body))
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

// itemsNamed is the ids of the items called name, in id order.
func (v *fakeInv) itemsNamed(name string) []msg.UUID {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []msg.UUID
	for _, it := range v.items {
		if it.name == name {
			out = append(out, it.id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// folderOf is the folder an item is in.
func (v *fakeInv) folderOf(id msg.UUID) msg.UUID {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, it := range v.items {
		if it.id == id {
			return it.folder
		}
	}
	return msg.UUID{}
}
