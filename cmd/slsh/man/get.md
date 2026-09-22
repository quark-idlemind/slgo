`get` saves a texture to disk as a PNG.  `put` is the other
direction.  Between them a texture can be fetched, edited and
uploaded again -- though the upload is a new item and costs L$,
which this does not.

    get Textures/lantern

The argument is an inventory path, relative to the folder the shell
is in, or a uuid.  Anything that is not a texture is refused, a
folder included.  A folder of textures is still a folder.

## Options

**-o, --out** *FILE*

Write here rather than to a file named after the texture.  Used
exactly as given.

**-r, --raw**

Write the JPEG 2000 codestream exactly as the grid stores it, in a
file ending `.j2c`.  That is the form to keep for putting the same
texture back, and it is also what to fall back on when a texture
will not decode -- the refusal says so.

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

## Examples

    get Textures/lantern
    get -o /tmp/wall.png d8467e57-...
    get --raw Textures/lantern

## A link is followed

An outfit folder holds links rather than items.  A link carries the
same name as the thing it points at, and a listing tells the two apart
only by the word `link` in the type column of `ls -l`, so a path taken
from `My Outfits` names a link almost every time.

`get` follows one to the item at the other end, which is what the id
it sends has to be: the grid has no object for a link's id and answers
an id it does not recognise with silence rather than with a refusal.

A link this inventory cannot follow is refused, naming the id it
looked for.  A link outlives what it pointed at, so an outfit put
together years ago may name things that have since been deleted.

See also: `put`, `cat` for a notecard or a script, and `ls` for what
is there.
