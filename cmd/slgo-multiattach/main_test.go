package main

// The two questions this experiment asks of a session, over a grid that
// is not there.
//
// What the program does is one straight line: attach a second object to a
// point that already holds one, and count what is there afterwards.  The
// line itself needs a daemon to dial, an inventory to copy an item in and
// an avatar to wear it, and none of those can be stood in for here.
//
// What CAN be reached is the two functions that line is built out of:
// where a taken object lands, and what is on one attachment point.  Both
// are decisions about what the grid answered.  A folder read as the root
// where there is an Objects folder finds no seed in it, and the run stops
// saying so.  A point filter that let everything through would not stop
// it: it would count the whole avatar and print YES whatever happened.

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var _ sl.Backend = (*fakeGrid)(nil)

// Who the fake says we are.  sl.New refuses a backend with no agent or
// session id, so these are load bearing rather than decoration.
var (
	testMe        = msg.MustParseUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")
	testSessionID = msg.MustParseUUID("72427e57-7e57-c0de-39d2-e78c47465eb6")
	testRoot      = msg.MustParseUUID("23077e57-7e57-c0de-245c-d6b83f1a8b6d")
	testObjects   = msg.MustParseUUID("a9a87e57-7e57-c0de-b748-062ee08c11ee")
)

// fakeGrid answers the two things these functions ask: what is in the top
// of inventory, and what is worn.
//
// Inventory is served over httptest rather than answered from a field,
// because inventory is AIS and not a message -- a fake that
// short-circuited it would be testing a path this program does not take.
type fakeGrid struct {
	mu sync.Mutex

	msgs chan *sl.Message
	done chan struct{}
	once sync.Once

	// folders is the top of inventory, and empty is an account whose
	// inventory has no Objects folder in it at all.
	folders []struct {
		ID   msg.UUID
		Name string
	}

	worn       []*sl.Seen
	objectsErr error

	caps   map[string]string
	capErr error
}

func newFakeSession(t *testing.T) (*sl.Session, *fakeGrid) {
	t.Helper()
	f := &fakeGrid{
		msgs: make(chan *sl.Message),
		done: make(chan struct{}),
		caps: map[string]string{},
	}
	f.folders = append(f.folders, struct {
		ID   msg.UUID
		Name string
	}{testObjects, "Objects"})
	t.Cleanup(func() { f.Close() })
	f.serveInventory(t)

	s, err := sl.New(f)
	if err != nil {
		t.Fatalf("sl.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s, f
}

// serveInventory answers a read of the top of inventory with whatever
// folders the test put there.
func (f *fakeGrid) serveInventory(t *testing.T) {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" ?><llsd><map>`)
		fmt.Fprintf(&b, `<key>category_id</key><string>%s</string>`, testRoot)
		fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, msg.UUID{})
		b.WriteString(`<key>name</key><string>My Inventory</string>`)
		b.WriteString(`<key>type_default</key><integer>-1</integer>`)
		b.WriteString(`<key>version</key><integer>1</integer>`)
		b.WriteString(`<key>_embedded</key><map><key>categories</key><map>`)
		for _, sub := range f.folders {
			fmt.Fprintf(&b, `<key>%s</key><map>`, sub.ID)
			fmt.Fprintf(&b, `<key>category_id</key><string>%s</string>`, sub.ID)
			fmt.Fprintf(&b, `<key>parent_id</key><string>%s</string>`, testRoot)
			fmt.Fprintf(&b, `<key>name</key><string>%s</string>`, xmlText(sub.Name))
			b.WriteString(`<key>type_default</key><integer>6</integer>`)
			b.WriteString(`<key>version</key><integer>1</integer></map>`)
		}
		b.WriteString(`</map><key>items</key><map/><key>links</key><map/></map></map></llsd>`)
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, b.String())
	}))
	t.Cleanup(s.Close)
	f.mu.Lock()
	f.caps[agent.InventoryCap] = s.URL
	f.mu.Unlock()
}

func xmlText(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return b.String()
}

// attached is one object worn on a point, as the grid describes it.
func attached(name string, local uint32, point int) *sl.Seen {
	id := msg.MustParseUUID(fmt.Sprintf("88fa7e57-7e57-c0de-af42-813fbc8c%04d", local))
	item := msg.MustParseUUID(fmt.Sprintf("c75d7e57-7e57-c0de-b372-%012d", local))
	return &sl.Seen{
		Object:      sl.Object{ID: id, Local: local, Name: name},
		AttachItem:  item,
		AttachPoint: point,
	}
}

// ---------------------------------------------------------------- tests

// TestATakenObjectLandsInObjectsAndTheRootWillDo: the seed is copied into
// a folder and the copy has to land where the seed is, or the experiment
// compares two different things.  An inventory with no Objects folder is
// unusual rather than broken, and stopping would be a worse answer than a
// slightly untidy inventory.
func TestATakenObjectLandsInObjectsAndTheRootWillDo(t *testing.T) {
	s, f := newFakeSession(t)

	got, err := objectsFolder(context.Background(), s)
	if err != nil {
		t.Fatalf("objectsFolder: %v", err)
	}
	if got != testObjects {
		t.Errorf("objectsFolder = %s, want the Objects folder", got)
	}

	f.mu.Lock()
	f.folders = nil
	f.mu.Unlock()
	if got, err = objectsFolder(context.Background(), s); err != nil {
		t.Fatalf("objectsFolder: %v", err)
	}
	if got != s.InventoryRoot() {
		t.Errorf("objectsFolder = %s, want the root", got)
	}
}

// TestAnInventoryThatWillNotAnswerIsNotGuessedPast: everything after this
// copies an item into whatever comes back, so an inventory that cannot be
// read must not turn into a zero folder -- that is not the root, it is
// nowhere, and the copy would go missing.
func TestAnInventoryThatWillNotAnswerIsNotGuessedPast(t *testing.T) {
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.capErr = fmt.Errorf("the capability is not answering")
	f.mu.Unlock()

	if _, err := objectsFolder(context.Background(), s); err == nil {
		t.Error("objectsFolder found a folder in an inventory it could not read")
	}
}

// TestWhatIsOnOnePointIsNotWhatIsOnTheAvatar: the whole experiment is a
// count of what is on ONE point before and after, so a filter that let
// the other attachments through would count the avatar's hair and print
// YES whatever the attach did.
func TestWhatIsOnOnePointIsNotWhatIsOnTheAvatar(t *testing.T) {
	s, f := newFakeSession(t)
	f.mu.Lock()
	f.worn = []*sl.Seen{
		attached("auto", 100, sl.HUDBottomLeft),
		attached("auto probe", 101, sl.HUDBottomLeft),
		attached("hair", 102, sl.HUDTopRight),
	}
	f.mu.Unlock()

	got := worn(context.Background(), s, sl.HUDBottomLeft)
	if len(got) != 2 {
		t.Fatalf("%d objects on the bottom left point, want the two put there", len(got))
	}
	for _, a := range got {
		if a.Point != sl.HUDBottomLeft {
			t.Errorf("%s is on point %d", a.Object.Name, a.Point)
		}
	}

	// A point with nothing on it is the state this starts from on an
	// account that has never worn one, and is not an error.
	if got := worn(context.Background(), s, sl.HUDCenter1); len(got) != 0 {
		t.Errorf("%d objects on a point nothing is on", len(got))
	}
}

// ------------------------------------------------------------- backend

func (f *fakeGrid) Info() *sl.Info {
	return &sl.Info{
		Name: "quark", AgentID: testMe, SessionID: testSessionID,
		AvatarName: "Quark Idlemind", Region: "Test Region", InventoryRoot: testRoot,
	}
}

// Refresh is Backend's; nothing here rebuilds a session underneath, so
// the identity it hands back is the one it has.
func (f *fakeGrid) Refresh(context.Context) (*sl.Info, error) { return f.Info(), nil }

// Control is nothing here.  Nothing this fake stands in for sits
// down or stands up; the method exists because sl.Backend has it,
// so that the one place an AgentUpdate is built stays the one place
// that owns the camera.
func (f *fakeGrid) Control(ctx context.Context, flags uint32) error { return nil }

func (f *fakeGrid) Send(ctx context.Context, m msg.Message, reliable bool) error { return nil }

func (f *fakeGrid) Messages() <-chan *sl.Message { return f.msgs }

// Events is nothing: nothing here reads the event queue, and a nil
// channel blocks rather than reading as a queue that has ended.
func (f *fakeGrid) Events() <-chan *sl.QueueEvent { return nil }
func (f *fakeGrid) Done() <-chan struct{}         { return f.done }
func (f *fakeGrid) Err() error                    { return nil }

// SimAttachments: this fake has never heard an appearance.
func (f *fakeGrid) SimAttachments(ctx context.Context, avatar msg.UUID) (*sl.SimAttachments, error) {
	return nil, nil
}

func (f *fakeGrid) Presence(ctx context.Context, drawDistance float32) (*sl.Presence, error) {
	return &sl.Presence{Region: "Test Region"}, nil
}

func (f *fakeGrid) Objects(ctx context.Context, named, id string) ([]*sl.Seen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.objectsErr != nil {
		return nil, f.objectsErr
	}
	return append([]*sl.Seen(nil), f.worn...), nil
}

func (f *fakeGrid) Land(ctx context.Context) (*sl.Land, error) {
	return &sl.Land{Overlay: agent.OverlayFrom(nil, 0)}, nil
}

// Ground knows no land: none has arrived.
func (f *fakeGrid) Ground(ctx context.Context, west, south, east, north float32) (float32, bool, error) {
	return 0, false, nil
}

func (f *fakeGrid) Region(ctx context.Context) (*sl.Region, bool, error) {
	return &sl.Region{Name: "Test Region"}, true, nil
}

func (f *fakeGrid) SimStats(ctx context.Context) (*sl.SimStats, error) {
	return &sl.SimStats{}, nil
}

func (f *fakeGrid) LastRegionDetails(context.Context) (*sl.RegionDetails, uint64, error) {
	return nil, 0, nil
}

func (f *fakeGrid) Neighbours(ctx context.Context, set *bool) (*sl.Neighbours, error) {
	return &sl.Neighbours{}, nil
}

func (f *fakeGrid) Flush(ctx context.Context) (int, error) { return 0, nil }

func (f *fakeGrid) ConfirmLinkOrder(ctx context.Context, root msg.UUID, keys []msg.UUID) (*sl.LinkConfirmation, error) {
	return nil, sl.ErrNotSupported
}
func (f *fakeGrid) Friends(ctx context.Context) ([]sl.Friend, error) { return nil, nil }

func (f *fakeGrid) NoteFriend(ctx context.Context, id msg.UUID, online bool) error { return nil }

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
	f.once.Do(func() {
		close(f.msgs)
		close(f.done)
	})
	return nil
}

// RegionChanges is never told of one: nothing here teleports.
func (f *fakeGrid) RegionChanges() <-chan *sl.RegionChange { return nil }
