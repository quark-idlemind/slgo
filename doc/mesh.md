# Uploading a mesh

`mesh.Encode` writes Second Life's mesh asset from a model given as
JSON, and `sl.Session.UploadMesh` uploads it the way the viewer does;
`putmesh` in slsh is the two together. This page is what the format and
the exchange are, where each was read from, and what was measured. The
viewer is Firestorm 885631b93a, `indra/` relative.

Measured on Aditi, the beta grid, on 2026-11-11, with two test avatars
and two invented models: a 0.5 m cube of 12 triangles, and an open tube
around the torso of 256 triangles, rigged to `mPelvis`, `mTorso` and
`mChest`. **No mesh has yet been uploaded through this code**: both
accounts were refused at the last step for want of the right to upload
mesh, which is the account's and not the request's (below). Everything
up to that step was measured; the asset's acceptance by the grid was
not.

## The model file

    {
      "name": "test cube",
      "faces": [
        {
          "positions": [[x, y, z], ...],
          "normals":   [[x, y, z], ...],
          "uvs":       [[u, v], ...],
          "indices":   [i0, i1, i2, ...],
          "weights":   [[[joint, weight], ...], ...]
        }
      ],
      "skin": {
        "joints":       ["mPelvis", "mTorso", ...],
        "bind_shape":   [16 numbers],
        "inverse_bind": [[16 numbers], ...]
      }
    }

One to eight faces, one per material. Positions are meters in the
model's own frame, Z up. Normals and uvs are optional and, when given,
one per position. Indices are triangles, at most 65536 vertices a face
since an index is 16 bits. `weights` and `skin` are for a rigged mesh:
each vertex has one to four pairs of an index into `joints` and a
weight, summing to one.

The matrices are in the viewer's `LLMatrix4` order: sixteen numbers row
by row, the translation in elements 12 to 14, applied to a row vector
on the left. That is the transpose of Collada's `<matrix>`, and a file
that says `"matrix_order": "collada"` is transposed on reading, as
`LLDAELoader::processDomModel` does (`llprimitive/lldaeloader.cpp:1296`).

## The asset

`LLModel::writeModel` and `writeModelToStream`
(`llprimitive/llmodel.cpp:830` and `:1123`). A binary LLSD header --
`LLSDSerialize::toBinary`, with no `<? LLSD/Binary ?>` line in front --
then each section, zlib-deflated binary LLSD (`zip_llsd`,
`llcommon/llsdserialize.cpp:2148`). The header maps each section's name
to its `offset`, counted from the end of the header, and its `size`.
Sections follow in the order `skin`, `physics_convex`, `lowest_lod`,
`low_lod`, `medium_lod`, `high_lod`.

A level of detail is an array of faces, each a map:

- `Position`: three little-endian 16-bit integers a vertex, each a
  fraction of `PositionDomain`'s `Min` to `Max`.
- `Normal`: the same, over -1 to 1.
- `TexCoord0`: two a vertex, over `TexCoord0Domain`.
- `TriangleList`: 16-bit indices.
- `Weights`, rigged only: per vertex, up to four of a joint byte and a
  16-bit weight, and a `0xFF` after fewer than four.
- `NormalizedScale`: the model's size, below.

The positions are not the modeler's. `LLModel::normalizeVolumeFaces`
(`llmodel.cpp:203`) moves the model's center to the origin and shrinks
each axis to fit a unit cube, and scales the normals the opposite way.
The size goes back on at upload, as the instance's `scale`. A rigged
mesh is drawn through its skin rather than its instance, so the DAE
loader multiplies the undoing into the bind shape matrix
(`lldaeloader.cpp:1276-1308`), and so does `mesh.Encode`: a stored
vertex times the stored bind shape matrix is where the modeler put it
before the bind shape applied.

What `mesh.Encode` does differently from the viewer, and why:

- Every level of detail is the same geometry. The viewer makes lower
  ones or takes them from the modeler; a model that streams the same
  triangles at every distance costs more, which the quote shows.
- The physics shape is the bounding box: the eight corners of the unit
  cube as `physics_convex`'s `BoundingVerts`. The viewer's is the convex
  hull of the physics level of detail.
- Each face's `PositionDomain` is its own box. The viewer writes the
  whole model's into every face. The viewer's reader takes each face's
  domain as it finds it, so both read back the same; not measured
  against the grid.

## The exchange

`LLMeshUploadThread` (`newview/llmeshrepository.cpp`): `wholeModelToLLSD`
at `:3020`, `packModelIntance` at `:2811`, the fee request at `:3277`,
the upload at `:3215` and both answers at `:3322`.

The first request goes to `NewFileAgentInventory`, like any file upload,
but carries the model rather than a promise of one: `asset_type` "mesh",
`inventory_type` "object", the folder, name and permissions, and
`asset_resources` -- the encoded asset in `mesh_list`, an empty
`texture_list`, and one entry in `instance_list` saying which mesh, its
scale, its faces' colors and its physics shape type. No
`expected_upload_cost` is sent: the grid names the price.

It answers with `state` "upload", an `uploader` URL, an `upload_price`
and a `data` map of the weights the price came from. Measured:

| model | price | streaming | physics | breakdown |
|---|---|---|---|---|
| cube, 12 triangles | L$11 | 0.05 | 0.36 | model 10, instance 1 |
| rigged tube, 256 triangles | L$17 | 5.13 | 0.36 | model 10, instance 1, streaming 6 |

Asking charged nothing: the balance was the same afterwards.

The second request posts `asset_resources` alone, as LLSD XML, to the
uploader. A completed upload answers `state` "complete" with
`new_inventory_item` and `new_asset`; the viewer then corrects the item's
asset type to "object" (`:3375`).

## The right to upload mesh

Both test accounts were refused at the second request:

    state "failure": Mesh upload status is not valid. (Upload_MeshUploadStatus)

after a quote that had succeeded, and neither balance moved. The quote
does not check the right; the upload does.

The viewer asks first. It reads the `MeshUploadFlag` capability with a
GET (`newview/llfloatermodeluploadbase.cpp:43`) and allows the upload
when `mesh_upload_status` is "valid" or empty
(`newview/llfloatermodelpreview.cpp:2244-2250`), and otherwise shows
"You don't have rights to upload mesh models."
(`skins/default/xui/en/floater_model_preview.xml`). slgod now asks for
that capability and `UploadMesh` reads it before quoting. Measured for
both accounts: `mesh_upload_status` "invalid", and `putmesh` refuses
with nothing sent.

How an account gets the right is not something this tree can do or has
watched happen. It is granted to the account by Linden Lab, not
requested by a client.
