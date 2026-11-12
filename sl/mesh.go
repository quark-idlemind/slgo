package sl

// Uploading a mesh.
//
// The same capability as any other file upload, NewFileAgentInventory,
// but not the same exchange.  The first request carries the whole model
// -- the encoded mesh, and an instance of it saying how big it is -- and
// the grid answers with what it will charge rather than being told; the
// second sends the model again, as LLSD, to the uploader URL.  What is
// sent is what the viewer's LLMeshUploadThread sends
// (newview/llmeshrepository.cpp, wholeModelToLLSD and packModelIntance).
//
// Why: doc/mesh.md

import (
	"context"
	"fmt"

	"github.com/quark-idlemind/slgo/agent"
	"github.com/quark-idlemind/slgo/llsd"
	"github.com/quark-idlemind/slgo/mesh"
	"github.com/quark-idlemind/slgo/msg"
)

// MeshUpload says what to make of an encoded mesh.
type MeshUpload struct {
	Name string
	Desc string

	// Folder is where the object lands; zero means Objects.
	Folder msg.UUID

	// NextOwner zero means PermAll, as for Upload.
	NextOwner uint32
	Group     uint32
	Everyone  uint32

	// MaxPrice is the most the caller will pay.  The grid names the
	// price only when asked, and UploadMesh asks first and refuses a
	// price above this before anything is charged.  Zero pays nothing,
	// so only a quote of L$0 goes ahead.
	MaxPrice int
}

// MeshQuote is the grid's answer to the first half: the price, and
// where to send the model to pay it.
type MeshQuote struct {
	Price    int
	Uploader string

	// Data is the grid's breakdown of the cost -- its "data" map, with
	// the streaming, physics and server weights -- as it sent it.
	Data map[string]any
	Body []byte
}

// LL_MCODE_WOOD and PHYSICS_SHAPE_CONVEX_HULL, which the viewer sends for
// an instance with no physics model of its own.
const (
	meshMaterialWood     = 3
	meshPhysicsShapeHull = 2
)

// meshModel is the request the viewer's wholeModelToLLSD makes, and
// resources the asset_resources inside it that the uploader is sent.
func (w *Session) meshModel(ctx context.Context, u MeshUpload, faces int, e *mesh.Encoded) (model, resources map[string]any, folder msg.UUID, err error) {
	if u.Name == "" {
		return nil, nil, folder, fmt.Errorf("sl: a mesh upload needs a name")
	}
	folder, textures := u.Folder, u.Folder
	if folder.IsZero() {
		if folder, err = w.Folder(ctx, systemFolder[AssetMesh]); err != nil {
			return nil, nil, folder, err
		}
		if textures, err = w.Folder(ctx, systemFolder[AssetTexture]); err != nil {
			return nil, nil, folder, err
		}
	}
	faceList := make([]any, faces)
	for i := range faceList {
		faceList[i] = map[string]any{
			"diffuse_color": []any{1.0, 1.0, 1.0, 1.0},
			"fullbright":    false,
		}
	}
	instance := map[string]any{
		"position":           []any{0.0, 0.0, 0.0},
		"rotation":           []any{0.0, 0.0, 0.0, 1.0},
		"scale":              []any{e.Scale[0], e.Scale[1], e.Scale[2]},
		"material":           meshMaterialWood,
		"physics_shape_type": meshPhysicsShapeHull,
		"mesh":               0,
		"mesh_name":          u.Name,
		"face_list":          faceList,
	}
	resources = map[string]any{
		"mesh_list":     []any{e.Asset},
		"texture_list":  []any{},
		"instance_list": []any{instance},
		"metric":        "MUT_Unspecified",
		"source_format": map[string]any{"high": 0},
	}
	next := u.NextOwner
	if next == 0 {
		next = PermAll
	}
	desc := u.Desc
	if desc == "" {
		desc = "(No Description)"
	}
	model = map[string]any{
		"folder_id":         llsd.UUID(folder.String()),
		"texture_folder_id": llsd.UUID(textures.String()),
		"asset_type":        "mesh",
		"inventory_type":    "object",
		"name":              u.Name,
		"description":       desc,
		"next_owner_mask":   int(int32(next)),
		"group_mask":        int(int32(u.Group)),
		"everyone_mask":     int(int32(u.Everyone)),
		"asset_resources":   resources,
	}
	return model, resources, folder, nil
}

// QuoteMesh asks what uploading a mesh would cost, and pays nothing:
// the first half writes nothing and charges nothing, as for any upload.
func (w *Session) QuoteMesh(ctx context.Context, u MeshUpload, m *mesh.Model, e *mesh.Encoded) (*MeshQuote, error) {
	model, _, _, err := w.meshModel(ctx, u, len(m.Faces), e)
	if err != nil {
		return nil, err
	}
	return w.quoteMesh(ctx, u.Name, model)
}

func (w *Session) quoteMesh(ctx context.Context, name string, model map[string]any) (*MeshQuote, error) {
	req, err := llsd.Encode(model)
	if err != nil {
		return nil, err
	}
	body, err := w.describeUpload(ctx, UploadCap, req)
	if err != nil {
		return nil, err
	}
	m := llsd.Map(decodeLLSD(body))
	q := &MeshQuote{Body: body}
	if m == nil {
		return q, fmt.Errorf("sl: %s: %s answered with no map: %s", name, UploadCap, snippet(body))
	}
	if state := llsd.String(m, "state"); state != "upload" {
		return q, fmt.Errorf("sl: %s: the grid will not take the mesh (state %q): %s", name, state, meshError(m, body))
	}
	q.Uploader = llsd.String(m, "uploader")
	q.Price = int(llsd.Int(m, "upload_price"))
	q.Data = llsd.Map(m["data"])
	if q.Uploader == "" {
		return q, fmt.Errorf("sl: %s: %s gave no uploader: %s", name, UploadCap, snippet(body))
	}
	return q, nil
}

// meshError is what a refusal says.  The mesh service puts it in an
// "error" map, with a message and an identifier, where the other upload
// paths put a bare message.
func meshError(m map[string]any, body []byte) string {
	if e := llsd.Map(m["error"]); e != nil {
		s := llsd.String(e, "message")
		if id := llsd.String(e, "identifier"); id != "" {
			s += " (" + id + ")"
		}
		if s != "" {
			return s
		}
	}
	if s := llsd.String(m, "message"); s != "" {
		return s
	}
	return snippet(body)
}

// UploadMesh uploads an encoded mesh as an object in inventory, having
// first asked its price and held it to u.MaxPrice.
//
// It returns the quote whatever happens after it, and the UploadResult
// once the model has been sent, since by then it may have been paid for.
// The item is the one found in the folder afterwards, as UploadAsset
// finds its own.
func (w *Session) UploadMesh(ctx context.Context, u MeshUpload, m *mesh.Model, e *mesh.Encoded) (*Item, *UploadResult, *MeshQuote, error) {
	model, resources, folder, err := w.meshModel(ctx, u, len(m.Faces), e)
	if err != nil {
		return nil, nil, nil, err
	}
	// As the viewer does: an empty status, or none to be had, lets the
	// grid decide.
	if st, err := w.MeshUploadStatus(ctx); err == nil && st != "" && st != "valid" {
		return nil, nil, nil, fmt.Errorf("sl: %s: this account may not upload mesh (mesh_upload_status %q); "+
			"nothing was sent or paid", u.Name, st)
	}
	q, err := w.quoteMesh(ctx, u.Name, model)
	if err != nil {
		return nil, nil, q, err
	}
	if q.Price > u.MaxPrice {
		return nil, nil, q, fmt.Errorf("sl: %s: the grid wants L$%d, more than the L$%d allowed; nothing was sent or paid",
			u.Name, q.Price, u.MaxPrice)
	}

	body, err := llsd.Encode(resources)
	if err != nil {
		return nil, nil, q, err
	}
	second, err := w.capDo(ctx, agentCapPost(q.Uploader, body))
	if err != nil {
		return nil, nil, q, err
	}
	res := uploadResult(second)
	if res.State != "complete" || res.NewItem.IsZero() {
		m := llsd.Map(decodeLLSD(second))
		return nil, res, q, fmt.Errorf("sl: %s: the mesh upload did not complete (state %q): %s",
			u.Name, res.State, meshError(m, second))
	}

	items, err := w.FolderItems(ctx, folder)
	if err != nil {
		return nil, res, q, fmt.Errorf("sl: %s uploaded as %s, but reading the folder back failed: %w", u.Name, res.NewItem, err)
	}
	for _, it := range items {
		if it.ID == res.NewItem {
			return it, res, q, nil
		}
	}
	return nil, res, q, fmt.Errorf("sl: %s uploaded as item %s, asset %s, but it is not in the folder it was uploaded to",
		u.Name, res.NewItem, res.NewAsset)
}

// MeshUploadCap is the capability that says whether the account may
// upload mesh.
const MeshUploadCap = "MeshUploadFlag"

// MeshUploadStatus is the account's mesh_upload_status, which the
// viewer reads before it offers to upload a mesh: "valid" when the
// account may.  An account that may not is refused at the second half
// of an upload, after the price has been quoted, with "Mesh upload
// status is not valid."
func (w *Session) MeshUploadStatus(ctx context.Context) (string, error) {
	body, err := w.capDo(ctx, agent.CapRequest{Cap: MeshUploadCap, Method: "GET"})
	if err != nil {
		return "", err
	}
	m := llsd.Map(decodeLLSD(body))
	if m == nil {
		return "", fmt.Errorf("sl: %s answered with no map: %s", MeshUploadCap, snippet(body))
	}
	return llsd.String(m, "mesh_upload_status"), nil
}
