package msg

// A prim's form, as the protocol carries it.
//
// Four messages carry exactly these eighteen fields under four sets of
// names -- ObjectAdd to rez a prim, ObjectShape to change one,
// ObjectUpdate to describe one, and the compressed update to describe
// one more cheaply -- so the fields live here once and each message
// converts.
//
// Nothing here interprets them: they are the packed bytes, and what
// each one means in a person's terms is sl.Shape's business.
//
// The type itself is in compressed.go, where the compressed update
// decoder that first needed it put it.

// IsZero reports whether nothing has been said about this shape.  A
// real prim never packs to all zeros: even a default box has a profile
// curve and a top size.
func (p PrimShape) IsZero() bool { return p == PrimShape{} }

// ShapeOfUpdate reads the shape out of one object's ObjectUpdate.
func ShapeOfUpdate(d *ObjectUpdate_ObjectData) PrimShape {
	return PrimShape{
		PathCurve: d.PathCurve, ProfileCurve: d.ProfileCurve,
		PathBegin: d.PathBegin, PathEnd: d.PathEnd,
		PathScaleX: d.PathScaleX, PathScaleY: d.PathScaleY,
		PathShearX: d.PathShearX, PathShearY: d.PathShearY,
		PathTwist: d.PathTwist, PathTwistBegin: d.PathTwistBegin,
		PathRadiusOffset: d.PathRadiusOffset,
		PathTaperX:       d.PathTaperX, PathTaperY: d.PathTaperY,
		PathRevolutions: d.PathRevolutions, PathSkew: d.PathSkew,
		ProfileBegin: d.ProfileBegin, ProfileEnd: d.ProfileEnd,
		ProfileHollow: d.ProfileHollow,
	}
}

// FillAdd puts the shape into an ObjectAdd, which is how a prim of this
// form is rezzed.
func (p PrimShape) FillAdd(d *ObjectAdd_ObjectData) {
	d.PathCurve, d.ProfileCurve = p.PathCurve, p.ProfileCurve
	d.PathBegin, d.PathEnd = p.PathBegin, p.PathEnd
	d.PathScaleX, d.PathScaleY = p.PathScaleX, p.PathScaleY
	d.PathShearX, d.PathShearY = p.PathShearX, p.PathShearY
	d.PathTwist, d.PathTwistBegin = p.PathTwist, p.PathTwistBegin
	d.PathRadiusOffset = p.PathRadiusOffset
	d.PathTaperX, d.PathTaperY = p.PathTaperX, p.PathTaperY
	d.PathRevolutions, d.PathSkew = p.PathRevolutions, p.PathSkew
	d.ProfileBegin, d.ProfileEnd = p.ProfileBegin, p.ProfileEnd
	d.ProfileHollow = p.ProfileHollow
}

// FillShape puts the shape into an ObjectShape, which is how an
// existing prim is reshaped.
func (p PrimShape) FillShape(d *ObjectShape_ObjectData) {
	d.PathCurve, d.ProfileCurve = p.PathCurve, p.ProfileCurve
	d.PathBegin, d.PathEnd = p.PathBegin, p.PathEnd
	d.PathScaleX, d.PathScaleY = p.PathScaleX, p.PathScaleY
	d.PathShearX, d.PathShearY = p.PathShearX, p.PathShearY
	d.PathTwist, d.PathTwistBegin = p.PathTwist, p.PathTwistBegin
	d.PathRadiusOffset = p.PathRadiusOffset
	d.PathTaperX, d.PathTaperY = p.PathTaperX, p.PathTaperY
	d.PathRevolutions, d.PathSkew = p.PathRevolutions, p.PathSkew
	d.ProfileBegin, d.ProfileEnd = p.ProfileBegin, p.ProfileEnd
	d.ProfileHollow = p.ProfileHollow
}
