`putmesh` uploads a mesh, described by a JSON model on this machine, as
an object in inventory.  It can cost L$, and it asks the price before
it pays.

    putmesh --quote cube.json
    putmesh --max 20 cube.json

The file is a JSON model: faces, each with positions in meters,
normals, texture coordinates and triangles, and for a rigged mesh the
weights of each vertex and a skin naming the joints with their bind
matrices.  doc/mesh.md says what each field is.

The grid names the price of a mesh only when asked, and the price
depends on the model.  So the upload asks first, says the price, and
goes ahead only if it is no more than `--max`.  Without `--max` only a
free upload goes ahead.  `--quote` asks and stops there; asking costs
nothing.

Every level of detail is the same model, and the physics shape is its
bounding box.

## Options

**-n, --name** *NAME*

Call the item this instead of the model's name.

**-d, --desc** *TEXT*

The item's description.

**-f, --folder** *PATH*

The inventory folder it lands in.  Without it, `Objects`.

**-m, --max** *L$*

The most to pay.  A price above it is refused before anything is sent.

**-q, --quote**

Ask the grid the price, print it with the weights it was worked out
from, and stop.

**-o, --out** *FILE*

Write the encoded mesh asset here instead of uploading.

**-N, --dry-run**

Encode the model and say how big it is, without asking the grid
anything.
