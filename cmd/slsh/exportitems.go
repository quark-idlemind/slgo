package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

func init() {
	exportCommands["export-items"] = &command{
		params:   "[PATH|UUID ...]",
		flags:    func() any { return new(exportItemsFlags) },
		brief:    "write inventory items, one folder each, for an offline viewer to dress an avatar in; changes nothing worn",
		keywords: "export items inventory wearables attachments capture library offline viewer",
		man:      "export-items",
		run:      cmdExportItems,
	}
}

type exportItemsFlags struct {
	Out  string `getopt:"--out -o=DIR      the library to write into: one folder per item"`
	Worn bool   `getopt:"--worn -w         every item worn now, as well as any named"`
	Help bool   `getopt:"--help -h         show what this command takes"`
}

// itemFormat names an item folder's layout.
const itemFormat = "3dview-item/1"

// itemManifest is DIR/ITEM/item.json.
type itemManifest struct {
	Format     string            `json:"format"`
	Item       string            `json:"item"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Captured   string            `json:"captured"`
	Wearable   *exportWearable   `json:"wearable,omitempty"`
	Attachment *exportAttachment `json:"attachment,omitempty"`
	Problems   []string          `json:"problems,omitempty"`
}

// exportTarget is one item to write.
type exportTarget struct {
	Item  msg.UUID
	Name  string
	Kind  sl.AssetType
	Flags uint32
	Asset msg.UUID
}

func cmdExportItems(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o exportItemsFlags
	args, done, err := subOptions("export-items", &o, out, args)
	if err != nil || done {
		return err
	}
	if o.Out == "" || (len(args) == 0 && !o.Worn) {
		return usageError("export-items")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()

	var targets []exportTarget
	seen := map[msg.UUID]bool{}
	add := func(t exportTarget) {
		if !seen[t.Item] {
			seen[t.Item] = true
			targets = append(targets, t)
		}
	}
	for _, a := range args {
		e, err := sh.thingAt(ctx, a)
		if err != nil {
			return err
		}
		if e.Folder {
			return fmt.Errorf("%s is a folder; name the items in it", a)
		}
		add(exportTarget{Item: e.ID, Name: e.Name, Kind: sl.AssetType(e.Type), Flags: e.Flags, Asset: e.Asset})
	}
	if o.Worn {
		links, err := sh.s.Outfit(ctx)
		if err != nil {
			return err
		}
		for _, l := range links {
			if l.Folder {
				continue
			}
			if !l.Found {
				fmt.Fprintf(out, "%s: the outfit's link leads to nothing this session found\n", l.Name)
				continue
			}
			it, err := sh.s.OutfitItem(ctx, l)
			if err != nil {
				fmt.Fprintf(out, "%s: %v\n", l.Name, err)
				continue
			}
			add(exportTarget{Item: it.ID, Name: it.Name, Kind: sl.AssetType(it.Type), Flags: it.Flags, Asset: it.AssetID})
		}
	}

	for _, t := range targets {
		fmt.Fprintf(out, "%s\n", exportItem(ctx, sh, o.Out, t))
	}
	return nil
}

// exportItem writes one item's folder and says what happened.
func exportItem(ctx context.Context, sh *Shell, lib string, t exportTarget) string {
	dir := filepath.Join(lib, t.Item.String())
	label := fmt.Sprintf("%s (%s)", t.Name, t.Item)
	m := itemManifest{Format: itemFormat, Item: t.Item.String(), Name: t.Name,
		Captured: time.Now().UTC().Format(time.RFC3339)}

	switch {
	case sl.IsWearable(t.Kind):
		e := &exporter{sh: sh, dir: dir, files: map[msg.UUID]string{}}
		slot, _ := sl.SlotOf(t.Kind, t.Flags)
		w := e.wearable(ctx, t.Name, slot, t.Kind, t.Item, t.Asset)
		m.Kind, m.Wearable, m.Problems = "wearable", &w, e.problems

	case t.Kind == sl.AssetObject:
		a, worn := sh.s.WornFromItem(ctx, t.Item)
		if !worn {
			// An object's asset is not served to a client, so what it
			// is can only be read off the region while it is worn.  A
			// capture from an earlier wearing stands.
			if _, err := os.Stat(filepath.Join(dir, "item.json")); err == nil {
				return label + ": not worn now; kept the capture from before"
			}
			return label + ": not worn now and never captured; wear it once and run this again"
		}
		e := &exporter{sh: sh, dir: dir, files: map[msg.UUID]string{}}
		x := e.attachment(ctx, a)
		m.Kind, m.Attachment, m.Problems = "attachment", &x, e.problems

	default:
		return fmt.Sprintf("%s: a %s is neither worn nor an attachment", label, t.Kind)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Sprintf("%s: %v", label, err)
	}
	j, err := json.MarshalIndent(m, "", "  ")
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "item.json"), append(j, '\n'), 0o644)
	}
	if err != nil {
		return fmt.Sprintf("%s: %v", label, err)
	}
	return fmt.Sprintf("%s: %s, %d problems", label, m.Kind, len(m.Problems))
}
