`put` uploads a picture from this machine as a texture in inventory.
It costs L$ every time.  `get` is the other direction and does not.

    put photo.jpg

The argument is a file.  PNG, JPEG, GIF, BMP and TIFF are read, and
the orientation a camera left in the file is applied.  A JPEG 2000
codestream (`.j2c` and its relatives) goes up untouched.

Each side of the picture must be a power of two and at most 2048.
Almost nothing already is, so the picture is resampled and the report
says from what size to what.  A side above 2048 comes down to it
whatever the rounding says.  Nothing is resized when both sides are
already legal.

The fee is ten lindens up to a megapixel of area, 1024x1024 included.
Above that it is fifty -- but that larger figure belongs to the account
rather than to the grid, and fifty is what a beta grid wanted on the
day it was measured.  Nothing here reads the real price, so the figure
is not sent hopefully: it travels with the upload, the simulator
compares it against its own, and a request naming the wrong one is
refused.  An account whose large-texture price is not fifty therefore
gets a refusal naming the right number rather than a wrong bill.

The grid charges before it looks, so a refusal afterwards has still
cost the fee.  Every check it would make is made here first; `-N` stops
before anything is sent.

## Options

**-n, --name** *NAME*

Call the item this instead of the file's name.

**-d, --desc** *TEXT*

The item's description.

**-f, --folder** *PATH*

The inventory folder it lands in.  Without it, `Textures`.

**-N, --dry-run**

Print what would go up, how many bytes it is, and the fee, then stop.

**-o, --out** *FILE*

Write the converted picture here instead of uploading.  The extension
picks the format, so the rounding and the filter can be looked at
without paying.

**--round** *MODE*

How both sides round to a power of two: `nearest`, `up` or `down`.
Without it the rounding is the viewer's, biased downwards -- a side
goes up only past 1.75 times the power of two below it, so 800 becomes
512 and 900 becomes 1024.

**--round-x** *MODE*

Round the width only.

**--round-y** *MODE*

Round the height only.

**--filter** *NAME*

How the picture is resampled.  The default is `lanczos`.  Photographs
want that; pixel art and masks want `nearest`; diagrams want
`catmullrom`.  The filter only matters when the picture is resized.

**--filters**

List the filters and what each is good for, then stop.

**-l, --lossless**

Store every pixel, at several times the size.  Below an area of
128x128 nothing is compressed whether it was asked for or not, so a
512x32 banner is lossless as surely as a 128x128 square is.

**--ratio** *N*

Compress about *N*:1 rather than the default 8.  Compression does not
change the fee; the price is settled by the dimensions.

## Examples

    put -N photo.jpg
    put --filter nearest --round up icons/heart.png
    put --name "wall panel" -f Textures/panels panel.png

See also: `get`, `new` for a notecard or a script, which cost nothing,
and `ls` for finding what has been uploaded afterwards.
