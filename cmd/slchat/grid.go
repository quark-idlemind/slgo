package main

// Everything that talks to Second Life.
//
// Open chat and instant messages are two unrelated messages that happen
// to both be conversation: ChatFromViewer carries a channel and is
// heard by whoever is near, ImprovedInstantMessage carries a person and
// a "dialog" that says what kind of thing it is.  Friendship rides on
// the second one -- an offer is an instant message with dialog 38 -- so
// the two arrive down the same pipe and are told apart here.

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/client"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/msg"
)

// subscriptions are the messages slchat needs relayed to it.  Anything
// not named here never arrives.
var subscriptions = []string{
	"ChatFromSimulator",
	"ImprovedInstantMessage",
	"UUIDNameReply",
	"AvatarPickerReply",
	"OnlineNotification",
	"OfflineNotification",
	"ChangeUserRights",
	"AlertMessage",
}

// Instant message dialogs, from the viewer's EInstantMessage.  These
// are the ones slchat acts on; the rest arrive and are ignored.
const (
	dialogMessage            = 0
	dialogMessageBox         = 1
	dialogBusyAutoResponse   = 19
	dialogSessionSend        = 17
	dialogFriendshipOffered  = 38
	dialogFriendshipAccepted = 39
	dialogFriendshipDeclined = 40
	dialogTypingStart        = 41
	dialogTypingStop         = 42
)

// chatTypeSay is ordinary speech, heard about twenty metres away.
const chatTypeSay = 1

// offer is a friendship offer somebody made us.
//
// The transaction is the id the offer arrived with, and accepting means
// sending it back: the simulator has no other way to tell which offer
// is being answered, and it is not derived from anything -- an offer
// nobody kept the id of can never be accepted.
type offer struct {
	From        msg.UUID
	Name        string
	Transaction msg.UUID
	At          time.Time
}

// sessionID is the id an instant message between two people carries.
//
// The viewer computes it rather than inventing it, so that both ends
// agree without having to be told: it is the two agent ids exclusive
// ored together.  Getting this wrong does not stop the message
// arriving, but it puts it in a session the other end thinks is new.
func sessionID(a, b msg.UUID) msg.UUID {
	var out msg.UUID
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}

func randomUUID() msg.UUID {
	var u msg.UUID
	rand.Read(u[:])
	return u
}

// nulTerm is a string as the protocol wants it: the length prefix says
// how long it is, and the content is still terminated, because that is
// what every viewer sends and some things on the far end read it that
// way.
func nulTerm(s string) []byte { return append([]byte(s), 0) }

func trimNul(b []byte) string { return strings.TrimRight(string(b), "\x00") }

// Say speaks on the region's open channel.
func (a *App) Say(ctx context.Context, text string) error {
	m := &msg.ChatFromViewer{}
	m.AgentData.AgentID, m.AgentData.SessionID = a.me, a.sess
	m.ChatData.Message = nulTerm(text)
	m.ChatData.Type = chatTypeSay
	m.ChatData.Channel = 0
	return a.conn.Send(ctx, m, true)
}

// SendIM sends an instant message to one person.
func (a *App) SendIM(ctx context.Context, to msg.UUID, text string) error {
	m := a.im(to, dialogMessage, text)
	m.MessageBlock.ID = sessionID(a.me, to)
	return a.conn.Send(ctx, m, true)
}

// im builds an instant message with the fields every kind of them
// needs.  Position and region are what the receiving end uses to offer
// a teleport to where it was sent from; neither is required, and
// neither is worth failing a message over, so a lookup that fails
// leaves them zero.
func (a *App) im(to msg.UUID, dialog uint8, text string) *msg.ImprovedInstantMessage {
	m := &msg.ImprovedInstantMessage{}
	m.AgentData.AgentID, m.AgentData.SessionID = a.me, a.sess
	m.MessageBlock.ToAgentID = to
	m.MessageBlock.Dialog = dialog
	m.MessageBlock.Offline = 0 // IM_ONLINE
	m.MessageBlock.FromAgentName = nulTerm(a.myName)
	m.MessageBlock.Message = nulTerm(text)
	m.MessageBlock.ID = randomUUID()

	a.mu.Lock()
	m.MessageBlock.RegionID = a.regionID
	m.MessageBlock.Position = a.position
	a.mu.Unlock()
	return m
}

// OfferFriendship asks somebody to be a friend.
//
// The offer is an instant message, and the id it carries is the
// transaction: whoever accepts sends that id back, so it is the only
// thread between the two halves.
func (a *App) OfferFriendship(ctx context.Context, to msg.UUID, text string) error {
	if text == "" {
		text = "Would you be my friend?"
	}
	m := a.im(to, dialogFriendshipOffered, text)
	return a.conn.Send(ctx, m, true)
}

// AcceptFriendship answers an offer.
//
// The folder is where the calling card would go.  A viewer sends its
// Calling Cards folder; slchat has not read its inventory and sends
// zero, which the simulator accepts -- the friendship is formed either
// way, and the calling card is the part that does not happen.
func (a *App) AcceptFriendship(ctx context.Context, o offer) error {
	m := &msg.AcceptFriendship{}
	m.AgentData.AgentID, m.AgentData.SessionID = a.me, a.sess
	m.TransactionBlock.TransactionID = o.Transaction
	m.FolderData = []msg.AcceptFriendship_FolderData{{}}
	return a.conn.Send(ctx, m, true)
}

// DeclineFriendship refuses an offer.  The simulator needs to hear it:
// an offer nobody answers stays pending on the other side.
func (a *App) DeclineFriendship(ctx context.Context, o offer) error {
	m := &msg.DeclineFriendship{}
	m.AgentData.AgentID, m.AgentData.SessionID = a.me, a.sess
	m.TransactionBlock.TransactionID = o.Transaction
	return a.conn.Send(ctx, m, true)
}

// AskNames asks the simulator who some ids are.
//
// Names are not carried by anything that merely mentions somebody: an
// avatar in the region arrives as an object update with a uuid and
// nothing else, so a list of who is nearby is a list of uuids until
// this has been asked and answered.
func (a *App) AskNames(ctx context.Context, ids []msg.UUID) error {
	ids = a.roster.Unknown(ids)
	if len(ids) == 0 {
		return nil
	}
	m := &msg.UUIDNameRequest{}
	for _, id := range ids {
		m.UUIDNameBlock = append(m.UUIDNameBlock, msg.UUIDNameRequest_UUIDNameBlock{ID: id})
	}
	return a.conn.Send(ctx, m, true)
}

// waitNames gives the replies a moment to arrive, so that a listing
// prints names rather than uuids.  It gives up rather than blocking:
// somebody the simulator will not name should not stop the list.
func (a *App) waitNames(ctx context.Context, ids []msg.UUID, d time.Duration) {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		missing := false
		for _, id := range ids {
			if a.roster.Name(id) == "" {
				missing = true
				break
			}
		}
		if !missing {
			return
		}
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return
		}
	}
}

// person is somebody in the region.
type person struct {
	ID       msg.UUID
	Name     string
	Distance float32
}

// Nearby is who else is in the region, nearest first.
//
// The avatars come from the server, which has been listening since
// before slchat started: an avatar that was already standing there when
// we attached was described once, to the session, and never again.
func (a *App) Nearby(ctx context.Context) ([]person, error) {
	resp, err := a.conn.Objects(ctx, "", "")
	if err != nil {
		return nil, err
	}
	where, err := a.conn.Presence(ctx, 0)
	if err != nil {
		return nil, err
	}
	me := msg.Vector3{}
	if p := where.Position; p != nil {
		me = msg.Vector3{X: p.X, Y: p.Y, Z: p.Z}
	}

	var ids []msg.UUID
	var out []person
	for _, o := range resp.Objects {
		if o.Pcode != pcodeAvatar {
			continue
		}
		id, err := msg.ParseUUID(o.Id)
		if err != nil || id == a.me {
			continue
		}
		p := person{ID: id}
		if o.Position != nil {
			dx, dy, dz := o.Position.X-me.X, o.Position.Y-me.Y, o.Position.Z-me.Z
			p.Distance = sqrt(dx*dx + dy*dy + dz*dz)
		}
		out = append(out, p)
		ids = append(ids, id)
	}

	a.AskNames(ctx, ids)
	a.waitNames(ctx, ids, 2*time.Second)
	for i := range out {
		out[i].Name = a.roster.NameOr(out[i].ID)
	}
	sortPeople(out)
	return out, nil
}

// pcodeAvatar is what the simulator calls an avatar in an object
// update; 9 is an ordinary prim.
const pcodeAvatar = 47

// Friends is the friend list, with the online ones named.
func (a *App) Friends(ctx context.Context) ([]person, []person, error) {
	fs, err := a.conn.Friends(ctx)
	if err != nil {
		return nil, nil, err
	}
	var online, offline []person
	var ids []msg.UUID
	for _, f := range fs {
		id, err := msg.ParseUUID(f.Id)
		if err != nil {
			continue
		}
		ids = append(ids, id)
		if f.Online {
			online = append(online, person{ID: id})
		} else {
			offline = append(offline, person{ID: id})
		}
	}
	// Only the online ones are worth a round trip for a name: the
	// rest are a count, and a hundred sleeping friends would be a
	// hundred names nobody asked to see.
	var want []msg.UUID
	for _, p := range online {
		want = append(want, p.ID)
	}
	a.AskNames(ctx, want)
	a.waitNames(ctx, want, 2*time.Second)
	for i := range online {
		online[i].Name = a.roster.NameOr(online[i].ID)
	}
	sortPeople(online)
	return online, offline, nil
}

// handle is one relayed message from the grid.
func (a *App) handle(raw *client.Message) {
	v, err := raw.Decode()
	if err != nil || v == nil {
		return
	}
	switch m := v.(type) {
	case *msg.ChatFromSimulator:
		a.heard(m)
	case *msg.ImprovedInstantMessage:
		a.instantMessage(m)
	case *msg.AvatarPickerReply:
		a.pickerReply(m)
	case *msg.UUIDNameReply:
		for _, b := range m.UUIDNameBlock {
			name := strings.TrimSpace(trimNul(b.FirstName) + " " + trimNul(b.LastName))
			a.roster.Learn(b.ID, name)
		}
	case *msg.OnlineNotification:
		for _, b := range m.AgentBlock {
			a.friendChanged(b.AgentID, true)
		}
	case *msg.OfflineNotification:
		for _, b := range m.AgentBlock {
			a.friendChanged(b.AgentID, false)
		}
	case *msg.AlertMessage:
		if s := trimNul(m.AlertData.Message); s != "" {
			a.notice("%s", s)
		}
	}
}

// heard is something said on a channel we can hear.
func (a *App) heard(m *msg.ChatFromSimulator) {
	c := m.ChatData
	text := trimNul(c.Message)
	if text == "" {
		return
	}
	from := trimNul(c.FromName)
	// Our own words come back from the simulator, and printing them
	// again would double every line we say.
	if c.SourceID == a.me {
		return
	}
	a.roster.Learn(c.SourceID, from)
	a.incoming("Local", from, text)
}

// instantMessage is one instant message, of whatever kind.
func (a *App) instantMessage(m *msg.ImprovedInstantMessage) {
	b := m.MessageBlock
	from := m.AgentData.AgentID
	name := trimNul(b.FromAgentName)
	text := trimNul(b.Message)
	a.roster.Learn(from, name)
	if name == "" {
		name = a.roster.NameOr(from)
	}

	switch b.Dialog {
	case dialogMessage, dialogMessageBox, dialogBusyAutoResponse:
		if b.FromGroup || from.IsZero() {
			// A group or the system talking; there is nobody to
			// answer, so it is a notice rather than a session.
			a.notice("%s: %s", name, text)
			return
		}
		s, made := a.sessions.Open(from, name)
		if made {
			a.notice("new instant message session with %s", name)
			a.refreshPrompt()
		}
		a.incoming("IM "+s.Label(), "", text)

	case dialogSessionSend:
		a.notice("group message from %s: %s", name, text)

	case dialogFriendshipOffered:
		a.mu.Lock()
		a.offers[from] = offer{From: from, Name: name, Transaction: b.ID, At: time.Now()}
		a.mu.Unlock()
		a.notice("%s offers friendship -- %s accept %s, or %s decline %s",
			name, KeyName(a.cfg.Prefix), firstWord(name),
			KeyName(a.cfg.Prefix), firstWord(name))

	case dialogFriendshipAccepted:
		// An OnlineNotification for the new friend usually follows
		// this within the second, but saying so here does not
		// depend on it arriving.
		a.conn.NoteFriend(context.Background(), from, true)
		a.notice("%s accepted your friendship offer", name)

	case dialogFriendshipDeclined:
		a.notice("%s declined your friendship offer", name)

	case dialogTypingStart, dialogTypingStop:
		// Not shown: it would be a line of noise per keystroke.

	default:
		a.notice("instant message from %s of a kind slchat does not know (dialog %d): %s",
			name, b.Dialog, text)
	}
}

// friendChanged is a friend coming or going.
//
// The burst of these arrives seconds after logging in, before anybody
// has had reason to ask who the ids belong to, so the name usually has
// to be fetched before there is anything worth printing.  That happens
// on its own goroutine: this runs on the relay, and a second spent
// waiting here is a second of chat not being shown.
func (a *App) friendChanged(id msg.UUID, online bool) {
	say := func(name string) {
		if online {
			a.notice("%s is online", name)
		} else {
			a.notice("%s went offline", name)
		}
	}
	if name := a.roster.Name(id); name != "" {
		say(name)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a.AskNames(ctx, []msg.UUID{id})
		a.waitNames(ctx, []msg.UUID{id}, 10*time.Second)
		say(a.roster.NameOr(id))
	}()
}

// track keeps the position and region an instant message carries.
func (a *App) track(ctx context.Context) {
	update := func() {
		if r, err := a.conn.Region(ctx); err == nil && r.Known {
			if id, err := msg.ParseUUID(r.Id); err == nil {
				a.mu.Lock()
				a.regionID = id
				a.mu.Unlock()
			}
		}
		if p, err := a.conn.Presence(ctx, 0); err == nil && p.Position != nil {
			a.mu.Lock()
			a.position = msg.Vector3{X: p.Position.X, Y: p.Position.Y, Z: p.Position.Z}
			a.mu.Unlock()
		}
	}
	update()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			update()
		case <-ctx.Done():
			return
		}
	}
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i > 0 {
		return s[:i]
	}
	return s
}

// sortPeople puts the nearest first, and settles ties by name so that a
// list where every distance is the same -- a friend list -- still comes
// out in a stable order.
func sortPeople(ps []person) {
	sort.Slice(ps, func(i, j int) bool {
		if ps[i].Distance != ps[j].Distance {
			return ps[i].Distance < ps[j].Distance
		}
		return ps[i].Name < ps[j].Name
	})
}

func sqrt(f float32) float32 { return float32(math.Sqrt(float64(f))) }

// found is somebody a search turned up.
type found struct {
	ID       msg.UUID
	Name     string // the legacy first-and-last name, which is what everything else here uses
	Display  string // what they call themselves, when it differs
	Username string // the login name, for telling two similar people apart
}

// Lookup searches for people by part of their name.
//
// There are two ways to ask and they are not equivalent. The UDP
// AvatarPickerRequest is still answered, but only ever matches a whole
// name: asking it for "Quark Idlemind" finds them and asking it for
// "quark" comes back with one row holding a zero uuid and no name,
// which is its way of saying nothing matched. Measured against the live
// grid, not assumed.
//
// The AvatarPickerSearch capability is the one that searches, over
// display names as well as login names, which is why the session asks
// for it at login. So: the capability when it is there, and the whole
// name message when it is not, since an exact match is better than a
// refusal.
func (a *App) Lookup(ctx context.Context, want string) ([]found, error) {
	var (
		out []found
		err error
	)
	if a.conn.HasCap(capAvatarPicker) {
		out, err = a.lookupByCap(ctx, want)
	} else {
		out, err = a.lookupByName(ctx, want)
	}
	sortFound(out)
	return out, err
}

// sortFound puts the results in the order they will be read in.
//
// What comes back is in whatever order the search felt like -- ninety
// five people called Quark arrived as Ling, Sabra, Blister, Yifu --
// which is no order at all to look a name up in.  Case is ignored,
// since "quark" and "QuArK" are the same name to anybody reading the
// list, and a tie is settled by the name as written so that two
// spellings of it never swap places between one search and the next.
func sortFound(fs []found) {
	sort.Slice(fs, func(i, j int) bool {
		li, lj := strings.ToLower(fs[i].Name), strings.ToLower(fs[j].Name)
		if li != lj {
			return li < lj
		}
		return fs[i].Name < fs[j].Name
	})
}

const capAvatarPicker = "AvatarPickerSearch"

func (a *App) lookupByCap(ctx context.Context, want string) ([]found, error) {
	// The viewer turns dots into spaces before asking, so that a
	// username typed as "first.last" searches as a name.
	query := strings.ReplaceAll(want, ".", " ")
	resp, err := a.conn.DoCap(ctx, agent.CapRequest{
		Cap:    capAvatarPicker,
		Method: "GET",
		Path:   "/?page_size=100&names=" + url.QueryEscape(query),
	})
	if err != nil {
		return nil, err
	}
	if !resp.OK() {
		return nil, fmt.Errorf("the search returned status %d", resp.Status)
	}
	v, err := llsd.Decode(bytes.NewReader(resp.Body))
	if err != nil {
		return nil, fmt.Errorf("the search answered with something unreadable: %w", err)
	}
	agents, _ := llsd.Map(v)["agents"].([]any)

	var out []found
	for _, e := range agents {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		id, err := msg.ParseUUID(llsdString(m["id"]))
		if err != nil || id.IsZero() {
			continue
		}
		f := found{
			ID:       id,
			Display:  llsdString(m["display_name"]),
			Username: llsdString(m["username"]),
		}
		f.Name = strings.TrimSpace(llsdString(m["legacy_first_name"]) + " " + llsdString(m["legacy_last_name"]))
		if f.Name == "" {
			// Some accounts have no legacy name; the username is
			// then the only thing to call them.
			f.Name = f.Username
		}
		out = append(out, f)
	}
	return out, nil
}

// lookupByName is the whole-name fallback.  One reply arrives per
// query; a row with a zero uuid is the simulator saying nothing
// matched.
func (a *App) lookupByName(ctx context.Context, want string) ([]found, error) {
	query := randomUUID()
	replies := make(chan []found, 1)

	a.mu.Lock()
	a.pickers[query] = replies
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pickers, query)
		a.mu.Unlock()
	}()

	m := &msg.AvatarPickerRequest{}
	m.AgentData.AgentID, m.AgentData.SessionID = a.me, a.sess
	m.AgentData.QueryID = query
	m.Data.Name = nulTerm(want)
	if err := a.conn.Send(ctx, m, true); err != nil {
		return nil, err
	}

	select {
	case out := <-replies:
		return out, nil
	case <-time.After(15 * time.Second):
		return nil, fmt.Errorf("the simulator did not answer the search")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// pickerReply routes one AvatarPickerReply to whoever asked.
func (a *App) pickerReply(m *msg.AvatarPickerReply) {
	var out []found
	for _, d := range m.Data {
		// The zero uuid row is "nothing matched", not somebody.
		if d.AvatarID.IsZero() {
			continue
		}
		name := strings.TrimSpace(trimNul(d.FirstName) + " " + trimNul(d.LastName))
		out = append(out, found{ID: d.AvatarID, Name: name})
		a.roster.Learn(d.AvatarID, name)
	}

	a.mu.Lock()
	ch := a.pickers[m.AgentData.QueryID]
	a.mu.Unlock()
	if ch != nil {
		select {
		case ch <- out:
		default:
		}
	}
}

func llsdString(v any) string {
	s, _ := v.(string)
	return s
}
