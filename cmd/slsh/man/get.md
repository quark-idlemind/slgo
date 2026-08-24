`get` saves a texture to disk as a PNG.  `put` is the other
direction.  Between them a texture can be fetched, edited and
uploaded again -- though the upload is a new item and costs L$,
which this does not.

    get Textures/lantern

The argument is an inventory path, relative to the folder the shell
is in, or a uuid.  Anything that is not a texture is refused, a
folder included.  A folder of textures is still a folder.

## A uuid here is an asset id

Not an item id.  Textures are served by asset id alone, so a texture
on somebody else's object -- named nowhere in this avatar's
inventory -- is fetchable the moment its id is known.  That id comes
from wherever the object was inspected, not from a listing here: no
inventory command prints an asset id, and the third column of
`ls -l` is the item id, which the network does not answer to.  A
texture that is in inventory is named by its path instead, and `get`
looks its asset up itself.

## PNG, or the codestream

The grid stores JPEG 2000.  What is written is a PNG, and the report
says the size it decoded to and how many bytes that came from.

The file is the item's name, with separators replaced, and `.png` or
`.j2c` on the end.  A texture asked for by uuid has no name here, so
the file is called after the uuid.

## Options

**-o, --out** *FILE*

Write here rather than to a file named after the texture.  Used
exactly as given.

**-r, --raw**

Write the JPEG 2000 codestream exactly as the grid stores it, in a
file ending `.j2c`.  That is the form to keep for putting the same
texture back, and it is also what to fall back on when a texture
will not decode -- the refusal says so.

## Examples

    get Textures/lantern
    get -o /tmp/wall.png d8467e57-...
    get --raw Textures/lantern

See also: `put`, `cat` for a notecard or a script, and `ls` for what
is there.
