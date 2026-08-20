package sl

// Who somebody is.
//
// Nothing that merely mentions a person carries their name.  An avatar
// standing in the region arrives as an object update with a uuid and
// nothing else; a friend coming online is a uuid; a permission request
// names its owner but a dialog names only the object.  So a name is
// always either something that came along with a message -- chat and
// instant messages both carry one -- or the answer to a question asked
// on purpose.
//
// Both go in the same place, and it is kept for the life of the
// session: names do change, but not within a session, and asking twice
// for something already answered is a round trip spent on nothing.

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// learn records a name that arrived with something else.
func (w *Session) learn(id msg.UUID, name string) {
	if id.IsZero() || name == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.names == nil {
		w.names = map[msg.UUID]string{}
	}
	w.names[id] = name
	delete(w.asking, id)
}

// Name is what somebody is called, if the session already knows.  It
// does not ask; see Names for that.
func (w *Session) Name(id msg.UUID) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.names[id]
}

// NameOr is Name with the id, shortened, standing in for an answer that
// has not arrived.
func (w *Session) NameOr(id msg.UUID) string {
	if n := w.Name(id); n != "" {
		return n
	}
	s := id.String()
	if len(s) > 8 {
		s = s[:8]
	}
	return "(" + s + ")"
}

// AskNames sends the question for any of these the session cannot
// already answer, and does not wait.
//
// Asking twice for the same id is a wasted round trip, so one that has
// been asked about is not asked about again until the answer arrives.
func (w *Session) AskNames(ctx context.Context, ids []msg.UUID) error {
	w.mu.Lock()
	if w.asking == nil {
		w.asking = map[msg.UUID]bool{}
	}
	var want []msg.UUID
	for _, id := range ids {
		if id.IsZero() || w.names[id] != "" || w.asking[id] {
			continue
		}
		w.asking[id] = true
		want = append(want, id)
	}
	w.mu.Unlock()

	if len(want) == 0 {
		return nil
	}
	m := &msg.UUIDNameRequest{}
	for _, id := range want {
		m.UUIDNameBlock = append(m.UUIDNameBlock, msg.UUIDNameRequest_UUIDNameBlock{ID: id})
	}
	return w.Send(ctx, m)
}

// Names asks about a list of people and waits for the answers, giving
// up at the timeout with whatever arrived.
//
// Giving up rather than failing is deliberate: a listing of thirty
// avatars should not be lost because the simulator will not name one of
// them, and the ones it did name are still worth showing.
func (w *Session) Names(ctx context.Context, ids []msg.UUID, timeout time.Duration) map[msg.UUID]string {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	w.AskNames(ctx, ids)

	deadline := time.Now().Add(timeout)
	for {
		out, missing := map[msg.UUID]string{}, false
		w.mu.Lock()
		for _, id := range ids {
			if n := w.names[id]; n != "" {
				out[id] = n
			} else if !id.IsZero() {
				missing = true
			}
		}
		w.mu.Unlock()
		if !missing || time.Now().After(deadline) {
			return out
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return out
		}
	}
}

// Find matches a name the way a person would type it: the whole thing,
// or the start of it, or the start of either half, ignoring case.
//
// Every match is returned, so an ambiguous one can be refused rather
// than guessed at.  It searches only what the session already knows;
// Lookup asks the grid.
func (w *Session) Find(want string) []msg.UUID {
	want = strings.TrimSpace(strings.ToLower(want))
	if want == "" {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	var exact, prefix []msg.UUID
	for id, name := range w.names {
		low := strings.ToLower(name)
		switch {
		case low == want:
			exact = append(exact, id)
		case strings.HasPrefix(low, want):
			prefix = append(prefix, id)
		default:
			if first, last, ok := strings.Cut(low, " "); ok {
				if strings.HasPrefix(first, want) || strings.HasPrefix(last, want) {
					prefix = append(prefix, id)
				}
			}
		}
	}
	if len(exact) > 0 {
		return exact
	}
	sort.Slice(prefix, func(i, j int) bool { return w.names[prefix[i]] < w.names[prefix[j]] })
	return prefix
}

// Person is somebody, with as much as is known about where they are.
type Person struct {
	ID   msg.UUID
	Name string

	// Distance is how far away they are, in metres, and Position is
	// where they are standing in the region's own metres.  Both are
	// zero when position is not the question: FriendList hands back
	// this same type for people who are not in the region at all, and a
	// zero here is "nobody said" rather than the corner of the region.
	//
	// The two are kept separately because they answer different
	// questions and one cannot be had from the other: a distance is
	// what "who" sorts and prints, and a position is what a picture of
	// the region needs, since 20 metres away says nothing about which
	// way.
	Distance float32
	Position msg.Vector3
}

// Nearby is who else is in the region, nearest first.
//
// The avatars come from the backend, which has been listening since
// before this program started: one already standing there was described
// once, to the session, and never again.  Their names are asked for.
func (w *Session) Nearby(ctx context.Context) ([]Person, error) {
	all, err := w.b.Objects(ctx, "", "")
	if err != nil {
		return nil, err
	}
	where, err := w.Where(ctx)
	if err != nil {
		return nil, err
	}

	var ids []msg.UUID
	var out []Person
	for _, o := range all {
		if o.PCode != pcodeAvatar || o.ID == w.me {
			continue
		}
		dx := o.Position.X - where.Position.X
		dy := o.Position.Y - where.Position.Y
		dz := o.Position.Z - where.Position.Z
		// The position is kept as well as the distance it was worked
		// out from.  It is the region's own metres rather than the
		// offset, so that a caller drawing the region and a caller
		// drawing the ground around this avatar both have what they
		// need; the offset is a subtraction away and the region
		// coordinate is not recoverable from a distance.
		out = append(out, Person{
			ID:       o.ID,
			Distance: sqrt(dx*dx + dy*dy + dz*dz),
			Position: o.Position,
		})
		ids = append(ids, o.ID)
	}

	w.Names(ctx, ids, 3*time.Second)
	for i := range out {
		out[i].Name = w.NameOr(out[i].ID)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Distance != out[j].Distance {
			return out[i].Distance < out[j].Distance
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Found is somebody a search turned up.
type Found struct {
	ID       msg.UUID
	Name     string // the legacy first-and-last name, which everything else here uses
	Display  string // what they call themselves, when it differs
	Username string // the login name, for telling two similar people apart
}

// PickerCap is the capability a name search goes through.
const PickerCap = "AvatarPickerSearch"

// Lookup searches the grid for people by part of a name.
//
// There are two ways to ask and they are not equivalent.  The UDP
// AvatarPickerRequest is still answered, but only ever matches a whole
// name: asking it for "Quark Idlemind" finds them and asking it for
// "quark" comes back with one row holding a zero uuid, which is its way
// of saying nothing matched.  Measured against the live grid.
//
// The AvatarPickerSearch capability is the one that searches, over
// display names as well, which is why the session asks for it at login.
// So: the capability when it is there, and the whole-name message when
// it is not, since an exact match beats a refusal.
func (w *Session) Lookup(ctx context.Context, want string) ([]Found, error) {
	want = strings.TrimSpace(want)
	if want == "" {
		return nil, fmt.Errorf("sl: nothing to look up")
	}

	var out []Found
	var err error
	if w.b.HasCap(PickerCap) {
		out, err = w.lookupByCap(ctx, want)
	} else {
		out, err = w.lookupByName(ctx, want)
	}
	if err != nil {
		return nil, err
	}

	for _, f := range out {
		w.learn(f.ID, f.Name)
	}
	// In name order, since a search answers in none.
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// LookupLimit is the page Lookup asks the search for, and so the most
// rows it can hand back.
//
// It is exported because a result of exactly this many is a page rather
// than an answer: one letter matches thousands of people and the reply
// says nothing about how many were left behind, so a caller showing the
// rows to somebody has to be able to tell a full page from a complete
// one.  Nothing here asks for the next page -- a person who typed too
// little of a name wants to type more of it, not to read a hundred more
// names -- which is why the number stays modest.
const LookupLimit = 100

func (w *Session) lookupByCap(ctx context.Context, want string) ([]Found, error) {
	// The viewer turns dots into spaces before asking, so that a
	// username typed as "first.last" searches as a name.
	query := strings.ReplaceAll(want, ".", " ")
	body, err := w.capDo(ctx, agent.CapRequest{
		Cap:    PickerCap,
		Method: "GET",
		Path: fmt.Sprintf("/?page_size=%d&names=%s",
			LookupLimit, url.QueryEscape(query)),
	})
	if err != nil {
		return nil, err
	}
	v, err := llsd.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("sl: the search answered with something unreadable: %w", err)
	}
	agents, _ := llsd.Map(v)["agents"].([]any)

	var out []Found
	for _, e := range agents {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		id, err := msg.ParseUUID(llsdString(m["id"]))
		if err != nil || id.IsZero() {
			continue
		}
		f := Found{
			ID:       id,
			Display:  llsdString(m["display_name"]),
			Username: llsdString(m["username"]),
		}
		f.Name = strings.TrimSpace(llsdString(m["legacy_first_name"]) + " " + llsdString(m["legacy_last_name"]))
		if f.Name == "" {
			f.Name = f.Username
		}
		out = append(out, f)
	}
	return out, nil
}

// lookupByName is the whole-name fallback.  One reply arrives per
// query; a row with a zero uuid is the simulator saying nothing
// matched.
func (w *Session) lookupByName(ctx context.Context, want string) ([]Found, error) {
	query := randomUUID()
	replies := make(chan []Found, 1)

	w.mu.Lock()
	if w.pickers == nil {
		w.pickers = map[msg.UUID]chan []Found{}
	}
	w.pickers[query] = replies
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		delete(w.pickers, query)
		w.mu.Unlock()
	}()

	m := &msg.AvatarPickerRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = w.agentBlock()
	m.AgentData.QueryID = query
	m.Data.Name = append([]byte(want), 0)
	if err := w.Send(ctx, m); err != nil {
		return nil, err
	}

	select {
	case out := <-replies:
		return out, nil
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("sl: the simulator did not answer the search")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// nameReply records the answers to UUIDNameRequest.
func (w *Session) nameReply(m *msg.UUIDNameReply) {
	for _, b := range m.UUIDNameBlock {
		w.learn(b.ID, strings.TrimSpace(trimNul(b.FirstName)+" "+trimNul(b.LastName)))
	}
}

// pickerReply routes one AvatarPickerReply to whoever asked.
func (w *Session) pickerReply(m *msg.AvatarPickerReply) {
	var out []Found
	for _, d := range m.Data {
		if d.AvatarID.IsZero() { // "nothing matched", not somebody
			continue
		}
		name := strings.TrimSpace(trimNul(d.FirstName) + " " + trimNul(d.LastName))
		out = append(out, Found{ID: d.AvatarID, Name: name})
		w.learn(d.AvatarID, name)
	}

	w.mu.Lock()
	ch := w.pickers[m.AgentData.QueryID]
	w.mu.Unlock()
	if ch != nil {
		select {
		case ch <- out:
		default:
		}
	}
}

func sqrt(f float32) float32 { return float32(math.Sqrt(float64(f))) }

func llsdString(v any) string {
	s, _ := v.(string)
	return s
}
