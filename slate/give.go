package slate

// expect give: the offer an object made, the accept the runner sends, and
// the new inventory item that proves it arrived.
// Why: doc/slate-runner.md#give

import (
	"context"
	"fmt"
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

// giveRun is one give expectation of a step.
type giveRun struct {
	from    *binding
	name    func(string) bool
	text    textMatch // name, with the groups it binds
	item    string    // the name in the offer that was accepted
	armHeld int       // items of that name at the arm point

	sent    bool
	im      *sl.IM
	lastInv time.Time
	count   int // items of that name at the last look; -1 before one
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
	gr := &giveRun{from: b, name: match.match, text: match, count: -1}
	if s.obs.gives == nil {
		s.obs.gives = map[*expState]*giveRun{}
	}
	s.obs.gives[x] = gr
	x.match = never
	x.eval = func(ctx context.Context) error { return s.evalGive(ctx, x, gr) }
	x.noteFn = func() string {
		if !gr.sent || gr.count != gr.armHeld {
			return ""
		}
		return fmt.Sprintf("; accept was sent and inventory still has %d %s of that name", gr.count, plural(gr.count, "item", "items"))
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
		if x.e.Give != nil && !x.neg {
			want = true
		}
	}
	if !want {
		return nil
	}
	items, err := s.r.items(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.sent = "not sent: " + err.Error()
		s.fail(1, "%v", err)
		return nil
	}
	s.obs.snap = items
	for x, g := range s.obs.gives {
		if x.neg {
			continue
		}
		for _, name := range items {
			if g.name(name) {
				g.armHeld++
			}
		}
	}
	return nil
}

// items is every item in the inventory, by id, with its name. Folders are
// not items, and neither is a link, which an accept never makes. The read
// is a whole fetch.
func (r *runner) items(ctx context.Context) (map[msg.UUID]string, error) {
	inv, err := r.sess.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	// A fetch whose context ended mid-walk returns what it had, with no
	// error, and what it had is not the inventory.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := map[msg.UUID]string{}
	inv.Walk(func(f *agent.Folder, _ int) bool {
		for _, it := range inv.Contents(f.ID) {
			if !it.IsLink {
				out[it.ID] = it.Name
			}
		}
		return true
	})
	return out, nil
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
	items, err := s.r.items(callCtx)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A read that failed is tried again on the next look.
		return nil
	}
	var fresh []msg.UUID
	g.count = 0
	for id, name := range items {
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
		s.why = fmt.Sprintf("ambiguous: two new items match: %s (%q) and %s (%q)", fresh[0], items[fresh[0]], fresh[1], items[fresh[1]])
		s.fail(1, "ambiguous")
	case len(fresh) == 1:
		x.matched = true
		x.ev = &event{kind: evAccept, at: time.Now(), text: fmt.Sprintf("new item %s %q", fresh[0], items[fresh[0]])}
		v := capValue{typ: CapText, text: g.item}
		s.bindMatched(x, &v, groupSrc{g.text, g.item})
	}
	return nil
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

// offers are the object offers heard since the arm point, not yet taken,
// whose giver is a prim of the binding's linkset and whose item matches.
func (s *stepRun) offers(g *giveRun) []*event {
	var out []*event
	for _, ev := range s.r.log.events[s.t.mark:] {
		if ev.kind != evIM || !ev.eligible(s.arm) || ev.im.Dialog != sl.DialogTaskInventoryOffered {
			continue
		}
		item, ok := offerItemName(ev.im.Text)
		if ok && g.from.named(ev.im.FromName) && g.name(item) {
			out = append(out, ev)
		}
	}
	return out
}

// acceptOffer sends the accept of the one offer that matched, into the
// folder that kind of item is delivered to.
func (s *stepRun) acceptOffer(ctx context.Context, x *expState, g *giveRun, ev *event) error {
	im := ev.im
	if len(im.Bucket) == 0 {
		s.why = "the offer carries no asset type, so the folder it goes to is not known"
		s.fail(1, "no asset type")
		return nil
	}
	name, ok := giveFolders[im.Bucket[0]]
	if !ok {
		s.why = fmt.Sprintf("the offer is of asset type %d, whose default folder is not known; only a script's has been measured", im.Bucket[0])
		s.fail(1, "unknown asset type")
		return nil
	}
	callCtx, cancel := s.callCtx(ctx, s.r.cfg.props)
	defer cancel()
	folder, err := s.r.sess.Folder(callCtx, name)
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

// giveText is the transcript line of an offer heard.
func (r *runner) giveText(im *sl.IM) string {
	item, ok := offerItemName(im.Text)
	if !ok {
		item = im.Text
	}
	who := r.whoPrim(msg.UUID{}, func(n string) bool { return n != "" && n == im.FromName })
	if who == "" {
		who = sl.SenderObject.Label(im.FromName)
	}
	return fmt.Sprintf("give from %s: %q", who, item)
}
