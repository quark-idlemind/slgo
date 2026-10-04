package session

// Where the auto objects sit, how many an avatar may wear, and the
// commands that change either.
//
// # The layout
//
// Every auto object is a small cube worn on HUD Bottom Left.  Idle, it
// is PARKED: off the screen, left of and below the corner.  `auto show`
// lays them out along the bottom edge for a person to look at.  Moving
// one is a MultipleObjectUpdate on a worn single-prim HUD, which needs
// no script (sl.Place).
// Why: doc/slots.md#one-point-twenty-four-objects
//
// # How many may be worn
//
// An avatar wears 38 attachments in all, HUD and body points together,
// and the region refuses the 39th without a word.  What an avatar wears
// is counted here as the union of two lists, because neither is whole:
// the objects linked in its Current Outfit folder, and the attachments
// the region has described.  After a login the region does not describe
// HUDs, so the second list misses them; and an object worn by Wear alone
// leaves no link, so the first list misses that.
// Why: doc/slots.md#counting-what-is-worn

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

// AutoSize is the edge of an auto object, in metres.
const AutoSize float32 = 0.05

// AutoParked is where an idle auto object sits, in the frame of the point
// it is worn on.  The HUD frame has +Y to the left and +Z up, so from
// Bottom Left the screen is at -Y and +Z; <0, 1, -1> is a metre left of
// the corner and a metre below it, which is off the screen, and well
// within the 3.5 m the region lets an attachment stray from its point.
var AutoParked = msg.Vector3{X: 0, Y: 1, Z: -1}

// AutoTile is where `auto show` puts slot i: in a row along the bottom
// of the screen, the first with its corner at the screen's corner and
// each next one a cube's width further in.  Twenty-four of them make a
// row 1.2 m long.
func AutoTile(slot int) msg.Vector3 {
	return msg.Vector3{
		X: 0,
		Y: -AutoSize/2 - AutoSize*float32(slot),
		Z: AutoSize / 2,
	}
}

// AutoReserve is how many of the avatar's attachment slots `auto -n`
// leaves free: room for the bridge a viewer wears when it attaches.
const AutoReserve = 1

// AutoLeaseReserve is how many attachment slots a run's lease leaves
// free when it has to put objects on that nobody asked for at that
// moment, so a lease stops at 34 worn in all.  It is larger than
// AutoReserve because it acts without anybody having said so: a person
// who types `auto -n 24` has chosen to fill the avatar, and a benchmark
// that merely wants twelve objects has not.  The room is for the owner's
// own attachments, for a viewer's bridge, and for a product test's HUD.
// The owner may change it.
const AutoLeaseReserve = 4

// defaultMaxAttachments is the limit assumed when the region does not
// say: what SimulatorFeatures has said wherever it has been measured.
const defaultMaxAttachments = 38

// autoWearWait is how long a wear is waited for.  A wear past the limit
// is never answered, so this is also how long the refusal takes to show,
// and the only time a setup waits for one.
var autoWearWait = 40 * time.Second

// autoOffWait is how long the region is given to stop listing what was
// taken off, and autoUndescribedSettle how long to wait after taking off
// something the region never described, which nothing can be polled for:
// Worn waits the same ten seconds in the same case.
var (
	autoOffWait           = 15 * time.Second
	autoUndescribedSettle = 10 * time.Second
)

// IsAutoName is whether an item is named like one of the auto objects:
// exactly "auto", or "auto" and a number.  "autobench" and "automate"
// are somebody else's.
func IsAutoName(name string) bool {
	if name == AutoObject {
		return true
	}
	rest, ok := strings.CutPrefix(name, AutoObject+" ")
	if !ok || rest == "" {
		return false
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// autoSlot is the slot an item's name stands for, when it is the name
// that slot is given.  "auto 1" and "auto 07" are auto names that no
// slot answers to.
func autoSlot(name string) (int, bool) {
	if name == AutoObject {
		return 0, true
	}
	if !IsAutoName(name) || len(name) > len(AutoObject)+1+6 {
		return 0, false
	}
	n, err := strconv.Atoi(name[len(AutoObject)+1:])
	if err != nil || n < 2 {
		return 0, false
	}
	return n - 1, AutoName(n-1) == name
}

// AutoPlaced is one auto object a setup or an arranging left worn.
type AutoPlaced struct {
	Slot   int
	Name   string
	Object *sl.Object
	Point  int
}

// AutoReport is what `auto -n`, reset, show, hide, clear and delete did.
type AutoReport struct {
	// Placed is the objects worn on Bottom Left after the command, in
	// slot order.
	Placed []AutoPlaced

	// Want is how many -n was asked for.
	Want int

	// Moved is how many were taken off and worn again on Bottom Left,
	// Removed how many were taken off for good, and Trashed how many
	// items went to the Trash.
	Moved, Removed, Trashed int

	// Worn is what the avatar wears in all after the command, and Limit
	// what it may.
	Worn, Limit int

	// Note says why fewer were worn than asked for, and is empty when
	// nothing fell short.
	Note string

	// Warnings are things that went wrong that did not stop the rest: a
	// link left in the outfit folder, a rebake the service refused.
	Warnings []string
}

// wornItem is one thing the avatar wears, known by the inventory item it
// came from.
type wornItem struct {
	Item msg.UUID
	Name string

	// Point is where the region says it is worn, and Object and Scale
	// what it described, when it has described it.  A thing known only
	// from the Current Outfit folder has none of them.
	Point  int
	Object *sl.Object
	Scale  msg.Vector3
}

// wornSet is everything an avatar wears, by item.
type wornSet struct {
	items map[msg.UUID]*wornItem
}

func (w *wornSet) total() int { return len(w.items) }

// autos are the worn items named like auto objects, in slot order and
// then by name.
func (w *wornSet) autos() []*wornItem {
	var out []*wornItem
	for _, it := range w.items {
		if IsAutoName(it.Name) {
			out = append(out, it)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		si, oki := autoSlot(out[i].Name)
		sj, okj := autoSlot(out[j].Name)
		if oki != okj {
			return oki
		}
		if oki && si != sj {
			return si < sj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// named is the worn item with this name, if any.
func (w *wornSet) named(name string) *wornItem {
	for _, it := range w.items {
		if it.Name == name {
			return it
		}
	}
	return nil
}

// readWorn counts what the avatar wears: the union of the Current
// Outfit folder's object links and the attachments the region describes.
// Names come from the items in folder, which is where the auto objects
// are kept, and from the links for anything else.
//
// A system wearable is a link too and takes no attachment slot, so only
// links to objects count.
// Why: doc/slots.md#counting-what-is-worn
func readWorn(ctx context.Context, s *sl.Session, folder msg.UUID) (*wornSet, error) {
	described, err := s.WornObjects(ctx)
	if err != nil {
		return nil, err
	}
	links, err := s.Outfit(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the Current Outfit folder: %w", err)
	}
	items, err := s.FolderItems(ctx, folder)
	if err != nil {
		return nil, err
	}
	names := make(map[msg.UUID]string, len(items))
	for _, it := range items {
		names[it.ID] = it.Name
	}

	set := &wornSet{items: map[msg.UUID]*wornItem{}}
	add := func(id msg.UUID) *wornItem {
		w := set.items[id]
		if w == nil {
			w = &wornItem{Item: id, Name: names[id]}
			set.items[id] = w
		}
		return w
	}
	for _, a := range described {
		w := add(a.Item)
		o := a.Object
		w.Object, w.Point, w.Scale = &o, a.Point&^sl.AttachAdd, a.Scale
	}
	for _, l := range links {
		// An attachment's link, found or not: a link to something kept
		// deeper than Outfit looks still says it is an object, and an
		// attachment missed here is one more than the count allows.
		object := l.Kind == sl.AssetObject || (!l.Found && l.InvType == sl.InvTypeObject)
		if l.Folder || l.Wearable || !object {
			continue
		}
		if w := add(l.Item); w.Name == "" {
			w.Name = l.Name
		}
	}
	return set, nil
}

// attachmentLimit is how many attachments the avatar may wear in all,
// by what the region says and 38 when it does not.
func attachmentLimit(ctx context.Context, s *sl.Session) int {
	if f, err := s.Features(ctx); err == nil {
		if n := f.MaxAgentAttachments(); n > 0 {
			return n
		}
	}
	return defaultMaxAttachments
}

// holdAll takes every place the avatar has, as one grant, and refuses
// when any is busy.  doing is the command, for the refusal.
//
// Moving or removing attachments underneath a running benchmark is a
// wrong number rather than a failure, so this happens when nothing is
// using any of them, or it does not happen.  Without waiting: a person
// who ran this while a benchmark was going wants to be told, not to have
// their terminal hang until it finishes.  The places go back marked not
// clean on purpose: this moved things about, and what was in them is not
// what the next holder put there.
func holdAll(ctx context.Context, s *sl.Session, doing string) (func(), error) {
	g, ok := s.Backend().(granter)
	if !ok {
		return func() {}, nil
	}
	declarePool(g)
	got, err := g.TrySlots(ctx, AutoPool(), time.Hour, s.Info().Name)
	if err != nil {
		return nil, fmt.Errorf("asking for the %s objects: %w", AutoObject, err)
	}
	if !got.Held() {
		return nil, fmt.Errorf("the %s objects are in use (%s); "+
			"%s moves attachments about and cannot be done "+
			"under a running benchmark",
			AutoObject, got.Why, doing)
	}
	return func() { g.ReleaseSlots(got.ID, false) }, nil
}

// slotsWord is "slot" or "slots" for a count, as a sentence needs it.
func slotsWord(n int) string {
	if n == 1 {
		return "slot"
	}
	return "slots"
}

// takeOff takes things off and out of the Current Outfit folder, as a
// person's detach does, without the rebake: the caller does that once,
// at the end, with bake.
//
// Everything is asked first and the region is then polled until it stops
// listing them, because each detach is answered by nothing; one that is
// still listed when the wait runs out is an error, since wearing it
// again on top would be the double that this exists to avoid.  A link
// that cannot be removed is a warning: the thing is off, and the link
// would only put it back at the next login.
// Why: doc/slsh.md#why-detach-waits-as-well-as-takeoff
func takeOff(ctx context.Context, s *sl.Session, list []*wornItem, rep *AutoReport) error {
	if len(list) == 0 {
		return nil
	}
	undescribed := false
	for _, w := range list {
		if err := s.TakeOff(ctx, w.Item); err != nil {
			return fmt.Errorf("taking off %s: %w", w.Name, err)
		}
		if w.Object == nil {
			undescribed = true
		}
	}
	if err := waitOff(ctx, s, list); err != nil {
		return err
	}
	if undescribed {
		if err := s.Settle(ctx, autoUndescribedSettle); err != nil {
			return err
		}
	}
	for _, w := range list {
		if _, err := s.ForgetWorn(ctx, w.Item); err != nil {
			rep.Warnings = append(rep.Warnings, fmt.Sprintf(
				"%s: its link is still in the Current Outfit folder, so it will come back "+
					"at the next login: %v", w.Name, err))
		}
	}
	return nil
}

// waitOff polls what the region describes until none of these is in it.
func waitOff(ctx context.Context, s *sl.Session, list []*wornItem) error {
	deadline := time.Now().Add(autoOffWait)
	for {
		worn, err := s.WornObjects(ctx)
		if err != nil {
			return err
		}
		still := ""
		for _, a := range worn {
			for _, w := range list {
				if a.Item == w.Item {
					still = w.Name
				}
			}
		}
		if still == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s was asked to come off and the region still lists it as worn "+
				"%v later", still, autoOffWait)
		}
		if err := s.Settle(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// bake rebuilds the appearance once, after the Current Outfit folder has
// changed, as a viewer does.
// Why: doc/outfit.md#a-bake-after-every-change-to-the-folder
func bake(ctx context.Context, s *sl.Session, rep *AutoReport) {
	if err := s.UpdateAppearance(ctx); err != nil {
		rep.Warnings = append(rep.Warnings, "the appearance was not rebaked, so the "+
			"simulator's list of what is worn may still show what came off: "+err.Error())
	}
}

// itemFor is the inventory item an auto name means, or nil when there is
// none.  Several of one name are refused, as everywhere.
func itemFor(items []*sl.Item, folder msg.UUID, name string) (*sl.Item, error) {
	it, err := sl.PickNamedFunc(items, name, "item", "in folder "+folder.String(),
		func(it *sl.Item) (string, msg.UUID) { return it.Name, it.ID })
	var ne *sl.NameError
	if err != nil {
		if errors.As(err, &ne) && len(ne.IDs) == 0 {
			return nil, nil
		}
		return nil, err
	}
	return it, nil
}

// putOn wears the item for a slot on the slot's point, and returns what it
// became.  An item that exists is worn as it is, which is quick; one that
// does not is built, the slow way, by EnsureAttached, once in the life of
// an account.  Either way the add bit is laid over the point, because
// every object is worn on the one point and a bare attach would throw the
// others off it.
func putOn(ctx context.Context, s *sl.Session, folder msg.UUID, items []*sl.Item, slot int) (*sl.Attached, error) {
	name := AutoName(slot)
	it, err := itemFor(items, folder, name)
	if err != nil {
		return nil, err
	}
	point := AutoPoints[slot] | sl.AttachAdd
	if it == nil {
		return s.EnsureAttached(ctx, folder, name, point)
	}
	return s.Wear(ctx, it, point, autoWearWait)
}

// park and size put one worn object where an idle one sits, at the size
// of every auto object.
func park(ctx context.Context, s *sl.Session, o *sl.Object) error {
	return s.Place(ctx, o, AutoParked, msg.Quaternion{}, msg.Vector3{X: AutoSize, Y: AutoSize, Z: AutoSize})
}

// SetupAuto sets the number of auto objects worn to n: the first n are
// worn on Bottom Left, parked and sized, any that were worn on another
// point are worn again there, and any beyond n are taken off.
//
// It takes EVERY place the avatar has first, and refuses if any is busy.
// Wearing things is not something to do underneath a benchmark: an item
// that cannot be found worn is taken off and put back on, and a run whose
// object went away reports nothing useful about why.
//
// It wears only what fits.  The avatar's attachments are counted
// (readWorn) against the region's limit less AutoReserve, and what does
// not fit is not asked for; the report says how many of n it wore and
// why not the rest.  A wear that times out is read as the region
// refusing at the limit, and is the only wait: setup stops at once.
// Objects it took off to wear them again are put back first and without
// counting, since they were worn before and the avatar is no fuller for
// them.
// Why: doc/slots.md#setting-up-and-the-limit
func SetupAuto(ctx context.Context, s *sl.Session, n int) (*AutoReport, error) {
	if n < 1 || n > AutoPool() {
		return nil, fmt.Errorf("auto -n takes a number from 1 to %d, not %d", AutoPool(), n)
	}

	release, err := holdAll(ctx, s, "auto -n")
	if err != nil {
		return nil, err
	}
	defer release()

	folder, err := objectsFolder(ctx, s)
	if err != nil {
		return nil, err
	}
	if err := EnsureAutoItems(ctx, s, folder, n); err != nil {
		return nil, err
	}
	st, err := readWorn(ctx, s, folder)
	if err != nil {
		return nil, err
	}
	items, err := s.FolderItems(ctx, folder)
	if err != nil {
		return nil, err
	}
	limit := attachmentLimit(ctx, s)
	rep := &AutoReport{Want: n, Limit: limit}

	// Sort what is worn: in place on Bottom Left and described; to be
	// worn again there; or not wanted.
	inPlace := map[int]*wornItem{}
	again := map[int]*wornItem{}
	var gone, off []*wornItem
	for _, w := range st.autos() {
		slot, ok := autoSlot(w.Name)
		switch {
		case !ok || slot >= n:
			gone = append(gone, w)
			off = append(off, w)
		case w.Object == nil || w.Point != sl.HUDBottomLeft:
			again[slot] = w
			off = append(off, w)
		default:
			inPlace[slot] = w
		}
	}
	if err := takeOff(ctx, s, off, rep); err != nil {
		return rep, err
	}
	rep.Removed, rep.Moved = len(gone), len(again)
	worn := st.total() - len(off)

	placed := map[int]*sl.Object{}
	for slot, w := range inPlace {
		placed[slot] = w.Object
	}

	wear := func(slot int) error {
		at, err := putOn(ctx, s, folder, items, slot)
		if err != nil {
			return err
		}
		placed[slot] = &at.Object
		worn++
		return nil
	}

	// A wear that did not happen is a note, and only an error when
	// nothing at all is worn: fewer objects is less at once, not a
	// failure, but a setup that did nothing has to be told.
	var failed error
	stop := func(slot int, err error) {
		if errors.Is(err, sl.ErrTimeout) {
			rep.Note = fmt.Sprintf("wore %d of %d: the region did not confirm %s within %v; "+
				"it refuses a wear past its limit of %d attachments without a word, "+
				"and the avatar wears %d",
				len(placed), n, AutoName(slot), autoWearWait, limit, worn)
			return
		}
		failed = fmt.Errorf("%s: %w", AutoName(slot), err)
		rep.Note = fmt.Sprintf("wore %d of %d: %v", len(placed), n, failed)
	}

	// Those that were worn before go back whatever the count, then the
	// new ones as far as there is room.
	for slot := 0; slot < n && rep.Note == ""; slot++ {
		if again[slot] != nil {
			if err := wear(slot); err != nil {
				stop(slot, err)
			}
		}
	}
	for slot := 0; slot < n && rep.Note == ""; slot++ {
		if placed[slot] != nil || again[slot] != nil {
			continue
		}
		if worn >= limit-AutoReserve {
			rep.Note = fmt.Sprintf("wore %d of %d: the avatar wears %d, keeping %d %s free",
				len(placed), n, worn, AutoReserve, slotsWord(AutoReserve))
			break
		}
		if err := wear(slot); err != nil {
			stop(slot, err)
		}
	}

	// Every one that is worn is parked and sized, whether it was just
	// put on or has been there all along.
	var errs []error
	for slot := 0; slot < n; slot++ {
		o := placed[slot]
		if o == nil {
			continue
		}
		if err := park(ctx, s, o); err != nil {
			errs = append(errs, fmt.Errorf("parking %s: %w", AutoName(slot), err))
			continue
		}
		rep.Placed = append(rep.Placed, AutoPlaced{
			Slot: slot, Name: AutoName(slot), Object: o, Point: sl.HUDBottomLeft,
		})
	}
	if len(off) > 0 {
		bake(ctx, s, rep)
	}
	rep.Worn = worn
	if failed != nil && len(placed) == 0 {
		errs = append([]error{failed}, errs...)
	}
	return rep, errors.Join(errs...)
}

// AutoLayout is what ArrangeAuto does to the objects that are worn.
type AutoLayout int

const (
	// AutoReset puts them at their size and parks them.
	AutoReset AutoLayout = iota
	// AutoShow lays them along the bottom of the screen.
	AutoShow
	// AutoHide parks them and leaves their size.
	AutoHide
)

// ArrangeAuto moves every worn auto object, and leaves the count as it
// was.
//
// One that is not on Bottom Left, or that the region has not described
// (it does not describe HUDs after a login), is taken off and worn there
// first, unconditionally: it was worn before and the avatar is no
// fuller.  What is placed is then read again from the region, so that a
// size left alone is the size it has.
func ArrangeAuto(ctx context.Context, s *sl.Session, how AutoLayout) (*AutoReport, error) {
	doing := map[AutoLayout]string{AutoReset: "auto reset", AutoShow: "auto show", AutoHide: "auto hide"}[how]
	release, err := holdAll(ctx, s, doing)
	if err != nil {
		return nil, err
	}
	defer release()

	folder, err := objectsFolder(ctx, s)
	if err != nil {
		return nil, err
	}
	st, err := readWorn(ctx, s, folder)
	if err != nil {
		return nil, err
	}
	rep := &AutoReport{Limit: attachmentLimit(ctx, s)}

	var off []*wornItem
	for _, w := range st.autos() {
		if w.Object == nil || w.Point != sl.HUDBottomLeft {
			off = append(off, w)
		}
	}
	if len(off) > 0 {
		items, err := s.FolderItems(ctx, folder)
		if err != nil {
			return nil, err
		}
		if err := takeOff(ctx, s, off, rep); err != nil {
			return rep, err
		}
		rep.Moved = len(off)
		for _, w := range off {
			it, err := itemFor(items, folder, w.Name)
			if err == nil && it == nil {
				err = fmt.Errorf("there is no item called %q in the Objects folder", w.Name)
			}
			if err == nil {
				_, err = s.Wear(ctx, it, sl.HUDBottomLeft|sl.AttachAdd, autoWearWait)
			}
			if err != nil {
				return rep, fmt.Errorf("wearing %s again on %s: %w", w.Name,
					sl.AttachPointName(sl.HUDBottomLeft), err)
			}
		}
		bake(ctx, s, rep)
		if st, err = readWorn(ctx, s, folder); err != nil {
			return rep, err
		}
	}
	rep.Worn = st.total()

	var errs []error
	for _, w := range st.autos() {
		if w.Object == nil {
			errs = append(errs, fmt.Errorf("%s is worn but the region has not described it", w.Name))
			continue
		}
		slot, ok := autoSlot(w.Name)
		if !ok {
			slot = -1
		}
		size := w.Scale
		if how == AutoReset || size == (msg.Vector3{}) {
			size = msg.Vector3{X: AutoSize, Y: AutoSize, Z: AutoSize}
		}
		at := AutoParked
		if how == AutoShow && slot >= 0 {
			at = AutoTile(slot)
		}
		if err := s.Place(ctx, w.Object, at, msg.Quaternion{}, size); err != nil {
			errs = append(errs, fmt.Errorf("moving %s: %w", w.Name, err))
			continue
		}
		rep.Placed = append(rep.Placed, AutoPlaced{Slot: slot, Name: w.Name, Object: w.Object, Point: w.Point})
	}
	return rep, errors.Join(errs...)
}

// ClearAuto takes off every auto object that is worn, wherever it is
// worn, and its link in the Current Outfit folder, and rebakes once at
// the end as a person's detach does.
func ClearAuto(ctx context.Context, s *sl.Session) (*AutoReport, error) {
	release, err := holdAll(ctx, s, "auto clear")
	if err != nil {
		return nil, err
	}
	defer release()
	return clearAuto(ctx, s)
}

func clearAuto(ctx context.Context, s *sl.Session) (*AutoReport, error) {
	folder, err := objectsFolder(ctx, s)
	if err != nil {
		return nil, err
	}
	st, err := readWorn(ctx, s, folder)
	if err != nil {
		return nil, err
	}
	rep := &AutoReport{Limit: attachmentLimit(ctx, s)}
	off := st.autos()
	if err := takeOff(ctx, s, off, rep); err != nil {
		return rep, err
	}
	rep.Removed = len(off)
	rep.Worn = st.total() - len(off)
	if len(off) > 0 {
		bake(ctx, s, rep)
	}
	return rep, nil
}

// DeleteAuto clears the auto objects, then moves every auto item in the
// Objects folder to the Trash.  The Trash is not emptied: it is the
// owner's to purge.  Only items named exactly "auto" or "auto" and a
// number are touched.
func DeleteAuto(ctx context.Context, s *sl.Session) (*AutoReport, error) {
	release, err := holdAll(ctx, s, "auto delete")
	if err != nil {
		return nil, err
	}
	defer release()

	rep, err := clearAuto(ctx, s)
	if err != nil {
		return rep, err
	}
	folder, err := objectsFolder(ctx, s)
	if err != nil {
		return rep, err
	}
	trash, err := s.TrashFolder(ctx)
	if err != nil {
		return rep, err
	}
	items, err := s.FolderItems(ctx, folder)
	if err != nil {
		return rep, err
	}
	for _, it := range items {
		if !IsAutoName(it.Name) {
			continue
		}
		if err := s.MoveItem(ctx, it.ID, trash); err != nil {
			return rep, fmt.Errorf("moving %q to the Trash: %w", it.Name, err)
		}
		rep.Trashed++
	}
	return rep, nil
}

// CountAuto is how many auto objects the avatar wears, counted as the
// union that readWorn counts.
func CountAuto(ctx context.Context, s *sl.Session) (int, error) {
	folder, err := objectsFolder(ctx, s)
	if err != nil {
		return 0, err
	}
	st, err := readWorn(ctx, s, folder)
	if err != nil {
		return 0, err
	}
	return len(st.autos()), nil
}
