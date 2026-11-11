package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/quark-idlemind/slgo/mesh"
	"github.com/quark-idlemind/slgo/msg"
	"github.com/quark-idlemind/slgo/sl"
)

var meshCommands = map[string]*command{
	"putmesh": {
		params:   "FILE",
		flags:    func() any { return new(putMeshFlags) },
		brief:    "upload a mesh from a JSON model; asks the price first and pays at most --max",
		keywords: "upload mesh model object rigged clothing import costs",
		man:      "putmesh",
		run:      cmdPutMesh,
	},
}

type putMeshFlags struct {
	Name   string `getopt:"--name -n=NAME     call it this in inventory, rather than the model's name"`
	Desc   string `getopt:"--desc -d=TEXT     the item's description"`
	Folder string `getopt:"--folder -f=PATH   put it here, rather than in Objects"`
	Max    int    `getopt:"--max -m=L$        pay at most this; without it only a free upload goes ahead"`
	Quote  bool   `getopt:"--quote -q         ask the grid the price, and upload nothing"`
	Out    string `getopt:"--out -o=FILE      write the encoded asset here instead of uploading"`
	DryRun bool   `getopt:"--dry-run -N       encode it and say what it is, without asking the grid anything"`
	Help   bool   `getopt:"--help -h          show what this command takes"`
}

func cmdPutMesh(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o putMeshFlags
	args, done, err := subOptions("putmesh", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 1 {
		return usageError("putmesh")
	}
	if o.Max < 0 {
		return fmt.Errorf("--max is L$%d, and a price is not negative", o.Max)
	}
	file := args[0]
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	m, err := mesh.ReadJSON(f)
	f.Close()
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}
	e, err := mesh.Encode(m)
	if err != nil {
		return fmt.Errorf("%s: %w", file, err)
	}

	name := o.Name
	if name == "" {
		name = m.Name
	}
	if name == "" {
		name = strings.TrimSuffix(filepath.Base(file), filepath.Ext(file))
	}
	kind := "unrigged"
	if e.Rigged {
		kind = fmt.Sprintf("rigged to %d joints", len(m.Skin.Joints))
	}
	fmt.Fprintf(out, "%s: %d faces, %d vertices, %d triangles, %s, %.3f x %.3f x %.3f m, %d bytes encoded\n",
		name, len(m.Faces), e.Vertices, e.Triangles, kind, e.Scale[0], e.Scale[1], e.Scale[2], len(e.Asset))

	if o.Out != "" {
		if err := os.WriteFile(o.Out, e.Asset, 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: written, not uploaded\n", o.Out)
		return nil
	}
	if o.DryRun {
		fmt.Fprintln(out, "not uploaded: --dry-run")
		return nil
	}

	u := sl.MeshUpload{Name: name, Desc: o.Desc, MaxPrice: o.Max}
	if o.Folder != "" {
		ent, err := sh.entryAt(ctx, o.Folder)
		if err != nil {
			return err
		}
		if !ent.Folder {
			return fmt.Errorf("%s is not a folder", o.Folder)
		}
		u.Folder = ent.ID
	}

	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if o.Quote {
		q, err := sh.s.QuoteMesh(ctx, u, m, e)
		if err != nil {
			return err
		}
		printQuote(out, q)
		fmt.Fprintln(out, "not uploaded: --quote")
		return nil
	}
	it, res, q, err := sh.s.UploadMesh(ctx, u, m, e)
	if q != nil {
		printQuote(out, q)
	}
	if err != nil {
		if res != nil && !res.NewAsset.IsZero() {
			fmt.Fprintf(out, "asset %s\n", res.NewAsset)
		}
		return err
	}
	fmt.Fprintf(out, "%s: item %s, asset %s\n", it.Name, it.ID, assetOf(res))
	return nil
}

func assetOf(res *sl.UploadResult) msg.UUID {
	if res == nil {
		return msg.UUID{}
	}
	return res.NewAsset
}

// printQuote says the price, and the weights the grid worked it out
// from where it gave them.
func printQuote(out io.Writer, q *sl.MeshQuote) {
	fmt.Fprintf(out, "price: L$%d\n", q.Price)
	for _, k := range []string{"resource_cost", "model_streaming_cost", "simulation_cost", "physics_cost", "upload_price_breakdown"} {
		if v, ok := q.Data[k]; ok {
			fmt.Fprintf(out, "  %s: %v\n", k, v)
		}
	}
}
