# What a face shows

A texture is uploaded whole. The face then turns it, tiles it and slides
it, and the part that hangs off the face is not drawn. `Face.Picture` in
`sl` does that to a texture already in hand, and `Session.FacePicture`
fetches an object's texture and does it for one face. A test that asks
where a label is on a sign needs the picture the face shows, not the
file that was uploaded.

```go
pic, err := s.FacePicture(ctx, obj, 0)
```

The object is an `*sl.Object` the caller already has, from `ObjectsNamed`
or from a binding. `FacePicture` reads its faces with the session's own
look at the appearance, and fetches the texture with `TextureImage`.

## The mapping

`Face.Picture` maps the texture the way `Face.SurfaceToTexture` does:
about the middle of the face it turns, then tiles by the repeats, then
shifts by the offset, and crops to the face. One repeat with no offset
and no rotation is the texture unchanged. A repeat of 0.5 keeps the
middle half. A repeat of 2 lays two copies, centred, so the picture
starts on the right half of the texture. A positive offset samples
further along the texture, and the part that slides off one edge wraps
to the other. A negative repeat flips. Half a turn (`Rotation` 16384)
swaps both ends, and a quarter turn (`Rotation` 8192) takes

```
A B      to      B D
C D              A C
```

A wide texture turned a quarter is tall: a 20 by 10 becomes 10 by 20,
one texel per pixel of the face. A side is not drawn past
`sl.MaxDecodeSize`; a repeat that would pass it is drawn smaller and is
still the whole face. The nearest texel is taken, so the crop lands on
whole pixels, and a sub-image contributes its own bounds and not the
picture it was cut from. Row 0 of the picture is the high end of T, since
T runs up the face and an image's rows run down the page.

None of this was measured against a face on the grid; the tests check the
mapping against the viewer's.

## Which faces are refused

`FacePicture` refuses a face whose picture the texture entry alone does
not say, with an error that wraps a sentinel and names the face:

- `ErrPlanarFace`: the face maps its texture by planar projection, so its
  offset, repeats and rotation are not the picture on it.
- `ErrAnimatedFace`: a texture animation is running on the face, for it
  or, when the animation names no face the object has, for every face.
  `sl` refuses to invent a touch coordinate for such a face for the same
  reason, and `FacePicture` asks the same question.
- `ErrNoTexture`: the face wears none, so there is no picture. A caller
  that wants every textured face skips this one.

`Session.FaceRefusals` asks the same question of every face of an object at once, without fetching a texture, and gives each face's error or nil: it is how Slate's `touch ... element` finds the faces it must refuse ([Element records](slate-language.md#element-records)).

A face with no texture is `ErrNoTexture` before it is asked whether it
is planar or animated, since nothing is drawn on it either way. A face
number the object does not have is an error of its own.

The texture as uploaded, before the face turns, tiles or slides it, is
`Faces` and `TextureImage`, and a planar or an animated face has one.
