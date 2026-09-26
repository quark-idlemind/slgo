package sl

// Reading inventory, and reading what is inside a rezzed object.
//
// Three protocols answer these questions and not one of them is the one
// the question went out on.  A folder listing is AIS: an ordinary HTTPS
// GET returning LLSD, with no message involved at all.  Creating an item
// is a UDP message confirmed by a callback id echoed back in a different
// one.  An object's contents arrive as the NAME of a file, and the file
// comes over the xfer protocol as packets that have to be acknowledged
// and reassembled.
//
// Each of those is somewhere a call can hang, or confirm the wrong
// thing, or answer from a reply that belonged to an earlier question --
// and none of them can be reached without something pretending to be the
// far end, which is what fake_test.go is for.  The calls that wait
// cannot be the goroutine that relays what they are waiting for, so they
// are run aside.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/msg"
)

// theObjectsFolder is a folder as AIS describes it, which is what a
// lookup by name reads.
func theObjectsFolder() *Folder {
	return &Folder{ID: aFolder, ParentID: testInvRoot, Name: "Objects", Type: 6}
}

// serveFolders answers every folder read with one set of child folders
// under the root and nothing anywhere else, which is all a lookup by
// name looks at.
func serveFolders(t *testing.T, f *fakeBackend, folders ...*Folder) {
	t.Helper()
	f.ServeInventoryTree(t, func(id msg.UUID) ([]*Folder, []*Item) {
		if id != testInvRoot {
			return nil, nil
		}
		return folders, nil
	})
}

// TestAFolderIsFoundByNameBeneathTheRoot: everything that takes a folder
// id has to get it from somewhere, and the only somewhere is the name a
// person would use.
func TestAFolderIsFoundByNameBeneathTheRoot(t *testing.T) {
	w, f := newFakeSession(t)
	serveFolders(t, f, theObjectsFolder(), &Folder{
		ID: theOther, ParentID: testInvRoot, Name: "Notecards", Type: 7,
	})

	got, err := w.ObjectsFolder(context.Background())
	if err != nil {
		t.Fatalf("ObjectsFolder: %v", err)
	}
	if got != aFolder {
		t.Errorf("Objects is %s, want %s", got, aFolder)
	}

	if got, err = w.Folder(context.Background(), "Notecards"); err != nil || got != theOther {
		t.Errorf("Folder(Notecards) = %s, %v", got, err)
	}
}

// TestFolderSaysWhichOfTheTwoThingsWentWrong: a folder that is not there
// and an inventory that could not be read are different problems, and a
// caller told only "no such folder" would go looking for the folder.
func TestFolderSaysWhichOfTheTwoThingsWentWrong(t *testing.T) {
	t.Run("no folder of that name", func(t *testing.T) {
		w, f := newFakeSession(t)
		serveFolders(t, f, theObjectsFolder())
		_, err := w.Folder(context.Background(), "Landmarks")
		if err == nil || !strings.Contains(err.Error(), `"Landmarks"`) {
			t.Errorf("Folder = %v, want it to name what was looked for", err)
		}
	})

	t.Run("the inventory could not be read", func(t *testing.T) {
		// No capability served at all, which is what a session that
		// never got one looks like.
		w, _ := newFakeSession(t)
		if _, err := w.Folder(context.Background(), "Objects"); err == nil {
			t.Error("Folder found something without reading anything")
		}
	})
}

// TestFindItemPassesOnAFolderThatCouldNotBeRead: a folder that answered
// nothing is not a folder without the item in it.
func TestFindItemPassesOnAFolderThatCouldNotBeRead(t *testing.T) {
	w, f := newFakeSession(t)
	f.ServeInventory(t, func(msg.UUID) []*Item { return []*Item{anItem(theChild, "workbench")} })

	if _, err := w.FindItem(context.Background(), aFolder, "workbench"); err != nil {
		t.Fatalf("FindItem: %v", err)
	}
	if _, err := w.FindItem(context.Background(), aFolder, "anvil"); err == nil {
		t.Error("FindItem found something that is not in the folder")
	}

	f.mu.Lock()
	f.capErr = errors.New("the capability is gone")
	f.mu.Unlock()
	if _, err := w.FindItem(context.Background(), aFolder, "workbench"); err == nil {
		t.Error("FindItem reported an item though the folder could not be read")
	}
}

// TestCreatingAnItemWaitsForItsCallbackToComeBack: the reply to a
// creation is an UpdateCreateInventoryItem that says nothing about what
// was asked for except the callback id it was sent with.  That id is
// chosen here, so it is the only thread back to the request -- and the
// item the caller is handed is built entirely out of the reply.
func TestCreatingAnItemWaitsForItsCallbackToComeBack(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	wait := aside(t, func() (*Item, error) {
		return w.CreateItem(context.Background(), "a script", "made here",
			int8(AssetLSLText), int8(AssetLSLText))
	})

	m := waitSent[*msg.CreateInventoryItem](t, f)
	b := &m.InventoryBlock
	if got := trimNul(b.Name); got != "a script" {
		t.Errorf("asked for an item called %q", got)
	}
	if n := len(b.Name); n == 0 || b.Name[n-1] != 0 {
		t.Errorf("the name went out unterminated: %q", b.Name)
	}
	if got := trimNul(b.Description); got != "made here" {
		t.Errorf("described it as %q", got)
	}
	if b.Type != int8(AssetLSLText) || b.InvType != int8(AssetLSLText) {
		t.Errorf("asked for type %d/%d", b.Type, b.InvType)
	}
	if b.CallbackID == 0 {
		t.Error("no callback id went out, so nothing could confirm the item")
	}

	f.Relay(t, &msg.UpdateCreateInventoryItem{
		InventoryData: []msg.UpdateCreateInventoryItem_InventoryData{{
			CallbackID: b.CallbackID,
			ItemID:     theChild, FolderID: aFolder, AssetID: theOther,
			Name: []byte("a script\x00"), Description: []byte("made here\x00"),
			Type: 10, InvType: 10, Flags: 3,
			CreatorID: testAgentID, OwnerID: testAgentID, GroupID: msg.UUID{},
			BaseMask: PermAll, OwnerMask: PermAll, GroupMask: 0,
			EveryoneMask: 0, NextOwnerMask: PermCopy,
			SaleType: 2, SalePrice: 11,
		}},
	})

	it, err := wait()
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	want := &Item{
		ID: theChild, ParentID: aFolder, AssetID: theOther,
		Name: "a script", Desc: "made here", Type: 10, InvType: 10, Flags: 3,
		CreatorID: testAgentID, OwnerID: testAgentID,
		BaseMask: PermAll, OwnerMask: PermAll, NextOwnerMask: PermCopy,
		SaleType: 2, SalePrice: 11,
	}
	if *it != *want {
		t.Errorf("created %+v,\n   want %+v", *it, *want)
	}
}

// TestCreateItemCallbackIDsAreNeverShared: the reply says nothing about
// what was asked for except the callback id, so two creations sharing
// one each take whichever item is answered first.  Ids read off the
// clock were shared by two creations in the same tick.
func TestCreateItemCallbackIDsAreNeverShared(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	const n = 50
	type result struct {
		asked string
		it    *Item
		err   error
	}
	results := make(chan result, n)
	var start sync.WaitGroup
	start.Add(1)
	for i := range n {
		name := fmt.Sprintf("note %d", i)
		go func() {
			start.Wait()
			it, err := w.CreateItem(context.Background(), name, "", 7, 7)
			results <- result{name, it, err}
		}()
	}
	start.Done()

	var sent []*msg.CreateInventoryItem
	waitFor(t, "every creation to be asked for", func() bool {
		sent = sentOf[*msg.CreateInventoryItem](f)
		return len(sent) == n
	})
	asked := map[uint32]string{}
	for _, m := range sent {
		cb, name := m.InventoryBlock.CallbackID, trimNul(m.InventoryBlock.Name)
		if other, shared := asked[cb]; shared {
			t.Errorf("%q and %q both went out as callback %d", other, name, cb)
		}
		asked[cb] = name
	}

	// Each answered with the item it asked for, as the simulator does.
	for cb, name := range asked {
		f.Relay(t, &msg.UpdateCreateInventoryItem{
			InventoryData: []msg.UpdateCreateInventoryItem_InventoryData{{
				CallbackID: cb, ItemID: theChild, FolderID: aFolder,
				Name: append([]byte(name), 0), Type: 7, InvType: 7,
			}},
		})
	}
	for range n {
		select {
		case r := <-results:
			if r.err != nil {
				t.Errorf("CreateItem %q: %v", r.asked, r.err)
			} else if r.it.Name != r.asked {
				t.Errorf("asked for %q and was handed %q", r.asked, r.it.Name)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("a creation never saw its reply")
		}
	}

	// And nothing is kept once every wait is over.
	w.mu.Lock()
	left := len(w.created)
	w.mu.Unlock()
	if left != 0 {
		t.Errorf("%d replies are still kept after every creation finished", left)
	}
}

// TestCreateItemForgetsWhatItStoppedWaitingFor: a reply that arrives
// after its wait has ended is kept by nobody.  Kept, it sat in the
// session for as long as the session lasted, waiting to be taken as the
// answer to a later request that happened to draw the same id.
func TestCreateItemForgetsWhatItStoppedWaitingFor(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	ctx, cancel := context.WithCancel(context.Background())
	wait := aside(t, func() (*Item, error) {
		return w.CreateItem(ctx, "a script", "", int8(AssetLSLText), int8(AssetLSLText))
	})
	m := waitSent[*msg.CreateInventoryItem](t, f)
	cancel()
	if _, err := wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateItem = %v, want the context's reason", err)
	}

	relayCreated(t, f, m.InventoryBlock.CallbackID)
	w.mu.Lock()
	left := len(w.created)
	w.mu.Unlock()
	if left != 0 {
		t.Errorf("a reply nobody was waiting for was kept")
	}
}

// TestCreateItemReportsWhatDidNotHappen: an item nobody confirmed is not
// an item, and the two ways of not being confirmed are worth telling
// apart.
func TestCreateItemReportsWhatDidNotHappen(t *testing.T) {
	t.Run("the request never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.CreateItem(context.Background(), "a script", "", 10, 10); err == nil {
			t.Error("CreateItem reported an item though nothing was sent")
		}
	})

	t.Run("the caller gave up", func(t *testing.T) {
		w, _ := newFakeSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := w.CreateItem(ctx, "a script", "", 10, 10)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("CreateItem = %v, want the context's reason", err)
		}
	})
}

// uploadServer stands in for the two halves of an asset upload: the
// capability that says where to write, and the URL it names.
//
// They are separate servers because the second one's address has to be
// known before the first can answer with it.
type uploadServer struct {
	dest *httptest.Server

	// body is what was written to the second one, and asked is the
	// LLSD the capability was given.
	body  chan []byte
	asked chan []byte
}

// serveUpload points a capability at an upload that answers with reply.
func serveUpload(t *testing.T, f *fakeBackend, cap string, reply func() (int, string)) *uploadServer {
	t.Helper()
	u := &uploadServer{body: make(chan []byte, 4), asked: make(chan []byte, 4)}
	u.dest = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := readAllBody(r)
		u.body <- b
		status, text := reply()
		w.Header().Set("Content-Type", "application/llsd+xml")
		w.WriteHeader(status)
		fmt.Fprint(w, text)
	}))
	t.Cleanup(u.dest.Close)

	f.ServeCap(t, cap, func(w http.ResponseWriter, r *http.Request) {
		b, _ := readAllBody(r)
		u.asked <- b
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(w, `<llsd><map><key>state</key><string>upload</string>`+
			`<key>uploader</key><string>%s/upload</string></map></llsd>`, u.dest.URL)
	})
	return u
}

func readAllBody(r *http.Request) ([]byte, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(r.Body)
	return b.Bytes(), err
}

// TestSavingAScriptIsTwoRequestsAndAVerdict: the capability does not
// take the bytes, it names somewhere to put them -- and what comes back
// from THERE is the compiler's opinion, which is the only reason
// anything here cares what an upload answered.
func TestSavingAScriptIsTwoRequestsAndAVerdict(t *testing.T) {
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateScriptAgent", func() (int, string) {
		return 200, `<llsd><map>` +
			`<key>state</key><string>complete</string>` +
			`<key>new_asset</key><string>` + theOther.String() + `</string>` +
			`<key>compiled</key><boolean>0</boolean>` +
			`<key>errors</key><array><string>(1,1) : ERROR : Syntax error</string></array>` +
			`</map></llsd>`
	})

	res, err := w.SaveScript(context.Background(), theChild, "default {}")
	if err != nil {
		t.Fatalf("SaveScript: %v", err)
	}
	if res.State != "complete" || res.NewAsset != theOther {
		t.Errorf("upload said state %q, asset %s", res.State, res.NewAsset)
	}
	// A script that does not compile still completes and still gets a
	// new asset id, so Compiled and not State is the verdict.
	if res.Compiled {
		t.Error("a script with a syntax error was reported as compiled")
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0], "Syntax error") {
		t.Errorf("errors = %q", res.Errors)
	}

	if got := string(<-up.body); got != "default {}" {
		t.Errorf("uploaded %q", got)
	}
	// The first request describes what is being written; without the
	// item id the grid would not know what to attach the asset to.
	if got := string(<-up.asked); !strings.Contains(got, theChild.String()) ||
		!strings.Contains(got, "mono") {
		t.Errorf("the capability was asked %q", got)
	}
}

// TestSaveScriptReportsEveryWayAnUploadFails: it is two round trips to
// two different places, and a caller told only "it failed" cannot tell
// which of them to look at.
func TestSaveScriptReportsEveryWayAnUploadFails(t *testing.T) {
	t.Run("no capability to ask", func(t *testing.T) {
		w, _ := newFakeSession(t)
		if _, err := w.SaveScript(context.Background(), theChild, "default {}"); err == nil {
			t.Error("SaveScript uploaded to a capability the session does not have")
		}
	})

	t.Run("the capability named nowhere to write", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.ServeCap(t, "UpdateScriptAgent", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `<llsd><map><key>state</key><string>error</string></map></llsd>`)
		})
		_, err := w.SaveScript(context.Background(), theChild, "default {}")
		if err == nil || !strings.Contains(err.Error(), "uploader") {
			t.Errorf("SaveScript = %v, want it to say there was nowhere to write", err)
		}
	})

	t.Run("the uploader refused", func(t *testing.T) {
		w, f := newFakeSession(t)
		serveUpload(t, f, "UpdateScriptAgent", func() (int, string) {
			return 500, "no room"
		})
		if _, err := w.SaveScript(context.Background(), theChild, "default {}"); err == nil {
			t.Error("SaveScript reported an upload the far end refused")
		}
	})

	t.Run("an answer that is not LLSD", func(t *testing.T) {
		// Not an error: the bytes were written and the far end said
		// something unreadable about it, so the caller gets the body
		// and no verdict rather than a failure.
		w, f := newFakeSession(t)
		serveUpload(t, f, "UpdateScriptAgent", func() (int, string) {
			return 200, "thanks"
		})
		res, err := w.SaveScript(context.Background(), theChild, "default {}")
		if err != nil {
			t.Fatalf("SaveScript: %v", err)
		}
		if res.State != "" || res.Compiled || string(res.Body) != "thanks" {
			t.Errorf("upload = %+v", res)
		}
	})
}

// TestSavingANotecardWrapsTheTextInTheContainerFormat: a notecard asset
// is not the text.  Uploaded bare it is a notecard the viewer will not
// open, and the length in the header has to be the length of what
// follows.
func TestSavingANotecardWrapsTheTextInTheContainerFormat(t *testing.T) {
	w, f := newFakeSession(t)
	up := serveUpload(t, f, "UpdateNotecardAgentInventory", func() (int, string) {
		return 200, `<llsd><map><key>state</key><string>complete</string></map></llsd>`
	})

	if _, err := w.SaveNotecard(context.Background(), theChild, "hello\nthere\n"); err != nil {
		t.Fatalf("SaveNotecard: %v", err)
	}
	got := string(<-up.body)
	if !strings.HasPrefix(got, "Linden text version 2\n") {
		t.Errorf("the asset does not start with the container header: %q", got)
	}
	if !strings.Contains(got, "Text length 12\nhello\nthere\n}") {
		t.Errorf("the text is not in it as written: %q", got)
	}
}

// TestNewScriptCreatesTheItemBeforeSavingToIt: there is nothing to
// upload to until an item exists, so a failure at either step has to
// stop rather than upload into nowhere.
func TestNewScriptCreatesTheItemBeforeSavingToIt(t *testing.T) {
	t.Parallel()

	t.Run("both steps", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		up := serveUpload(t, f, "UpdateScriptAgent", func() (int, string) {
			return 200, `<llsd><map><key>state</key><string>complete</string>` +
				`<key>compiled</key><boolean>1</boolean></map></llsd>`
		})

		wait := aside(t, func() (*Item, error) {
			it, _, err := w.NewScript(context.Background(), "a script", "default {}")
			return it, err
		})
		m := waitSent[*msg.CreateInventoryItem](t, f)
		if got := trimNul(m.InventoryBlock.Description); got != "created by slgo" {
			t.Errorf("described the new script as %q", got)
		}
		relayCreated(t, f, m.InventoryBlock.CallbackID)

		it, err := wait()
		if err != nil {
			t.Fatalf("NewScript: %v", err)
		}
		if it.ID != theChild {
			t.Errorf("created %s", it.ID)
		}
		if got := string(<-up.body); got != "default {}" {
			t.Errorf("uploaded %q", got)
		}
	})

	t.Run("the item was never created", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, _, err := w.NewScript(context.Background(), "a script", "default {}"); err == nil {
			t.Error("NewScript went on to upload without an item to upload to")
		}
	})

	t.Run("the source could not be saved", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		wait := aside(t, func() (*Item, error) {
			it, _, err := w.NewScript(context.Background(), "a script", "default {}")
			return it, err
		})
		m := waitSent[*msg.CreateInventoryItem](t, f)
		relayCreated(t, f, m.InventoryBlock.CallbackID)
		// No upload capability, so the item exists and the source did
		// not get to it, which is not a script.
		it, err := wait()
		if err == nil {
			t.Error("NewScript reported a script whose source was never saved")
		}
		// But the item is in inventory, and the caller is the only one
		// who can tidy it away.
		if it == nil || it.ID != theChild {
			t.Errorf("NewScript = %+v, want the item it made", it)
		}
	})
}

// relayCreated answers a creation with the callback id it went out
// with, which is the only thing that ties the reply to the request.
func relayCreated(t *testing.T, f *fakeBackend, cb uint32) {
	t.Helper()
	f.Relay(t, &msg.UpdateCreateInventoryItem{
		InventoryData: []msg.UpdateCreateInventoryItem_InventoryData{{
			CallbackID: cb, ItemID: theChild, FolderID: aFolder,
			Name: []byte("a script\x00"), Type: 10, InvType: 10,
		}},
	})
}

// TestPutInObjectSendsTheWholeItem: the message carries the item's
// permissions, and anything left out of it is sent as zero -- which for
// a mask means handing over something the next owner cannot use.
func TestPutInObjectSendsTheWholeItem(t *testing.T) {
	w, f := newFakeSession(t)

	it := anItem(theChild, "a script")
	it.Desc = "does something"
	it.AssetID = theOther
	it.Flags, it.SaleType, it.SalePrice = 7, 2, 11
	it.CreatorID, it.OwnerID, it.GroupID = testAgentID, testAgentID, theOther
	it.BaseMask, it.OwnerMask = PermAll, PermAll
	it.GroupMask, it.EveryoneMask, it.NextOwnerMask = PermCopy, PermMove, PermTransfer

	o := &Object{ID: thePrim, Local: 77}
	if err := w.PutInObject(context.Background(), o, it); err != nil {
		t.Fatalf("PutInObject: %v", err)
	}

	m := onlySent[*msg.UpdateTaskInventory](t, f)
	if m.UpdateData.LocalID != 77 {
		t.Errorf("put it in local id %d", m.UpdateData.LocalID)
	}
	d := &m.InventoryData
	if d.ItemID != it.ID || d.FolderID != it.ParentID {
		t.Errorf("put in %s from %s", d.ItemID, d.FolderID)
	}
	if d.BaseMask != PermAll || d.NextOwnerMask != PermTransfer ||
		d.GroupMask != PermCopy || d.EveryoneMask != PermMove || d.OwnerMask != PermAll {
		t.Errorf("permissions went out as %+v", d)
	}
	if d.Flags != 7 || d.SaleType != 2 || d.SalePrice != 11 {
		t.Errorf("sale and flags went out as %+v", d)
	}
	if got := trimNul(d.Name); got != "a script" {
		t.Errorf("put in something called %q", got)
	}
	if got := trimNul(d.Description); got != "does something" {
		t.Errorf("described it as %q", got)
	}
}

// TestRemoveFromObjectNamesTheObjectsOwnId: the copy inside an object
// has an id of its own, and the agent inventory id it was copied from
// means nothing to the object.
func TestRemoveFromObjectNamesTheObjectsOwnId(t *testing.T) {
	w, f := newFakeSession(t)
	o := &Object{ID: thePrim, Local: 77}
	if err := w.RemoveFromObject(context.Background(), o, theChild); err != nil {
		t.Fatalf("RemoveFromObject: %v", err)
	}
	m := onlySent[*msg.RemoveTaskInventory](t, f)
	if m.InventoryData.LocalID != 77 || m.InventoryData.ItemID != theChild {
		t.Errorf("removed %s from local id %d", m.InventoryData.ItemID, m.InventoryData.LocalID)
	}
}

// theContentsFile is one item as the simulator writes an object's
// contents: nested braces of key and value, masks in hex without an 0x,
// and the trailing bar that the format puts on a value.
const theContentsFile = `	inv_object	0
	{
		obj_id	00000000-0000-0000-0000-000000000000
		type	category
		name	Contents|
	}
	inv_item	0
	{
		item_id	909e7e57-7e57-c0de-177e-107fc4869811
		parent_id	89ad7e57-7e57-c0de-08a1-04b25f97cc85
		permissions 0
		{
			base_mask	7fffffff
			owner_mask	7fffffff
			group_mask	00000000
			everyone_mask	00008000
			next_owner_mask	00082000
			creator_id	3ac37e57-7e57-c0de-5607-527da8fa08de
			owner_id	3ac37e57-7e57-c0de-5607-527da8fa08de
			group_id	00000000-0000-0000-0000-000000000000
		}
		asset_id	97c27e57-7e57-c0de-c041-be2c2f8cb586
		type	lsltext
		inv_type	lsltext
		flags	00000001
		sale_info	0
		{
			sale_type	not
			sale_price	0
		}
		name	a script|
		desc	does something|
		creation_date	1700000000
	}
`

// TestTheContentsFileIsBracesOfKeyAndValue: the object's inventory does
// not arrive as a message, it arrives as this -- so everything anything
// knows about what an object holds is what this reader takes out of it.
func TestTheContentsFileIsBracesOfKeyAndValue(t *testing.T) {
	got := parseTaskInventory([]byte(theContentsFile))
	if len(got) != 1 {
		t.Fatalf("read %d items out of the file, want 1: %+v", len(got), got)
	}
	it := got[0]
	if it.ID != theChild || it.Asset != theOther {
		t.Errorf("item %s, asset %s", it.ID, it.Asset)
	}
	if it.Name != "a script" || it.Desc != "does something" {
		t.Errorf("name %q, desc %q", it.Name, it.Desc)
	}
	if it.Type != "lsltext" || it.InvType != "lsltext" {
		t.Errorf("type %q/%q", it.Type, it.InvType)
	}
	if it.BaseMask != 0x7fffffff || it.OwnerMask != 0x7fffffff ||
		it.GroupMask != 0 || it.EveryoneMask != 0x8000 || it.NextOwnerMask != 0x82000 {
		t.Errorf("masks came out as %+v", it)
	}
	if it.CreatorID != testAgentID || it.OwnerID != testAgentID || !it.GroupID.IsZero() {
		t.Errorf("identities came out as %+v", it)
	}
	if it.Flags != 1 || it.SaleType != "not" || it.SalePrice != 0 {
		t.Errorf("flags and sale came out as %+v", it)
	}
	// The date is decimal in a file whose masks are hex, and nothing
	// says which is which, so it is read as hex.  Documented rather
	// than hidden: the masks are what a wrong reading would corrupt.
	if it.Created != 0x1700000000 {
		t.Errorf("creation date read as %d", it.Created)
	}
}

// TestTheContentsFileSurvivesWhatItMayHold: an item with no id is not an
// item, a shadow id stands in for an asset id that is not there, and
// stray braces belong to blocks this does not care about.
func TestTheContentsFileSurvivesWhatItMayHold(t *testing.T) {
	const file = `
}
	inv_item	0
	{
		nothing
		shadow_id	97c27e57-7e57-c0de-c041-be2c2f8cb586
		asset_id	00157e57-7e57-c0de-028f-000000000001
		item_id	909e7e57-7e57-c0de-177e-107fc4869811
		name	a name with spaces|
		wibble	3
	}
	inv_item	0
	{
		name	an item with no id|
	}
`
	got := parseTaskInventory([]byte(file))
	if len(got) != 1 {
		t.Fatalf("read %d items, want only the one with an id: %+v", len(got), got)
	}
	// The shadow id came first and wins: the real asset id of a
	// no-copy item is hidden behind it.
	if got[0].Asset != theOther {
		t.Errorf("asset = %s, want the shadow id", got[0].Asset)
	}
	if got[0].Name != "a name with spaces" {
		t.Errorf("name = %q", got[0].Name)
	}
}

// replyTaskInventory is the simulator naming the file it has written the
// contents to.
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

// TestTheContentsOfAnObjectComeOverXfer: RequestTaskInventory answers
// with a filename, and the file has to be pulled off the simulator a
// packet at a time before anything can be said about what the object
// holds.
func TestTheContentsOfAnObjectComeOverXfer(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	o := &Object{ID: thePrim, Local: 77}

	wait := aside(t, func() ([]TaskItem, error) {
		return w.TaskInventory(context.Background(), o)
	})

	req := waitSent[*msg.RequestTaskInventory](t, f)
	if req.InventoryData.LocalID != 77 {
		t.Errorf("asked local id %d for its contents", req.InventoryData.LocalID)
	}
	f.Relay(t, replyTaskInventory(thePrim, "inventory_37c9.tmp"))

	x := waitSent[*msg.RequestXfer](t, f)
	if got := trimNul(x.XferID.Filename); got != "inventory_37c9.tmp" {
		t.Errorf("asked for the file %q", got)
	}
	if x.XferID.FilePath != client.FilePathTaskInventory {
		t.Errorf("asked for it under path %d", x.XferID.FilePath)
	}

	// Two packets, because a contents file of any size arrives as
	// several and a reader that only ever saw one would not know.
	half := len(theContentsFile) / 2
	f.Relay(t, xferPacket(x.XferID.ID, 0, false, []byte(theContentsFile[:half])))
	f.Relay(t, xferPacket(x.XferID.ID, 1, true, []byte(theContentsFile[half:])))

	held, err := wait()
	if err != nil {
		t.Fatalf("TaskInventory: %v", err)
	}
	if len(held) != 1 || held[0].Name != "a script" {
		t.Errorf("the object holds %v", taskNames(held))
	}
}

// TestTaskInventoryAsksAgainRatherThanReuseAFilename: the transfer
// deletes the file when it completes, so the filename from the last
// reply names a file that is gone.  Reading twice has to be a fresh
// request and a fresh name, or the second read aborts.
func TestTaskInventoryAsksAgainRatherThanReuseAFilename(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	o := &Object{ID: thePrim, Local: 77}

	// The first read, answered with a name and nothing else, so the
	// session has one remembered.
	wait := aside(t, func() ([]TaskItem, error) {
		return w.TaskInventory(context.Background(), o)
	})
	waitSent[*msg.RequestTaskInventory](t, f)
	f.Relay(t, replyTaskInventory(thePrim, "inventory_37c9.tmp"))
	x := waitSent[*msg.RequestXfer](t, f)
	f.Relay(t, xferPacket(x.XferID.ID, 0, true, []byte(theContentsFile)))
	if _, err := wait(); err != nil {
		t.Fatalf("the first TaskInventory: %v", err)
	}

	// The second one must not answer from the name it was told the
	// first time.  An empty filename is the simulator saying the
	// object holds nothing, which is how this one ends without
	// another transfer.
	f.Forget()
	wait = aside(t, func() ([]TaskItem, error) {
		return w.TaskInventory(context.Background(), o)
	})
	waitSent[*msg.RequestTaskInventory](t, f)
	f.Relay(t, replyTaskInventory(thePrim, ""))

	held, err := wait()
	if err != nil {
		t.Fatalf("the second TaskInventory: %v", err)
	}
	if held != nil {
		t.Errorf("an object holding nothing came back with %v", taskNames(held))
	}
	if got := sentOf[*msg.RequestXfer](f); len(got) != 0 {
		t.Errorf("%d transfers were started for an object holding nothing", len(got))
	}
}

// TestTaskInventoryReportsWhatWentWrong: the request, the reply and the
// transfer are three separate things that can fail, and an empty answer
// from any of them reads like an empty object.
func TestTaskInventoryReportsWhatWentWrong(t *testing.T) {
	o := &Object{ID: thePrim, Local: 77}

	t.Run("the request never went", func(t *testing.T) {
		w, f := newFakeSession(t)
		f.FailSends(errors.New("the circuit is gone"))
		if _, err := w.TaskInventory(context.Background(), o); err == nil {
			t.Error("TaskInventory waited for a reply to a request that never went")
		}
	})

	t.Run("the caller gave up", func(t *testing.T) {
		w, _ := newFakeSession(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := w.TaskInventory(ctx, o); !errors.Is(err, context.Canceled) {
			t.Errorf("TaskInventory = %v, want the context's reason", err)
		}
	})

	t.Run("the transfer was aborted", func(t *testing.T) {
		t.Parallel()
		w, f := newFakeSession(t)
		wait := aside(t, func() ([]TaskItem, error) {
			return w.TaskInventory(context.Background(), o)
		})
		waitSent[*msg.RequestTaskInventory](t, f)
		f.Relay(t, replyTaskInventory(thePrim, "inventory_37c9.tmp"))

		x := waitSent[*msg.RequestXfer](t, f)
		abort := &msg.AbortXfer{}
		abort.XferID.ID = x.XferID.ID
		abort.XferID.Result = -1
		f.Relay(t, abort)

		if _, err := wait(); err == nil {
			t.Error("TaskInventory reported contents for a transfer that was aborted")
		}
	})
}

// TestFindInObjectAnswersNothingRatherThanFailing: not being in the
// object is an answer, and every caller of this is deciding whether to
// put something in.
func TestFindInObjectAnswersNothingRatherThanFailing(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	o := &Object{ID: thePrim, Local: 77}

	wait := aside(t, func() (*TaskItem, error) {
		return w.FindInObject(context.Background(), o, "a script")
	})
	waitSent[*msg.RequestTaskInventory](t, f)
	f.Relay(t, replyTaskInventory(thePrim, "inventory_37c9.tmp"))
	x := waitSent[*msg.RequestXfer](t, f)
	f.Relay(t, xferPacket(x.XferID.ID, 0, true, []byte(theContentsFile)))

	it, err := wait()
	if err != nil {
		t.Fatalf("FindInObject: %v", err)
	}
	if it == nil || it.ID != theChild {
		t.Fatalf("found %+v", it)
	}

	// The same object, holding nothing this time.
	f.Forget()
	waitMissing := aside(t, func() (*TaskItem, error) {
		return w.FindInObject(context.Background(), o, "a script")
	})
	waitSent[*msg.RequestTaskInventory](t, f)
	f.Relay(t, replyTaskInventory(thePrim, ""))
	if it, err := waitMissing(); err != nil || it != nil {
		t.Errorf("FindInObject = %+v, %v; want nothing and no error", it, err)
	}

	// And a read that failed, which is not the same as not finding it.
	f.FailSends(errors.New("the circuit is gone"))
	if _, err := w.FindInObject(context.Background(), o, "a script"); err == nil {
		t.Error("FindInObject reported nothing found when the read itself failed")
	}
}

// relayAsset answers a transfer request with a whole asset in one
// packet.  The last packet is marked in its status, not its number.
func relayAsset(t *testing.T, f *fakeBackend, id msg.UUID, body []byte) {
	t.Helper()
	info := &msg.TransferInfo{}
	info.TransferInfo.TransferID = id
	info.TransferInfo.ChannelType = 2
	info.TransferInfo.Status = 0
	info.TransferInfo.Size = int32(len(body))
	f.Relay(t, info)

	p := &msg.TransferPacket{}
	p.TransferData.TransferID = id
	p.TransferData.ChannelType = 2
	p.TransferData.Packet = 0
	p.TransferData.Status = 1
	p.TransferData.Data = body
	f.Relay(t, p)
}

// TestReadingAnAssetProvesWhoIsAllowedToReadIt: the ViewerAsset
// capability answers 403 for a notecard or a script, so these come over
// UDP -- and the request carries the identities the simulator checks the
// permissions against.  Getting those wrong is a refusal, not an empty
// answer.
func TestReadingAnAssetProvesWhoIsAllowedToReadIt(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	wait := aside(t, func() ([]byte, error) {
		// A timeout of zero asks for the default, which is not
		// reached here.
		return w.ReadAsset(context.Background(), client.AssetRef{
			Item: theChild, Asset: theOther, Type: int32(AssetNotecard),
		}, 0)
	})

	req := waitSent[*msg.TransferRequest](t, f)
	if req.TransferInfo.ChannelType != 2 || req.TransferInfo.SourceType != 3 {
		t.Errorf("asked on channel %d from source %d",
			req.TransferInfo.ChannelType, req.TransferInfo.SourceType)
	}
	// Six uuids and a type: agent, session, owner, task, item, asset.
	// The owner was left out by the caller and filled in with us,
	// since an asset with no owner belongs to nobody and is refused.
	p := req.TransferInfo.Params
	if len(p) != 6*16+4 {
		t.Fatalf("the request carries %d bytes of parameters", len(p))
	}
	if got := msg.UUID(p[32:48]); got != testAgentID {
		t.Errorf("asked as owner %s, want us", got)
	}
	if got := msg.UUID(p[48:64]); !got.IsZero() {
		t.Errorf("asked about task %s, want none for agent inventory", got)
	}
	if got := msg.UUID(p[64:80]); got != theChild {
		t.Errorf("asked about item %s", got)
	}

	relayAsset(t, f, req.TransferInfo.TransferID, []byte("Linden text version 2\n"))

	got, err := wait()
	if err != nil {
		t.Fatalf("ReadAsset: %v", err)
	}
	if string(got) != "Linden text version 2\n" {
		t.Errorf("read %q", got)
	}
}

// TestReadingSomethingInsideAnObjectNamesTheObject: the same transfer,
// with the object in it -- the simulator checks whether we may read the
// object's copy, which is a different question from the inventory one.
func TestReadingSomethingInsideAnObjectNamesTheObject(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	o := &Object{ID: thePrim, Local: 77}
	it := &TaskItem{ID: theChild, Asset: theOther, Name: "a script"}

	wait := aside(t, func() ([]byte, error) {
		return w.ReadTaskAsset(context.Background(), o, it, int32(AssetLSLText), 5*time.Second)
	})
	req := waitSent[*msg.TransferRequest](t, f)
	if got := msg.UUID(req.TransferInfo.Params[48:64]); got != thePrim {
		t.Errorf("asked about task %s, want the object", got)
	}
	relayAsset(t, f, req.TransferInfo.TransferID, []byte("default {}"))

	got, err := wait()
	if err != nil {
		t.Fatalf("ReadTaskAsset: %v", err)
	}
	if string(got) != "default {}" {
		t.Errorf("read %q", got)
	}
}

// TestANumberInTheContentsFileIsReadAsHexFirst: the masks are hex with
// no 0x and the prices are decimal, and the file does not say which is
// which.  Hex first is the choice that keeps the masks right.
func TestANumberInTheContentsFileIsReadAsHexFirst(t *testing.T) {
	for _, c := range []struct {
		in   string
		want uint64
	}{
		{"7fffffff", 0x7fffffff},
		{" 0008e000 ", 0x8e000},
		{"10", 16}, // a decimal ten reads as sixteen, and that is the trade
		// Too long to be hex and still a number, which is the only
		// way the decimal reading is ever reached.
		{"18446744073709551615", 18446744073709551615},
		{"not a number", 0},
		{"", 0},
	} {
		if got := hexOrDec(c.in); got != c.want {
			t.Errorf("hexOrDec(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestRenamingInsideAnObjectSendsTheWholeItemBack: the message carries
// the whole item, so anything not put back goes to zero -- and for a
// permission mask that is a rename that quietly takes the rights away.
func TestRenamingInsideAnObjectSendsTheWholeItemBack(t *testing.T) {
	w, f := newFakeSession(t)
	o := &Object{ID: thePrim, Local: 77}
	it := TaskItem{
		ID: theChild, Asset: theOther, Name: "a script", Desc: "does something",
		Type: "lsltext", InvType: "lsltext", Flags: 1, Created: 1700000000,
		CreatorID: testAgentID, OwnerID: testAgentID,
		BaseMask: PermAll, OwnerMask: PermAll, GroupMask: PermCopy,
		EveryoneMask: PermMove, NextOwnerMask: PermTransfer,
		SaleType: "not", SalePrice: 5,
	}

	if err := w.RenameInObject(context.Background(), o, it, ""); err == nil {
		t.Error("RenameInObject renamed something to nothing")
	}
	if err := w.RenameInObject(context.Background(), o, it, "renamed"); err != nil {
		t.Fatalf("RenameInObject: %v", err)
	}

	m := onlySent[*msg.UpdateTaskInventory](t, f)
	if m.UpdateData.LocalID != 77 || m.UpdateData.Key != 0 {
		t.Errorf("update data = %+v; key 0 selects the object's inventory", m.UpdateData)
	}
	d := &m.InventoryData
	if got := trimNul(d.Name); got != "renamed" {
		t.Errorf("renamed it to %q", got)
	}
	if got := trimNul(d.Description); got != "does something" {
		t.Errorf("the description came back as %q", got)
	}
	if d.ItemID != theChild || d.CreatorID != testAgentID {
		t.Errorf("renamed %s, created by %s", d.ItemID, d.CreatorID)
	}
	if d.BaseMask != PermAll || d.OwnerMask != PermAll || d.GroupMask != PermCopy ||
		d.EveryoneMask != PermMove || d.NextOwnerMask != PermTransfer {
		t.Errorf("the permissions came back as %+v", d)
	}
	// The words in the contents file go back as the numbers the
	// protocol takes.
	if d.Type != 10 || d.InvType != 10 {
		t.Errorf("type went out as %d/%d, want lsltext both", d.Type, d.InvType)
	}
	if d.CreationDate != 1700000000 || d.SalePrice != 5 || d.Flags != 1 {
		t.Errorf("dates and sale came back as %+v", d)
	}

	// An item whose inv_type the file did not give falls back to its
	// type rather than going out as zero, which is a texture.
	f.Forget()
	it.InvType = ""
	if err := w.RenameInObject(context.Background(), o, it, "renamed"); err != nil {
		t.Fatalf("RenameInObject: %v", err)
	}
	if got := onlySent[*msg.UpdateTaskInventory](t, f).InventoryData.InvType; got != 10 {
		t.Errorf("inv type with nothing to go on = %d, want the asset type", got)
	}
}

// TestAssetTypeWordsAreTheProtocolsNumbers: the contents file says
// "lsltext" where the message wants 10, and a word nobody knows is -1
// rather than 0 -- 0 is a texture, and a script sent as a texture is a
// silent corruption.
func TestAssetTypeWordsAreTheProtocolsNumbers(t *testing.T) {
	want := map[string]int8{
		"texture": 0, "sound": 1, "callcard": 2, "landmark": 3, "script": 4,
		"clothing": 5, "object": 6, "notecard": 7, "category": 8, "root": 8,
		"lsltext": 10, "lsl": 10, "bodypart": 13, "snapshot": 15, "attach": 17,
		"wearable": 18, "animatn": 20, "animation": 20, "gesture": 21, "mesh": 49,
		"": -1, "something else": -1,
	}
	for word, n := range want {
		if got := assetTypeNumber(word); got != n {
			t.Errorf("assetTypeNumber(%q) = %d, want %d", word, got, n)
		}
	}
}

// TestFirstNonEmptyStrPrefersTheFirst: it is what makes a missing
// inv_type fall back to the type, and getting it the other way round
// would make every item's inv_type its asset type.
func TestFirstNonEmptyStrPrefersTheFirst(t *testing.T) {
	if got := firstNonEmptyStr("a", "b"); got != "a" {
		t.Errorf("firstNonEmptyStr(a, b) = %q", got)
	}
	if got := firstNonEmptyStr("", "b"); got != "b" {
		t.Errorf("firstNonEmptyStr(, b) = %q", got)
	}
}

// ------------------------------------- asking a capability again

// describeAttempts serves the first half of an upload, failing the first
// n times it is asked and then answering properly, and counts the asks.
func describeAttempts(t *testing.T, f *fakeBackend, cap string, fail int) (*uploadServer, *atomic.Int32) {
	t.Helper()
	u := &uploadServer{body: make(chan []byte, 4), asked: make(chan []byte, 4)}
	u.dest = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := readAllBody(r)
		u.body <- b
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprint(w, `<llsd><map><key>state</key><string>complete</string>`+
			`<key>compiled</key><boolean>true</boolean></map></llsd>`)
	}))
	t.Cleanup(u.dest.Close)

	var asks atomic.Int32
	f.ServeCap(t, cap, func(w http.ResponseWriter, r *http.Request) {
		b, _ := readAllBody(r)
		u.asked <- b
		if int(asks.Add(1)) <= fail {
			// What Second Life's own service does when it is having a
			// bad moment: a 500 with a Python stack trace in it.
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `<html><body><dl><dt>stack-trace</dt><dd>[{'method': 'handle_request'`)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(w, `<llsd><map><key>state</key><string>upload</string>`+
			`<key>uploader</key><string>%s/upload</string></map></llsd>`, u.dest.URL)
	})
	return u, &asks
}

// TestAnUploadAsksAgainWhenTheCapabilityHasABadMoment: measured on Agni,
// thirty scripts at once drew one or two 500s per run out of
// UpdateScriptTask, carrying a Python stack trace from Linden Lab's own
// service.  Every one of them was the FIRST half of the upload, which
// writes nothing -- so a whole run of thirty was thrown away over a
// hiccup that could have been asked again.
func TestAnUploadAsksAgainWhenTheCapabilityHasABadMoment(t *testing.T) {
	s, f := newFakeSession(t)
	_, asks := describeAttempts(t, f, "UpdateScriptAgent", 1)

	res, err := s.SaveScript(context.Background(), msg.UUID{15: 1}, "default {}")
	if err != nil {
		t.Fatalf("SaveScript: %v", err)
	}
	if !res.Compiled {
		t.Errorf("the script did not compile: %+v", res)
	}
	if got := asks.Load(); got != 2 {
		t.Errorf("the capability was asked %d times, want the failure and one more", got)
	}
}

// TestAnUploadAsksAgainOnlyOnce: a far end still failing a second later
// is having more than a moment, and a program that kept asking would be
// adding to whatever is wrong.
func TestAnUploadAsksAgainOnlyOnce(t *testing.T) {
	s, f := newFakeSession(t)
	_, asks := describeAttempts(t, f, "UpdateScriptAgent", 99)

	_, err := s.SaveScript(context.Background(), msg.UUID{15: 1}, "default {}")
	if err == nil {
		t.Fatal("an upload came back well from a capability that never answered")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Errorf("SaveScript = %v, want what the capability said", err)
	}
	if got := asks.Load(); got != 2 {
		t.Errorf("the capability was asked %d times, want two", got)
	}
}

// TestAnUploadDoesNotAskAgainWhenItWasRefused: a 4xx is this end having
// asked for something it will not get however often it asks -- no such
// item, not allowed, not enough money.  Asking again is a second failure
// and a wait for nothing.
func TestAnUploadDoesNotAskAgainWhenItWasRefused(t *testing.T) {
	s, f := newFakeSession(t)

	var asks atomic.Int32
	f.ServeCap(t, "UpdateScriptAgent", func(w http.ResponseWriter, r *http.Request) {
		asks.Add(1)
		http.Error(w, "no such item", http.StatusNotFound)
	})

	if _, err := s.SaveScript(context.Background(), msg.UUID{15: 1}, "default {}"); err == nil {
		t.Fatal("a refused upload came back well")
	}
	if got := asks.Load(); got != 1 {
		t.Errorf("a refusal was asked about %d times", got)
	}
}

// TestOnlyTheHalfThatWritesNothingIsAskedAgain: the second half hands
// over the bytes, so an answer that went missing may have been an upload
// that landed.  Asking again there could install a script twice, make a
// second inventory item, or pay a second time -- upload is shared by the
// capability that creates items and charges for them.
func TestOnlyTheHalfThatWritesNothingIsAskedAgain(t *testing.T) {
	s, f := newFakeSession(t)

	var writes atomic.Int32
	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writes.Add(1)
		http.Error(w, "the service is unwell", http.StatusInternalServerError)
	}))
	t.Cleanup(dest.Close)
	f.ServeCap(t, "UpdateScriptAgent", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(w, `<llsd><map><key>state</key><string>upload</string>`+
			`<key>uploader</key><string>%s/upload</string></map></llsd>`, dest.URL)
	})

	if _, err := s.SaveScript(context.Background(), msg.UUID{15: 1}, "default {}"); err == nil {
		t.Fatal("an upload whose write failed came back well")
	}
	if got := writes.Load(); got != 1 {
		t.Errorf("the bytes were sent %d times; a write that may have landed "+
			"must not be sent again", got)
	}
}

// TestGivingUpWhileWaitingToAskAgain: the pause before a second ask is a
// pause a cancelled command must not sit through.
func TestGivingUpWhileWaitingToAskAgain(t *testing.T) {
	s, f := newFakeSession(t)
	describeAttempts(t, f, "UpdateScriptAgent", 99)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := s.SaveScript(ctx, msg.UUID{15: 1}, "default {}")
		done <- err
	}()
	// The first ask has to have failed before the wait can be
	// interrupted, so give it a moment and then give up.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("SaveScript = %v, want the cancellation", err)
		}
	case <-time.After(uploadRetryWait + 2*time.Second):
		t.Error("giving up did not interrupt the wait before asking again")
	}
}

// theTaskItem is the id an object gives its own copy of an item.
var theTaskItem = msg.MustParseUUID("a7877e57-7e57-c0de-9b5a-4cabdaeab941")

// TestFetchFromObjectWaitsForTheItemItBecame: MoveTaskInventory is
// answered by nothing, so the item is looked for in the folder it was
// sent to -- and an item of the same name that was there already is not
// it.
func TestFetchFromObjectWaitsForTheItemItBecame(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)

	old := anItem(theOther, "a notecard")
	fetched := anItem(theChild, "a notecard")
	var mu sync.Mutex
	arrived := false
	f.ServeInventory(t, func(msg.UUID) []*Item {
		mu.Lock()
		defer mu.Unlock()
		if !arrived {
			return []*Item{old}
		}
		return []*Item{old, fetched}
	})

	o := &Object{ID: thePrim, Local: 77, Name: "lantern"}
	inside := TaskItem{ID: theTaskItem, Name: "a notecard"}
	wait := aside(t, func() (*Item, error) {
		return w.FetchFromObject(context.Background(), o, inside, aFolder, 0)
	})

	m := waitSent[*msg.MoveTaskInventory](t, f)
	if m.AgentData.FolderID != aFolder {
		t.Errorf("sent it to folder %s", m.AgentData.FolderID)
	}
	// The object's own id for its copy, and the object by local id.
	if m.InventoryData.LocalID != 77 || m.InventoryData.ItemID != theTaskItem {
		t.Errorf("asked for %s from local id %d", m.InventoryData.ItemID, m.InventoryData.LocalID)
	}

	mu.Lock()
	arrived = true
	mu.Unlock()

	it, err := wait()
	if err != nil {
		t.Fatalf("FetchFromObject: %v", err)
	}
	if it.ID != theChild {
		t.Errorf("fetched %s, want the item that was not there before", it.ID)
	}
}

func TestFetchFromObjectSaysWhenNothingArrives(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	f.ServeInventory(t, func(msg.UUID) []*Item { return nil })

	o := &Object{ID: thePrim, Local: 77, Name: "lantern"}
	_, err := w.FetchFromObject(context.Background(), o,
		TaskItem{ID: theTaskItem, Name: "a notecard"}, aFolder, 3*time.Second)
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("FetchFromObject = %v, want a timeout", err)
	}
}

// TestFetchFromObjectWantsAFolder: a zero folder lets the simulator
// choose, and then nothing knows where to look for the item.
func TestFetchFromObjectWantsAFolder(t *testing.T) {
	t.Parallel()
	w, f := newFakeSession(t)
	o := &Object{ID: thePrim, Local: 77}
	if _, err := w.FetchFromObject(context.Background(), o, TaskItem{ID: theTaskItem}, msg.UUID{}, time.Second); err == nil {
		t.Error("fetched into no folder")
	}
	if len(f.Sent()) != 0 {
		t.Errorf("sent %d messages anyway", len(f.Sent()))
	}
}

// TestMayCopyOutIsTheViewersTest: copy for whoever is asking, and
// transfer when the item is not already theirs.
func TestMayCopyOutIsTheViewersTest(t *testing.T) {
	t.Parallel()
	me, them, group := testAgentID, theOther, aFolder
	for _, c := range []struct {
		name  string
		it    TaskItem
		group msg.UUID
		want  bool
	}{
		{"mine and copyable", TaskItem{OwnerID: me, OwnerMask: PermCopy}, msg.UUID{}, true},
		{"mine, no copy", TaskItem{OwnerID: me, OwnerMask: PermAll &^ PermCopy, EveryoneMask: PermCopy}, msg.UUID{}, false},
		{"theirs, everyone may copy and it transfers", TaskItem{OwnerID: them, OwnerMask: PermAll, EveryoneMask: PermCopy}, msg.UUID{}, true},
		{"theirs, everyone may copy but no transfer", TaskItem{OwnerID: them, OwnerMask: PermAll &^ PermTransfer, EveryoneMask: PermCopy}, msg.UUID{}, false},
		{"theirs, only the owner may copy", TaskItem{OwnerID: them, OwnerMask: PermAll}, msg.UUID{}, false},
		{"theirs, the group may copy", TaskItem{OwnerID: them, GroupID: group, OwnerMask: PermAll, GroupMask: PermCopy}, group, true},
		{"theirs, another group may copy", TaskItem{OwnerID: them, GroupID: group, OwnerMask: PermAll, GroupMask: PermCopy}, theChild, false},
	} {
		if got := MayCopyOut(c.it, me, c.group); got != c.want {
			t.Errorf("%s: MayCopyOut = %v, want %v", c.name, got, c.want)
		}
	}
}
