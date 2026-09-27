package main

// A grid that runs benchmark scripts and is not there.
//
// The measurement machinery has always been testable offline -- that is
// what --test is for -- but --test is a script.v1 backend and never
// reaches runner.go, the part that actually gets a script into Second
// Life and reads back what it said, so that was reached only by a live
// test with SLGO_LIVE set.  That is the half of this program that has to
// talk three protocols at once, and it was the half nothing checked.
//
// fakeGrid is an sl.Backend that plays all three.  The object's contents
// arrive over xfer, the compile is an http upload to a capability served
// by httptest on loopback, and the output is chat relayed back as though
// the script had said it.  What it says is worked out from the script
// itself: buildScript writes the copy count and the pad into a comment,
// so the fake reads them out and answers with the same staircase
// the --test model uses.  A benchmark can therefore be run end to end
// through the real transport, which is the only way the transport is
// exercised at all.
//
// It is not a simulator.  What is checked through it is what this program
// does with what it is told -- which readings it takes, which failures it
// backs off from -- and never what Second Life would have said.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
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
	testMe        = msg.MustParseUUID("3ac37e57-7e57-c0de-5607-527da8fa08de")
	testSessionID = msg.MustParseUUID("72427e57-7e57-c0de-39d2-e78c47465eb6")
	testRoot      = msg.MustParseUUID("23077e57-7e57-c0de-245c-d6b83f1a8b6d")

	// testRegion names the region the fake is in.  sl sends a local id
	// found by looking an object up only while the region it was found
	// in is named and still the avatar's.
	testRegion = msg.MustParseUUID("a4fd7e57-7e57-c0de-559f-7a9b7da6044a")
)

// fakeObject is one prim: what it is called, the benchmark script it
// holds, and whether that has run.
type fakeObject struct {
	obj  sl.Object
	item msg.UUID // the id of the benchmark script inside it
	ran  bool
}

// fakeGrid answers what a simulator would, for the few things running a
// benchmark script asks of one.
type fakeGrid struct {
	mu sync.Mutex

	// scripts is whether each script is running, as SetScriptRunning
	// last left it.
	scripts map[msg.UUID]bool

	info *sl.Info
	msgs chan *sl.Message
	done chan struct{}
	once sync.Once

	objects map[uint32]*fakeObject
	caps    map[string]string

	// crossing and codeSize are the model: the pad at which the copy-free
	// base script first tips into the next block, and what one copy of
	// CODE costs.  The same staircase the --test model uses, so that a
	// benchmark run through the transport has to come out with the same
	// numbers as one run above it.
	crossing, codeSize int

	// refuse names objects whose scripts Second Life will not compile,
	// and fault names those whose scripts stop with a run-time error.
	// Per object, because that is how a probe in a spare object is made
	// to fail while the measured one carries on.
	refuse map[msg.UUID][]string
	fault  map[msg.UUID]string

	// silent names objects whose scripts run and never say DONE, which
	// is the timeout the runner has to report rather than hang on.
	silent map[msg.UUID]bool

	// commentary is said after the reading, as an INFO line.  The
	// benchmark harness says none -- it reports one number and nothing
	// else -- so a test that is about SIFTING what a script says has to
	// supply something to sift.
	commentary string

	// refuseOver and faultOver are Second Life's two size limits, as copy
	// counts.  They are not the same limit: above faultOver the script
	// compiles and then collides stack with heap the moment it runs, and
	// above refuseOver the compiler will not take it at all.  Measured
	// live 2026-08-03, 256 copies of the reference shape compiled and
	// then collided, and 512 were refused outright.  Zero means no limit.
	refuseOver, faultOver int

	// sendErr is a circuit that has gone away, and capErr a capability
	// that will not answer.
	sendErr error
	capErr  error

	// ran counts the scripts that were actually uploaded, which is what
	// says whether something was served from a cache or sent.
	ran int
}

// newFakeGrid builds a grid holding one object per name given, the first
// of which is the measured one.
func newFakeGrid(t *testing.T, crossing, codeSize int, names ...string) *fakeGrid {
	t.Helper()
	f := &fakeGrid{
		info: &sl.Info{
			Name: "quark", AgentID: testMe, SessionID: testSessionID,
			AvatarName: "Quark Idlemind", Region: "Test Region", InventoryRoot: testRoot,
		},
		msgs:     make(chan *sl.Message, 8),
		done:     make(chan struct{}),
		objects:  map[uint32]*fakeObject{},
		caps:     map[string]string{},
		crossing: crossing, codeSize: codeSize,
		refuse: map[msg.UUID][]string{},
		fault:  map[msg.UUID]string{},
		silent: map[msg.UUID]bool{},
	}
	for i, name := range names {
		local := uint32(100 + i)
		f.objects[local] = &fakeObject{
			obj: sl.Object{
				ID:    msg.MustParseUUID(fmt.Sprintf("88fa7e57-7e57-c0de-af42-813fbc8c%04d", i)),
				Local: local, Name: name,
			},
			item: msg.MustParseUUID(fmt.Sprintf("c75d7e57-7e57-c0de-b372-%012d", i)),
		}
	}
	t.Cleanup(func() { f.Close() })
	f.serveUpload(t)
	return f
}

// newFakeRunner is a runner over a fake grid: the measured object, and
// spare objects to take probes in.
func newFakeRunner(t *testing.T, crossing, codeSize, spares int) (*runner, *fakeGrid) {
	t.Helper()
	names := []string{"auto"}
	for i := 0; i < spares; i++ {
		names = append(names, fmt.Sprintf("auto %d", i+2))
	}
	f := newFakeGrid(t, crossing, codeSize, names...)

	s, err := sl.New(f)
	if err != nil {
		t.Fatalf("sl.New: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	b := &runner{
		places:  []place{{s, f.object(100)}},
		Timeout: 20 * time.Second,
	}
	for i := 0; i < spares; i++ {
		b.places = append(b.places, place{s, f.object(uint32(101 + i))})
	}
	return b, f
}

// object is one of the prims, by local id.
func (f *fakeGrid) object(local uint32) *sl.Object {
	f.mu.Lock()
	defer f.mu.Unlock()
	o := f.objects[local].obj
	return &o
}

// fakeAnchor is where this fake's staircase starts.  5412 is a value that
// has been seen live; nothing depends on it beyond its being larger than
// any pad the fake is asked about.
const fakeAnchor = 5412

// mem is the model: what llGetUsedMemory would report for cnt copies at
// pad.  The same staircase the offline model uses, written out again
// rather than shared: this is the fake for the GRID transport, and a
// benchmark run through it has to come out with the same numbers as one
// run through the contract without the two agreeing by construction.
func (f *fakeGrid) mem(cnt, pad int) int {
	used := cnt*f.codeSize + pad - f.crossing
	return fakeAnchor + ((used + blockSize) &^ (blockSize - 1))
}

// harnessCall reads the copy count and the pad back out of a rendered
// script.  buildScript writes them into a COMMENT, so the fake
// knows what it is being asked to run without being told separately --
// and a script whose harness call went missing would be a script the
// benchmark could not read either.
var harnessCall = regexp.MustCompile(`(?m)^// slbench cnt=(\d+) pad=(-?\d+)$`)

func (f *fakeGrid) readHarness(src string) (cnt, pad int, ok bool) {
	m := harnessCall.FindStringSubmatch(src)
	if m == nil {
		return 0, 0, false
	}
	cnt, _ = strconv.Atoi(m[1])
	pad, _ = strconv.Atoi(m[2])
	return cnt, pad, true
}

// ------------------------------------------------------- the capability

// serveUpload plays both halves of an asset upload: the capability that
// says where to write, and the URL it names.  They are separate servers
// because the second one's address has to be known before the first can
// answer with it.
//
// The uploader's path carries the object and whether the script is to be
// started, because probes upload to several objects at once and a fake
// that remembered the last one asked about would answer the wrong prim.
func (f *fakeGrid) serveUpload(t *testing.T) {
	t.Helper()

	dest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		id, _ := msg.ParseUUID(parts[len(parts)-2])
		running := parts[len(parts)-1] == "run"
		src, _ := io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/llsd+xml")
		io.WriteString(w, f.compile(id, string(src), running))
	}))
	t.Cleanup(dest.Close)

	f.ServeCap(t, "UpdateScriptTask", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		v, err := llsd.Decode(bytes.NewReader(body))
		if err != nil {
			http.Error(w, "not llsd", http.StatusBadRequest)
			return
		}
		m := llsd.Map(v)
		run := "compile"
		if llsd.Bool(m, "is_script_running") {
			run = "run"
		}
		w.Header().Set("Content-Type", "application/llsd+xml")
		fmt.Fprintf(w, `<llsd><map><key>state</key><string>upload</string>`+
			`<key>uploader</key><string>%s/upload/%s/%s</string></map></llsd>`,
			dest.URL, llsd.String(m, "task_id"), run)
	})
}

// compile is Second Life's verdict on a script, and -- when it is to be
// started -- the script running.
func (f *fakeGrid) compile(id msg.UUID, src string, running bool) string {
	cnt, _, ok := f.readHarness(src)

	f.mu.Lock()
	refused := f.refuse[id]
	if ok && f.refuseOver > 0 && cnt > f.refuseOver {
		refused = []string{"Internal server compile error"}
	}
	f.mu.Unlock()

	if len(refused) > 0 {
		var b strings.Builder
		b.WriteString(`<llsd><map><key>state</key><string>complete</string>` +
			`<key>compiled</key><boolean>0</boolean><key>errors</key><array>`)
		for _, e := range refused {
			fmt.Fprintf(&b, `<string>%s</string>`, e)
		}
		b.WriteString(`</array></map></llsd>`)
		return b.String()
	}

	if running {
		// After the verdict, not before: a script says what it has to say
		// the instant it is started, and the run is listening by now.
		go f.run(id, src)
	}
	return `<llsd><map><key>state</key><string>complete</string>` +
		`<key>compiled</key><boolean>1</boolean></map></llsd>`
}

// run is the benchmark script executing: it says what the harness in
// main.go says, in the same order and with the same labels.
func (f *fakeGrid) run(id msg.UUID, src string) {
	cnt, pad, ok := f.readHarness(src)

	f.mu.Lock()
	f.ran++
	var o *fakeObject
	for _, cand := range f.objects {
		if cand.obj.ID == id {
			o = cand
		}
	}
	if o == nil {
		f.mu.Unlock()
		return
	}
	fault, silent := f.fault[id], f.silent[id]
	if ok && f.faultOver > 0 && cnt > f.faultOver {
		fault = "Stack-Heap Collision"
	}
	mem := f.mem(cnt, pad)
	o.ran = true
	name := o.obj.Name
	f.mu.Unlock()

	if fault != "" {
		// A fault is two messages: the header naming the script, then one
		// line with the reason.  Both are the simulator commenting on the
		// script rather than the script speaking, so both are on the debug
		// channel.
		f.say(id, sl.ChatDebug, name+" [script:"+scriptName+"] Script run-time error")
		f.say(id, sl.ChatDebug, fault)
		return
	}
	if !ok {
		// A script with no harness call in it says nothing this program
		// can read, which is what a probe of something else looks like.
		f.say(id, sl.ChatSay, "DONE")
		return
	}

	// One number, which is the whole of what a benchmark script says: the
	// arithmetic that used to happen in LSL happens in the program now.
	f.say(id, sl.ChatSay, "")
	f.say(id, sl.ChatSay, fmt.Sprintf("RESULT:MEM=%d", mem))
	if f.commentary != "" {
		f.say(id, sl.ChatSay, "INFO:"+f.commentary)
	}
	if silent {
		return
	}
	f.say(id, sl.ChatSay, "DONE")
}

// say is an object speaking, which is the only way a script says
// anything: chat carries the object's name and key and no more.
func (f *fakeGrid) say(from msg.UUID, chatType uint8, text string) {
	m := &msg.ChatFromSimulator{}
	m.ChatData.SourceID = from
	m.ChatData.OwnerID = testMe
	m.ChatData.FromName = append([]byte("a prim"), 0)
	m.ChatData.SourceType = sl.SourceObject
	m.ChatData.ChatType = chatType
	m.ChatData.Message = append([]byte(text), 0)
	f.relay(m)
}

// relay hands a message to the session as though the grid had sent it.
//
// It gives up rather than failing the test, because everything here
// happens on a goroutine of the fake's own and a failure from one of
// those is a panic instead of a failure.  What a message nobody read
// looks like from the test's side is a run that timed out, which says
// the same thing and says it where it can be read.
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

// ---------------------------------------------------- what an object holds

// inventoryFile is the simulator's format for what a prim contains: one
// entry for the benchmark script and one for the script the compile
// check installs, so that neither has to be created -- creating one costs
// six seconds of waiting for the object to admit it is there, and it is
// not what any of this is about.
func (f *fakeGrid) inventoryFile(o *fakeObject) string {
	var b strings.Builder
	for i, name := range []string{scriptName, scriptName + "-compile"} {
		id := o.item
		if i > 0 {
			// A second item, distinct from the first: the compile check
			// installs under its own name so that asking whether something
			// compiles cannot replace the script a measurement is using.
			id = msg.MustParseUUID(fmt.Sprintf("d22b7e57-7e57-c0de-0e4e-%012d", o.obj.Local))
		}
		fmt.Fprintf(&b, "\tinv_item\t0\n\t{\n\t\titem_id\t%s\n\t\tname\t%s|\n"+
			"\t\ttype\tlsltext\n\t\tinv_type\tlsl\n\t}\n", id, name)
	}
	return b.String()
}

// answer plays the simulator's side of a message the session sent.  It
// runs on its own goroutine because the answer goes back through the
// reader, and a backend that answered inside Send would be waiting for
// the reader that is waiting for it.
func (f *fakeGrid) answer(m msg.Message) {
	switch v := m.(type) {
	case *msg.SetScriptRunning:
		// Remembered, so that the question below is answered as a
		// simulator would answer it.
		f.mu.Lock()
		if f.scripts == nil {
			f.scripts = map[msg.UUID]bool{}
		}
		f.scripts[v.Script.ItemID] = v.Script.Running
		f.mu.Unlock()

	case *msg.GetScriptRunning:
		// Every run asks whether the script an earlier run left is still
		// running before it starts listening, and a region answers.
		f.mu.Lock()
		running := f.scripts[v.Script.ItemID]
		f.mu.Unlock()
		r := &msg.ScriptRunningReply{}
		r.Script.ObjectID, r.Script.ItemID, r.Script.Running = v.Script.ObjectID, v.Script.ItemID, running
		f.relay(r)

	case *msg.RequestTaskInventory:
		f.mu.Lock()
		o := f.objects[v.InventoryData.LocalID]
		f.mu.Unlock()
		if o == nil {
			return
		}
		r := &msg.ReplyTaskInventory{}
		r.InventoryData.TaskID = o.obj.ID
		r.InventoryData.Serial = 1
		r.InventoryData.Filename = append([]byte("inventory_"+o.obj.ID.String()+".tmp"), 0)
		f.relay(r)

	case *msg.RequestXfer:
		name := strings.TrimRight(string(v.XferID.Filename), "\x00")
		f.mu.Lock()
		var body string
		for _, o := range f.objects {
			if strings.Contains(name, o.obj.ID.String()) {
				body = f.inventoryFile(o)
			}
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
	var out []*sl.Seen
	for _, o := range f.objects {
		if named != "" && o.obj.Name != named {
			continue
		}
		if id != "" && !strings.EqualFold(o.obj.ID.String(), id) {
			continue
		}
		out = append(out, &sl.Seen{Object: o.obj})
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
