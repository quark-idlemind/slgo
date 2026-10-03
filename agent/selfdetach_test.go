package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quark-idlemind/slgo/msg"
)

// outfitServer is an AIS that holds a root, a Current Outfit folder and
// the links in it, and records every DELETE.
type outfitServer struct {
	mu      sync.Mutex
	cof     string
	links   map[string]string // link id -> the item it links to
	deleted []string
	gone    bool // answer every DELETE 404, as when someone else got there first
	asked   int
}

func (s *outfitServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.asked++
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if r.Method == http.MethodDelete {
			id := parts[len(parts)-1] // /cap/x/item/<id>
			s.deleted = append(s.deleted, id)
			if s.gone {
				http.Error(w, "gone", http.StatusNotFound)
				return
			}
			delete(s.links, id)
			return
		}
		id := parts[len(parts)-2] // /cap/x/category/<id>/children
		var b strings.Builder
		b.WriteString(`<llsd><map><key>_embedded</key><map>`)
		if id == uid(0) {
			fmt.Fprintf(&b, `<key>categories</key><map><key>%s</key><map>
			  <key>category_id</key><string>%s</string><key>parent_id</key><string>%s</string>
			  <key>name</key><string>Current Outfit</string><key>type_default</key><integer>46</integer>
			</map></map><key>items</key><map/><key>links</key><map/>`, s.cof, s.cof, uid(0))
		} else {
			b.WriteString(`<key>categories</key><map/><key>items</key><map/><key>links</key><map>`)
			for l, item := range s.links {
				fmt.Fprintf(&b, `<key>%s</key><map><key>item_id</key><string>%s</string>
				  <key>parent_id</key><string>%s</string><key>name</key><string>a link</string>
				  <key>type</key><integer>24</integer><key>linked_id</key><string>%s</string></map>`, l, l, s.cof, item)
			}
			b.WriteString(`</map>`)
		}
		b.WriteString(`</map></map></llsd>`)
		fmt.Fprint(w, b.String())
	})
}

func (s *outfitServer) requests() (asked int, deleted []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asked, append([]string(nil), s.deleted...)
}

// outfitSaid is a log that can be waited on: the agent says one line
// for each save it deals with, whatever it does.
type outfitSaid struct {
	logLines
	ch chan string
}

func newOutfitSaid() *outfitSaid { return &outfitSaid{ch: make(chan string, 16)} }

func (o *outfitSaid) log(format string, v ...any) {
	o.logLines.log(format, v...)
	select {
	case o.ch <- fmt.Sprintf(format, v...):
	default:
	}
}

// savedRoundTrip is the longest a save takes to be dealt with against a
// loopback AIS (two reads and a delete), measured on a loaded machine:
// 12.8 ms at most over 2100 waits with two full test runs alongside
// (median 0.25 ms), rounded up to 50 ms for the race detector's
// overhead.  The test waits three times it.
const savedRoundTrip = 50 * time.Millisecond

func (o *outfitSaid) next(t *testing.T) string {
	t.Helper()
	select {
	case s := <-o.ch:
		return s
	case <-time.After(3 * savedRoundTrip):
		t.Fatal("the agent said nothing about the save")
		return ""
	}
}

// aSession is an offline session with an AIS behind it and a log.
func aSession(t *testing.T, s *outfitServer) (*Agent, *outfitSaid) {
	t.Helper()
	hs := httptest.NewServer(s.handler())
	t.Cleanup(hs.Close)
	a, _ := offlineSession(t)
	a.Inventory = newInventory(msg.MustParseUUID(uid(0)))
	a.SetCaps(Caps{InventoryCap: hs.URL + "/cap/x"})
	said := newOutfitSaid()
	a.opts.Log = said.log
	return a, said
}

func saved(item string) msg.Message {
	m := &msg.SaveAssetIntoInventory{}
	m.InventoryData.ItemID = msg.MustParseUUID(item)
	return m
}

// TestAnAttachmentThatTakesItselfOffLeavesTheOutfit: the region saying
// an item went back into inventory is what takes its links out of the
// Current Outfit folder, since the folder is what is worn at the next
// login.
// Why: doc/outfit.md#an-attachment-that-takes-itself-off
func TestAnAttachmentThatTakesItselfOffLeavesTheOutfit(t *testing.T) {
	item, other := uid(10), uid(11)

	t.Run("its link is deleted", func(t *testing.T) {
		s := &outfitServer{cof: uid(1), links: map[string]string{uid(20): item, uid(21): other}}
		a, said := aSession(t, s)
		feed(t, a, saved(item))
		line := said.next(t)
		if _, got := s.requests(); len(got) != 1 || got[0] != uid(20) {
			t.Errorf("deleted %v, want only %s; said %q", got, uid(20), line)
		}
		if !strings.Contains(line, "removed 1") || !strings.Contains(line, item) {
			t.Errorf("said %q", line)
		}
	})

	t.Run("two links to it both go", func(t *testing.T) {
		s := &outfitServer{cof: uid(1), links: map[string]string{uid(20): item, uid(22): item, uid(21): other}}
		a, said := aSession(t, s)
		feed(t, a, saved(item))
		line := said.next(t)
		if _, got := s.requests(); len(got) != 2 {
			t.Errorf("deleted %v, want both links; said %q", got, line)
		}
		if !strings.Contains(line, "removed 2") {
			t.Errorf("said %q", line)
		}
	})

	t.Run("an item the outfit does not link", func(t *testing.T) {
		s := &outfitServer{cof: uid(1), links: map[string]string{uid(21): other}}
		a, said := aSession(t, s)
		feed(t, a, saved(item))
		line := said.next(t)
		if _, got := s.requests(); len(got) != 0 {
			t.Errorf("deleted %v for an item with no link; said %q", got, line)
		}
		if !strings.Contains(line, "no link") {
			t.Errorf("said %q", line)
		}
	})

	t.Run("a link already gone is not an error", func(t *testing.T) {
		s := &outfitServer{cof: uid(1), links: map[string]string{uid(20): item}, gone: true}
		a, said := aSession(t, s)
		feed(t, a, saved(item))
		line := said.next(t)
		if _, got := s.requests(); len(got) != 1 {
			t.Errorf("deleted %v; said %q", got, line)
		}
		if strings.Contains(line, "failed") || !strings.Contains(line, "1 already gone") {
			t.Errorf("a 404 was reported as %q", line)
		}
	})

	t.Run("not while the item is described worn", func(t *testing.T) {
		s := &outfitServer{cof: uid(1), links: map[string]string{uid(20): item}}
		a, said := aSession(t, s)
		a.SetLook(Look{Far: 128})
		feed(t, a, arriving(t, msg.ObjectUpdate_ObjectData{
			ID:         11,
			FullID:     aPrim,
			PCode:      9,
			State:      0x32,
			ObjectData: placement(msg.Vector3{}, msg.Quaternion{}),
			NameValue:  []byte("AttachItemID STRING RW DS " + item + "\n\x00"),
		}))
		feed(t, a, saved(item))
		line := said.next(t)
		if asked, _ := s.requests(); asked != 0 {
			t.Errorf("%d requests for an item still worn; said %q", asked, line)
		}
	})

	t.Run("not once a logout has begun", func(t *testing.T) {
		s := &outfitServer{cof: uid(1), links: map[string]string{uid(20): item}}
		a, said := aSession(t, s)
		a.loggingOut.Store(true)
		feed(t, a, saved(item))
		line := said.next(t)
		if asked, _ := s.requests(); asked != 0 {
			t.Errorf("%d requests during a logout; said %q", asked, line)
		}
	})

	t.Run("not once the logout is answered", func(t *testing.T) {
		s := &outfitServer{cof: uid(1), links: map[string]string{uid(20): item}}
		a, said := aSession(t, s)
		a.loggedOut.fire()
		feed(t, a, saved(item))
		said.next(t)
		if asked, _ := s.requests(); asked != 0 {
			t.Errorf("%d requests after the logout reply", asked)
		}
	})
}
