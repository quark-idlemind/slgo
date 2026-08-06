package sl

// Listing inventory by path.
//
// The tree is fetched over AIS, which takes a depth on the request, so
// asking for two levels is one round trip rather than a walk with a
// round trip per folder.  That matters: a full inventory is over a
// hundred requests and the better part of a minute, and most questions
// are about one folder.

import (
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

	Creator msg.UUID
	Owner   msg.UUID

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

func (e Entry) String() string {
	if e.Folder {
		return e.Path + "/"
	}
	return e.Path
}

// PathSeparator is what separates folder names in a path.
//
// An inventory name may itself contain a slash, so a path is not always
// reversible.  Where that matters, ListFolder takes the folder's id and
// asks nothing of names.
const PathSeparator = "/"

// SplitPath breaks a path into folder names, ignoring empty segments so
// that "", "/", "Objects" and "/Objects/" all mean what they look like.
func SplitPath(path string) []string {
	var out []string
	for _, s := range strings.Split(path, PathSeparator) {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
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
		var next msg.UUID
		for _, e := range kids {
			if e.Folder && strings.EqualFold(e.Name, name) {
				next = e.ID
				break
			}
		}
		if next.IsZero() {
			where := strings.Join(walked, PathSeparator)
			if where == "" {
				where = "the inventory root"
			}
			return msg.UUID{}, "", fmt.Errorf("sl: no folder %q in %s", name, where)
		}
		id, walked = next, append(walked, name)
	}
	return id, strings.Join(walked, PathSeparator), nil
}

func (w *Session) listFolder(ctx context.Context, folder msg.UUID, at string, depth uint) ([]Entry, error) {
	if folder.IsZero() {
		return nil, fmt.Errorf("sl: no folder to list")
	}
	out, err := w.children(ctx, folder, depth)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].Path = join(at, out[i].Path)
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
				ID: it.ID, Parent: it.ParentID, Name: it.Name,
				Path: join(prefix, it.Name), Depth: level,
				Type: it.Type, InvType: it.InvType, Asset: it.AssetID,
				Desc: it.Desc, Created: it.Created, IsLink: it.IsLink,
				Creator: it.CreatorID, Owner: it.OwnerID,
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

func join(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + PathSeparator + name
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
	// The same path twice, which only a folder and an item of one
	// name can be.  The folder first, so its contents follow it.
	return a.Folder && !b.Folder
}
