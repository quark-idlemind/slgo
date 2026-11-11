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

var exportCommands = map[string]*command{
	"export-outfit": {
		params:   "",
		flags:    func() any { return new(exportFlags) },
		brief:    "write what the avatar is wearing to a directory, as the grid stores it, for an offline viewer",
		keywords: "export outfit appearance shape visual params bakes baked textures mesh attachments wearables offline viewer",
		man:      "export-outfit",
		run:      cmdExportOutfit,
	},
}

type exportFlags struct {
	Out  string `getopt:"--out -o=DIR      write here; it is made if it is not there"`
	Name string `getopt:"--name -n=NAME    the outfit's name in the manifest, rather than Current Outfit"`
	Bake bool   `getopt:"--bake -b         ask for a fresh bake of what is worn first"`
	HUDs bool   `getopt:"--huds            include HUD attachments, which are not part of how the avatar looks"`
	Help bool   `getopt:"--help -h         show what this command takes"`
}

// exportBakeWait is how long a bake's appearance is waited for: three
// times the five seconds sl allows for one that, measured on Agni,
// arrives within a second.
const exportBakeWait = 15 * time.Second

// exportLookWait bounds the wait for the region to describe what is
// worn, which Describe waits on.
const exportLookWait = 30 * time.Second

// outfitFormat names the manifest's layout, for the program reading it.
const outfitFormat = "3dview-outfit/1"

// outfitManifest is DIR/outfit.json.  Paths in it are relative to DIR.
type outfitManifest struct {
	Format       string             `json:"format"`
	Name         string             `json:"name"`
	Heard        string             `json:"heard"`
	CofVersion   int                `json:"cof_version"`
	VisualParams []int              `json:"visual_params"`
	Bakes        map[string]string  `json:"bakes"`
	BakeIDs      map[string]string  `json:"bake_ids"`
	Wearables    []exportWearable   `json:"wearables"`
	Attachments  []exportAttachment `json:"attachments"`
	Problems     []string           `json:"problems,omitempty"`
}

type exportWearable struct {
	Name  string `json:"name"`
	Slot  string `json:"slot"`
	Type  string `json:"type"`
	Asset string `json:"asset,omitempty"`
	File  string `json:"file,omitempty"`
}

type exportAttachment struct {
	Name  string       `json:"name"`
	Point int          `json:"point"`
	Where string       `json:"point_name"`
	Prims []exportPrim `json:"prims"`
}

type exportPrim struct {
	Name   string         `json:"name,omitempty"`
	Pos    []float32      `json:"pos"`
	Rot    []float32      `json:"rot"`
	Scale  []float32      `json:"scale"`
	Mesh   string         `json:"mesh,omitempty"`
	MeshID string         `json:"mesh_id,omitempty"`
	Volume map[string]int `json:"volume,omitempty"`
	Faces  []exportFace   `json:"faces"`
}

type exportFace struct {
	Texture    string     `json:"texture,omitempty"`
	TextureID  string     `json:"texture_id"`
	Color      [4]float64 `json:"color"`
	Repeats    [2]float32 `json:"repeats"`
	Offset     [2]float32 `json:"offset"`
	Rot        float32    `json:"rot"`
	Fullbright bool       `json:"fullbright"`
	Glow       float64    `json:"glow,omitempty"`
	Material   string     `json:"material,omitempty"`
	AlphaMode  string     `json:"alpha_mode,omitempty"`
	AlphaCut   *float64   `json:"alpha_cutoff,omitempty"`
}

// exporter fetches each asset once, however many places name it.
type exporter struct {
	sh       *Shell
	dir      string
	files    map[msg.UUID]string
	problems []string
}

func (e *exporter) problem(format string, args ...any) {
	e.problems = append(e.problems, fmt.Sprintf(format, args...))
}

// fetch writes an asset under sub/ as the grid stores it and says where,
// or says nothing and notes why.
func (e *exporter) fetch(ctx context.Context, id msg.UUID, t sl.AssetType, sub, ext string) string {
	if p, ok := e.files[id]; ok {
		return p
	}
	b, err := e.sh.s.Asset(ctx, id, t)
	if err != nil {
		e.problem("%s %s: %v", t, id, err)
		e.files[id] = ""
		return ""
	}
	rel := filepath.Join(sub, id.String()+ext)
	if err := os.WriteFile(filepath.Join(e.dir, rel), b, 0o644); err != nil {
		e.problem("%s: %v", rel, err)
		e.files[id] = ""
		return ""
	}
	e.files[id] = rel
	return rel
}

func cmdExportOutfit(ctx context.Context, sh *Shell, out io.Writer, args []string) error {
	var o exportFlags
	args, done, err := subOptions("export-outfit", &o, out, args)
	if err != nil || done {
		return err
	}
	if len(args) != 0 || o.Out == "" {
		return usageError("export-outfit")
	}
	name := o.Name
	if name == "" {
		name = "Current Outfit"
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	app, err := sh.s.SimAttachments(ctx, msg.UUID{})
	if err != nil {
		return err
	}
	if app == nil || o.Bake {
		if !sh.s.Rebake(ctx, exportBakeWait) {
			return fmt.Errorf("asked for a bake, and no appearance came back within %s", exportBakeWait)
		}
		if app, err = sh.s.SimAttachments(ctx, msg.UUID{}); err != nil {
			return err
		}
	}

	for _, sub := range []string{"bakes", "wearables", "meshes", "textures"} {
		if err := os.MkdirAll(filepath.Join(o.Out, sub), 0o755); err != nil {
			return err
		}
	}
	e := &exporter{sh: sh, dir: o.Out, files: map[msg.UUID]string{}}
	m := outfitManifest{
		Format:      outfitFormat,
		Name:        name,
		Heard:       app.Heard.UTC().Format(time.RFC3339),
		CofVersion:  app.CofVersion,
		Bakes:       map[string]string{},
		BakeIDs:     map[string]string{},
		Wearables:   []exportWearable{},
		Attachments: []exportAttachment{},
	}
	for _, p := range app.VisualParams {
		m.VisualParams = append(m.VisualParams, int(p))
	}

	bakes, err := app.Bakes()
	if err != nil {
		e.problem("bakes: %v", err)
	}
	for _, b := range bakes {
		m.BakeIDs[b.Slot.Name] = b.Texture.String()
		j2c, err := sh.s.FetchBake(ctx, msg.UUID{}, app, b)
		if err != nil {
			e.problem("bake %s: %v", b.Slot.Name, err)
			continue
		}
		rel := filepath.Join("bakes", b.Slot.Name+".j2c")
		if err := os.WriteFile(filepath.Join(o.Out, rel), j2c, 0o644); err != nil {
			return err
		}
		m.Bakes[b.Slot.Name] = rel
	}

	links, err := sh.s.Outfit(ctx)
	if err != nil {
		e.problem("outfit: %v", err)
	}
	for _, l := range links {
		if !l.Found && !l.Folder {
			// Its kind is not known, so it may have been a wearable.
			e.problem("outfit link %s leads to nothing this session found", l.Name)
			continue
		}
		if !l.Wearable {
			continue
		}
		w := exportWearable{Name: l.Name, Slot: l.Slot.String(), Type: l.Kind.String()}
		if it, err := sh.s.OutfitItem(ctx, l); err != nil {
			e.problem("wearable %s: %v", l.Name, err)
		} else {
			w.Asset = it.AssetID.String()
			w.File = e.fetch(ctx, it.AssetID, l.Kind, "wearables", ".bin")
		}
		m.Wearables = append(m.Wearables, w)
	}

	worn, err := sh.s.WornObjects(ctx)
	if err != nil {
		e.problem("attachments: %v", err)
	}
	for _, a := range worn {
		if a.Point >= sl.HUDCenter2 && a.Point <= sl.HUDBottomRight && !o.HUDs {
			continue
		}
		m.Attachments = append(m.Attachments, e.attachment(ctx, a))
	}
	m.Problems = e.problems

	j, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(o.Out, "outfit.json"), append(j, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s: %d visual params, %d of %d bakes, %d wearables, %d attachments, %d problems\n",
		filepath.Join(o.Out, "outfit.json"), len(m.VisualParams), len(m.Bakes), len(bakes),
		len(m.Wearables), len(m.Attachments), len(m.Problems))
	for _, p := range m.Problems {
		fmt.Fprintf(out, "  %s\n", p)
	}
	return nil
}

// attachment is one worn object: each prim where dump puts it, its mesh
// or its shape, and its faces.
func (e *exporter) attachment(ctx context.Context, a *sl.Attached) exportAttachment {
	x := exportAttachment{Name: a.Object.Name, Point: a.Point, Where: sl.AttachPointName(a.Point), Prims: []exportPrim{}}
	desc, err := e.sh.s.Describe(ctx, &a.Object, exportLookWait)
	if err != nil {
		e.problem("attachment %s: %v", a.Object.Name, err)
		return x
	}
	parts, err := e.sh.s.Linkset(ctx, &a.Object)
	if err != nil {
		e.problem("attachment %s: %v", a.Object.Name, err)
	}
	byID := map[string]*sl.Seen{}
	for _, s := range parts {
		byID[s.ID.String()] = s
	}
	for i := range desc.Prims {
		p := desc.Prims[i]
		xp := exportPrim{Name: p.Name, Pos: p.Pos, Rot: p.Rot, Scale: p.Size, Faces: []exportFace{}}
		if s, ok := byID[p.UUID]; ok {
			// The region's sculpt block, not dump's, which writes a mesh
			// as the box it cannot otherwise say.
			if s.Sculpt.Kind == msg.SculptMesh && !s.Sculpt.ID.IsZero() {
				xp.MeshID = s.Sculpt.ID.String()
				xp.Mesh = e.fetch(ctx, s.Sculpt.ID, sl.AssetMesh, "meshes", ".llmesh")
			}
			if xp.Mesh == "" && xp.MeshID == "" && s.Shape != (msg.PrimShape{}) {
				xp.Volume = volume(s.Shape)
			}
			faces, err := e.sh.s.Faces(ctx, &s.Object)
			if err != nil {
				e.problem("faces of %s in %s: %v", p.UUID, a.Object.Name, err)
			}
			for _, f := range faces {
				xp.Faces = append(xp.Faces, e.face(ctx, f))
			}
		}
		x.Prims = append(x.Prims, xp)
	}
	return x
}

func (e *exporter) face(ctx context.Context, f sl.Face) exportFace {
	os, ot := f.OffsetsF()
	x := exportFace{
		TextureID:  f.Texture.String(),
		Repeats:    [2]float32{f.ScaleS, f.ScaleT},
		Offset:     [2]float32{os, ot},
		Rot:        f.RotationRad(),
		Fullbright: f.Fullbright(),
		Glow:       float64(f.Glow) / 255,
	}
	for k := range 4 {
		x.Color[k] = float64(f.Colour[k]) / 255
	}
	if !f.Material.IsZero() {
		x.Material = f.Material.String()
		// A face with no material is left for the reader to decide by
		// the texture, as the viewer does.
		mode, cut, err := e.sh.s.AlphaModeOf(ctx, f)
		if err != nil {
			e.problem("material %s: %v", f.Material, err)
		} else if mode != sl.AlphaModeDefault {
			x.AlphaMode = mode.String()
			if mode == sl.AlphaModeMask {
				c := float64(cut) / 255
				x.AlphaCut = &c
			}
		}
	}
	// A bakes-on-mesh placeholder stays the id it is, which the reader
	// knows; there is nothing to fetch for it.
	switch _, baked := sl.BakedPlaceholder(f.Texture); {
	case baked:
		x.Texture = f.Texture.String()
	case !f.Texture.IsZero() && f.Colour[3] > 0:
		// A face with no opacity at all draws nothing, so its texture
		// is not fetched; texture_id still names it.
		x.Texture = e.fetch(ctx, f.Texture, sl.AssetTexture, "textures", ".j2c")
	}
	return x
}

// volume is a prim's shape as its ObjectUpdate carries it, by the names
// the reader's RawShape gives the fields.
func volume(p msg.PrimShape) map[string]int {
	return map[string]int{
		"path_curve":         int(p.PathCurve),
		"profile_curve":      int(p.ProfileCurve),
		"path_begin":         int(p.PathBegin),
		"path_end":           int(p.PathEnd),
		"path_scale_x":       int(p.PathScaleX),
		"path_scale_y":       int(p.PathScaleY),
		"path_shear_x":       int(p.PathShearX),
		"path_shear_y":       int(p.PathShearY),
		"path_twist":         int(p.PathTwist),
		"path_twist_begin":   int(p.PathTwistBegin),
		"path_radius_offset": int(p.PathRadiusOffset),
		"path_taper_x":       int(p.PathTaperX),
		"path_taper_y":       int(p.PathTaperY),
		"path_revolutions":   int(p.PathRevolutions),
		"path_skew":          int(p.PathSkew),
		"profile_begin":      int(p.ProfileBegin),
		"profile_end":        int(p.ProfileEnd),
		"profile_hollow":     int(p.ProfileHollow),
	}
}
