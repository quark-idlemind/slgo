package sl

// Listing inventory by path.
//
// The tree is fetched over AIS, which takes a depth on the request, so
// asking for two levels is one round trip rather than a walk with a
// round trip per folder.  That matters: a full inventory is over a
// hundred requests and the better part of a minute, and most questions
// are about one folder.

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/msg"
)

// Entry is one thing in inventory: a folder or an item.
//
// One type rather than two because a listing is a mixed list and
// everything that reads one wants to print it or filter it, which a sum
// type makes harder than it is.  Folder says which this is, and the
// fields that only apply to the other are zero.
type Entry struct {
	ID     msg.UUID
	Parent msg.UUID
	Name   string

	// Folder is true for a folder.  A folder has no asset and no
	// permissions; an item has both.
	Folder bool

	// Path is where this sits, from the inventory root, with the
	// entry's own name on the end.
	Path string

	// Depth is how far below the folder that was asked about this
	// was found.  Zero is directly in it.
	Depth int

	// Type is the asset type for an item and the folder's preferred
	// type for a folder -- the thing that makes Trash the trash.
	Type int

	// InvType is what kind of inventory entry an item is, which is
	// not the same as what its asset is: a link to a notecard is a
	// link, and points at a notecard.
	InvType int

	// Asset is what the item points at, and is zero for a folder and
	// for an item whose asset the simulator did not name.
	Asset msg.UUID

	Desc    string
	Created int64
	IsLink  bool

	// Flags is the item's flag word.  Its low byte is the slot a
	// system wearable occupies -- which shirt layer, which body part
	// -- and is the only place that is recorded: a skin and a shape
	// are both "bodypart" and are told apart by nothing else.
	Flags uint32

	// Creator, Owner, LastOwner and Group are the item's, from its
	// permissions.  A rez or a wear sends the group back as the item's
	// own, and putting it in an object sums the last owner into the
	// checksum.
	Creator   msg.UUID
	Owner     msg.UUID
	LastOwner msg.UUID
	Group     msg.UUID

	BaseMask      uint32
	OwnerMask     uint32
	GroupMask     uint32
	EveryoneMask  uint32
	NextOwnerMask uint32

	SaleType  int
	SalePrice int
}

// IsFolder reports whether this is a folder, for callers who prefer
// asking to reading a field.
func (e Entry) IsFolder() bool { return e.Folder }

// Item is the entry as the item the operations take: RezFromInventory,
// Wear, PutInObject and the rest.
//
// A listing reads the same AIS record an item is made from, so nothing
// needs fetching again.  Every field is copied, because those calls
// send them all back as the item's own: a mask left behind would go out
// as zero, which takes that permission away.
func (e Entry) Item() *Item {
	return &Item{
		ID:            e.ID,
		ParentID:      e.Parent,
		AssetID:       e.Asset,
		Name:          e.Name,
		Desc:          e.Desc,
		Type:          e.Type,
		InvType:       e.InvType,
		Flags:         e.Flags,
		Created:       e.Created,
		CreatorID:     e.Creator,
		OwnerID:       e.Owner,
		LastOwnerID:   e.LastOwner,
		GroupID:       e.Group,
		BaseMask:      e.BaseMask,
		OwnerMask:     e.OwnerMask,
		GroupMask:     e.GroupMask,
		EveryoneMask:  e.EveryoneMask,
		NextOwnerMask: e.NextOwnerMask,
		SaleType:      e.SaleType,
		SalePrice:     e.SalePrice,
		IsLink:        e.IsLink,
	}
}

func (e Entry) String() string {
	if e.Folder {
		return e.Path + string(PathSeparator)
	}
	return e.Path
}

// PathSeparator is what separates names in a path, and PathEscape is
// what puts one inside a name.
//
// An inventory name may hold very nearly any printable character,
// including both of these, so a path is only reversible if they are
// escaped: \/ is a slash in a name and \\ is a backslash.  SplitPath
// and JoinPath are inverses over every name the grid accepts, which is
// what lets a listing be written to a file, edited, and read back.
//
// What the grid accepts was measured rather than assumed: an item was
// created for each character from space to tilde, all ninety-five listed
// back, and every one came back byte for byte -- including / and \.
// The two exceptions are at the edges, where the grid trims: a name
// given a leading or a trailing space comes back without it.  So a name
// can contain anything printable, and cannot begin or end with a
// space.
const (
	PathSeparator = '/'
	PathEscape    = '\\'
)

// SplitPath breaks a path into the names it holds.
//
// Empty segments are dropped, so "", "/", "Objects" and "/Objects/" all
// mean what they look like.  Nothing else is touched: a name of one
// space is a name of one space, and trimming it here would make a
// listing that cannot be read back.  A shell that wants to be forgiving
// about what somebody typed should be forgiving before calling this.
//
// A backslash takes the next character literally.  Before anything but
// a separator or another backslash it is itself, so a name written with
// a stray backslash still means what it looks like.
func SplitPath(path string) []string {
	var out []string
	var cur []rune
	started := false

	rs := []rune(path)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; c {
		case PathEscape:
			started = true
			if i+1 < len(rs) && (rs[i+1] == PathSeparator || rs[i+1] == PathEscape) {
				cur = append(cur, rs[i+1])
				i++
				continue
			}
			cur = append(cur, c) // a backslash before anything else
		case PathSeparator:
			if started {
				out = append(out, string(cur))
			}
			cur, started = nil, false
		default:
			started = true
			cur = append(cur, c)
		}
	}
	if started {
		out = append(out, string(cur))
	}
	return out
}

// JoinPath turns names into a path, escaping what would otherwise be
// read as structure.
func JoinPath(names ...string) string {
	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteRune(PathSeparator)
		}
		b.WriteString(EscapeName(n))
	}
	return b.String()
}

// EscapeName is one name as it appears inside a path.
func EscapeName(name string) string {
	if !strings.ContainsRune(name, PathSeparator) && !strings.ContainsRune(name, PathEscape) {
		return name
	}
	var b strings.Builder
	for _, c := range name {
		if c == PathSeparator || c == PathEscape {
			b.WriteRune(PathEscape)
		}
		b.WriteRune(c)
	}
	return b.String()
}

// ListInventory lists what is in a folder, by path from the inventory
// root.
//
// An empty path is the root itself.  Depth is how far to descend: 0 is
// the entries directly in the folder, 1 adds what is in those folders,
// and so on.  Folders are listed as well as items, so a listing shows
// the shape of the tree rather than only its leaves.
//
// The result is in tree order: every folder immediately followed by
// what is inside it, and siblings by name, the way ls -R reads.
func (w *Session) ListInventory(ctx context.Context, path string, depth uint) ([]Entry, error) {
	id, at, err := w.resolvePath(ctx, path)
	if err != nil {
		return nil, err
	}
	return w.listFolder(ctx, id, at, depth)
}

// ListFolder is ListInventory for a folder whose id is already known,
// which is the way to reach a folder whose name contains a separator.
func (w *Session) ListFolder(ctx context.Context, folder msg.UUID, depth uint) ([]Entry, error) {
	return w.listFolder(ctx, folder, "", depth)
}

// resolvePath walks the names down from the root, one fetch per level.
//
// The levels have to be walked because a path is names and AIS answers
// about ids; there is no call that takes a path.  Each step is one
// request, so a deep path costs a request per segment -- which is why
// the id form exists.
//
// Each name picks out one folder through PickNamed: exactly, and never
// the first of two folders of one name.
func (w *Session) resolvePath(ctx context.Context, path string) (msg.UUID, string, error) {
	id := w.invRoot
	if id.IsZero() {
		return msg.UUID{}, "", fmt.Errorf("sl: this session has no inventory root")
	}
	var walked []string
	for _, name := range SplitPath(path) {
		kids, err := w.children(ctx, id, 0)
		if err != nil {
			return msg.UUID{}, "", err
		}
		var folders []Entry
		for _, e := range kids {
			if e.Folder {
				folders = append(folders, e)
			}
		}
		where := JoinPath(walked...)
		if where == "" {
			where = "the inventory root"
		}
		next, err := PickNamed(folders, name, "folder", "in "+where)
		if err != nil {
			return msg.UUID{}, "", fmt.Errorf("sl: %w", err)
		}
		id, walked = next.ID, append(walked, name)
	}
	return id, JoinPath(walked...), nil
}

func (w *Session) listFolder(ctx context.Context, folder msg.UUID, at string, depth uint) ([]Entry, error) {
	if folder.IsZero() {
		return nil, fmt.Errorf("sl: no folder to list")
	}
	out, err := w.children(ctx, folder, depth)
	if err != nil {
		return nil, err
	}
	// The entries already carry paths built from escaped names, so
	// the prefix is put in front rather than joined: join escapes what
	// it is given, and escaping a path turns its separators into
	// literal characters.
	if at != "" {
		for i := range out {
			out[i].Path = at + string(PathSeparator) + out[i].Path
		}
	}
	sortEntries(out)
	return out, nil
}

// children fetches one folder with a depth and flattens the answer.
//
// AIS takes the depth on the request, so this is one round trip
// whatever the depth -- the alternative, a fetch per folder, is what
// makes reading a whole inventory take a minute.
func (w *Session) children(ctx context.Context, folder msg.UUID, depth uint) ([]Entry, error) {
	inv := agent.NewInventory(folder)
	if err := agent.FetchFolderDepth(ctx, w.b, inv, folder, int(depth)); err != nil {
		return nil, fmt.Errorf("sl: listing %s: %w", folder, err)
	}

	var out []Entry
	var walk func(id msg.UUID, prefix string, level int)
	walk = func(id msg.UUID, prefix string, level int) {
		for _, f := range inv.Children(id) {
			e := Entry{
				ID: f.ID, Parent: f.ParentID, Name: f.Name, Folder: true,
				Path: join(prefix, f.Name), Depth: level, Type: f.Type,
			}
			out = append(out, e)
			if uint(level) < depth {
				walk(f.ID, e.Path, level+1)
			}
		}
		for _, it := range inv.Contents(id) {
			out = append(out, Entry{
				ID: it.ID, Parent: it.ParentID, Name: it.Name, Flags: it.Flags,
				Path: join(prefix, it.Name), Depth: level,
				Type: it.Type, InvType: it.InvType, Asset: it.AssetID,
				Desc: it.Desc, Created: it.Created, IsLink: it.IsLink,
				Creator: it.CreatorID, Owner: it.OwnerID,
				LastOwner: it.LastOwnerID, Group: it.GroupID,
				BaseMask: it.BaseMask, OwnerMask: it.OwnerMask,
				GroupMask: it.GroupMask, EveryoneMask: it.EveryoneMask,
				NextOwnerMask: it.NextOwnerMask,
				SaleType:      it.SaleType, SalePrice: it.SalePrice,
			})
		}
	}
	walk(folder, "", 0)
	return out, nil
}

// join adds a name to a path, escaping it on the way in so the result
// splits back into the names it was built from.
func join(prefix, name string) string {
	if prefix == "" {
		return EscapeName(name)
	}
	return prefix + string(PathSeparator) + EscapeName(name)
}

// sortEntries puts a listing in tree order: every folder immediately
// followed by what is inside it, and siblings by name.
//
// The comparison is segment by segment rather than over the whole
// string, and that is not fussiness.  Comparing the strings would sort
// "Alpha Two" between "Alpha" and "Alpha/Deep", because a space is
// below a slash -- so a sibling folder would land in the middle of
// another folder's contents.
func sortEntries(es []Entry) {
	sort.SliceStable(es, func(i, j int) bool {
		return lessPath(es[i], es[j])
	})
}

func lessPath(a, b Entry) bool {
	as, bs := SplitPath(a.Path), SplitPath(b.Path)
	for i := 0; i < len(as) && i < len(bs); i++ {
		x, y := as[i], bs[i]
		if x == y {
			continue
		}
		if lx, ly := strings.ToLower(x), strings.ToLower(y); lx != ly {
			return lx < ly
		}
		return x < y
	}
	// One path is the other with more on the end: the parent first.
	if len(as) != len(bs) {
		return len(as) < len(bs)
	}
	// The same path twice, which a folder and an item of one name can
	// be -- and so can two items, since a folder may hold a dozen
	// things called the same thing.  The folder first, so its contents
	// follow it.
	if a.Folder != b.Folder {
		return a.Folder
	}
	// Then newest first, and then the id.
	//
	// The id is not for the reader; it is so that the order is an
	// order.  AIS hands the contents of a folder over in a map, and a
	// map has no order, so anything left tied here came out differently
	// on every listing: fifteen listings of one folder of same-named
	// notecards gave five different orders.  A listing is meant to be
	// written to a file and edited into commands, and two listings of
	// an unchanged folder have to match for that to be worth anything.
	if a.Created != b.Created {
		return a.Created > b.Created
	}
	return bytes.Compare(a.ID[:], b.ID[:]) < 0
}
