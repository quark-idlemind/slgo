package sl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// parcelBody is a ParcelProperties event as the grid sends one, with
// the sequence id under test.
func parcelBody(name string, local, seq int) string {
	return fmt.Sprintf(`<llsd><map>
	  <key>ParcelData</key><array><map>
	    <key>Name</key><string>%s</string>
	    <key>LocalID</key><integer>%d</integer>
	    <key>SequenceID</key><integer>%d</integer>
	    <key>Area</key><integer>2048</integer>
	  </map></array>
	</map></llsd>`, name, local, seq)
}

// answerParcel plays the simulator: it waits for the request, reads the
// sequence id off it, and sends the answer back carrying that id.
func answerParcel(t *testing.T, f *fakeBackend, name string, local int) {
	t.Helper()
	f.onSend = func(m msg.Message) {
		var seq int32
		switch r := m.(type) {
		case *msg.ParcelPropertiesRequest:
			seq = r.ParcelData.SequenceID
		case *msg.ParcelPropertiesRequestByID:
			seq = r.ParcelData.SequenceID
		default:
			return
		}
		// Answered from inside Send, on the caller's goroutine: the
		// waiter was registered before the request went out, and the
		// reader that delivers it is a goroutine of its own.
		f.RelayEvent(t, "ParcelProperties", parcelBody(name, local, int(seq)))
	}
}

// TestParcelAtAsksAndIsAnswered: the ask is the whole of what stage 0 of
// doc/history/parcel.md said was impossible, and the sequence id is what
// pairs the answer with the question.
func TestParcelAtAsksAndIsAnswered(t *testing.T) {
	w, f := newFakeSession(t)
	answerParcel(t, f, "Thrushmoor", 5)

	p, err := w.ParcelAt(context.Background(), 28, 72, 2*time.Second)
	if err != nil {
		t.Fatalf("ParcelAt: %v", err)
	}
	if p.Name != "Thrushmoor" || p.LocalID != 5 {
		t.Errorf("parcel = %q local %d", p.Name, p.LocalID)
	}

	var r *msg.ParcelPropertiesRequest
	for _, s := range f.Sent() {
		if q, ok := s.Msg.(*msg.ParcelPropertiesRequest); ok {
			r = q
		}
	}
	if r == nil {
		t.Fatal("no ParcelPropertiesRequest was sent")
	}
	// Snapped to the 4 metre grid the overlay is drawn on: a rectangle
	// inside one square asks about one parcel.
	if r.ParcelData.West != 28 || r.ParcelData.South != 72 ||
		r.ParcelData.East != 32 || r.ParcelData.North != 76 {
		t.Errorf("asked about %v,%v to %v,%v", r.ParcelData.West, r.ParcelData.South,
			r.ParcelData.East, r.ParcelData.North)
	}
	if r.ParcelData.SequenceID >= 0 {
		t.Errorf("asked with sequence %d: a non-negative id is a push and its answer could not be told from one",
			r.ParcelData.SequenceID)
	}
	if r.AgentData.AgentID != testAgentID || r.AgentData.SessionID != testSessionID {
		t.Error("the request did not carry this session's ids")
	}
}

// TestTwoAsksDoNotTakeEachOthersAnswers: the answers arrive on the
// event queue, where nothing but the sequence id says which question
// each belongs to.
func TestTwoAsksDoNotTakeEachOthersAnswers(t *testing.T) {
	w, f := newFakeSession(t)

	// Both requests are held until both have been sent, and then
	// answered in the wrong order.
	//
	// The sequence ids come back on a channel rather than into a slice.
	// The hook is called from the two goroutines below, at the same
	// time, which is the whole point of the test -- so a slice appended
	// to from inside it is written from two places at once, and read
	// from a third.  It was, and "go test ./sl/ -race" failed on it
	// every run.  A channel is the synchronisation as well as the
	// carrier: sending happens before receiving, so the ids are safe to
	// read by the time they are read.
	//
	// Buffered past the two expected, because the hook runs on the
	// sender's own goroutine: a third request -- which would be a fault
	// in the code under test -- must fail this test rather than wedge
	// it.
	seqs := make(chan int32, 8)
	f.onSend = func(m msg.Message) {
		if r, ok := m.(*msg.ParcelPropertiesRequest); ok {
			seqs <- r.ParcelData.SequenceID
		}
	}

	type got struct {
		p   *agent.Parcel
		err error
	}
	first := make(chan got, 1)
	go func() {
		p, err := w.ParcelAt(context.Background(), 28, 72, 5*time.Second)
		first <- got{p, err}
	}()
	// The second is sent from here so that both are outstanding at
	// once, which is the case this is about.
	second := make(chan got, 1)
	go func() {
		<-time.After(50 * time.Millisecond)
		p, err := w.ParcelAt(context.Background(), 200, 200, 5*time.Second)
		second <- got{p, err}
	}()

	// In the order they were sent, which is the order they were asked
	// in: the second ask waits 50ms so that it is second.
	var asked [2]int32
	for i := range asked {
		select {
		case asked[i] = <-seqs:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of the two requests were sent", i)
		}
	}

	f.RelayEvent(t, "ParcelProperties", parcelBody("Quill Lodge", 9, int(asked[1])))
	f.RelayEvent(t, "ParcelProperties", parcelBody("Thrushmoor", 5, int(asked[0])))

	a, b := <-first, <-second
	if a.err != nil || a.p.Name != "Thrushmoor" {
		t.Errorf("the first ask got %+v %v", a.p, a.err)
	}
	if b.err != nil || b.p.Name != "Quill Lodge" {
		t.Errorf("the second ask got %+v %v", b.p, b.err)
	}
}

// TestAPushDoesNotAnswerAnAsk: a push arrives while an ask is
// outstanding -- an avatar walking over a boundary is enough -- and
// answering with it would describe the wrong parcel with every
// appearance of being the answer.
func TestAPushDoesNotAnswerAnAsk(t *testing.T) {
	w, f := newFakeSession(t)

	sent := make(chan struct{})
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.ParcelPropertiesRequest); ok {
			close(sent)
		}
	}

	done := make(chan error, 1)
	go func() {
		_, err := w.ParcelAt(context.Background(), 28, 72, 400*time.Millisecond)
		done <- err
	}()

	select {
	case <-sent:
	case <-time.After(5 * time.Second):
		t.Fatal("the request was never sent")
	}
	// Sequence 4: a push, of the kind an arrival produces.
	f.RelayEvent(t, "ParcelProperties", parcelBody("Somewhere else", 7, 4))

	err := <-done
	if err == nil {
		t.Fatal("a push was taken as the answer to an ask")
	}
	if !strings.Contains(err.Error(), "no answer") {
		t.Errorf("error = %v, want the timeout", err)
	}
}

// TestParcelByIDAsksByLocalID: the local id is what a push and an
// overlay name a parcel by, so it has to be answerable on its own.
func TestParcelByIDAsksByLocalID(t *testing.T) {
	w, f := newFakeSession(t)
	answerParcel(t, f, "OMEN", 6)

	p, err := w.ParcelByID(context.Background(), 6, 2*time.Second)
	if err != nil {
		t.Fatalf("ParcelByID: %v", err)
	}
	if p.LocalID != 6 || p.Name != "OMEN" {
		t.Errorf("parcel = %q local %d", p.Name, p.LocalID)
	}
	var asked int32
	for _, s := range f.Sent() {
		if q, ok := s.Msg.(*msg.ParcelPropertiesRequestByID); ok {
			asked = q.ParcelData.LocalID
		}
	}
	if asked != 6 {
		t.Errorf("asked about local id %d, want 6", asked)
	}
}

// TestDwellCarriesBothNamingsOfAParcel: the local id is the region's
// and the uuid is the grid's, and this reply is the only message that
// says them in the same breath.
func TestDwellCarriesBothNamingsOfAParcel(t *testing.T) {
	w, f := newFakeSession(t)

	id := msg.MustParseUUID("3c8f7e57-7e57-c0de-5937-2df1a39c07cc")
	f.onSend = func(m msg.Message) {
		if _, ok := m.(*msg.ParcelDwellRequest); !ok {
			return
		}
		reply := &msg.ParcelDwellReply{}
		reply.Data.LocalID = 5
		reply.Data.ParcelID = id
		reply.Data.Dwell = 4328
		f.Relay(t, reply)
	}

	dwell, got, err := w.Dwell(context.Background(), 5, 2*time.Second)
	if err != nil {
		t.Fatalf("Dwell: %v", err)
	}
	if dwell != 4328 || got != id {
		t.Errorf("dwell %v for %v", dwell, got)
	}
}

// TestParcelIDSendsTheTwoFieldsTheCapabilityAnswers: adding
// region_handle turns the answer into a 404, and the failure is silent
// enough that it cost half an hour once.  See doc/history/parcel.md.
func TestParcelIDSendsTheTwoFieldsTheCapabilityAnswers(t *testing.T) {
	w, f := newFakeSession(t)

	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		b := make([]byte, r.ContentLength)
		r.Body.Read(b)
		body = string(b)
		if strings.Contains(body, "region_handle") {
			http.Error(rw, "not found", http.StatusNotFound)
			return
		}
		fmt.Fprint(rw, `<llsd><map><key>parcel_id</key>`+
			`<uuid>3c8f7e57-7e57-c0de-5937-2df1a39c07cc</uuid></map></llsd>`)
	}))
	defer srv.Close()
	f.caps["RemoteParcelRequest"] = srv.URL

	id, err := w.ParcelID(context.Background(), 28, 72, 2001)
	if err != nil {
		t.Fatalf("ParcelID: %v", err)
	}
	if want := msg.MustParseUUID("3c8f7e57-7e57-c0de-5937-2df1a39c07cc"); id != want {
		t.Errorf("parcel id = %v", id)
	}
	if !strings.Contains(body, "region_id") || !strings.Contains(body, "location") {
		t.Errorf("the request was %s", body)
	}
	if strings.Contains(body, "region_handle") {
		t.Error("the request carried region_handle, which the capability answers with a 404")
	}
}

// TestLandIsWhatTheSessionWasTold: the overlay cannot be asked for
// twice, so a client that attached after the avatar arrived can only
// get it from the session that was there.
func TestLandIsWhatTheSessionWasTold(t *testing.T) {
	w, f := newFakeSession(t)

	squares := make([]byte, agent.OverlaySquares)
	for i := range squares[:1024] {
		squares[i] = agent.OverlayGroup
	}
	f.land = &Land{
		Told:    &Told{Name: "Thrushmoor", LocalID: 5},
		Overlay: agent.OverlayFrom(squares, 0b0001),
	}

	l, err := w.Land(context.Background())
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if l.Told == nil || l.Told.Name != "Thrushmoor" || l.Told.LocalID != 5 {
		t.Errorf("told %+v", l.Told)
	}
	if l.Overlay.Complete() {
		t.Error("one packet of four is not a complete overlay")
	}
	if got, ok := l.Overlay.At(10, 10); !ok || got != agent.OverlayGroup {
		t.Errorf("square = %#02x ok %v", got, ok)
	}
	if _, ok := l.Overlay.At(10, 200); ok {
		t.Error("a square from a packet that never arrived answered")
	}
}
