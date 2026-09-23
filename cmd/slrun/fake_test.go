package main

// A grid that runs scripts and is not there.
//
// slrun is a thin thing wrapped around one call -- put a script in an
// object, watch what it says, report whether it got to the end -- and
// everything it decides is about what came back from that call.  So the
// only way to test it at all is to stand in for the three protocols a run
// needs: the object's contents arrive over xfer, the compile is an http
// upload to a capability, and the output is chat relayed back.
//
// fakeGrid plays all three from httptest on loopback and a channel.  What
// a script "says" is whatever the test set it to say, because what is
// being checked here is what slrun does with the lines rather than
// what Second Life would have produced them from.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var _ sl.Backend = (*fakeGrid)(nil)

// Who the fake says we are.  sl.New refuses a backend with no agent or
// session id, so these are load bearing rather than decoration.
var (
	testMe        = msg.MustParseUUID("a5707e57-7e57-c0de-65b6-5a7ceceb4b01")
	testSessionID = msg.MustParseUUID("e6887e57-7e57-c0de-2a6a-c862daab753b")
	testRoot      = msg.MustParseUUID("23077e57-7e57-c0de-622e-77274d813d21")
	thePrim       = msg.MustParseUUID("88fa7e57-7e57-c0de-af42-813fbc8c4b63")
	theItem       = msg.MustParseUUID("c75d7e57-7e57-c0de-b372-000000000001")
)

// fakeGrid answers the few things running one script asks of a
// simulator, and says whatever the test told it to.
type fakeGrid struct {
	mu sync.Mutex

	info *sl.Info
	msgs chan *sl.Message
	done chan struct{}
	once sync.Once

	obj  sl.Object
	held string // what the object already holds, by name

	// seen is what the region says is in range, which for the object a
	// script runs in is the whole of what a caller has to go on.  It
	// starts as the one object and is added to by a test that needs an
	// ATTACHMENT rather than a prim -- the shared auto object is worn,
	// and is found by the inventory item it was worn from.
	seen []*sl.Seen

	caps map[string]string

	// says is what the script says when it runs, in order, and sentinel
	// is appended to it unless silent -- so a run that is meant to time
	// out is one word away from one that is not.
	says     []string
	sentinel string
	silent   bool

	// refuse is Second Life declining to compile, and fault is a script
	// that stopped where it was.  refuseSilently is the same refusal with
	// nothing attached to it, which happens more often than one would
	// like: at 512 copies of a benchmark shape the whole message is
	// "Internal server compile error", and sometimes there is not even
	// that.
	refuse         []string
	refuseSilently bool
	fault          string

	sendErr error

	// onRun is called as a script is uploaded, which is the moment a run
	// is certainly in flight.  It is how a test raises a signal at a
	// point where the program is listening for one.
	onRun func()

	// ran counts the scripts started, and sources keeps what was sent, so
	// that a test can assert on the source as well as on the answer.
	ran     int
	sources []string
}

// newFakeGrid is the grid on its own, for the runs that reach it through
// a daemon rather than as a backend of their own.  See daemon_test.go:
// everything a hosted session asks for is answered from here, and the
// daemon is a proxy in front of it.
func newFakeGrid(t *testing.T, held string) *fakeGrid {
	t.Helper()
	f := &fakeGrid{
		info: &sl.Info{
			Name: "quark", AgentID: testMe, SessionID: testSessionID,
			AvatarName: "Quark Idlemind", Region: "Test Region", InventoryRoot: testRoot,
		},
		msgs:     make(chan *sl.Message, 8),
		done:     make(chan struct{}),
		obj:      sl.Object{ID: thePrim, Local: 77, Name: "a prim"},
		held:     held,
		caps:     map[string]string{},
		sentinel: "DONE",
	}
	f.seen = []*sl.Seen{{Object: f.obj}}
	t.Cleanup(func() { f.Close() })
	f.serveUpload(t)
	return f
}

// newFakeSession is a real sl.Session over a grid that is not there,
// holding one object with one script already inside it.
//
// Already inside it because creating one costs six seconds of waiting for
// the object to admit it is there, and none of what slrun does is
// about that.
func newFakeSession(t *testing.T, held string) (*sl.Session, *sl.Object, *fakeGrid) {
	t.Helper()
	f := newFakeGrid(t, held)

	s, err := sl.New(f)
	if err != nil {
		t.Fatalf("sl.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	o := f.obj
	return s, &o, f
}

// ------------------------------------------------------- the capability

// serveUpload plays both halves of an asset upload: the capability that
// says where to write, and the URL it names.  They are separate servers
// because the second one's address has to be known before the first can
// answer with it.
func (f *fakeGrid) serveUpload(t *testing.T) {
	t.Helper()

	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		src, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		refused := append([]string(nil), f.refuse...)
		silently := f.refuseSilently
		f.sources = append(f.sources, string(src))
		if len(refused) == 0 && !silently {
			f.ran++
		}
		f.mu.Unlock()

		w.Header().Set("Content-Type", "application/llsd+xml")
		if silently {
			io.WriteString(w, `<llsd><map><key>state</key><string>complete</string>`+
				`<key>compiled</key><boolean>0</boolean></map></llsd>`)
			return
		}
		if len(refused) > 0 {
			var b strings.Builder
			b.WriteString(`<llsd><map><key>state</key><string>complete</string>` +
				`<key>compiled</key><boolean>0</boolean><key>errors</key><array>`)
			for _, e := range refused {
				fmt.Fprintf(&b, `<string>%s</string>`, e)
			}
			b.WriteString(`</array></map></llsd>`)
			io.WriteString(w, b.String())
			return
		}
		f.mu.Lock()
		hook := f.onRun
		f.mu.Unlock()
		if hook != nil {
			hook()
		}
		// After the verdict, not before: a script says what it has to say
		// the instant it is started, and the run is listening by now.
		go f.run()
		io.WriteString(w, `<llsd><map><key>state</key><string>complete</string>`+
			`<key>compiled</key><boolean>1</boolean></map></llsd>`)
	}))
	t.Cleanup(dest.Close)

	f.ServeCap(t, "UpdateScriptTask", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if _, err := llsd.Decode(bytes.NewReader(body)); err != nil {
			http.Error(w, "not llsd", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(w, `<llsd><map><key>state</key><string>upload</string>`+
			`<key>uploader</key><string>%s/upload</string></map></llsd>`, dest.URL)
	})
}

// run is the script executing: it says what it was told to say, and then
// the sentinel unless it is one of the ones that never finishes.
func (f *fakeGrid) run() {
	f.mu.Lock()
	says := append([]string(nil), f.says...)
	fault, silent, sentinel := f.fault, f.silent, f.sentinel
	f.mu.Unlock()

	for _, line := range says {
		f.say(sl.ChatSay, line)
	}
	if fault != "" {
		// A fault is two messages: the header naming the script, then one
		// line with the reason.  Both are the simulator commenting on the
		// script rather than the script speaking, so both are on the debug
		// channel.
		f.say(sl.ChatDebug, "a prim [script:"+flags.Script+"] Script run-time error")
		f.say(sl.ChatDebug, fault)
		return
	}
	if silent {
		return
	}
	f.say(sl.ChatSay, sentinel)
}

// say is the object speaking, which is the only way a script says
// anything: chat carries the object's name and key and no more.
func (f *fakeGrid) say(chatType uint8, text string) {
	m := &msg.ChatFromSimulator{}
	m.ChatData.SourceID = f.obj.ID
	m.ChatData.OwnerID = testMe
	m.ChatData.FromName = append([]byte("a prim"), 0)
	m.ChatData.SourceType = sl.SourceObject
	m.ChatData.ChatType = chatType
	m.ChatData.Message = append([]byte(text), 0)
	f.relay(m)
}

// relay hands a message to the session as though the grid had sent it.
// It gives up rather than failing the test: everything here happens on a
// goroutine of the fake's own, and a failure from one of those is a panic
// instead of a failure.  What a message nobody read looks like from the
// test's side is a run that timed out, which says the same thing where it
// can be read.
func (f *fakeGrid) relay(m msg.Message) {
	body, err := m.Encode()
	if err != nil {
		return
	}
	raw := &sl.Message{ID: msg.IDOf(m), Name: m.MsgInfo().Name, Body: body, At: time.Now()}
	select {
	case f.msgs <- raw:
	case <-f.done:
	case <-time.After(10 * time.Second):
	}
}

// ServeCap points a capability at an http server that lives as long as
// the test.  Everything that reaches a capability goes through DoCap, so
// this is how those calls are exercised: httptest listens on loopback,
// which is not the network and does not need one.
func (f *fakeGrid) ServeCap(t *testing.T, name string, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	f.mu.Lock()
	f.caps[name] = s.URL
	f.mu.Unlock()
	return s
}

// answer plays the simulator's side of a message the session sent.  It
// runs on its own goroutine because the answer goes back through the
// reader, and a backend that answered inside Send would be waiting for
// the reader that is waiting for it.
func (f *fakeGrid) answer(m msg.Message) {
	switch v := m.(type) {
	case *msg.RequestTaskInventory:
		r := &msg.ReplyTaskInventory{}
		r.InventoryData.TaskID = f.obj.ID
		r.InventoryData.Serial = 1
		r.InventoryData.Filename = append([]byte("inventory.tmp"), 0)
		f.relay(r)

	case *msg.RequestXfer:
		f.mu.Lock()
		body := fmt.Sprintf("\tinv_item\t0\n\t{\n\t\titem_id\t%s\n\t\tname\t%s|\n"+
			"\t\ttype\tlsltext\n\t\tinv_type\tlsl\n\t}\n", theItem, f.held)
		if f.held == "" {
			body = ""
		}
		f.mu.Unlock()

		p := &msg.SendXferPacket{}
		p.XferID.ID = v.XferID.ID
		p.XferID.Packet = 0 | 0x80000000
		p.DataPacket.Data = append(
			[]byte{byte(len(body)), byte(len(body) >> 8), byte(len(body) >> 16), byte(len(body) >> 24)},
			body...)
		f.relay(p)
	}
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
	err := f.sendErr
	f.mu.Unlock()
	if err != nil {
		return err
	}
	go f.answer(m)
	return nil
}

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
	return &sl.Presence{Position: msg.Vector3{X: 128, Y: 128, Z: 25}, Region: "Test Region"}, nil
}

func (f *fakeGrid) Objects(ctx context.Context, named, id string) ([]*sl.Seen, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*sl.Seen, 0, len(f.seen))
	for _, s := range f.seen {
		if named != "" && named != s.Object.Name {
			continue
		}
		if id != "" && !strings.EqualFold(id, s.Object.ID.String()) {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeGrid) Land(ctx context.Context) (*sl.Land, error) {
	return &sl.Land{Overlay: agent.OverlayFrom(nil, 0)}, nil
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
	base := f.caps[r.Cap]
	f.mu.Unlock()

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

// Close ends the session.  Only done is closed: the relays run on
// goroutines of the fake's own, and closing the channel they send on
// would turn a message that arrived a moment late into a panic.
func (f *fakeGrid) Close() error {
	f.once.Do(func() { close(f.done) })
	return nil
}

// RegionChanges is never told of one: nothing here teleports.
func (f *fakeGrid) RegionChanges() <-chan *sl.RegionChange { return nil }
