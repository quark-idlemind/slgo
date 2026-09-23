What the simulator said about the region when the avatar arrived: its
name and key, who owns it, its access rating, the height of the water,
and which product it runs as.  `where` is the same question asked about
the avatar rather than about the land, and `objects` is what is
standing on it.

    look
    Testville
      id       d8467e57-...
      owner    7e7c7e57-...
      access   general
      water    20.0m
      product  Estate / Full Region
      objects  195 described so far

The access rating is printed in the words a viewer shows -- general,
moderate, adult -- and as `access N` where the grid sends one nothing
here has a word for, which is the same rendering `regions` gives it.
The label and that rendering double up, so an unknown rating reads
`access   access 21`.

A region describes itself as the avatar arrives, and never again.  A
shell attached to a daemon therefore gets the same answer as one that
logged in itself, and a shell that attached an hour late gets it too.
A refusal here means the handshake has not arrived yet, which happens
for a second or two after a login and not otherwise.

## The count of objects is a count of what has been heard

The last line is how many objects this session has been told about, and
that is not how many are in the region.

The simulator describes what is near the camera and nothing else, so an
object beyond the draw distance is not merely unnamed here, it is
unknown.  The count grows as the avatar moves about, and it falls
again as things drift out of range -- about half a minute after they
go, since something out of range is kept that long in case the camera
it was judged by was the moment's wrong one.  Read it as what the session is
holding now rather than as a census.

## Examples

    look

See also: `where`, `who`, `objects`, `regions` for the same question
asked about somewhere else on the grid, `caps` and `features` for what
this simulator offers a client, and `lsl` for the language it
implements.
