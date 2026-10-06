package slate

// expect give: the offer an object made, the accept the runner sends, and
// the new inventory item, or with `folder` the new folder, that proves it
// arrived.
// Why: doc/slate-runner.md#give

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// offerItemName reads the item's name out of the text of an object's
// offer; see sl.TaskOfferItemName.
func offerItemName(text string) (string, bool) { return sl.TaskOfferItemName(text) }

// giveFolders maps the asset type an offer carries, its one byte of
// bucket, to the standard folder that type is delivered into. Only a
// script, 10 and its bytecode twin, was measured: the Scripts folder took
// it. The others are the viewer's defaults for their kinds and have not
// been tried, and a type that is not here is refused rather than guessed.
// Why: doc/im-senders.md#an-objects-give
var giveFolders = map[uint8]string{
	0:  "Textures",
	1:  "Sounds",
	2:  "Calling Cards",
	3:  "Landmarks",
	5:  "Clothing",
	6:  "Objects",
	7:  "Notecards",
	10: "Scripts",
	11: "Scripts",
	13: "Body Parts",
	20: "Animations",
	21: "Gestures",
}

// giveFolderInto is where a folder offer goes: the root of inventory, as
// the viewer's own accept sends it. The offer's one byte is the asset type
// 8, AT_CATEGORY, and the viewer looks its folder up as the folder type of
// that number, FT_ROOT_INVENTORY, which findCategoryUUIDForType answers
// with the root.
// Why: doc/slate-runner.md#a-folder-given
const giveFolderInto = "My Inventory"

// giveRun is one give expectation of a step.
type giveRun struct {
	from    *binding
	name    func(string) bool
	text    textMatch // name, with the groups it binds
	item    string    // the name in the offer that was accepted
	armHeld int       // items (folders, for a folder) of that name at the arm point

	folder  bool
	holding []textMatch // the items a folder has to hold
	held    []string    // what the new folder held at the last look
	sawNew  bool        // a new folder of the name was there at the last look
	wrong   int         // offers of the right name and the wrong kind, heard

	sent    bool
	im      *sl.IM
	lastInv time.Time
	count   int // items (folders) of that name at the last look; -1 before one
	final   bool
}

// giveExpect makes a give expectation. A give names no link: the offer
// does not say which prim sent it.
func (s *stepRun) giveExpect(x *expState) error {
	g := x.e.Give
	b := s.r.lookup(g.From.Text)
	if b == nil {
		return fmt.Errorf("%s is not an object", g.From.Text)
	}
	match, err := s.textMatch(g.Item)
	if err != nil {
		return err
	}
	if g.To != nil {
		return s.giveToSecond(x, b, match, g.To.Text)
	}
	gr := &giveRun{from: b, name: match.match, text: match, count: -1, folder: g.Folder}
	for _, h := range g.Holding {
		hm, err := s.textMatch(h)
		if err != nil {
			return err
		}
		gr.holding = append(gr.holding, hm)
	}
	if s.obs.gives == nil {
		s.obs.gives = map[*expState]*giveRun{}
	}
	s.obs.gives[x] = gr
	x.match = never
	x.eval = func(ctx context.Context) error { return s.evalGive(ctx, x, gr) }
	x.noteFn = gr.note
	return nil
}

// note is what the unmatched line says after the expectation: the offer of
// the wrong kind that was heard, what a new folder held that did not hold
// what was asked, or that the accept brought nothing new.
func (g *giveRun) note() string {
	kind, plurals := "item", "items"
	if g.folder {
		kind, plurals = "folder", "folders"
	}
	switch {
	case !g.sent && g.wrong > 0 && g.folder:
		return "; the offer of that name is an item, and expect give without folder takes it"
	case !g.sent && g.wrong > 0:
		return "; the offer of that name is a folder, and expect give folder takes it"
	case g.sent && g.folder && g.sawNew:
		if len(g.held) == 0 {
			return "; the new folder holds nothing"
		}
		return "; the new folder holds " + quoteList(g.held)
	case g.sent && g.count == g.armHeld:
		return fmt.Sprintf("; accept was sent and inventory still has %d %s of that name", g.count, plural(g.count, kind, plurals))
	}
	return ""
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}

// giveToSecond makes a give expectation of an offer to a second avatar. The
// runner declines every such offer as it is heard (observeSecondIM), so
// the expectation only has to see it: there is no accept, no inventory to
// read, and the avatar is left as found.
// Why: doc/slate-language.md#a-second-avatar
func (s *stepRun) giveToSecond(x *expState, from *binding, item textMatch, to string) error {
	x.match = func(ev *event) bool {
		if ev.kind != evIM || ev.to != to || ev.im.Dialog != sl.DialogTaskInventoryOffered {
			return false
		}
		name, ok := offerItemName(ev.im.Text)
		return ok && from.named(ev.im.FromName) && item.match(name)
	}
	if !x.neg {
		x.onMatch = func(ev *event) {
			name, _ := offerItemName(ev.im.Text)
			v := capValue{typ: CapText, text: name}
			s.bindMatched(x, &v, groupSrc{item, name})
		}
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// snapshot is the arming snapshot: the readings and the root list are
// made as it begins, and, for a step that expects a give, the ids of
// the items in the inventory. The fetch is a whole AIS read, so it is made
// only then, and a `then` step takes it when it begins: a give that
// finished since the arm point is already in it.
// Why: doc/slate-runner.md#arming-snapshot
func (s *stepRun) snapshot(ctx context.Context) error {
	if err := s.t.watch.poll(ctx, true); err != nil {
		return err
	}
	want := false
	for _, x := range s.exps {
		if x.e.Give != nil && x.e.Give.To == nil && !x.neg {
			want = true
		}
	}
	if !want {
		return nil
	}
	view, err := s.r.look(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.sent = "not sent: " + err.Error()
		s.fail(1, "%v", err)
		return nil
	}
	s.obs.snap = view.items
	s.obs.folders = map[msg.UUID]string{}
	for id, f := range view.folders {
		s.obs.folders[id] = f.name
	}
	for x, g := range s.obs.gives {
		if x.neg {
			continue
		}
		g.armHeld = view.count(g)
	}
	return nil
}

// count is how many of what the expectation looks for, items or folders,
// carry its name.
func (v *invView) count(g *giveRun) int {
	n := 0
	if g.folder {
		for _, f := range v.folders {
			if g.name(f.name) {
				n++
			}
		}
		return n
	}
	for _, name := range v.items {
		if g.name(name) {
			n++
		}
	}
	return n
}

// invView is the inventory as a look found it: every item by id with its
// name, and every folder below the root by id with its name and the names
// of the items directly in it.
type invView struct {
	items   map[msg.UUID]string
	folders map[msg.UUID]*folderView
}

type folderView struct {
	name string
	held []string
}

// look reads the inventory: a whole fetch. Links are neither items nor
// held, since an accept never makes one.
func (r *runner) look(ctx context.Context) (*invView, error) {
	inv, err := r.sess.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	// A fetch whose context ended mid-walk returns what it had, with no
	// error, and what it had is not the inventory.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	v := &invView{items: map[msg.UUID]string{}, folders: map[msg.UUID]*folderView{}}
	inv.Walk(func(f *agent.Folder, _ int) bool {
		fv := &folderView{name: f.Name}
		for _, it := range inv.Contents(f.ID) {
			if !it.IsLink {
				v.items[it.ID] = it.Name
				fv.held = append(fv.held, it.Name)
			}
		}
		sort.Strings(fv.held)
		v.folders[f.ID] = fv
		return true
	})
	return v, nil
}

// evalGive takes in the offers heard, accepts the one that matches, and
// looks for the new item once the accept is out.
func (s *stepRun) evalGive(ctx context.Context, x *expState, g *giveRun) error {
	if x.neg {
		// Silence is the pass: an offer that matches is the forbidden event.
		for _, ev := range s.offers(g) {
			x.ev, x.forbidden = ev, true
			return nil
		}
		return nil
	}
	offers := s.offers(g)
	switch {
	case len(offers) > 1 || g.sent && len(offers) > 0:
		first := offers
		var named []string
		if g.im != nil {
			named = append(named, offerLabel(g.im))
		}
		for _, ev := range first {
			named = append(named, offerLabel(ev.im))
		}
		s.why = "ambiguous: two offers match: " + strings.Join(named, " and ")
		s.fail(1, "ambiguous")
		return nil
	case len(offers) == 1:
		if err := s.acceptOffer(ctx, x, g, offers[0]); err != nil || s.state == stFailed {
			return err
		}
	}
	if !g.sent {
		return nil
	}
	now := time.Now()
	last := !now.Before(x.limit) && !g.final
	if !last && now.Sub(g.lastInv) < s.r.cfg.invEvery() {
		return nil
	}
	g.final = g.final || last
	g.lastInv = now
	// The look that decides the count of the unmatched line is made when the
	// deadline is reached, so it has the cap and not the time left.
	var callCtx context.Context
	var cancel context.CancelFunc
	if last {
		callCtx, cancel = context.WithTimeout(ctx, s.r.cfg.props)
	} else {
		callCtx, cancel = s.callCtx(ctx, s.r.cfg.props)
	}
	defer cancel()
	view, err := s.r.look(callCtx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A read that failed is tried again on the next look.
		return nil
	}
	if g.folder {
		s.evalFolder(x, g, view)
		return nil
	}
	var fresh []msg.UUID
	g.count = 0
	for id, name := range view.items {
		if !g.name(name) {
			continue
		}
		g.count++
		if _, old := s.obs.snap[id]; !old {
			fresh = append(fresh, id)
		}
	}
	switch {
	case len(fresh) > 1:
		sortUUIDs(fresh)
		s.why = fmt.Sprintf("ambiguous: two new items match: %s (%q) and %s (%q)", fresh[0], view.items[fresh[0]], fresh[1], view.items[fresh[1]])
		s.fail(1, "ambiguous")
	case len(fresh) == 1:
		x.matched = true
		x.ev = &event{kind: evAccept, at: time.Now(), text: fmt.Sprintf("new item %s %q", fresh[0], view.items[fresh[0]])}
		v := capValue{typ: CapText, text: g.item}
		s.bindMatched(x, &v, groupSrc{g.text, g.item})
	}
	return nil
}

// evalFolder decides a folder expectation from one look: a new folder of
// the name is there, and holds each item asked for. A folder that is there
// and does not yet hold them is looked at again, since the items of a
// folder may be listed after the folder is.
func (s *stepRun) evalFolder(x *expState, g *giveRun, view *invView) {
	var fresh []msg.UUID
	g.count = view.count(g)
	for id, f := range view.folders {
		if !g.name(f.name) {
			continue
		}
		if _, old := s.obs.folders[id]; !old {
			fresh = append(fresh, id)
		}
	}
	g.sawNew = len(fresh) > 0
	switch {
	case len(fresh) > 1:
		sortUUIDs(fresh)
		s.why = fmt.Sprintf("ambiguous: two new folders match: %s (%q) and %s (%q)", fresh[0], view.folders[fresh[0]].name, fresh[1], view.folders[fresh[1]].name)
		s.fail(1, "ambiguous")
	case len(fresh) == 1:
		f := view.folders[fresh[0]]
		g.held = f.held
		for _, h := range g.holding {
			if !slices.ContainsFunc(f.held, h.match) {
				return
			}
		}
		x.matched = true
		x.ev = &event{kind: evAccept, at: time.Now(), text: fmt.Sprintf("new folder %s %q", fresh[0], f.name)}
		v := capValue{typ: CapText, text: g.item}
		s.bindMatched(x, &v, groupSrc{g.text, g.item})
	}
}

func sortUUIDs(ids []msg.UUID) {
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j].String() < ids[j-1].String(); j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
}

func offerLabel(im *sl.IM) string {
	name, _ := offerItemName(im.Text)
	return fmt.Sprintf("%q from %s transaction %s", name, im.FromName, im.ID)
}

// isFolderOffer says the offer is of a folder: its one byte of bucket is
// the asset type, and a category's is 8.
func isFolderOffer(im *sl.IM) bool {
	return len(im.Bucket) > 0 && sl.AssetType(int8(im.Bucket[0])) == sl.AssetCategory
}

// offers are the object offers heard since the arm point, not yet taken,
// whose giver is a prim of the binding's linkset, whose name matches and
// whose kind, item or folder, is the expectation's. An offer of the other
// kind is counted in g.wrong, for the unmatched line, and is not taken.
func (s *stepRun) offers(g *giveRun) []*event {
	var out []*event
	g.wrong = 0
	for _, ev := range s.r.log.events[s.t.mark:] {
		if ev.kind != evIM || ev.to != "" || !ev.eligible(s.arm) || ev.im.Dialog != sl.DialogTaskInventoryOffered {
			continue
		}
		item, ok := offerItemName(ev.im.Text)
		if !ok || !g.from.named(ev.im.FromName) || !g.name(item) {
			continue
		}
		if isFolderOffer(ev.im) != g.folder {
			g.wrong++
			continue
		}
		out = append(out, ev)
	}
	return out
}

// acceptOffer sends the accept of the one offer that matched, into the
// folder that kind of item is delivered to, or for a folder offer the root
// of inventory.
func (s *stepRun) acceptOffer(ctx context.Context, x *expState, g *giveRun, ev *event) error {
	im := ev.im
	if len(im.Bucket) == 0 {
		s.why = "the offer carries no asset type, so the folder it goes to is not known"
		s.fail(1, "no asset type")
		return nil
	}
	name, ok := giveFolders[im.Bucket[0]]
	if g.folder {
		name, ok = giveFolderInto, true
	}
	if !ok {
		s.why = fmt.Sprintf("the offer is of asset type %d, whose default folder is not known; only a script's has been measured", im.Bucket[0])
		s.fail(1, "unknown asset type")
		return nil
	}
	callCtx, cancel := s.callCtx(ctx, s.r.cfg.props)
	defer cancel()
	var folder msg.UUID
	var err error
	if g.folder {
		folder = s.r.sess.InventoryRoot()
	} else {
		folder, err = s.r.sess.Folder(callCtx, name)
	}
	if err == nil {
		err = s.acceptGive(callCtx, im, folder)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.why = fmt.Sprintf("the accept was not sent: %v", err)
		s.fail(1, "accept failed")
		return nil
	}
	ev.consumed = true
	g.sent, g.im = true, im
	g.item, _ = offerItemName(im.Text)
	line := &event{kind: evAccept, at: time.Now(), consumed: true,
		text: fmt.Sprintf("give accept sent to %s transaction %s into %s", im.From, im.ID, name)}
	s.r.log.add(line)
	s.r.printEvent(line)
	return nil
}

// acceptGive answers the give through the session's InventoryOffer: the
// one it kept, so that it stops waiting, or one read from the message.
func (s *stepRun) acceptGive(ctx context.Context, im *sl.IM, folder msg.UUID) error {
	for _, o := range s.r.sess.InventoryOffers() {
		if o.Transaction == im.ID && o.From == im.From {
			return o.Accept(ctx, folder)
		}
	}
	o, ok := sl.InventoryOfferFrom(im)
	if !ok {
		return fmt.Errorf("the message is not an offer")
	}
	return s.r.sess.AcceptInventoryOffer(ctx, o, folder)
}

// giveText is the transcript line of an offer heard; to is the second
// avatar it came to, or "" for the tester.
func (r *runner) giveText(im *sl.IM, to string) string {
	item, ok := offerItemName(im.Text)
	if !ok {
		item = im.Text
	}
	kind := ""
	if isFolderOffer(im) {
		kind = "folder "
	}
	who := r.whoPrim(msg.UUID{}, func(n string) bool { return n != "" && n == im.FromName })
	if who == "" {
		who = sl.SenderObject.Label(im.FromName)
	}
	if to != "" {
		return fmt.Sprintf("give to %s from %s: %s%q", to, who, kind, item)
	}
	return fmt.Sprintf("give from %s: %s%q", who, kind, item)
}
