package sl

// A face's GLTF material overrides, as the region last said them.
//
// A face has a GLTF material when its object's render material extra
// parameter names one for it, and a script (PRIM_GLTF_*) or a build tool
// can then override single fields of it for that face: base colour,
// emissive colour, metallic and roughness factors, alpha mode and cutoff,
// double sided, texture ids and texture transforms.  The region tells a
// session of them in a GenericStreamingMessage, but only a session that
// asked the seed for ModifyMaterialParams, which every session here does.
// A face with no GLTF material takes no override: a script's setting is
// accepted and ignored, and nothing is sent.
//
// The session holding the connection -- slgod, or this process -- keeps
// them, from login and whether or not anything is watching, since the
// region says every override in view at once when the avatar arrives and
// says none of them again.  They are kept with the object they are on
// (agent.Object.GLTF) and read here with the rest of what is known of it,
// which is how a client behind slgod gets them: through Objects, a field
// of each object, and not through a subscription.
// Why: doc/gltf.md

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// OverridesCap is the capability a session must have asked for, and the
// region offered, to be sent a face's GLTF material overrides.  It is in
// agent.DefaultCaps; the region sends overrides to a session that asked
// the seed for it and to no other, and the capability itself is never
// used.
const OverridesCap = "ModifyMaterialParams"

// ErrNoOverridesCap is a session that holds no ModifyMaterialParams
// capability, so the region has sent it no overrides: the region does not
// offer it, or the daemon the session is attached to is older than this
// package and did not ask, or the session logged in before it was
// upgraded.  A face reads as having none, which is not the same as having
// none, so a read says so rather than answer.
var ErrNoOverridesCap = errors.New("sl: this session holds no " + OverridesCap + " capability, so the region has sent it no GLTF material overrides")

// A FaceGLTF is what is known of one face's GLTF material: the id of the
// material, which is the zero id for a face with none, and what has been
// overridden on it, nil for nothing.
type FaceGLTF struct {
	Material msg.UUID
	Override *msg.GLTFOverride
}

// GLTFMaterial is the id of the GLTF material the object's face has, or the
// zero id for a face with none.
func (s *Seen) GLTFMaterial(face int) msg.UUID { return s.RenderMaterials[face] }

// GLTFOverride is what the region last said is overridden on the object's
// face, or nil for nothing.  Never nil for a face with a field set; never
// to be written through.
func (s *Seen) GLTFOverride(face int) *msg.GLTFOverride { return s.GLTF[face] }

// FaceGLTF is the face's GLTF material and its override.
func (s *Seen) FaceGLTF(face int) FaceGLTF {
	return FaceGLTF{Material: s.GLTFMaterial(face), Override: s.GLTFOverride(face)}
}

// HoldsOverrides is whether the session holds the capability that makes
// the region send it GLTF material overrides.  A session that does not has
// none to read, whatever the faces have.
func (w *Session) HoldsOverrides() bool { return w.b.HasCap(OverridesCap) }

// GLTFOverrides is the overrides on each face of the object that has any,
// by face, as the region last said them: an empty map for an object with
// none, and no entry for a face with none.  A session that holds no
// ModifyMaterialParams capability is ErrNoOverridesCap.
//
// What the region said before the avatar was in range of the object, or
// before this session was up, is here too if the session was up at
// login: they are kept, not watched for.
func (w *Session) GLTFOverrides(ctx context.Context, o *Object) (map[int]*msg.GLTFOverride, error) {
	s, err := w.seenForGLTF(ctx, o)
	if err != nil {
		return nil, err
	}
	out := make(map[int]*msg.GLTFOverride, len(s.GLTF))
	for f, ov := range s.GLTF {
		out[f] = ov
	}
	return out, nil
}

// FaceGLTF is one face's GLTF material and override.  See GLTFOverrides.
func (w *Session) FaceGLTF(ctx context.Context, o *Object, face int) (FaceGLTF, error) {
	s, err := w.seenForGLTF(ctx, o)
	if err != nil {
		return FaceGLTF{}, err
	}
	return s.FaceGLTF(face), nil
}

func (w *Session) seenForGLTF(ctx context.Context, o *Object) (*Seen, error) {
	if o == nil {
		return nil, fmt.Errorf("sl: nothing to look at")
	}
	if !w.HoldsOverrides() {
		return nil, ErrNoOverridesCap
	}
	// Not ObjectByID, which asks the region for the name of an object
	// nothing has named and waits for it: overrides need no name.
	found, err := w.fetch(ctx, "", o.ID.String())
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, fmt.Errorf("sl: %s is not in the region", o)
	}
	return found[0], nil
}

// gltfPoll is how often WaitGLTF looks, which is not a cost: it reads what
// the session already holds.
const gltfPoll = 100 * time.Millisecond

// WaitGLTF waits until the face's GLTF material and override satisfy ok,
// polling what the session holds, and returns the first that does.  The
// region says an override a few seconds after a script sets it (about
// three, measured), so a caller that has just asked for a change waits
// for it rather than reading once.  Running out of the timeout is
// ErrTimeout.
func (w *Session) WaitGLTF(ctx context.Context, o *Object, face int, timeout time.Duration, ok func(FaceGLTF) bool) (FaceGLTF, error) {
	var got FaceGLTF
	err := poll(ctx, timeout, gltfPoll, fmt.Sprintf("face %d of %s to have the GLTF material state asked for", face, o), func(ctx context.Context) (bool, error) {
		f, err := w.FaceGLTF(ctx, o, face)
		if err != nil {
			return false, err
		}
		got = f
		return ok(f), nil
	})
	return got, err
}

// renderMaterialsFromPB and gltfFromPB are renderMaterialsPB and gltfPB's
// inverses in internal/server.  An entry whose id will not read, or whose
// override says nothing, is left out.
func renderMaterialsFromPB(ms []*pb.PrimFaceMaterial) map[int]msg.UUID {
	var out map[int]msg.UUID
	for _, m := range ms {
		id := parseUUIDOrZero(m.GetMaterial())
		if id.IsZero() {
			continue
		}
		if out == nil {
			out = map[int]msg.UUID{}
		}
		out[int(m.GetFace())] = id
	}
	return out
}

func gltfFromPB(ps []*pb.PrimGLTFOverride) map[int]*msg.GLTFOverride {
	var out map[int]*msg.GLTFOverride
	for _, p := range ps {
		o := &msg.GLTFOverride{
			Metallic:    p.Metallic,
			Roughness:   p.Roughness,
			AlphaCutoff: p.AlphaCutoff,
			DoubleSided: p.DoubleSided,
		}
		if p.AlphaMode != nil {
			am := msg.GLTFAlphaMode(*p.AlphaMode)
			o.AlphaMode = &am
		}
		if len(p.BaseColour) == 4 {
			o.BaseColour = &[4]float32{p.BaseColour[0], p.BaseColour[1], p.BaseColour[2], p.BaseColour[3]}
		}
		if len(p.Emissive) == 3 {
			o.Emissive = &[3]float32{p.Emissive[0], p.Emissive[1], p.Emissive[2]}
		}
		for i, t := range p.Textures {
			if id := parseUUIDOrZero(t); i < msg.GLTFSlots && !id.IsZero() {
				o.Textures[i] = &id
			}
		}
		for i, t := range p.Transforms {
			if i >= msg.GLTFSlots {
				break
			}
			if len(t.GetOffset()) == 2 {
				o.Transforms[i].Offset = &[2]float32{t.Offset[0], t.Offset[1]}
			}
			if len(t.GetScale()) == 2 {
				o.Transforms[i].Scale = &[2]float32{t.Scale[0], t.Scale[1]}
			}
			o.Transforms[i].Rotation = t.Rotation
		}
		if o.Empty() {
			continue
		}
		if out == nil {
			out = map[int]*msg.GLTFOverride{}
		}
		out[int(p.GetFace())] = o
	}
	return out
}
