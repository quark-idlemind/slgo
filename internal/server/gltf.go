package server

// A face's GLTF material and the overrides on it, as ObjectInfo says them.
// Why: doc/gltf.md#how-slate-reaches-it

import (
	"slices"

	"github.com/quark-idlemind/slgo/msg"
	pb "github.com/quark-idlemind/slgo/proto/slgov1"
)

// renderMaterialsPB is an object's GLTF material for each face that has
// one, in face order.
func renderMaterialsPB(m map[int]msg.UUID) []*pb.PrimFaceMaterial {
	var out []*pb.PrimFaceMaterial
	for face, id := range m {
		out = append(out, &pb.PrimFaceMaterial{Face: uint32(face), Material: id.String()})
	}
	slices.SortFunc(out, func(a, b *pb.PrimFaceMaterial) int { return int(a.Face) - int(b.Face) })
	return out
}

// gltfPB is an object's overrides in face order, with each field a face
// did not set left absent.
func gltfPB(m map[int]*msg.GLTFOverride) []*pb.PrimGLTFOverride {
	var out []*pb.PrimGLTFOverride
	for face, o := range m {
		if o.Empty() {
			continue
		}
		p := &pb.PrimGLTFOverride{
			Face:        uint32(face),
			Metallic:    o.Metallic,
			Roughness:   o.Roughness,
			AlphaCutoff: o.AlphaCutoff,
			DoubleSided: o.DoubleSided,
		}
		if o.AlphaMode != nil {
			am := int32(*o.AlphaMode)
			p.AlphaMode = &am
		}
		if o.BaseColour != nil {
			p.BaseColour = slices.Clone(o.BaseColour[:])
		}
		if o.Emissive != nil {
			p.Emissive = slices.Clone(o.Emissive[:])
		}
		if slices.ContainsFunc(o.Textures[:], func(t *msg.UUID) bool { return t != nil }) {
			p.Textures = make([]string, msg.GLTFSlots)
			for i, t := range o.Textures {
				if t != nil {
					p.Textures[i] = t.String()
				}
			}
		}
		if slices.ContainsFunc(o.Transforms[:], func(t msg.GLTFTransform) bool {
			return t.Offset != nil || t.Scale != nil || t.Rotation != nil
		}) {
			p.Transforms = make([]*pb.PrimGLTFTransform, msg.GLTFSlots)
			for i, t := range o.Transforms {
				x := &pb.PrimGLTFTransform{Rotation: t.Rotation}
				if t.Offset != nil {
					x.Offset = slices.Clone(t.Offset[:])
				}
				if t.Scale != nil {
					x.Scale = slices.Clone(t.Scale[:])
				}
				p.Transforms[i] = x
			}
		}
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *pb.PrimGLTFOverride) int { return int(a.Face) - int(b.Face) })
	return out
}
