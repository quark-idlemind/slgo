`export-outfit` writes what the avatar looks like to a directory, as the
grid stores it, for a program that draws it offline.

    export-outfit -o ~/outfits/today

Everything is written as the grid sent it, nothing converted:

- `outfit.json`, the manifest, which names everything else by its path
  in the directory.
- `bakes/SLOT.j2c`: the baked skin and system clothing of each region of
  the body -- `head`, `upper`, `lower`, `eyes`, `skirt`, `hair`,
  `leftarm`, `leftleg`, `aux1` to `aux3` -- as JPEG 2000 codestreams.
- `wearables/ID.bin`: each body part and piece of clothing the outfit
  names (shape, skin, eyes, hair and the rest), as the wearable asset.
- `meshes/ID.llmesh`: the mesh asset of each mesh prim worn.
- `textures/ID.j2c`: each texture a worn prim's faces use.

The manifest has the shape -- the appearance's visual parameters, one
number each in the order the simulator sent them -- and the bakes, by
slot, with the id of each.  Each attachment is listed with its point and
its prims: where each sits in the point's frame, as `dump` gives it, its
size, its mesh or, for a prim that is not a mesh, the shape fields of
its update as the region sent them, and its faces.  A face names its texture file, or for a
bakes-on-mesh face the placeholder id that says which bake it is drawn
with, and its alpha mode and mask cutoff when its material has one.

The shape and the bakes are from the appearance the simulator last sent
about this avatar, so they are of the outfit as last baked.  An avatar
that has not been baked since it logged in has none, and is asked for a
bake first; `--bake` asks for one anyway.  Asking for a bake changes
nothing worn.

What could not be fetched is listed under `problems` in the manifest
and printed, and the rest is written regardless.  HUDs are left out.

## Options

**-o, --out** *DIR*

The directory to write.  It is made if it is not there, and files in it
are replaced.

**-n, --name** *NAME*

The outfit's name in the manifest.  Without it, `Current Outfit`.

**-b, --bake**

Ask the grid for a fresh bake of what is worn before reading it.

**--huds**

Include HUD attachments.
