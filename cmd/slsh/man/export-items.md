`export-items` writes inventory items into a library, one folder each,
so that an offline viewer can dress an avatar in them without the
avatar wearing them.  It changes nothing worn.

    export-items -o ~/library/items "Clothing/Red Dress"
    export-items -o ~/library/items --worn

Each item gets a folder named by its item id, holding `item.json` and
what it names, as the grid stores it:

- A body part or piece of clothing -- a shape, skin, eyes, hair, a
  tattoo, an alpha -- is written whether worn or not: its wearable asset
  as `wearables/ID.bin`, and every texture it names as
  `textures/ID.j2c`, for a viewer that bakes for itself.
- An object is written only while it is worn: its prims, meshes and
  textures as `export-outfit` writes an attachment.  The grid does not
  serve an object's asset to a client, so the region's description of it
  while worn is the only way to read one.  A folder written while it was
  worn is kept when it is not worn now, so each object needs wearing
  once; `--worn` captures everything on at the time.

Each line printed says what became of one item.

## Options

**-o, --out** *DIR*

The library.  Folders in it are replaced when their item is written
again, and kept otherwise.

**-w, --worn**

Every item worn now, as well as any named.
